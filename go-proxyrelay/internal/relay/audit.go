package relay

import (
	"encoding/json"
	"io"
	"sync"
	"time"
)

// AuditEvent contains relay metadata only. Tickets, targets, and payloads are
// deliberately absent so callers cannot accidentally log them.
type AuditEvent struct {
	Time         time.Time `json:"time"`
	Event        string    `json:"event"`
	CapabilityID string    `json:"capability_id,omitempty"`
	StreamID     string    `json:"stream_id,omitempty"`
	ProfileID    string    `json:"profile_id,omitempty"`
	AgentID      string    `json:"agent_id,omitempty"`
	Role         string    `json:"role,omitempty"`
	Reason       string    `json:"reason,omitempty"`
}

// AuditFunc receives one metadata-only relay event.
type AuditFunc func(AuditEvent)

// NewJSONAuditLogger returns a concurrency-safe JSON-lines audit sink.
func NewJSONAuditLogger(writer io.Writer) AuditFunc {
	var mu sync.Mutex
	encoder := json.NewEncoder(writer)
	return func(event AuditEvent) {
		mu.Lock()
		defer mu.Unlock()
		_ = encoder.Encode(event)
	}
}
