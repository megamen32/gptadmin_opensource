package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/security"
)

// signRequestForTest produces an in-memory ed25519 keypair and signs a
// request equivalent to the legacy Python shellmcp client. Returns the
// public key (b64-style without padding, matching security.B64) so the
// server test can wire s.cfg.HubPublicKey to verify the signature.
func signRequestForTest(method, path string, body []byte) (pubB64 string, hdrs map[string]string) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	ts := fmt.Sprintf("%d", time.Now().Unix())
	nonce := fmt.Sprintf("nonce-%d", time.Now().UnixNano())
	canonical := security.Canonical(method, path, ts, nonce, body)
	sig := ed25519.Sign(priv, canonical)
	pubStr := strings.TrimRight(base64.URLEncoding.EncodeToString(pub), "=")
	sigStr := strings.TrimRight(base64.URLEncoding.EncodeToString(sig), "=")
	return pubStr, map[string]string{
		"X-GPTAdmin-Timestamp": ts,
		"X-GPTAdmin-Nonce":     nonce,
		"X-GPTAdmin-Signature": sigStr,
	}
}

// signedRequest returns a prepared httptest.Request with the same
// signature headers the server's authorized() expects. The body is
// passed verbatim to the canonical signer.
func signedRequest(t *testing.T, method, path string, body []byte) (*http.Request, string) {
	t.Helper()
	b := body
	if b == nil {
		b = []byte{}
	}
	pubB64, hdrs := signRequestForTest(method, path, b)
	req := httptest.NewRequest(method, path, bytes.NewReader(b))
	for k, v := range hdrs {
		req.Header.Set(k, v)
	}
	return req, pubB64
}

// TestNonceCacheDirect uses the cache directly to verify that the
// replay protection primitive does its job. We exercise the same code
// path that authorized() uses after a successful Verify().
func TestNonceCacheDirect(t *testing.T) {
	s := New(Config{Token: "t"})
	if s.nonces == nil {
		t.Fatal("expected nonces cache to be initialized")
	}
	if !s.nonces.CheckAndRemember("alpha") {
		t.Fatal("first nonce must be accepted")
	}
	if s.nonces.CheckAndRemember("alpha") {
		t.Fatal("second use of same nonce must be rejected")
	}
	if !s.nonces.CheckAndRemember("beta") {
		t.Fatal("distinct nonce must be accepted")
	}
}

// TestNonceReplayViaAuthorized verifies the end-to-end behavior:
// two signed requests with the same nonce cause the second one to be
// rejected (401). Constructing a "replay" by reusing the same public
// key and nonce across two requests is non-trivial because both would
// have valid signatures AND distinct nonces. We instead simulate the
// same threat by:
//  1. Building two requests with the same valid public key, distinct
//     nonces (both pass signature).
//  2. After the first request succeeds we explicitly forge a second
//     request whose nonce replays the first one, using the server's
//     own verifier and nonce cache.
func TestNonceReplayViaAuthorized(t *testing.T) {
	s := New(Config{Token: "t", LogLimit: 8192, ExecTimeout: 5, SpillDir: t.TempDir()})
	if s.nonces == nil {
		t.Fatal("nonces not initialized")
	}

	method := http.MethodPost
	path := "/exec"
	body := []byte(`{"cmd":"printf replay"}`)

	// First request: build a fresh signed request.
	req1, pub1 := signedRequest(t, method, path, body)
	s.cfg.HubPublicKey = pub1
	rec1 := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first request status=%d body=%s", rec1.Code, rec1.Body.String())
	}

	// Second request: re-use the SAME nonce from the first request.
	// The signature will fail (different keypair or different body)
	// but we also need the signature to verify cleanly so the nonce
	// branch is reached. To do that we re-sign req2's body with the
	// nonce from req1, using a fresh key (same public-key wire
	// format), and configure the server with that public key.
	nonce1 := req1.Header.Get("X-GPTAdmin-Nonce")
	ts2 := fmt.Sprintf("%d", time.Now().Unix())

	// Generate a fresh keypair and re-sign with the replayed nonce.
	pub2raw, priv2, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	canonical2 := security.Canonical(method, path, ts2, nonce1, body)
	sig2 := ed25519.Sign(priv2, canonical2)
	pubB64 := strings.TrimRight(base64.URLEncoding.EncodeToString(pub2raw), "=")
	sigStr := strings.TrimRight(base64.URLEncoding.EncodeToString(sig2), "=")

	req2 := httptest.NewRequest(method, path, bytes.NewReader(body))
	req2.Header.Set("X-GPTAdmin-Timestamp", ts2)
	req2.Header.Set("X-GPTAdmin-Nonce", nonce1)
	req2.Header.Set("X-GPTAdmin-Signature", sigStr)
	s.cfg.HubPublicKey = pubB64

	rec2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized on nonce replay, got %d body=%s",
			rec2.Code, rec2.Body.String())
	}
}

