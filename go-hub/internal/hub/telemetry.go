package hub

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	telemetryStateFilename     = "telemetry_state.json"
	telemetryStateMaxBytes     = 16 << 10
	telemetryExporterQueueSize = 64
)

type telemetryRecord struct {
	Name       string
	Timestamp  time.Time
	Attributes map[string]string
}

type telemetryExportItem struct {
	Record *telemetryRecord
	Done   chan struct{}
}

type telemetryExporter struct {
	client   *http.Client
	endpoint string
	queue    chan telemetryExportItem
}

type otlpAttribute struct {
	Key   string `json:"key"`
	Value struct {
		StringValue string `json:"stringValue"`
	} `json:"value"`
}

type otlpLogRecord struct {
	TimeUnixNano string            `json:"timeUnixNano"`
	SeverityText string            `json:"severityText"`
	Body         map[string]string `json:"body"`
	Attributes   []otlpAttribute   `json:"attributes,omitempty"`
}

type otlpScopeLogs struct {
	Scope struct {
		Name string `json:"name"`
	} `json:"scope"`
	LogRecords []otlpLogRecord `json:"logRecords"`
}

type otlpResourceLogs struct {
	Resource struct {
		Attributes []otlpAttribute `json:"attributes"`
	} `json:"resource"`
	ScopeLogs []otlpScopeLogs `json:"scopeLogs"`
}

type otlpLogsPayload struct {
	ResourceLogs []otlpResourceLogs `json:"resourceLogs"`
}

var telemetryExportAttributeKeys = map[string]struct{}{
	"access_mode":      {},
	"actor":            {},
	"approval_mode":    {},
	"job_id":           {},
	"method":           {},
	"policy_decision":  {},
	"profile_id":       {},
	"result_reference": {},
	"server_id":        {},
	"status":           {},
	"target":           {},
	"tool":             {},
	"trace_id":         {},
	"traceparent":      {},
	"retry_count":      {},
	"retry_outcome":    {},
}

func newTelemetryExporter(endpoint string) (*telemetryExporter, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return nil, nil
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("OTLP endpoint must be an absolute URL without credentials, query or fragment")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopbackServiceHost(parsed.Hostname())) {
		return nil, errors.New("OTLP endpoint must use HTTPS outside loopback")
	}
	if parsed.Path == "" || parsed.Path == "/" {
		parsed.Path = "/v1/logs"
	}
	exporter := &telemetryExporter{client: &http.Client{Timeout: 2 * time.Second}, endpoint: parsed.String(), queue: make(chan telemetryExportItem, telemetryExporterQueueSize)}
	go exporter.run()
	return exporter, nil
}

func (e *telemetryExporter) run() {
	for item := range e.queue {
		if item.Record != nil {
			e.post(*item.Record)
		}
		if item.Done != nil {
			close(item.Done)
		}
	}
}

func (e *telemetryExporter) submit(record telemetryRecord) {
	select {
	case e.queue <- telemetryExportItem{Record: &record}:
	default:
		log.Printf("OTLP telemetry queue full; dropping record")
	}
}

func (e *telemetryExporter) flush(timeout time.Duration) bool {
	if timeout <= 0 {
		timeout = time.Second
	}
	done := make(chan struct{})
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case e.queue <- telemetryExportItem{Done: done}:
	case <-timer.C:
		return false
	}
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

func (e *telemetryExporter) post(record telemetryRecord) {
	var payload otlpLogsPayload
	entry := otlpResourceLogs{}
	entry.Resource.Attributes = []otlpAttribute{{Key: "service.name", Value: struct {
		StringValue string `json:"stringValue"`
	}{StringValue: "gptadmin-hub"}}}
	scope := otlpScopeLogs{}
	scope.Scope.Name = "gptadmin/telemetry"
	attributes := make([]otlpAttribute, 0, len(record.Attributes))
	for key, value := range record.Attributes {
		attribute := otlpAttribute{Key: "gptadmin." + key}
		attribute.Value.StringValue = value
		attributes = append(attributes, attribute)
	}
	scope.LogRecords = []otlpLogRecord{{TimeUnixNano: strconv.FormatInt(record.Timestamp.UnixNano(), 10), SeverityText: "INFO", Body: map[string]string{"stringValue": record.Name}, Attributes: attributes}}
	entry.ScopeLogs = []otlpScopeLogs{scope}
	payload.ResourceLogs = []otlpResourceLogs{entry}
	data, err := json.Marshal(payload)
	if err != nil {
		log.Printf("OTLP telemetry encode failed")
		return
	}
	req, err := http.NewRequest(http.MethodPost, e.endpoint, bytes.NewReader(data))
	if err != nil {
		log.Printf("OTLP telemetry request construction failed")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		log.Printf("OTLP telemetry export failed")
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("OTLP telemetry export returned status=%d", resp.StatusCode)
	}
}

func telemetryScalar(value any) (string, bool) {
	switch value := value.(type) {
	case string:
		return value, true
	case bool:
		return strconv.FormatBool(value), true
	case int:
		return strconv.Itoa(value), true
	case int64:
		return strconv.FormatInt(value, 10), true
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64), true
	default:
		return "", false
	}
}

