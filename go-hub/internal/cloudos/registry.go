package cloudos

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// Registry manages connected computers in a thread-safe in-memory store.
type Registry struct {
	mu        sync.RWMutex
	computers map[string]*Computer
}

// NewRegistry creates an empty computer registry.
func NewRegistry() *Registry {
	return &Registry{
		computers: make(map[string]*Computer),
	}
}

// Register adds or updates a computer connection. If a computer with the
// same ID already exists its fields are overwritten.
func (r *Registry) Register(c *Computer) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Direct agents reconnect after a Hub restart or a local service restart.
	// Keep one canonical record for the same agent session, while allowing
	// different computers with the same OS or display name to coexist.
	if c.Endpoint != "" {
		for id, existing := range r.computers {
			if existing.Endpoint != "" && c.SessionID != "" && existing.SessionID == c.SessionID {
				delete(r.computers, id)
			}
		}
	}
	r.computers[c.ID] = c
}

// Deregister removes a computer from the registry. It is a no-op if the
// computer does not exist.
func (r *Registry) Deregister(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.computers, id)
}

// Get retrieves a computer by ID. The returned pointer references the
// internal storage and must not be mutated directly.
func (r *Registry) Get(id string) (*Computer, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	c, ok := r.computers[id]
	if !ok {
		return nil, false
	}
	return c, true
}

// List returns a snapshot of all registered computers. Modifications to
// the returned slice do not affect the registry.
func (r *Registry) List() []*Computer {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]*Computer, 0, len(r.computers))
	for _, c := range r.computers {
		out = append(out, c)
	}
	return out
}

// UpdateLastSeen sets the LastSeen field of a computer to the current
// UTC time in RFC 3339 format. Returns false if the computer is not
// registered.
func (r *Registry) UpdateLastSeen(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	c, ok := r.computers[id]
	if !ok {
		return false
	}
	c.LastSeen = time.Now().UTC().Format(time.RFC3339)
	return true
}

// newComputerID generates a random 16-byte hex computer ID.
func newComputerID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "comp-" + hex.EncodeToString(b)
}

// newStreamID generates a random 12-byte hex stream ID.
func newStreamID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// newJTI generates a random 16-byte hex JTI (JWT Token ID) for
// gpr1 ticket replay protection.
func newJTI() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