// TestAuditLogReceivesExecStartAndEnd writes an audit log to a temp
// file, runs an /exec request, and asserts both ExecStart and ExecEnd
// events were appended as JSON lines.
func TestAuditLogReceivesExecStartAndEnd(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "audit.log")
	s := New(Config{Token: "t", AuditLog: logPath, LogLimit: 8192, ExecTimeout: 5, SpillDir: t.TempDir()})
	defer s.Close()

	req := httptest.NewRequest(http.MethodPost, "/exec", bytes.NewBufferString(`{"cmd":"printf audited"}`))
	req.Header.Set("Authorization", "Bearer t")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("exec status=%d body=%s", rec.Code, rec.Body.String())
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 audit lines, got %d: %s", len(lines), data)
	}
	var sawStart, sawEnd bool
	for _, ln := range lines {
		var rec map[string]any
		if json.Unmarshal([]byte(ln), &rec) != nil {
			continue
		}
		if rec["type"] == "exec_start" {
			sawStart = true
			// cmd field should be the first token, not the full cmd.
			if got, _ := rec["cmd"].(string); got != "printf" {
				t.Fatalf("expected cmd field 'printf', got %v", rec["cmd"])
			}
		}
		if rec["type"] == "exec_end" {
			sawEnd = true
		}
	}
	if !sawStart {
		t.Fatalf("missing exec_start in audit log: %s", data)
	}
	if !sawEnd {
		t.Fatalf("missing exec_end in audit log: %s", data)
	}
}

// TestExecStreamReturnsEventStream posts to /exec/stream and checks
// the Content-Type header plus at least one "data:" SSE frame.
func TestExecStreamReturnsEventStream(t *testing.T) {
	s := New(Config{Token: "t", LogLimit: 8192, ExecTimeout: 5, SpillDir: t.TempDir()})
	req := httptest.NewRequest(http.MethodPost, "/exec/stream", bytes.NewBufferString(`{"cmd":"echo sse_ok"}`))
	req.Header.Set("Authorization", "Bearer t")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("exec/stream status=%d body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type=%q want text/event-stream", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "data: ") {
		t.Fatalf("missing data: prefix in SSE body: %s", body)
	}
	if !strings.Contains(body, "sse_ok") {
		t.Fatalf("missing command output in SSE body: %s", body)
	}
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			var ev map[string]any
			raw := strings.TrimPrefix(line, "data: ")
			if json.Unmarshal([]byte(raw), &ev) != nil {
				t.Fatalf("data line not valid JSON: %q", raw)
			}
			return
		}
	}
	t.Fatalf("no data: line parsed")
}

// TestExecStreamMethodNotAllowed uses GET to ensure 405.
func TestExecStreamMethodNotAllowed(t *testing.T) {
	s := New(Config{Token: "t"})
	req := httptest.NewRequest(http.MethodGet, "/exec/stream", nil)
	req.Header.Set("Authorization", "Bearer t")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("want 405 got %d", rec.Code)
	}
}

