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

// Child tool schemas are read directly from their owners. Keep them outside
// the per-search FTS transaction and
// refresh only on a cold entry, descriptor change, or TTL expiry.
const (
	childSearchSchemaTTL        = 5 * time.Minute
	childSearchErrorTTL         = 20 * time.Second
	childSearchColdLimit        = 4
	childSearchWarmLimit        = 96
	childSearchConcurrency      = 6
	childSearchRefreshWait      = 4 * time.Second
	childSearchSchemaLimit      = 16 << 10
	childSearchDescriptionLimit = 2000
)

// Bootstrap and heartbeat refresh cold reachable children with bounded
// concurrency. No durable execution jobs are created by schema reads.
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
	catalogDigest string
	versioned     bool
	childID       string
	parentID      string
	fingerprint   string
	loadedAt      time.Time
	attemptedAt   time.Time
	tools         []map[string]any
	err           string
	refreshing    bool
	done          chan struct{}
}

type childSearchSchemaCache struct {
	closed      bool
	wg          sync.WaitGroup
	subscribers map[string]searchCatalogSubscriber
	mu          sync.Mutex
	entries     map[string]*childSearchSchemaEntry
}

// registeredRootSchemaCache mirrors the child cache for independently
// registered MCP relay agents. Refreshes are per-target and bounded; queries
// never fan out across the entire registry.
type registeredRootSchemaCache struct {
	closed  bool
	wg      sync.WaitGroup
	mu      sync.Mutex
	entries map[string]*childSearchSchemaEntry
}

const registeredRootSearchMaxEntries = 512

var registeredRootSearchCaches sync.Map // map[*Server]*registeredRootSchemaCache

func (s *Server) registeredRootSearchCache() *registeredRootSchemaCache {
	if v, ok := registeredRootSearchCaches.Load(s); ok {
		return v.(*registeredRootSchemaCache)
	}
	v, _ := registeredRootSearchCaches.LoadOrStore(s, &registeredRootSchemaCache{entries: s.loadSearchSnapshot("root")})
	return v.(*registeredRootSchemaCache)
}

func (c *registeredRootSchemaCache) refresh(s *Server, r *http.Request, agent Agent, key, fingerprint string) <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
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
	if entry.fingerprint != "" && entry.fingerprint != fingerprint {
		entry.tools = nil
		entry.loadedAt, entry.attemptedAt = time.Time{}, time.Time{}
		entry.err = ""
	}
	entry.childID, entry.fingerprint = agent.AgentID, fingerprint
	entry.refreshing, entry.done = true, make(chan struct{})
	done := entry.done
	request := r
	if r != nil {
		request = r.Clone(r.Context())
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
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
				persist := failure == "" || (len(entry.tools) > 0 && entry.err != failure)
				entry.attemptedAt, entry.err = time.Now(), failure
				if failure == "" {
					entry.loadedAt = entry.attemptedAt
					entry.tools = cloneChildSearchTools(tools)
				}
				if persist {
					s.saveSearchSnapshot("root", c.entries)
				}
			}
		}
		close(done)
		c.mu.Unlock()
	}()
	return done
}

