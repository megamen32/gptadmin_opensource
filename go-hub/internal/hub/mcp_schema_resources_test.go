package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const schemaResourceTestIndex = "gptadmin://schemas/active"
const schemaResourceTestAll = "gptadmin://schemas/active/all"

func schemaResourceFixture(t *testing.T, respond http.HandlerFunc) (*Server, Agent, *http.Request, string, *atomic.Int32) {
	t.Helper()
	s, child, calls := directSchemaFixture(t, respond)
	s.mu.Lock()
	parent := s.agents["shell:peer"]
	parent.Meta["server_id"] = "enrolled-fixture-identity"
	parent.Meta["fingerprint"] = "fixture-key-fingerprint"
	parent.LastSeen = nowFloat()
	identity := fullTargetIdentityFromAgents(child.AgentID, targetAgentMap(s.publicAgentsLocked(nil)))
	s.mu.Unlock()
	if identity == "" {
		t.Fatal("fixture has no stable identity")
	}
	r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	r.Header.Set("Authorization", "Bearer schema-test-token")
	r = requestWithAuthClaims(r, map[string]any{"sub": "reader", "client_id": "resource-reader", "scope": "gptadmin.read gptadmin.exec", "access_mode": "full"})
	return s, child, r, "gptadmin://schemas/server/" + identity, calls
}

func schemaResourceReply(w http.ResponseWriter, tools []map[string]any) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "completed", "response": map[string]any{"tools": tools, "schema_complete": true, "schema_completeness": "complete"}})
}

func schemaResourceListForTest(t *testing.T, s *Server, r *http.Request) []map[string]any {
	t.Helper()
	value, err := s.agentResourcesList(r, Agent{AgentID: "hub"})
	if err != nil {
		t.Fatalf("resources/list: %v", err)
	}
	return searchChildToolMaps(mapValue(value)["resources"])
}

func schemaResourceDocument(t *testing.T, s *Server, r *http.Request, uri string) map[string]any {
	t.Helper()
	result := s.appsSDKResourceRead(r, uri)
	contents := searchChildToolMaps(result["contents"])
	if len(contents) != 1 {
		t.Fatalf("resource %s has no single content: %#v", uri, result)
	}
	if firstString(contents[0], "mimeType") != "application/json" {
		t.Fatalf("resource is not JSON: %#v", result)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(firstString(contents[0], "text")), &doc); err != nil {
		t.Fatalf("resource JSON: %v", err)
	}
	return doc
}

func schemaResourceHasURI(rows []map[string]any, uri string) bool {
	for _, row := range rows {
		if firstString(row, "uri") == uri {
			return true
		}
	}
	return false
}

