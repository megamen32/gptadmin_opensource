package hub

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOTLPExporterCorrelatesAuditWithoutSensitiveFields(t *testing.T) {
	requests := make(chan map[string]any, 16)
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("collector request method=%s content-type=%q", r.Method, r.Header.Get("Content-Type"))
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read collector body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("decode OTLP body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		requests <- payload
		w.WriteHeader(http.StatusAccepted)
	}))
	defer collector.Close()

	s := New(Config{CtlToken: "ctl", TelemetryOTLPEndpoint: collector.URL + "/v1/logs", DefaultTimeout: time.Second, PollMaxTimeout: time.Second})
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"demo","arguments":{}}}`))
	req.Header.Set("Authorization", "Bearer ctl")
	req.Header.Set("X-Request-ID", "trace-otlp-123")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("MCP status=%d body=%s", w.Code, w.Body.String())
	}
	if !s.flushTelemetry(time.Second) {
		t.Fatal("OTLP exporter did not flush")
	}

	deadline := time.After(time.Second)
	for {
		select {
		case payload := <-requests:
			encoded, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			body := string(encoded)
			if strings.Contains(body, "tool_policy_decision") {
				for _, marker := range []string{"trace-otlp-123", "demo", "tool_policy_decision"} {
					if !strings.Contains(body, marker) {
						t.Fatalf("OTLP payload missing %q: %s", marker, body)
					}
				}
				for _, forbidden := range []string{"arguments", "password", "token", "secret", "command", "payload", "https://"} {
					if strings.Contains(strings.ToLower(body), forbidden) {
						t.Fatalf("OTLP payload exposed forbidden material %q: %s", forbidden, body)
					}
				}
				return
			}
		case <-deadline:
			t.Fatal("collector did not receive OTLP policy record")
		}
	}
}

func TestOTLPExporterRejectsInsecureExternalEndpoint(t *testing.T) {
	s := New(Config{TelemetryOTLPEndpoint: "http://collector.example/v1/logs"})
	if s.telemetryExporter != nil {
		t.Fatal("insecure external OTLP endpoint enabled")
	}
}

func TestTelemetryRecordDropsURLAttributes(t *testing.T) {
	record := telemetryRecordFromAudit(auditEvent{Time: time.Now().Format(time.RFC3339), Name: "tool_policy_decision", Fields: map[string]any{
		"target": "https://internal.example/agent",
		"tool":   "demo",
	}})
	if _, ok := record.Attributes["target"]; ok {
		t.Fatalf("telemetry retained URL target: %v", record.Attributes)
	}
}
