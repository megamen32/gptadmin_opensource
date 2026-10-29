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