func TestMCPSchemaResourcesListingMetadataOnly(t *testing.T) {
	s, _, r, uri, calls := schemaResourceFixture(t, func(w http.ResponseWriter, r *http.Request) {
		schemaResourceReply(w, []map[string]any{{"name": "forbidden-eager-schema-marker", "inputSchema": map[string]any{"type": "object"}}})
	})
	rows := schemaResourceListForTest(t, s, r)
	for _, expected := range []string{schemaResourceTestIndex, schemaResourceTestAll, uri} {
		if !schemaResourceHasURI(rows, expected) {
			t.Errorf("resource picker is missing %s", expected)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("listing eagerly fetched %d child schemas", calls.Load())
	}
	encoded, _ := json.Marshal(rows)
	if strings.Contains(string(encoded), "forbidden-eager-schema-marker") {
		t.Fatal("listing injected schema contents")
	}
	doc := schemaResourceDocument(t, s, r, schemaResourceTestIndex)
	if calls.Load() != 0 || len(searchChildToolMaps(doc["servers"])) == 0 {
		t.Fatalf("index was not metadata-only: %#v calls=%d", doc, calls.Load())
	}
}

func TestMCPSchemaResourcesPreserveFullDefinitions(t *testing.T) {
	want := map[string]any{
		"name": "alpha", "title": "Exact title", "description": strings.Repeat("long schema description ", 180),
		"inputSchema":  map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string", "description": strings.Repeat("full-input-", 2400)}}, "additionalProperties": false},
		"outputSchema": map[string]any{"type": "object", "properties": map[string]any{"result": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}},
		"annotations":  map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true},
	}
	s, _, r, uri, calls := schemaResourceFixture(t, func(w http.ResponseWriter, r *http.Request) { schemaResourceReply(w, []map[string]any{want}) })
	for i := 0; i < 2; i++ {
		doc := schemaResourceDocument(t, s, r, uri)
		tools := searchChildToolMaps(doc["tools"])
		if firstString(doc, "status") != "complete" || !truthy(doc["complete"]) || len(tools) != 1 || !reflect.DeepEqual(tools[0], want) {
			t.Fatalf("exact schema was truncated or changed: status=%v count=%d", doc["status"], len(tools))
		}
		if firstString(doc, "schema_sha256", "version") == "" || firstString(doc, "retrieved_at") == "" || intFromAny(doc["schema_bytes"]) <= 16<<10 {
			t.Fatalf("schema freshness/size/version missing: %#v", doc)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("warm resource cache fetched %d times", calls.Load())
	}
	receipt := s.appsSDKResourceReceipt(r, uri)
	if !truthy(receipt["ok"]) || intFromAny(receipt["byte_size"]) < 16000 || calls.Load() != 1 {
		t.Fatalf("schema resource receipt failed: %#v", receipt)
	}
}

func TestMCPSchemaResourcesReauthorizeWarmedCache(t *testing.T) {
	s, child, r, uri, calls := schemaResourceFixture(t, func(w http.ResponseWriter, r *http.Request) {
		schemaResourceReply(w, []map[string]any{
			{"name": "alpha", "inputSchema": map[string]any{"type": "object"}},
			{"name": "private_beta", "inputSchema": map[string]any{"type": "object"}},
		})
	})
	profile := AccessProfile{ID: "same-profile", Version: 1, AccessMode: "full", AllowedTargets: []string{"hub", child.AgentID}, AllowedTools: []string{"*"}}
	r = requestWithAccessProfile(r, profile)
	if len(searchChildToolMaps(schemaResourceDocument(t, s, r, uri)["tools"])) != 2 {
		t.Fatal("warm fixture failed")
	}
	profile.AllowedTools = []string{"alpha"}
	restricted := requestWithAccessProfile(r, profile)
	doc := schemaResourceDocument(t, s, restricted, uri)
	tools := searchChildToolMaps(doc["tools"])
	if len(tools) != 1 || firstString(tools[0], "name") != "alpha" {
		t.Fatalf("same principal received stale tool grants: %#v", tools)
	}
	profile.AllowedTargets = []string{"hub"}
	revoked := requestWithAccessProfile(r, profile)
	before := calls.Load()
	if schemaResourceHasURI(schemaResourceListForTest(t, s, revoked), uri) {
		t.Fatal("revoked resource is still listed")
	}
	if len(searchChildToolMaps(s.appsSDKResourceRead(revoked, uri)["contents"])) != 0 || calls.Load() != before {
		t.Fatal("revoked resource was read or fetched")
	}
}

func TestMCPSchemaResourcesOwnerConnectionIsolation(t *testing.T) {
	s, child, r, uri, calls := schemaResourceFixture(t, func(w http.ResponseWriter, r *http.Request) {
		schemaResourceReply(w, []map[string]any{{"name": "owner_tool", "inputSchema": map[string]any{"type": "object"}}})
	})
	_ = schemaResourceDocument(t, s, r, uri)
	s.mu.Lock()
	descriptor := mapValue(sliceValue(s.agents["shell:peer"].Meta["mcp_agents"])[0])
	descriptor["owner_profile_id"] = "owner-profile"
	s.mu.Unlock()
	before := calls.Load()
	if schemaResourceHasURI(schemaResourceListForTest(t, s, r), uri) || len(searchChildToolMaps(s.appsSDKResourceRead(r, uri)["contents"])) != 0 || calls.Load() != before {
		t.Fatal("warm schema leaked after the child became personal")
	}
	owner := requestWithAccessProfile(r, AccessProfile{ID: "owner-profile", Version: 1, AccessMode: "full", AllowedTargets: []string{"hub", child.AgentID}, AllowedTools: []string{"*"}})
	if len(searchChildToolMaps(s.appsSDKResourceRead(owner, uri)["contents"])) != 0 {
		t.Fatal("profile alone bypassed managed connection")
	}
	owner = owner.WithContext(context.WithValue(owner.Context(), connectionContextKey{}, connectionScope{Target: child.AgentID, Key: "fixture-scope", Instance: "fixture-incarnation"}))
	doc := schemaResourceDocument(t, s, owner, uri)
	if len(searchChildToolMaps(doc["tools"])) != 1 {
		t.Fatal("exact owner connection could not read")
	}
}

func TestMCPSchemaResourcesRenameKeepsStableURI(t *testing.T) {
	s, _, r, uri, _ := schemaResourceFixture(t, func(w http.ResponseWriter, r *http.Request) { t.Fatal("listing contacted child") })
	if !schemaResourceHasURI(schemaResourceListForTest(t, s, r), uri) {
		t.Fatal("fixture URI absent")
	}
	s.mu.Lock()
	parent := s.agents["shell:peer"]
	delete(s.agents, "shell:peer")
	parent.AgentID, parent.Name = "shell:renamed", "Renamed host"
	s.agents[parent.AgentID] = parent
	s.mu.Unlock()
	if !schemaResourceHasURI(schemaResourceListForTest(t, s, r), uri) {
		t.Fatal("hostname rename changed enrolled schema URI")
	}
}

func TestMCPSchemaResourcesBoundedFailureIsHonest(t *testing.T) {
	for _, mode := range []string{"unavailable", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			s, _, r, uri, _ := schemaResourceFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if mode == "unavailable" {
					writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "offline"})
					return
				}
				schemaResourceReply(w, []map[string]any{{"name": "huge", "description": strings.Repeat("x", 3<<20), "inputSchema": map[string]any{"type": "object"}}})
			})
			doc := schemaResourceDocument(t, s, r, uri)
			if firstString(doc, "status") != mode || truthy(doc["complete"]) || len(searchChildToolMaps(doc["tools"])) != 0 {
				t.Fatalf("bounded failure claimed a full schema: %#v", doc)
			}
			snapshot := schemaResourceDocument(t, s, r, schemaResourceTestAll)
			if truthy(snapshot["complete"]) || firstString(snapshot, "status") != "partial" {
				t.Fatalf("full snapshot hid a failed child: %#v", snapshot)
			}
		})
	}
}

