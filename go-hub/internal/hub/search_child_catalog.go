package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Child tool schemas are expensive to obtain: they travel through the existing
// durable mcp_tools queue. Keep them outside the per-search FTS transaction and
// refresh only on a cold entry, descriptor change, or TTL expiry.
const (
	childSearchSchemaTTL        = 5 * time.Minute
	childSearchColdLimit        = 4
	childSearchWarmLimit        = 96
	childSearchConcurrency      = 6
	childSearchRefreshWait      = 4 * time.Second
	childSearchToolLimit        = 200
	childSearchSchemaLimit      = 16 << 10
	childSearchDescriptionLimit = 2000
)

// Every refresh goes through the durable mcp_tools queue. A search starts a
// refresh for every cold reachable child, but only this many run at once, so a
// large fleet warms in the background instead of four children per call.
var childSearchRefreshSlots = make(chan struct{}, childSearchConcurrency)

// childSearchUnreachable reports a child that cannot answer tools/list now.
// Such a child is listed as unreachable; it does not make coverage incomplete.
func childSearchUnreachable(child Agent) bool {
	switch strings.ToLower(strings.TrimSpace(child.Status)) {
	case "failed", "stale", "offline", "awaiting_approval":
		return true
	}
	return false
}

type childSearchSchemaEntry struct {
	childID     string
	parentID    string
	fingerprint string
	loadedAt    time.Time
	tools       []map[string]any
	err         string
	refreshing  bool
	done        chan struct{}
}

type childSearchSchemaCache struct {
	mu      sync.Mutex
	entries map[string]*childSearchSchemaEntry
}

// registeredRootSchemaCache mirrors the child cache for independently
// registered MCP relay agents. Refreshes are per-target and bounded; queries
// never fan out across the entire registry.
type registeredRootSchemaCache struct {
	mu      sync.Mutex
	entries map[string]*childSearchSchemaEntry
}

const registeredRootSearchMaxEntries = 512

var registeredRootSearchCaches sync.Map // map[*Server]*registeredRootSchemaCache

func (s *Server) registeredRootSearchCache() *registeredRootSchemaCache {
	v, _ := registeredRootSearchCaches.LoadOrStore(s, &registeredRootSchemaCache{entries: map[string]*childSearchSchemaEntry{}})
	return v.(*registeredRootSchemaCache)
}

func (c *registeredRootSchemaCache) refresh(s *Server, r *http.Request, agent Agent, key, fingerprint string) <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entries[key]
	if entry == nil {
		if len(c.entries) >= registeredRootSearchMaxEntries {
			oldestKey := ""
			var oldest time.Time
			for candidateKey, candidate := range c.entries {
				if candidate.refreshing {
					continue
				}
				if oldestKey == "" || candidate.loadedAt.Before(oldest) {
					oldestKey, oldest = candidateKey, candidate.loadedAt
				}
			}
			if oldestKey != "" {
				delete(c.entries, oldestKey)
			}
		}
		if len(c.entries) >= registeredRootSearchMaxEntries {
			done := make(chan struct{})
			close(done)
			return done
		}
		entry = &childSearchSchemaEntry{}
		c.entries[key] = entry
	}
	if entry.refreshing {
		return entry.done
	}
	entry.childID, entry.fingerprint = agent.AgentID, fingerprint
	entry.refreshing, entry.done = true, make(chan struct{})
	done := entry.done
	request := r
	if r != nil {
		request = r.Clone(r.Context())
	}
	go func() {
		value, rpcErr := s.agentToolsListForRequest(request, agent)
		var tools []map[string]any
		failure := ""
		if rpcErr == nil {
			rootResult, valid := value.(map[string]any)
			if !valid {
				failure = "registered root tools/list returned an invalid object"
			} else {
				tools = boundedSearchChildTools(searchChildToolMaps(rootResult["tools"]))
				if len(tools) == 0 {
					failure = "registered root returned no tools"
				}
			}
		} else {
			failure = "registered root schema refresh failed"
		}
		c.mu.Lock()
		if c.entries[key] == entry {
			entry.refreshing = false
			if rootSearchFingerprint(s, agent) == fingerprint {
				entry.loadedAt, entry.tools, entry.err = time.Now(), cloneChildSearchTools(tools), failure
			}
			close(done)
		}
		c.mu.Unlock()
	}()
	return done
}

