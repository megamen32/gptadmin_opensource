package security

import (
	"sync"
	"time"
)

// defaultNonceTTL is used when callers pass a non-positive TTL to NewNonceCache.
// It mirrors the Python shellmcp default (SHELLMCP_NONCE_TTL_S=300).
const defaultNonceTTL = 5 * time.Minute

// NonceCache tracks nonces seen within a TTL window for replay protection.
// It is safe for concurrent use.
//
// The cache grows as new nonces are inserted and shrinks via a lazy prune
// that runs on each insert: any entry whose age exceeds the TTL is removed.
// This bounds memory without requiring a background goroutine.
type NonceCache struct {
	ttl time.Duration

	mu    sync.Mutex
	seen  map[string]time.Time
	nowFn func() time.Time // injectable clock for tests
}

// NewNonceCache returns a NonceCache that rejects any nonce seen within ttl.
// A non-positive ttl is replaced with defaultNonceTTL (5 minutes) so the
// cache is always usable and never "never expires".
func NewNonceCache(ttl time.Duration) *NonceCache {
	if ttl <= 0 {
		ttl = defaultNonceTTL
	}
	return &NonceCache{
		ttl:  ttl,
		seen: make(map[string]time.Time),
		nowFn: func() time.Time {
			return time.Now()
		},
	}
}

// CheckAndRemember returns true iff nonce has NOT been seen within the TTL
// window; on true the nonce is recorded with the current timestamp so future
// calls return false until the entry expires. A fresh empty nonce is always
// rejected (it would be trivial to forge and provides no replay protection).
//
// Each call also lazily prunes expired entries so the map stays bounded.
func (c *NonceCache) CheckAndRemember(nonce string) bool {
	if nonce == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.nowFn()
	cutoff := now.Add(-c.ttl)

	// Lazy prune: drop every entry whose timestamp is older than the cutoff.
	for k, t := range c.seen {
		if t.Before(cutoff) {
			delete(c.seen, k)
		}
	}

	if _, exists := c.seen[nonce]; exists {
		return false
	}
	c.seen[nonce] = now
	return true
}

// size returns the current number of tracked nonces. Exposed for tests and
// internal health checks; not part of the stable API.
func (c *NonceCache) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.seen)
}