func TestMCPSchemaResourcesOlderNodeCompletenessIsUnverified(t *testing.T) {
	s, _, r, uri, _ := schemaResourceFixture(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "completed", "response": map[string]any{"tools": []map[string]any{{"name": "legacy", "inputSchema": map[string]any{}}}}})
	})
	doc := schemaResourceDocument(t, s, r, uri)
	if truthy(doc["complete"]) || firstString(doc, "status") != "partial" || firstString(doc, "reason") != "upstream_completeness_unverified" || len(searchChildToolMaps(doc["tools"])) != 1 {
		t.Fatalf("older Node's unproven first page was labelled full: %#v", doc)
	}
}

func TestMCPSchemaResourcesCachedFailureIsStale(t *testing.T) {
	var failing atomic.Bool
	s, _, r, uri, calls := schemaResourceFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if failing.Load() {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "offline"})
			return
		}
		schemaResourceReply(w, []map[string]any{{"name": "last_known", "inputSchema": map[string]any{"type": "object"}}})
	})
	_ = schemaResourceDocument(t, s, r, uri)
	s.schemaResources.mu.Lock()
	for _, entry := range s.schemaResources.entries {
		entry.loadedAt = time.Now().Add(-2 * mcpSchemaCacheTTL)
		entry.attemptedAt = entry.loadedAt
	}
	s.schemaResources.mu.Unlock()
	failing.Store(true)
	doc := schemaResourceDocument(t, s, r, uri)
	if firstString(doc, "status") != "stale" || truthy(doc["complete"]) || len(searchChildToolMaps(doc["tools"])) != 1 || calls.Load() != 2 {
		t.Fatalf("failed refresh was presented as fresh: %#v calls=%d", doc, calls.Load())
	}
	_ = schemaResourceDocument(t, s, r, uri)
	if calls.Load() != 2 {
		t.Fatal("failed upstream was immediately retried despite failure TTL")
	}
	s.schemaResources.mu.Lock()
	for _, entry := range s.schemaResources.entries {
		entry.loadedAt = time.Now().Add(-2 * mcpSchemaStaleTTL)
		entry.attemptedAt = time.Now().Add(-2 * mcpSchemaFailureTTL)
	}
	s.schemaResources.mu.Unlock()
	doc = schemaResourceDocument(t, s, r, uri)
	if firstString(doc, "status") != "unavailable" || len(searchChildToolMaps(doc["tools"])) != 0 {
		t.Fatal("expired stale content survived beyond the advertised retention bound")
	}
}