func rootSearchFingerprint(s *Server, agent Agent) string {
	meta := stableChildSearchDescriptor(agent.Meta)
	data, _ := json.Marshal(map[string]any{"id": agent.AgentID, "name": agent.Name, "kind": agent.Kind, "transport": agent.Transport, "status": agent.Status, "meta": meta})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func rootSearchCacheKey(r *http.Request, agent Agent) string {
	return childSearchScope(r) + "\x00" + agent.AgentID
}

func (s *Server) rootSearchCatalogForRequest(r *http.Request, targetFilter string) (rows []map[string]any, coverage map[string]any) {
	coverage = map[string]any{"complete": true, "status": "complete", "missing": []string{}, "pending": 0, "failed": 0}
	// Root schemas are fetched only for an explicitly selected target. An
	// unfiltered query uses warm cache entries and never scans the network.
	s.mu.Lock()
	visible := s.publicAgentsLocked(r)
	s.mu.Unlock()
	roots := []Agent{}
	for _, agent := range visible {
		if agent.AgentID == "hub" || isChildMCPAgent(agent) || isVirtualMCPAgent(agent) || strings.HasPrefix(agent.AgentID, "shell:") || strings.HasPrefix(agent.AgentID, "file:") || strings.HasPrefix(agent.AgentID, "control:") {
			continue
		}
		if enabled, specified := agent.Meta["enabled"]; specified && !truthyAny(enabled) {
			continue
		}
		if targetFilter != "" && targetFilter != agent.AgentID && targetFilter != exposedAgentSlug(agent) {
			continue
		}
		if !profileAllowsTarget(r, agent.AgentID) || !agentOwnerConnectionAllowed(r, agent) {
			continue
		}
		roots = append(roots, agent)
	}
	cache := s.registeredRootSearchCache()
	active := map[string]bool{}
	for _, agent := range roots {
		active[rootSearchCacheKey(r, agent)] = true
	}
	cache.mu.Lock()
	for key := range cache.entries {
		if targetFilter == "" && strings.HasPrefix(key, childSearchScope(r)+"\x00") && !active[key] {
			delete(cache.entries, key)
		}
	}
	cache.mu.Unlock()
	var waits []<-chan struct{}
	if targetFilter != "" {
		for _, agent := range roots {
			key, fingerprint := rootSearchCacheKey(r, agent), rootSearchFingerprint(s, agent)
			cache.mu.Lock()
			entry := cache.entries[key]
			fresh := entry != nil && entry.fingerprint == fingerprint && time.Since(entry.loadedAt) < childSearchSchemaTTL && !entry.loadedAt.IsZero()
			cache.mu.Unlock()
			if !fresh && len(waits) < childSearchColdLimit {
				waits = append(waits, cache.refresh(s, r, agent, key, fingerprint))
			}
		}
	}
	if len(waits) > 0 {
		waitCtx, cancelWait := context.WithTimeout(r.Context(), childSearchRefreshWait)
		defer cancelWait()
		for _, done := range waits {
			select {
			case <-done:
			case <-waitCtx.Done():
				coverage["complete"] = false
				coverage["pending"] = intFromAny(coverage["pending"]) + 1
			}
		}
	}
	for _, agent := range roots {
		if !profileAllowsTarget(r, agent.AgentID) || !agentOwnerConnectionAllowed(r, agent) {
			continue
		}
		key, fingerprint := rootSearchCacheKey(r, agent), rootSearchFingerprint(s, agent)
		cache.mu.Lock()
		entry := cache.entries[key]
		if entry != nil && entry.fingerprint == fingerprint && !entry.loadedAt.IsZero() && time.Since(entry.loadedAt) < childSearchSchemaTTL {
			for _, tool := range cloneChildSearchTools(entry.tools) {
				name := firstString(tool, "name")
				if authorizeToolCall(r, agent.AgentID, name) != nil {
					continue
				}
				rows = append(rows, catalogRuleMetadata(r, agent.AgentID, name, map[string]any{"target": agent.AgentID, "name": name, "title": firstString(tool, "title"), "description": firstString(tool, "description"), "inputSchema": mapValue(tool["inputSchema"]), "labels": agent.Name + " registered MCP"}))
			}
			if entry.err != "" {
				coverage["complete"] = false
				coverage["failed"] = intFromAny(coverage["failed"]) + 1
			}
		} else {
			coverage["complete"] = false
			coverage["missing"] = append(stringSlice(coverage["missing"]), agent.AgentID)
		}
		cache.mu.Unlock()
	}
	if !truthy(coverage["complete"]) {
		coverage["status"] = "incomplete"
	}
	return rows, coverage
}

var childSearchCaches sync.Map // map[*Server]*childSearchSchemaCache

func (s *Server) childSearchCache() *childSearchSchemaCache {
	v, _ := childSearchCaches.LoadOrStore(s, &childSearchSchemaCache{entries: map[string]*childSearchSchemaEntry{}})
	return v.(*childSearchSchemaCache)
}

func childSearchScope(r *http.Request) string {
	// operationPrincipal includes the authenticated caller, bound profile and
	// any managed-connection scope. A profile ID alone is not a cache security
	// boundary: two credentials can share a profile while having different
	// connection ownership.
	return operationPrincipal(r)
}

func childSearchFingerprint(s *Server, child Agent) string {
	// Health is refreshed independently of configuration and can change on
	// every heartbeat. It must not force another tools/list request. Keep the
	// child identity/config and the stable parent descriptor (including
	// transport, enabled state, owner/version and connection generation).
	childMeta := stableChildSearchDescriptor(child.Meta)
	descriptor := map[string]any{"id": child.AgentID, "name": child.Name, "kind": child.Kind, "transport": child.Transport, "child": childMeta}
	parentID := firstString(child.Meta, "parent_server_id")
	s.mu.Lock()
	if parent := s.agents[parentID]; parent != nil {
		for _, raw := range sliceValue(parent.Meta["mcp_agents"]) {
			candidate := mapValue(raw)
			if firstString(candidate, "ref") == firstString(child.Meta, "child_ref") {
				descriptor["descriptor"] = stableChildSearchDescriptor(candidate)
				break
			}
		}
	}
	s.mu.Unlock()
	b, _ := json.Marshal(descriptor)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func stableChildSearchDescriptor(value map[string]any) map[string]any {
	if len(value) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(value))
	for key, item := range value {
		lower := strings.ToLower(key)
		if lower == "health" || lower == "exposed_by_default" || strings.HasPrefix(lower, "public_mcp_") || strings.HasPrefix(lower, "last_checked") || strings.HasPrefix(lower, "last_handshake") || strings.HasPrefix(lower, "heartbeat") {
			continue
		}
		// Health can be nested inside connection/probe metadata too. Recurse so
		// timestamp-only updates remain outside the cache generation hash.
		if nested, ok := item.(map[string]any); ok {
			out[key] = stableChildSearchDescriptor(nested)
			continue
		}
		out[key] = item
	}
	return out
}

func childSearchCacheKey(r *http.Request, child Agent) string {
	return childSearchScope(r) + "\x00" + child.AgentID
}

func cloneChildSearchTools(tools []map[string]any) []map[string]any {
	b, _ := json.Marshal(tools)
	var out []map[string]any
	_ = json.Unmarshal(b, &out)
	if out == nil {
		return []map[string]any{}
	}
	return out
}

// startChildSearchRefresh deduplicates concurrent cold/expired requests. The
// relay call itself performs current profile, connection-owner, and approved
// parent checks before dispatch; cache readers repeat current authorization.
func (c *childSearchSchemaCache) startChildSearchRefresh(s *Server, r *http.Request, child Agent, key, fingerprint string) <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entries[key]
	if entry == nil {
		entry = &childSearchSchemaEntry{}
		c.entries[key] = entry
	}
	entry.childID = child.AgentID
	entry.parentID = firstString(child.Meta, "parent_server_id")
	if entry.refreshing {
		return entry.done
	}
	entry.refreshing, entry.done = true, make(chan struct{})
	done := entry.done
	request := r
	if r != nil {
		request = r.Clone(r.Context())
	}
	go func() {
		childSearchRefreshSlots <- struct{}{}
		response := s.childSchemaForRequest(request, child)
		<-childSearchRefreshSlots
		var tools []map[string]any
		failure := ""
		if firstString(response, "status") == "completed" {
			tools = boundedSearchChildTools(searchChildToolMaps(mapValue(response["response"])["tools"]))
			if len(tools) == 0 {
				failure = "child returned no tools"
			}
		} else {
			failure = firstString(mapValue(response["error"]), "message")
			if failure == "" {
				failure = "child schema refresh did not complete"
			}
		}
		c.mu.Lock()
		current := c.entries[key]
		if current == entry {
			current.refreshing = false
			if childSearchFingerprint(s, child) == fingerprint {
				current.fingerprint, current.loadedAt = fingerprint, time.Now()
				current.tools, current.err = cloneChildSearchTools(tools), failure
			}
			close(done)
		}
		c.mu.Unlock()
	}()
	return done
}