func sanitizeTelemetryValue(value string) string {
	if strings.Contains(strings.ToLower(value), "://") {
		return ""
	}
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(value))
	if len(value) > 256 {
		return value[:256]
	}
	return value
}

func telemetryRecordFromAudit(event auditEvent) telemetryRecord {
	attributes := map[string]string{}
	for key, value := range event.Fields {
		if _, ok := telemetryExportAttributeKeys[key]; !ok {
			continue
		}
		if scalar, ok := telemetryScalar(value); ok {
			if sanitized := sanitizeTelemetryValue(scalar); sanitized != "" {
				attributes[key] = sanitized
			}
		}
	}
	return telemetryRecord{Name: event.Name, Timestamp: time.Now().UTC(), Attributes: attributes}
}

func (s *Server) enqueueTelemetryAudit(event auditEvent) {
	if s.telemetryExporter != nil {
		s.telemetryExporter.submit(telemetryRecordFromAudit(event))
	}
}

func (s *Server) flushTelemetry(timeout time.Duration) bool {
	if s.telemetryExporter == nil {
		return true
	}
	return s.telemetryExporter.flush(timeout)
}

var activationTelemetryEvents = map[string]struct{}{
	"connection_page_viewed": {},
	"client_connected":       {},
	"first_tool":             {},
	"failure":                {},
}

type telemetryState struct {
	Enabled   bool           `json:"enabled"`
	Counters  map[string]int `json:"counters,omitempty"`
	UpdatedAt time.Time      `json:"updated_at"`
}

func defaultTelemetryState() telemetryState {
	return telemetryState{Counters: map[string]int{}}
}

func loadTelemetryState(path string) (telemetryState, error) {
	state := defaultTelemetryState()
	if path == "" {
		return state, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if len(data) > telemetryStateMaxBytes {
		return state, errors.New("telemetry state file is too large")
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return defaultTelemetryState(), err
	}
	if state.Counters == nil {
		state.Counters = map[string]int{}
	}
	for event, count := range state.Counters {
		if _, ok := activationTelemetryEvents[event]; !ok || count < 0 || count > 1_000_000 {
			delete(state.Counters, event)
		}
	}
	return state, nil
}

func saveTelemetryState(path string, state telemetryState) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".telemetry-state-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func (s *Server) telemetrySnapshot() telemetryState {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.telemetry
	state.Counters = cloneIntMap(state.Counters)
	return state
}

func cloneIntMap(source map[string]int) map[string]int {
	result := make(map[string]int, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func (s *Server) persistTelemetry(state telemetryState) error {
	return saveTelemetryState(s.telemetryPath, state)
}

func (s *Server) recordActivationTelemetry(event string) {
	if _, ok := activationTelemetryEvents[event]; !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.telemetry.Enabled {
		return
	}
	if s.telemetry.Counters == nil {
		s.telemetry.Counters = map[string]int{}
	}
	if s.telemetry.Counters[event] < 1_000_000 {
		s.telemetry.Counters[event]++
	}
	s.telemetry.UpdatedAt = s.now()
	if err := s.persistTelemetry(s.telemetry); err != nil {
		log.Printf("activation telemetry persist failed event=%s err=%v", event, err)
	}
}

func (s *Server) adminTelemetry(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		state := s.telemetrySnapshot()
		writeJSON(w, http.StatusOK, map[string]any{"enabled": state.Enabled, "counters": state.Counters, "updated_at": state.UpdatedAt, "local_only": true})
	case http.MethodPut:
		var req struct {
			Enabled *bool `json:"enabled"`
		}
		if err := readJSON(r, &req); err != nil || req.Enabled == nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "enabled boolean is required"})
			return
		}
		s.mu.Lock()
		state := s.telemetry
		state.Enabled = *req.Enabled
		state.UpdatedAt = s.now()
		s.telemetry = state
		s.mu.Unlock()
		if err := s.persistTelemetry(state); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "failed to persist telemetry preference"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"enabled": state.Enabled, "counters": state.Counters, "local_only": true})
	case http.MethodPost:
		if !strings.HasSuffix(r.URL.Path, "/event") {
			w.Header().Set("Allow", "GET, PUT")
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
			return
		}
		var req struct {
			Event string `json:"event"`
		}
		if err := readJSON(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
			return
		}
		event := strings.TrimSpace(req.Event)
		if _, ok := activationTelemetryEvents[event]; !ok {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "unsupported telemetry event"})
			return
		}
		s.mu.Lock()
		state := s.telemetry
		if !state.Enabled {
			s.mu.Unlock()
			writeJSON(w, http.StatusForbidden, map[string]any{"detail": "activation telemetry is disabled"})
			return
		}
		if state.Counters == nil {
			state.Counters = map[string]int{}
		}
		if state.Counters[event] < 1_000_000 {
			state.Counters[event]++
		}
		state.UpdatedAt = s.now()
		s.telemetry = state
		s.mu.Unlock()
		if err := s.persistTelemetry(state); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "failed to persist telemetry event"})
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "event": event, "local_only": true})
	default:
		w.Header().Set("Allow", "GET, PUT, POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
	}
}