func rootSearchFingerprint(s *Server, agent Agent) string {
	meta := stableChildSearchDescriptor(agent.Meta)
	data, _ := json.Marshal(map[string]any{"id": agent.AgentID, "name": agent.Name, "kind": agent.Kind, "transport": agent.Transport, "meta": meta})
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
			fresh := childSearchEntryFresh(entry, fingerprint)
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
		if entry != nil && entry.fingerprint == fingerprint && !entry.loadedAt.IsZero() {
			if entry.err != "" || time.Since(entry.loadedAt) >= childSearchSchemaTTL {
				coverage["stale"] = append(stringSlice(coverage["stale"]), agent.AgentID)
			}
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
	if v, ok := childSearchCaches.Load(s); ok {
		return v.(*childSearchSchemaCache)
	}
	v, _ := childSearchCaches.LoadOrStore(s, &childSearchSchemaCache{entries: s.loadSearchSnapshot("child")})
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
				stable := stableChildSearchDescriptor(candidate)
				if firstString(candidate, "catalog_config_digest") != "" {
					// Connection incarnation is still checked by authorization.
					// Stable definition + separately checked catalog generation
					// govern metadata reuse across a restart.
					delete(childMeta, "connection_instance")
					delete(stable, "connection_instance")
					delete(stable, "catalog_digest")
				}
				descriptor["descriptor"] = stable
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

// startChildSearchRefresh deduplicates cold/expired reads without durable
// Jobs. The owner performs current profile and connection checks; cache
// readers reauthorize every tool before exposing it.
func (c *childSearchSchemaCache) startChildSearchRefresh(s *Server, r *http.Request, child Agent, key, fingerprint string) <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	// Bound queued work as well as network concurrency. Pending children are
	// picked up by completion/heartbeat, not one waiting goroutine per target.
	if current := c.entries[key]; current != nil && current.refreshing {
		return current.done
	}
	active := 0
	for _, current := range c.entries {
		if current.refreshing {
			active++
		}
	}
	if active >= childSearchConcurrency {
		return nil
	}
	entry := c.entries[key]
	if entry == nil {
		entry = &childSearchSchemaEntry{}
		c.entries[key] = entry
	}
	if entry.fingerprint != "" && (entry.fingerprint != fingerprint || s.childSearchGenerationChanged(entry, child)) {
		entry.tools = nil
		entry.loadedAt = time.Time{}
		entry.err = ""
		entry.attemptedAt = time.Time{}
		entry.fingerprint = fingerprint
	}
	entry.catalogDigest = s.childSearchCatalogDigest(child)
	generation := entry.catalogDigest
	entry.versioned = s.childSearchHasGeneration(child)
	entry.childID = child.AgentID
	entry.parentID = firstString(child.Meta, "parent_server_id")
	if entry.refreshing {
		return entry.done
	}
	entry.refreshing, entry.done = true, make(chan struct{})
	done := entry.done
	request := r
	var cancel context.CancelFunc
	if r != nil {
		// A search can finish before a cold refresh. Retain authenticated
		// request values but bound the read independently of the client socket.
		var ctx context.Context
		ctx, cancel = context.WithTimeout(context.WithoutCancel(r.Context()), directChildSchemaTimeout)
		request = r.Clone(ctx)
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		if cancel != nil {
			defer cancel()
		}
		select {
		case childSearchRefreshSlots <- struct{}{}:
		case <-request.Context().Done():
			c.mu.Lock()
			entry.refreshing = false
			close(done)
			c.mu.Unlock()
			return
		}
		response := s.directChildSchemaForRequest(request, child)
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
			if childSearchFingerprint(s, child) == fingerprint && s.childSearchCatalogDigest(child) == generation {
				persist := failure == "" || (len(current.tools) > 0 && current.err != failure)
				current.fingerprint, current.attemptedAt = fingerprint, time.Now()
				current.err = failure
				if failure == "" {
					current.loadedAt = current.attemptedAt
					current.tools = cloneChildSearchTools(tools)
				}
				if persist {
					s.saveSearchSnapshot("child", c.entries)
				}
			}
		}
		close(done)
		c.mu.Unlock()
		c.refreshParent(s, "")
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
	out := make([]map[string]any, 0, len(tools))
	for _, raw := range tools {
		tool := mapValue(redactPublicMetadata(raw))
		if description := firstString(tool, "description"); len([]rune(description)) > childSearchDescriptionLimit {
			tool["description"] = string([]rune(description)[:childSearchDescriptionLimit])
		}
		schema := tool["inputSchema"]
		encoded, err := json.Marshal(schema)
		if err != nil || len(encoded) > childSearchSchemaLimit {
			// Search indexes every capability, not only small input schemas.
			// Exact schemas remain available through the schema endpoint.
			tool["inputSchema"] = map[string]any{"type": "object", "schema_truncated": true}
		}
		out = append(out, tool)
	}
	return out
}

// A failed read should be retried soon without stampeding a temporarily
// unreachable host. Valid successful schemas can keep their longer TTL.
func childSearchEntryFresh(entry *childSearchSchemaEntry, fingerprint string) bool {
	if entry == nil || entry.fingerprint != fingerprint {
		return false
	}
	ttl := childSearchSchemaTTL
	if entry.err != "" {
		attempted := entry.attemptedAt
		if attempted.IsZero() {
			attempted = entry.loadedAt
		}
		return !attempted.IsZero() && time.Since(attempted) < childSearchErrorTTL
	}
	if entry.loadedAt.IsZero() {
		return false
	}
	if entry.err == "" && entry.versioned {
		return true
	} // Descriptor/heartbeat generation, not wall-clock TTL, invalidates successful schemas.
	return time.Since(entry.loadedAt) < ttl
}

// childSearchCatalogForRequest returns cached schemas for currently visible,
// enabled child targets. Cold searches start bounded in-memory read-only
// refreshes (never durable Jobs). At most childSearchConcurrency reads run at
// once; the request waits for a short shared window. `coverage` reports
// failed and pending refreshes; callers must not imply complete coverage.
func (s *Server) childSearchCatalogForRequest(r *http.Request, targetFilter string) (rows []map[string]any, coverage map[string]any) {
	coverage = map[string]any{"complete": true, "status": "complete", "missing": []string{}, "pending": 0, "failed": 0, "failure_reasons": map[string]string{}}
	if targetFilter != "" && !strings.HasPrefix(targetFilter, "child:") && !strings.HasPrefix(targetFilter, "mcp:") {
		return []map[string]any{}, coverage
	}
	s.mu.Lock()
	visible := s.publicAgentsLocked(r)
	// Catalog transport completeness is distinct from schema-cache freshness.
	// Include visible parents even when their retained catalog has zero children.
	for _, parent := range visible {
		if !strings.HasPrefix(parent.AgentID, "shell:") || firstString(parent.Meta, "membership_catalog_status") != "incomplete" {
			continue
		}
		parentAllowed := profileAllowsTarget(r, parent.AgentID) && agentOwnerConnectionAllowed(r, parent)
		reasons := []string{}
		if targetFilter == "" && parentAllowed {
			reasons = append(reasons, parent.AgentID)
		}
		for _, child := range visible {
			if !isChildMCPAgent(child) || firstString(child.Meta, "parent_server_id") != parent.AgentID ||
				!profileAllowsTarget(r, child.AgentID) || !agentOwnerConnectionAllowed(r, child) {
				continue
			}
			if targetFilter != "" && targetFilter != child.AgentID && targetFilter != exposedAgentSlug(child) && targetFilter != "child:"+exposedAgentSlug(child) {
				continue
			}
			if parentAllowed {
				reasons = append(reasons, parent.AgentID)
			} else {
				reasons = append(reasons, child.AgentID)
			}
		}
		for _, id := range reasons {
			coverage["complete"] = false
			coverage["failure_reasons"].(map[string]string)[id] = "member executor catalog incomplete"
		}
	}
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
		}
		children = append(children, child)
	}
	coverage["unreachable"] = unreachable
	cache := s.childSearchCache()
	cache.subscribe(s, r)
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
		fresh := childSearchEntryFresh(entry, fingerprint) && !s.childSearchGenerationChanged(entry, child)
		cache.mu.Unlock()
		if targetFilter == "" {
			continue
		} // Query path is cache-only; bootstrap/heartbeat own refresh.
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
		if entry != nil && entry.fingerprint != "" && (entry.fingerprint != fingerprint || s.childSearchGenerationChanged(entry, child)) {
			delete(cache.entries, key)
			entry = nil
		}
		if entry != nil && entry.fingerprint == fingerprint && !entry.loadedAt.IsZero() {
			if childSearchUnreachable(child) || entry.err != "" || !childSearchEntryFresh(entry, fingerprint) {
				if coverage["last_successful_refresh"] == nil {
					coverage["last_successful_refresh"] = map[string]string{}
				}
				coverage["last_successful_refresh"].(map[string]string)[child.AgentID] = entry.loadedAt.UTC().Format(time.RFC3339Nano)
				coverage["stale"] = append(stringSlice(coverage["stale"]), child.AgentID)
			}
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
			if entry != nil && entry.err != "" {
				coverage["failure_reasons"].(map[string]string)[child.AgentID] = entry.err
			}
			if (entry == nil && targetFilter == "") || (entry != nil && entry.refreshing) {
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