func searchChildToolMaps(value any) []map[string]any {
	rows := outputRows(value)
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		if tool := mapValue(row); len(tool) > 0 {
			out = append(out, tool)
		}
	}
	return out
}

func boundedSearchChildTools(tools []map[string]any) []map[string]any {
	if len(tools) > childSearchToolLimit {
		tools = tools[:childSearchToolLimit]
	}
	out := make([]map[string]any, 0, len(tools))
	for _, raw := range tools {
		tool := mapValue(redactPublicMetadata(raw))
		if description := firstString(tool, "description"); len([]rune(description)) > childSearchDescriptionLimit {
			tool["description"] = string([]rune(description)[:childSearchDescriptionLimit])
		}
		schema := tool["inputSchema"]
		encoded, err := json.Marshal(schema)
		if err != nil || len(encoded) > childSearchSchemaLimit {
			continue
		}
		out = append(out, tool)
	}
	return out
}

// childSearchCatalogForRequest returns cached schemas for currently visible,
// enabled child targets. On cold search it queues refreshes for up to
// childSearchWarmLimit targets. At most childSearchConcurrency relay calls run
// at once; the request waits for a short shared window while queued refreshes
// continue in the background. `coverage` reports failed and pending refreshes;
// callers must not imply complete child coverage when it is false.
func (s *Server) childSearchCatalogForRequest(r *http.Request, targetFilter string) (rows []map[string]any, coverage map[string]any) {
	coverage = map[string]any{"complete": true, "status": "complete", "missing": []string{}, "pending": 0, "failed": 0, "failure_reasons": map[string]string{}}
	if targetFilter != "" && !strings.HasPrefix(targetFilter, "child:") && !strings.HasPrefix(targetFilter, "mcp:") {
		return []map[string]any{}, coverage
	}
	s.mu.Lock()
	visible := s.publicAgentsLocked(r)
	s.mu.Unlock()
	children := make([]Agent, 0)
	unreachable := []string{}
	for _, child := range visible {
		if !isChildMCPAgent(child) || (targetFilter != "" && targetFilter != child.AgentID && targetFilter != exposedAgentSlug(child) && targetFilter != "child:"+exposedAgentSlug(child)) {
			continue
		}
		if !profileAllowsTarget(r, child.AgentID) || !agentOwnerConnectionAllowed(r, child) {
			continue
		}
		if targetFilter == "" && childSearchUnreachable(child) {
			unreachable = append(unreachable, child.AgentID)
			continue
		}
		children = append(children, child)
	}
	coverage["unreachable"] = unreachable
	cache := s.childSearchCache()
	active := map[string]bool{}
	for _, child := range children {
		active[childSearchCacheKey(r, child)] = true
	}
	cache.mu.Lock()
	for key := range cache.entries {
		if targetFilter == "" && strings.HasPrefix(key, childSearchScope(r)+"\x00") && !active[key] {
			delete(cache.entries, key)
		}
	}
	cache.mu.Unlock()
	type refresh struct{ done <-chan struct{} }
	refreshes := make([]refresh, 0, childSearchColdLimit)
	for _, child := range children {
		key, fingerprint := childSearchCacheKey(r, child), childSearchFingerprint(s, child)
		cache.mu.Lock()
		entry := cache.entries[key]
		fresh := entry != nil && entry.fingerprint == fingerprint && time.Since(entry.loadedAt) < childSearchSchemaTTL && entry.loadedAt.After(time.Time{})
		cache.mu.Unlock()
		if !fresh && len(refreshes) < childSearchWarmLimit {
			refreshes = append(refreshes, refresh{cache.startChildSearchRefresh(s, r, child, key, fingerprint)})
		} else if !fresh {
			coverage["complete"] = false
			coverage["pending"] = intFromAny(coverage["pending"]) + 1
			coverage["missing"] = append(stringSlice(coverage["missing"]), child.AgentID)
		}
	}
	waitCtx, cancelWait := context.WithTimeout(r.Context(), childSearchRefreshWait)
	defer cancelWait()
	for _, item := range refreshes {
		select {
		case <-item.done:
		case <-waitCtx.Done():
			coverage["complete"] = false
			coverage["pending"] = intFromAny(coverage["pending"]) + 1
		}
	}
	for _, child := range children {
		// Recheck ACL immediately before copying schema into the caller's catalog.
		if !profileAllowsTarget(r, child.AgentID) || !agentOwnerConnectionAllowed(r, child) {
			continue
		}
		key, fingerprint := childSearchCacheKey(r, child), childSearchFingerprint(s, child)
		cache.mu.Lock()
		entry := cache.entries[key]
		if entry != nil && entry.fingerprint != "" && entry.fingerprint != fingerprint {
			delete(cache.entries, key)
			entry = nil
		}
		if entry != nil && entry.fingerprint == fingerprint && entry.loadedAt.After(time.Time{}) {
			for _, tool := range cloneChildSearchTools(entry.tools) {
				if authorizeToolCall(r, child.AgentID, firstString(tool, "name")) != nil {
					continue
				}
				rows = append(rows, catalogRuleMetadata(r, child.AgentID, firstString(tool, "name"), map[string]any{"target": child.AgentID, "name": firstString(tool, "name"), "title": firstString(tool, "title"), "description": firstString(tool, "description"), "inputSchema": mapValue(tool["inputSchema"]), "labels": child.Name + " " + exposedAgentSlug(child) + " child MCP"}))
			}
			if entry.err != "" {
				coverage["failed"] = intFromAny(coverage["failed"]) + 1
				coverage["unreachable"] = append(stringSlice(coverage["unreachable"]), child.AgentID)
				coverage["failure_reasons"].(map[string]string)[child.AgentID] = entry.err
			}
		} else {
			coverage["complete"] = false
			coverage["missing"] = append(stringSlice(coverage["missing"]), child.AgentID)
			if entry != nil && entry.refreshing {
				coverage["pending"] = intFromAny(coverage["pending"]) + 1
			} else {
				coverage["failed"] = intFromAny(coverage["failed"]) + 1
			}
		}
		cache.mu.Unlock()
	}
	if !truthy(coverage["complete"]) {
		coverage["status"] = "incomplete"
		if intFromAny(coverage["pending"]) > 0 {
			coverage["retry_hint"] = "children are still loading in the background; repeat the search in a few seconds"
		}
	}
	return rows, coverage
}

