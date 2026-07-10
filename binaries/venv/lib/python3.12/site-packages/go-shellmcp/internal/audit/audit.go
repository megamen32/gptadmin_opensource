// Package audit provides a goroutine-safe structured audit logger that
// mirrors the legacy Python shellmcp's SHELLMCP_AUDIT_LOG behavior.
//
// Each call to Event emits a single JSON line containing a UTC RFC3339
// timestamp (ts), an event type (type), and the caller-provided fields.
// When the logger is constructed with an empty path (or the file cannot
// be opened) the Logger is a no-op and Event/Close are safe to call.
package audit

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Standard event type constants. Event type names mirror the Python
// implementation where a stable name exists so log consumers keep
// working across the rewrite.
const (
	ExecStart      = "exec_start"
	ExecEnd        = "exec_end"
	AuthOK         = "auth_ok"
	AuthFail       = "auth_fail"
	UpdateApplied  = "update_applied"
	UpdateFailed   = "update_failed"
	ServiceAction  = "service_action"
	HeartbeatSent  = "heartbeat_sent"
	PollJob        = "poll_job"
	GenericError   = "error"
)

// Logger writes one JSON line per Event to an append-only file. A nil
// *Logger is a valid no-op so callers can call Event unconditionally
// without checking for nil. All exported methods are goroutine-safe.
type Logger struct {
	mu     sync.Mutex
	enc    *json.Encoder
	file   io.Closer
	closed bool
}

// New opens path in append-only mode, creating parent directories as
// needed. An empty path returns a no-op logger with no error so callers
// can wire the logger up unconditionally (e.g. when the audit env var
// is unset). On open failure a no-op logger is returned together with
// the error so the caller can decide to log the failure but still keep
// running.
func New(path string) (*Logger, error) {
	if path == "" {
		return &Logger{}, nil
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return &Logger{}, fmt.Errorf("audit: create parent dir %q: %w", dir, err)
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return &Logger{}, fmt.Errorf("audit: open %q: %w", path, err)
	}
	enc := json.NewEncoder(f)
	// Match the Python output: keys sorted, no HTML escaping, no
	// trailing newline (we add one explicitly so each event is a
	// single line).
	enc.SetEscapeHTML(false)
	return &Logger{enc: enc, file: f}, nil
}

// Event writes a single audit record as one JSON line. Calling Event
// on a nil receiver is a no-op, which lets callers wire the logger
// unconditionally:
//
//	auditLog.Event(audit.ExecStart, map[string]any{"cmd": "ls"})
//
// ts and type are added automatically; caller-supplied fields can
// override them if absolutely necessary but doing so is discouraged.
func (l *Logger) Event(typ string, fields map[string]any) {
	if l == nil || l.enc == nil {
		return
	}
	if fields == nil {
		fields = map[string]any{}
	}
	fields["ts"] = time.Now().UTC().Format(time.RFC3339Nano)
	fields["type"] = typ

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	if err := l.enc.Encode(fields); err != nil {
		// Best-effort: drop the record silently rather than crashing
		// the calling request path. Callers needing delivery
		// guarantees should layer their own retry around Event.
		return
	}
}

// Close flushes and closes the underlying file. Calling Close on a
// nil receiver, on a no-op logger, or more than once is safe.
func (l *Logger) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	return l.file.Close()
}