func TestMCPSchemaResourcesProtocolRoutes(t *testing.T) {
	s, _, _, uri, _ := schemaResourceFixture(t, func(w http.ResponseWriter, r *http.Request) {
		schemaResourceReply(w, []map[string]any{{"name": "native_resource", "inputSchema": map[string]any{"type": "object"}}})
	})
	for _, endpoint := range []string{"/mcp", "/server/hub/mcp"} {
		for _, version := range []string{"2025-11-25", "2026-07-28"} {
			for _, method := range []string{"resources/list", "resources/read"} {
				t.Run(endpoint+"/"+version+"/"+method, func(t *testing.T) {
					params := map[string]any{}
					if method == "resources/read" {
						params["uri"] = uri
					}
					if version == "2026-07-28" {
						params["_meta"] = map[string]any{
							"io.modelcontextprotocol/protocolVersion":    version,
							"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "schema-resource-tests", "version": "1"},
							"io.modelcontextprotocol/clientCapabilities": map[string]any{},
						}
					}
					body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
					req := httptest.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
					req.Header.Set("Authorization", "Bearer schema-test-token")
					req.Header.Set("Content-Type", "application/json")
					req.Header.Set("MCP-Protocol-Version", version)
					if version == "2026-07-28" {
						req.Header.Set("Mcp-Method", method)
						if method == "resources/read" {
							req.Header.Set("Mcp-Name", uri)
						}
					}
					rec := httptest.NewRecorder()
					s.Handler().ServeHTTP(rec, req)
					var reply map[string]any
					if json.Unmarshal(rec.Body.Bytes(), &reply) != nil || rec.Code != 200 || reply["error"] != nil {
						t.Fatalf("protocol resource request failed: HTTP %d %s", rec.Code, rec.Body.String())
					}
					result := mapValue(reply["result"])
					if method == "resources/list" && !schemaResourceHasURI(searchChildToolMaps(result["resources"]), uri) {
						t.Fatal("schema absent from actual picker route")
					}
					if method == "resources/read" && len(searchChildToolMaps(result["contents"])) != 1 {
						t.Fatal("schema read returned no content")
					}
					if version == "2026-07-28" && (firstString(result, "resultType") != "complete" || firstString(result, "cacheScope") != "private" || intFromAny(result["ttlMs"]) != 0) {
						t.Fatalf("schema resource cache metadata is unsafe: %#v", result)
					}
				})
			}
		}
	}
}

