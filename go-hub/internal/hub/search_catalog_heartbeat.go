package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"time"
)

type searchCatalogSubscriber struct {
	request *http.Request
	seen    time.Time
}

// Keep authentication only in memory and only for recently active callers.
// Persisted catalog keys contain a digest, not reusable credentials.
func (c *childSearchSchemaCache) subscribe(s *Server, r *http.Request) {
	if r == nil {
		return
	}
	key := childSearchScope(r)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	if c.subscribers == nil {
		c.subscribers = map[string]searchCatalogSubscriber{}
	}
	_, exists := c.subscribers[key]
	if !exists && len(c.subscribers) >= 512 {
		oldestKey := ""
		var oldest time.Time
		for k, item := range c.subscribers {
			if oldestKey == "" || item.seen.Before(oldest) {
				oldestKey = k
				oldest = item.seen
			}
		}
		delete(c.subscribers, oldestKey)
	}
	c.subscribers[key] = searchCatalogSubscriber{r.Clone(context.WithoutCancel(r.Context())), time.Now()}
	c.mu.Unlock()
	if !exists {
		c.scheduleRefresh(s, "", key)
	}
}
func (c *childSearchSchemaCache) refreshParent(s *Server, parentID string, onlyScope ...string) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	requests := []*http.Request{}
	for key, item := range c.subscribers {
		if len(onlyScope) > 0 && key != onlyScope[0] {
			continue
		}
		if time.Since(item.seen) > 15*time.Minute {
			delete(c.subscribers, key)
			continue
		}
		requests = append(requests, item.request)
	}
	c.mu.Unlock()
	for _, captured := range requests {
		// Re-run the normal authentication/binding path. A client can be
		// revoked or bound to a restricted profile after its last search.
		ctx := context.WithValue(captured.Context(), accessProfileContextKey{}, nil)
		ctx = context.WithValue(ctx, authClaimsContextKey{}, nil)
		r := captured.Clone(ctx)
		if !s.mcpAuth(httptest.NewRecorder(), r) || childSearchScope(r) != childSearchScope(captured) {
			c.mu.Lock()
			delete(c.subscribers, childSearchScope(captured))
			c.mu.Unlock()
			continue
		}
		s.mu.Lock()
		visible := s.publicAgentsLocked(r)
		s.mu.Unlock()
		for _, child := range visible {
			if !isChildMCPAgent(child) || childSearchUnreachable(child) || (parentID != "" && firstString(child.Meta, "parent_server_id") != parentID) {
				continue
			}
			if !profileAllowsTarget(r, child.AgentID) || !agentOwnerConnectionAllowed(r, child) {
				continue
			}
			key, fp := childSearchCacheKey(r, child), childSearchFingerprint(s, child)
			c.mu.Lock()
			fresh := childSearchEntryFresh(c.entries[key], fp) && !s.childSearchGenerationChanged(c.entries[key], child)
			c.mu.Unlock()
			if !fresh {
				c.startChildSearchRefresh(s, r, child, key, fp)
			}
		}
	}
}
func (s *Server) refreshSearchCatalogHeartbeat(parentID string) {
	if v, ok := childSearchCaches.Load(s); ok {
		v.(*childSearchSchemaCache).scheduleRefresh(s, parentID)
	}
}

func (s *Server) childSearchCatalogDigest(child Agent) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	parent := s.agents[firstString(child.Meta, "parent_server_id")]
	if parent == nil {
		return ""
	}
	for _, raw := range sliceValue(parent.Meta["mcp_agents"]) {
		descriptor := mapValue(raw)
		if firstString(descriptor, "ref") == firstString(child.Meta, "child_ref") {
			return firstString(descriptor, "catalog_digest")
		}
	}
	return ""
}

func (c *childSearchSchemaCache) scheduleRefresh(s *Server, parentID string, scope ...string) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.wg.Add(1)
	c.mu.Unlock()
	go func() { defer c.wg.Done(); c.refreshParent(s, parentID, scope...) }()
}
func (c *childSearchSchemaCache) close() {
	c.mu.Lock()
	c.closed = true
	c.subscribers = nil
	c.mu.Unlock()
	c.wg.Wait()
}

func (c *registeredRootSchemaCache) close() { c.mu.Lock(); c.closed = true; c.mu.Unlock(); c.wg.Wait() }

func (s *Server) childSearchHasGeneration(child Agent) bool {
	return s.childSearchCatalogDigest(child) != ""
}
func (s *Server) childSearchGenerationChanged(entry *childSearchSchemaEntry, child Agent) bool {
	if entry == nil {
		return false
	}
	generation := s.childSearchCatalogDigest(child)
	return generation != "" && generation != entry.catalogDigest
}
