package hub

import (
	"context"
	"github.com/coder/websocket"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNodeWriteFailureNeverFallsBackToHTTP(t *testing.T) {
	var delivered atomic.Int32
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { delivered.Add(1) }))
	defer fallback.Close()
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		<-r.Context().Done()
	}))
	defer wsServer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	conn := &nodeConn{name: "shell:peer", ws: ws, done: make(chan struct{}), pending: map[string]chan nodeConnResult{}}
	ws.CloseNow()
	s := peerTestHub(t, "token", map[string]string{"shell:peer": fallback.URL})
	s.nodeConns.put(conn)
	req := httptest.NewRequest(http.MethodPost, "/mcp-relay/call", nil)
	req.Header.Set("Authorization", "Bearer token")
	w := httptest.NewRecorder()
	if !s.forwardNodePeerRequest(w, req, "shell:peer", []byte(`{"target":"shell:peer","tool":"shell_exec"}`), "/mcp-relay/call") {
		t.Fatal("write failure was not handled")
	}
	if w.Code != http.StatusBadGateway || delivered.Load() != 0 || !strings.Contains(w.Body.String(), `"outcome":"unknown"`) {
		t.Fatalf("unsafe fallback: status=%d fallback=%d body=%s", w.Code, delivered.Load(), w.Body.String())
	}
}

func TestNodePeerCancellationPreservesUnknownOutcome(t *testing.T) {
	for _, readingJob := range []bool{false, true} {
		t.Run(map[bool]string{false: "call", true: "read_job"}[readingJob], func(t *testing.T) {
			received := make(chan struct{})
			fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ws, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer ws.CloseNow()
				if _, _, err = ws.Read(r.Context()); err == nil {
					close(received)
				}
				<-r.Context().Done()
			}))
			defer fixture.Close()
			ws, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(fixture.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer ws.CloseNow()
			conn := &nodeConn{name: "shell:peer", ws: ws, done: make(chan struct{}), pending: map[string]chan nodeConnResult{}}
			s := managedPeerUnitServer(conn)
			ctx, cancel := context.WithCancel(context.Background())
			req := httptest.NewRequest(http.MethodPost, "/mcp-relay/call", nil).WithContext(ctx)
			w := httptest.NewRecorder()
			done := make(chan struct{})
			go func() {
				s.forwardOverNodeConn(w, req, "shell:peer", "https://peer.example", []byte(`{}`), "/mcp-relay/call", readingJob)
				close(done)
			}()
			<-received
			cancel()
			<-done
			if w.Code != http.StatusGatewayTimeout || !strings.Contains(w.Body.String(), `"owner_target":"shell:peer"`) || strings.Contains(w.Body.String(), `"outcome":"unknown"`) == readingJob {
				t.Fatalf("wrong cancellation receipt: %d %s", w.Code, w.Body.String())
			}
			conn.pendingMu.Lock()
			pending := len(conn.pending)
			conn.pendingMu.Unlock()
			if pending != 0 {
				t.Fatalf("pending calls leaked: %d", pending)
			}
		})
	}
}

func TestCodeModePreservesUnknownPeerReceipt(t *testing.T) {
	response := map[string]any{"detail": "receipt unavailable", "outcome": "unknown", "retry_policy": "inspect owner", "owner_target": "shell:peer", "owner_endpoint": "https://peer.example"}
	payload := codeModeCallPayload(response, http.StatusGatewayTimeout, "shell:peer", "shell_exec")
	for _, key := range []string{"outcome", "retry_policy", "owner_target", "owner_endpoint"} {
		if payload[key] != response[key] {
			t.Fatalf("%s lost: %#v", key, payload)
		}
	}
}