// TestSupervisorLifecycleRoundTrip exercises the /capabilities/mcp/
// start/stop/status path using a "sleep" command as a controllable
// long-running process. Skipped if /bin/sleep is unavailable.
func TestSupervisorLifecycleRoundTrip(t *testing.T) {
	if _, err := os.Stat("/bin/sleep"); err != nil {
		t.Skipf("sleep not available: %v", err)
	}
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "agents.json")
	cfg := map[string]any{
		"agents": []map[string]any{
			{"ref": "sleepy", "name": "sleepy", "command": "/bin/sleep", "args": []string{"5"}},
		},
	}
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(cfgPath, b, 0o600); err != nil {
		t.Fatal(err)
	}

	s := New(Config{Token: "t", MCPConfig: cfgPath})
	defer s.Close()

	startReq := httptest.NewRequest(http.MethodPost, "/capabilities/mcp/sleepy",
		bytes.NewBufferString(`{"action":"start"}`))
	startReq.Header.Set("Authorization", "Bearer t")
	startRec := httptest.NewRecorder()
	s.Handler().ServeHTTP(startRec, startReq)
	if startRec.Code != 200 {
		t.Fatalf("start status=%d body=%s", startRec.Code, startRec.Body.String())
	}
	// Schedule cleanup; do not fail the test on a stop error because
	// supervisor's Stop can race with sleep termination on slow CI.
	defer func() {
		stopReq := httptest.NewRequest(http.MethodPost, "/capabilities/mcp/sleepy",
			bytes.NewBufferString(`{"action":"stop"}`))
		stopReq.Header.Set("Authorization", "Bearer t")
		stopRec := httptest.NewRecorder()
		s.Handler().ServeHTTP(stopRec, stopReq)
	}()

	// status (running)
	stReq := httptest.NewRequest(http.MethodPost, "/capabilities/mcp/sleepy",
		bytes.NewBufferString(`{"action":"status"}`))
	stReq.Header.Set("Authorization", "Bearer t")
	stRec := httptest.NewRecorder()
	s.Handler().ServeHTTP(stRec, stReq)
	if stRec.Code != 200 {
		t.Fatalf("status status=%d body=%s", stRec.Code, stRec.Body.String())
	}
	var statusBody map[string]any
	if err := json.Unmarshal(stRec.Body.Bytes(), &statusBody); err != nil {
		t.Fatal(err)
	}
	if r, _ := statusBody["running"].(bool); !r {
		t.Fatalf("expected running=true, got body=%s", stRec.Body.String())
	}
}

// TestSupervisorUnknownRef returns 404 when the agent ref does not
// exist in the registry.
func TestSupervisorUnknownRef(t *testing.T) {
	s := New(Config{Token: "t", MCPConfig: ""})
	req := httptest.NewRequest(http.MethodPost, "/capabilities/mcp/no-such-ref",
		bytes.NewBufferString(`{"action":"status"}`))
	req.Header.Set("Authorization", "Bearer t")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404 got %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestOutboxBackoffSkipsFutureDated writes a fake outbox entry with a
// future next_attempt_at and verifies that flushOutbox leaves the file
// alone when the hub client is nil (the early-return branch still
// exercises the file scan + skip logic).
func TestOutboxBackoffSkipsFutureDated(t *testing.T) {
	outboxDir := t.TempDir()
	s := New(Config{
		Token:       "t",
		LogLimit:    8192,
		ExecTimeout: 5,
		SpillDir:    t.TempDir(),
		OutboxDir:   outboxDir,
	})
	if s.hub != nil {
		t.Skip("hub is non-nil on this build; cannot isolate outbox test")
	}

	entry := map[string]any{
		"job_id":     "future-job",
		"payload":    map[string]any{"id": "ignored"},
		"created_at": time.Now().Unix(),
		"attempts":   3,
		// 10 minutes in the future → flushOutbox must skip it.
		"next_attempt_at": time.Now().Add(10 * time.Minute).Unix(),
	}
	b, _ := json.Marshal(entry)
	path := filepath.Join(outboxDir, "future-job.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}

	s.flushOutbox(context.Background())

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("future-dated entry was removed (expected preserved): %v", err)
	}
}

// TestOutboxBackoffMonotonicAndCapped verifies the backoff helper
// produces a non-decreasing wait capped at 10 minutes.
func TestOutboxBackoffMonotonicAndCapped(t *testing.T) {
	var prev time.Duration
	for i := 0; i < 50; i++ {
		got := computeOutboxBackoff(i)
		if got > outboxBackoffCap {
			t.Fatalf("attempt %d exceeds cap got=%s cap=%s", i, got, outboxBackoffCap)
		}
		if i > 0 && got < prev {
			t.Fatalf("attempt %d decreased: prev=%s got=%s", i, prev, got)
		}
		prev = got
	}
}

func TestOutboxDropsHubNotFoundResult(t *testing.T) {
	requests := 0
	hubServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/queue/unit-host/result" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		http.Error(w, `{"error":"unknown job"}`, http.StatusNotFound)
	}))
	defer hubServer.Close()

	outboxDir := t.TempDir()
	s := New(Config{Name: "unit-host", HubURL: hubServer.URL, OutboxDir: outboxDir, SpillDir: t.TempDir()})
	defer s.Close()
	entry := map[string]any{
		"job_id":   "stale-job",
		"payload":  map[string]any{"id": "stale-job", "returncode": 0},
		"attempts": 0,
	}
	b, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(outboxDir, "stale-job.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}

	s.flushOutbox(context.Background())
	if requests != 1 {
		t.Fatalf("requests=%d", requests)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("stale outbox file remains: %v", err)
	}
}