func TestMCPSchemaResourcesDeadlineCancelsUpstream(t *testing.T) {
	release := make(chan struct{})
	cancelled := make(chan struct{})
	defer close(release)
	s, _, r, uri, _ := schemaResourceFixture(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
			close(cancelled)
		case <-release:
		}
	})
	ctx, cancel := context.WithTimeout(r.Context(), 80*time.Millisecond)
	defer cancel()
	start := time.Now()
	doc := schemaResourceDocument(t, s, r.Clone(ctx), uri)
	if time.Since(start) > time.Second || truthy(doc["complete"]) || firstString(doc, "status") != "unavailable" {
		t.Fatalf("upstream ignored the caller's finite deadline: elapsed=%s status=%v", time.Since(start), doc["status"])
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("upstream request context was not cancelled")
	}
	s.schemaResources.mu.Lock()
	defer s.schemaResources.mu.Unlock()
	if s.schemaResources.active != 0 || len(s.schemaResources.flights) != 0 {
		t.Fatal("cancelled read leaked an active refresh")
	}
}

func TestMCPSchemaResourcesSnapshotFetchAndConcurrencyLimits(t *testing.T) {
	var active, maximum atomic.Int32
	s, _, r, _, calls := schemaResourceFixture(t, func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		for old := maximum.Load(); n > old && !maximum.CompareAndSwap(old, n); old = maximum.Load() {
		}
		defer active.Add(-1)
		time.Sleep(time.Millisecond)
		schemaResourceReply(w, []map[string]any{{"name": "bounded", "inputSchema": map[string]any{"type": "object"}}})
	})
	s.mu.Lock()
	children := make([]any, 0, mcpSchemaMaxFetches+20)
	for i := 0; i < mcpSchemaMaxFetches+20; i++ {
		children = append(children, map[string]any{"ref": fmt.Sprintf("schema-%03d", i), "name": fmt.Sprintf("Schema %d", i), "enabled": true, "transport": "streamable-http"})
	}
	s.agents["shell:peer"].Meta["mcp_agents"] = children
	s.mu.Unlock()
	result := s.appsSDKResourceRead(r, schemaResourceTestAll)
	contents := searchChildToolMaps(result["contents"])
	if len(contents) != 1 {
		t.Fatalf("snapshot missing: %#v", result)
	}
	var doc map[string]any
	text := firstString(contents[0], "text")
	if json.Unmarshal([]byte(text), &doc) != nil {
		t.Fatal("snapshot invalid")
	}
	if truthy(doc["complete"]) || firstString(doc, "status") != "partial" || calls.Load() > mcpSchemaMaxFetches || calls.Load() == 0 || maximum.Load() > mcpSchemaConcurrency || len(text) > mcpSchemaMaxReadBytes {
		t.Fatalf("snapshot exceeded a declared limit: complete=%v calls=%d concurrent=%d bytes=%d", doc["complete"], calls.Load(), maximum.Load(), len(text))
	}
}

func TestMCPSchemaResourcesCacheHasCountAndByteBounds(t *testing.T) {
	var large atomic.Bool
	s, _, r, uri, _ := schemaResourceFixture(t, func(w http.ResponseWriter, r *http.Request) {
		tool := map[string]any{"name": "bounded_cache", "inputSchema": map[string]any{"type": "object"}}
		if large.Load() {
			tool["description"] = strings.Repeat("c", 400<<10)
		}
		schemaResourceReply(w, []map[string]any{tool})
	})
	for i := 0; i < mcpSchemaMaxCacheEntries+5; i++ {
		req := requestWithAuthClaims(r, map[string]any{"sub": fmt.Sprintf("fixture-client-%d", i), "scope": "gptadmin.read gptadmin.exec"})
		_ = schemaResourceDocument(t, s, req, uri)
	}
	large.Store(true)
	for i := 0; i < 24; i++ {
		req := requestWithAuthClaims(r, map[string]any{"sub": fmt.Sprintf("fixture-large-%d", i), "scope": "gptadmin.read gptadmin.exec"})
		_ = schemaResourceDocument(t, s, req, uri)
	}
	s.schemaResources.mu.Lock()
	defer s.schemaResources.mu.Unlock()
	actualBytes := 0
	for _, entry := range s.schemaResources.entries {
		actualBytes += len(entry.data)
	}
	if len(s.schemaResources.entries) > mcpSchemaMaxCacheEntries || actualBytes != s.schemaResources.bytes || actualBytes > mcpSchemaMaxCacheBytes || actualBytes == 0 {
		t.Fatalf("schema cache exceeded bounds: entries=%d accounted=%d actual=%d", len(s.schemaResources.entries), s.schemaResources.bytes, actualBytes)
	}
}

