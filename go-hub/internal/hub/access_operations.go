package hub

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

// Record intent before applying a management mutation and completion before
// responding. Never include request bodies, responses or credentials. A crash
// leaves an explicit started operation instead of pretending nothing happened.
func (s *Server) trackAccessOperation(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			next(w, r)
			return
		}
		id := newID()
		fields := map[string]any{"operation_id": id, "actor": s.actorForRequest(r), "action": r.Method, "target": r.URL.Path, "status": "started"}
		if err := s.appendAccessOperation(fields); err != nil {
			writeJSON(w, 503, map[string]any{"detail": "operation history unavailable; no change applied"})
			return
		}
		captured := &accessToolResponse{header: make(http.Header)}
		next(captured, r)
		if captured.status == 0 {
			captured.status = 200
		}
		fields = cloneMap(fields)
		fields["http_status"] = captured.status
		fields["status"] = "completed"
		if captured.status >= 400 {
			fields["status"] = "failed"
		}
		if err := s.appendAccessOperation(fields); err != nil {
			writeJSON(w, 503, map[string]any{"detail": "change handled but completion history unavailable; inspect operation before retry", "operation_id": id, "applied": captured.status < 400})
			return
		}
		for key, values := range captured.header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.Header().Set("X-Operation-ID", id)
		w.WriteHeader(captured.status)
		_, _ = w.Write(captured.body.Bytes())
	}
}

func (s *Server) appendAccessOperation(fields map[string]any) error {
	event := auditEvent{Time: time.Now().UTC().Format(time.RFC3339Nano), Name: "access_operation", Fields: fields}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if path := s.auditStatePath(); path != "" {
		err = withStateFileLock(path, func() error {
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				return err
			}
			file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
			if err != nil {
				return err
			}
			defer file.Close()
			if _, err = file.Write(data); err != nil {
				return err
			}
			return file.Sync()
		})
		if err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.audit = append(s.audit, event)
	max := s.hubSettingIntLocked("audit_max_events")
	if len(s.audit) > max {
		s.audit = s.audit[len(s.audit)-max:]
	}
	s.mu.Unlock()
	return nil
}

func (s *Server) adminOperations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]any{"detail": "GET required"})
		return
	}
	limit, offset := 50, 0
	var err error
	if v := r.URL.Query().Get("limit"); v != "" {
		limit, err = strconv.Atoi(v)
		if err != nil || limit < 1 || limit > 200 {
			writeJSON(w, 400, map[string]any{"detail": "limit must be 1..200"})
			return
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		offset, err = strconv.Atoi(v)
		if err != nil || offset < 0 {
			writeJSON(w, 400, map[string]any{"detail": "offset must be non-negative"})
			return
		}
	}
	operations := map[string]map[string]any{}
	add := func(event auditEvent) {
		if event.Name != "access_operation" {
			return
		}
		id := firstString(event.Fields, "operation_id")
		if id == "" {
			return
		}
		current := operations[id]
		if current == nil {
			current = cloneMap(event.Fields)
			current["started_at"] = event.Time
			operations[id] = current
		}
		for key, value := range event.Fields {
			current[key] = value
		}
		current["updated_at"] = event.Time
	}
	path := s.auditStatePath()
	if path != "" {
		file, openErr := os.Open(path)
		if openErr != nil && !errors.Is(openErr, os.ErrNotExist) {
			writeJSON(w, 503, map[string]any{"detail": "operation history unavailable"})
			return
		}
		if file != nil {
			defer file.Close()
			reader := bufio.NewReader(file)
			for {
				line, readErr := reader.ReadBytes('\n')
				if len(line) > 0 && bytes.HasSuffix(line, []byte{'\n'}) {
					var event auditEvent
					if err := json.Unmarshal(line, &event); err != nil {
						writeJSON(w, 503, map[string]any{"detail": "invalid operation history record"})
						return
					}
					add(event)
				}
				if readErr != nil {
					if !errors.Is(readErr, io.EOF) {
						writeJSON(w, 503, map[string]any{"detail": "operation history read failed"})
						return
					}
					break
				}
			}
		}
	} else {
		s.mu.Lock()
		for _, event := range s.audit {
			add(event)
		}
		s.mu.Unlock()
	}
	items := make([]map[string]any, 0, len(operations))
	for _, item := range operations {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return firstString(items[i], "started_at") > firstString(items[j], "started_at") })
	total := len(items)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"operations": items[offset:end], "total": total, "offset": offset, "limit": limit})
}
