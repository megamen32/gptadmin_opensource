package hub

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"time"
)

const auditHistoryReadBytes = 4 << 20

func appendAuditWindow(events []auditEvent, event auditEvent, limit int) []auditEvent {
	if limit < 1 {
		limit = 1
	}
	if excess := len(events) - limit + 1; excess > 0 {
		// Reslicing alone keeps old maps reachable through the backing array.
		clear(events[:excess])
		events = events[excess:]
	}
	if cap(events) < limit || cap(events) > 2*limit {
		compact := make([]auditEvent, len(events), 2*limit)
		copy(compact, events)
		events = compact
	}
	return append(events, event)
}

func (s *Server) appendAuditMemoryLocked(event auditEvent) {
	s.audit = appendAuditWindow(s.audit, event, s.hubSettingIntLocked("audit_max_events"))
}

// History is optional UI data, never a startup dependency. Read only the
// bounded file tail and decode the requested recent window, newest first.
// A partial initial/final record is not a complete persisted event.
func readAuditTail(path string, limit int) ([]auditEvent, error) {
	if path == "" || limit <= 0 {
		return nil, nil
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := min(info.Size(), int64(auditHistoryReadBytes))
	start := info.Size() - size
	data := make([]byte, int(size))
	n, err := f.ReadAt(data, start)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	data = data[:n]
	begin := 0
	if start > 0 {
		begin = bytes.IndexByte(data, '\n') + 1
		if begin == 0 {
			return nil, nil
		}
	}
	end := bytes.LastIndexByte(data, '\n')
	var events []auditEvent
	for end >= begin && len(events) < limit {
		previous := bytes.LastIndexByte(data[begin:end], '\n') + begin
		line := bytes.TrimSpace(data[previous+1 : end])
		if len(line) > 0 {
			var event auditEvent
			if err := json.Unmarshal(line, &event); err != nil {
				return nil, fmt.Errorf("decode recent audit event: %w", err)
			}
			events = append(events, event)
		}
		end = previous
	}
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}
	return events, nil
}

func (s *Server) auditSnapshot() ([]auditEvent, error) {
	s.mu.Lock()
	current := append([]auditEvent(nil), s.audit...)
	limit := s.hubSettingIntLocked("audit_max_events")
	path := s.auditStatePath()
	s.mu.Unlock()
	history, err := readAuditTail(path, limit)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(history))
	for _, event := range history {
		encoded, _ := json.Marshal(event)
		seen[string(encoded)] = true
	}
	// Include current events whose disk append failed, without duplicating
	// successfully persisted events. Do not retain decoded history in Server.
	for _, event := range current {
		encoded, _ := json.Marshal(event)
		if !seen[string(encoded)] {
			history = append(history, event)
		}
	}
	sort.SliceStable(history, func(i, j int) bool {
		left, _ := time.Parse(time.RFC3339Nano, history[i].Time)
		right, _ := time.Parse(time.RFC3339Nano, history[j].Time)
		return left.Before(right)
	})
	if excess := len(history) - limit; excess > 0 {
		clear(history[:excess])
		history = history[excess:]
	}
	return history, nil
}