// isChildCatalogTarget checks the live enabled/visible catalog and current ACL.
func isChildCatalogTarget(s *Server, target string, r *http.Request) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, child := range s.publicAgentsLocked(r) {
		if isChildMCPAgent(child) && (target == child.AgentID || target == exposedAgentSlug(child) || target == "child:"+exposedAgentSlug(child)) && profileAllowsTarget(r, child.AgentID) && agentOwnerConnectionAllowed(r, child) {
			return true
		}
	}
	return false
}

// childSearchToolDefinitions returns schemas already cached by an authorized
// refresh. The caller must first pass isChildCatalogTarget and apply its normal
// per-tool authorization before exposing a result.
func childSearchToolDefinitions(s *Server, target string) []map[string]any {
	v, ok := childSearchCaches.Load(s)
	if !ok {
		return nil
	}
	cache := v.(*childSearchSchemaCache)
	cache.mu.Lock()
	defer cache.mu.Unlock()
	var out []map[string]any
	for _, entry := range cache.entries {
		if entry.childID != target || entry.fingerprint == "" || entry.loadedAt.IsZero() {
			continue
		}
		for _, tool := range cloneChildSearchTools(entry.tools) {
			out = append(out, tool)
		}
	}
	return out
}

// searchCatalogCoverage gives the main catalog builder a truthful snapshot.
// It never triggers a refresh; the request path calls childSearchCatalogForRequest
// once before indexing, which performs the bounded cold population.
func searchCatalogCoverage(s *Server, agents []Agent) map[string]any {
	coverage := map[string]any{"complete": true, "status": "complete", "missing": []string{}, "pending": 0, "failed": 0, "children": 0}
	cache, ok := childSearchCaches.Load(s)
	for _, agent := range agents {
		if !isChildMCPAgent(agent) {
			continue
		}
		coverage["children"] = intFromAny(coverage["children"]) + 1
		if !ok {
			coverage["complete"] = false
			coverage["pending"] = intFromAny(coverage["pending"]) + 1
			continue
		}
		c := cache.(*childSearchSchemaCache)
		c.mu.Lock()
		found, fresh, pending, failed := false, false, false, false
		for _, entry := range c.entries {
			if entry.childID != agent.AgentID {
				continue
			}
			found = true
			pending = pending || entry.refreshing
			failed = failed || entry.err != ""
			fresh = fresh || !entry.loadedAt.IsZero() && time.Since(entry.loadedAt) < childSearchSchemaTTL
		}
		c.mu.Unlock()
		if !found || !fresh || failed {
			coverage["complete"] = false
			coverage["missing"] = append(stringSlice(coverage["missing"]), agent.AgentID)
		}
		if pending {
			coverage["pending"] = intFromAny(coverage["pending"]) + 1
		}
		if !found || failed || !fresh && !pending {
			coverage["failed"] = intFromAny(coverage["failed"]) + 1
		}
	}
	if !truthy(coverage["complete"]) {
		coverage["status"] = "incomplete"
	}
	return coverage
}

// invalidateChildSearchSchemas removes cached descriptors for a parent. It is
// called by configuration/catalog lifecycle owners after add, edit, disable,
// or removal; fingerprint checks independently prevent stale rows meanwhile.
func (s *Server) invalidateChildSearchSchemas(parentID string) {
	v, ok := childSearchCaches.Load(s)
	if !ok {
		return
	}
	cache := v.(*childSearchSchemaCache)
	cache.mu.Lock()
	defer cache.mu.Unlock()
	for key, entry := range cache.entries {
		if entry.parentID == parentID {
			delete(cache.entries, key)
		}
	}
}
