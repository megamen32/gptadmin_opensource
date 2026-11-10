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

	"github.com/megamen32/gptadmin/go-shellmcp/internal/storagebudget"
)

// Standard event type constants. Event type names mirror the Python
// implementation where a stable name exists so log consumers keep
// working across the rewrite.
const (
	ExecStart     = "exec_start"
	ExecEnd       = "exec_end"
	AuthOK        = "auth_ok"
	AuthFail      = "auth_fail"
	UpdateApplied = "update_applied"
	UpdateFailed  = "update_failed"
	ServiceAction = "service_action"
	HeartbeatSent = "heartbeat_sent"
	PollJob       = "poll_job"
	GenericError  = "error"
)

// Logger writes one JSON line per Event to an append-only file. A nil
// *Logger is a valid no-op so callers can call Event unconditionally
// without checking for nil. All exported methods are goroutine-safe.
type Logger struct {
	mu         sync.Mutex
	enc        *json.Encoder
	file       *os.File
	path       string
	maxBytes   int64
	closed     bool
	preserve   bool
	afterWrite func()
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
	limit, err := storagebudget.FilesystemLimit(filepath.Dir(path))
	if err != nil {
		limit = 500 << 20
	}
	return NewWithLimit(path, limit)
}

func NewWithLimit(path string, maxBytes int64) (*Logger, error) {
	if path == "" {
		return &Logger{}, nil
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return &Logger{}, fmt.Errorf("audit: create parent dir %q: %w", dir, err)
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return &Logger{}, fmt.Errorf("audit: open %q: %w", path, err)
	}
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	return &Logger{enc: enc, file: f, path: path, maxBytes: maxBytes}, nil
}

// SetAfterWrite registers a bounded-storage hook. The callback runs after the
// logger mutex is released so it may safely inspect or prune managed files.
func (l *Logger) SetAfterWrite(callback func()) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.afterWrite = callback
	l.mu.Unlock()
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
	_ = l.writeEvent(typ, fields, false)
}

// Preserve prevents in-place rotation from discarding durable execution history.
// The server's existing storage hook protects this file as mandatory data.
func (l *Logger) Preserve() {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.preserve = true
	l.mu.Unlock()
}

// EventDurable returns only after the record has been written and fsynced. A
// disabled, failed or closed logger is an error, never a successful no-op.
func (l *Logger) EventDurable(typ string, fields map[string]any) error {
	return l.writeEvent(typ, fields, true)
}

func (l *Logger) writeEvent(typ string, fields map[string]any, durable bool) error {
	if l == nil {
		if durable {
			return fmt.Errorf("audit is unavailable")
		}
		return nil
	}
	record := make(map[string]any, len(fields)+2)
	for k, v := range fields {
		record[k] = v
	}
	record["ts"] = time.Now().UTC().Format(time.RFC3339Nano)
	record["type"] = typ
	l.mu.Lock()
	if l.closed || l.enc == nil {
		l.mu.Unlock()
		if durable {
			return fmt.Errorf("audit is unavailable or closed")
		}
		return nil
	}
	if durable {
		l.preserve = true
		if err := l.verifyDurablePath(); err != nil {
			l.mu.Unlock()
			return err
		}
	}
	if !l.preserve && l.maxBytes > 0 {
		if info, err := l.file.Stat(); err == nil && info.Size() >= l.maxBytes {
			if err := l.file.Truncate(0); err == nil {
				_, _ = l.file.Seek(0, io.SeekStart)
			}
		}
	}
	if err := l.enc.Encode(record); err != nil {
		l.mu.Unlock()
		return err
	}
	if durable {
		if err := l.file.Sync(); err != nil {
			l.mu.Unlock()
			return err
		}
		if err := l.verifyDurablePath(); err != nil {
			l.mu.Unlock()
			return err
		}
	}
	callback := l.afterWrite
	l.mu.Unlock()
	if callback != nil {
		callback()
	}
	return nil
}

// Called under mu. Fsync on a deleted or renamed inode succeeds, but no longer
// proves an addressable audit record. Never reopen, replace or replay silently.
func (l *Logger) verifyDurablePath() error {
	opened, err := l.file.Stat()
	if err != nil {
		return fmt.Errorf("audit descriptor unavailable: %w", err)
	}
	named, err := os.Stat(l.path)
	if err != nil {
		return fmt.Errorf("audit pathname unavailable: %w", err)
	}
	if !os.SameFile(opened, named) {
		return fmt.Errorf("audit pathname no longer names the open journal")
	}
	return nil
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