func TestMCPSchemaResourcesDoNotPublishHostnameIdentityFallback(t *testing.T) {
	s, _, r, _, calls := schemaResourceFixture(t, func(w http.ResponseWriter, r *http.Request) { t.Fatal("metadata contacted unenrolled upstream") })
	s.mu.Lock()
	delete(s.agents["shell:peer"].Meta, "fingerprint")
	s.mu.Unlock()
	for _, resource := range schemaResourceListForTest(t, s, r) {
		if strings.HasPrefix(firstString(resource, "uri"), mcpSchemaServerPrefix) && strings.Contains(firstString(resource, "uri"), "peer") {
			t.Fatal("hostname was substituted for enrolled identity")
		}
	}
	index := schemaResourceDocument(t, s, r, schemaResourceTestIndex)
	found := false
	for _, row := range searchChildToolMaps(index["servers"]) {
		if firstString(row, "server_id") == childTargetFixture() {
			found = firstString(row, "uri") == "" && firstString(row, "status") == "unavailable"
		}
	}
	if !found || calls.Load() != 0 {
		t.Fatalf("unavailable stable identity was hidden: %#v", index)
	}
}

func TestMCPSchemaResourcesRejectMalformedDefinitions(t *testing.T) {
	s, _, r, uri, _ := schemaResourceFixture(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "completed", "response": map[string]any{
			"schema_complete": true, "schema_completeness": "complete",
			"tools": []any{"invalid-definition", map[string]any{"name": "valid", "inputSchema": map[string]any{"type": "object"}}},
		}})
	})
	doc := schemaResourceDocument(t, s, r, uri)
	if truthy(doc["complete"]) || firstString(doc, "status") != "unavailable" {
		t.Fatalf("malformed definitions were silently dropped from a full schema: status=%v complete=%v", doc["status"], doc["complete"])
	}
}

func TestMCPSchemaResourcesSnapshotRegistryGrowthIsPartial(t *testing.T) {
	var server *Server
	s, child, r, _, _ := schemaResourceFixture(t, func(w http.ResponseWriter, r *http.Request) {
		server.mu.Lock()
		parent := server.agents["shell:peer"]
		parent.Meta["mcp_agents"] = append(sliceValue(parent.Meta["mcp_agents"]), map[string]any{
			"ref": "newly-online", "name": "New server", "enabled": true, "transport": "streamable-http",
		})
		server.mu.Unlock()
		schemaResourceReply(w, []map[string]any{{"name": "first", "inputSchema": map[string]any{"type": "object"}}})
	})
	server = s
	r = requestWithAccessProfile(r, AccessProfile{ID: "snapshot-reader", AccessMode: "full",
		AllowedTargets: []string{child.AgentID, "mcp:shell:peer:newly-online"}, AllowedTools: []string{"*"}})
	doc := schemaResourceDocument(t, s, r, schemaResourceTestAll)
	if truthy(doc["complete"]) || firstString(doc, "status") != "partial" || intFromAny(doc["omitted_servers"]) != 1 {
		t.Fatalf("registry growth was reported as a full snapshot: status=%v complete=%v omitted=%v", doc["status"], doc["complete"], doc["omitted_servers"])
	}
}
