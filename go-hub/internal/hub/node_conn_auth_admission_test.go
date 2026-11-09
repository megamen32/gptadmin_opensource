package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// A forwarded call must not retain the writer's generation read lock while
// the owner calls back to that writer for bearer admission. An intervening
// snapshot writer makes the nested RLock wait behind the first request.
func TestNodeConnReleasesAuthAdmissionBeforeWriterCallback(t *testing.T) {
	writer, member, token, _ := bearerAdmissionNodes(t)
	if _, err := member.verifyJWT(token); err == nil {
		t.Fatal("fixture reader unexpectedly has the writer's signer")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ownerResult := make(chan error, 1)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			ownerResult <- fmt.Errorf("accept owner connection: %w", err)
			return
		}
		defer ws.CloseNow()
		_, raw, err := ws.Read(ctx)
		if err != nil {
			ownerResult <- fmt.Errorf("read forwarded call: %w", err)
			return
		}
		var frame nodeConnFrame
		if err := json.Unmarshal(raw, &frame); err != nil {
			ownerResult <- fmt.Errorf("decode forwarded call: %w", err)
			return
		}
		if frame.Op != "call" || frame.Endpoint != "/mcp-relay/call" || frame.Auth != "Bearer "+token {
			ownerResult <- fmt.Errorf("forwarding changed the owner endpoint or original bearer")
			return
		}

		// Use real reader admission and the writer HTTP endpoint, including
		// signature, generation and digest verification. No tool is executed.
		request := httptest.NewRequest(http.MethodPost, "https://member.example/mcp", nil).WithContext(ctx)
		request.Header.Set("Authorization", frame.Auth)
		response := httptest.NewRecorder()
		var ownerErr error
		if !member.mcpAuth(response, request) {
			ownerErr = fmt.Errorf("owner bearer admission failed with status %d", response.Code)
		} else if AccessProfileIDFromRequest(request) != "fixture" ||
			authorizeToolCall(request, "shell:member-a", "shell_exec") != nil ||
			authorizeToolCall(request, "shell:other", "shell_exec") == nil ||
			authorizeToolCall(request, "shell:member-a", "mcp_manage") == nil {
			ownerErr = fmt.Errorf("owner reauthorization lost its scoped profile")
		}
		status := http.StatusOK
		if ownerErr != nil {
			status = http.StatusServiceUnavailable
		}
		reply, err := json.Marshal(nodeConnFrame{
			Op: "result", ID: frame.ID, Status: status,
			Body: json.RawMessage(`{"status":"completed"}`),
		})
		if err == nil {
			err = ws.Write(ctx, websocket.MessageText, reply)
		}
		if ownerErr == nil && err != nil {
			ownerErr = fmt.Errorf("deliver owner receipt: %w", err)
		}
		ownerResult <- ownerErr
	}))
	defer peer.Close()

	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(peer.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	conn := &nodeConn{
		name: "shell:member-a", ws: ws,
		done: make(chan struct{}), pending: map[string]chan nodeConnResult{},
	}
	writer.nodeConns.put(conn)
	readerDone := make(chan struct{})
	go func() {
		writer.nodeConnReadLoop(conn, conn.name)
		close(readerDone)
	}()
	var forwardDone, snapshotDone chan struct{}
	defer func() {
		cancel()
		_ = ws.CloseNow()
		for _, done := range []chan struct{}{forwardDone, readerDone, snapshotDone} {
			if done == nil {
				continue
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Error("isolated forwarding worker did not stop")
			}
		}
	}()

	admitted := make(chan struct{})
	dispatch := make(chan struct{})
	handler := writer.authSnapshotGate(http.HandlerFunc(writer.requireCtl(func(w http.ResponseWriter, r *http.Request) {
		if err := authorizeToolCall(r, conn.name, "shell_exec"); err != nil {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		close(admitted)
		select {
		case <-dispatch:
		case <-ctx.Done():
			return
		}
		if !writer.forwardOverNodeConn(w, r, conn.name, "https://unreachable.invalid",
			[]byte(`{"target":"shell:member-a","tool":"shell_exec"}`), "/mcp-relay/call", false) {
			w.WriteHeader(http.StatusBadGateway)
		}
	})))
	request := httptest.NewRequest(http.MethodPost, "/mcp-relay/call", nil).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	forwardDone = make(chan struct{})
	go func() {
		handler.ServeHTTP(response, request)
		close(forwardDone)
	}()
	select {
	case <-admitted:
	case <-ctx.Done():
		t.Fatal("writer never admitted the original request")
	}

	snapshotDone = make(chan struct{})
	go func() {
		writer.authMu.Lock()
		writer.authMu.Unlock()
		close(snapshotDone)
	}()
	// The admitted handler still owns an RLock. TryRLock therefore fails
	// only once the exclusive snapshot writer is pending, not because of
	// a guessed scheduling delay. Yield so that writer can enter Lock.
	for {
		select {
		case <-ctx.Done():
			t.Fatal("snapshot writer never became pending")
		default:
		}
		if !writer.authMu.TryRLock() {
			break
		}
		writer.authMu.RUnlock()
		runtime.Gosched()
	}
	close(dispatch)

	select {
	case err := <-ownerResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("owner callback blocked behind the forwarded request's auth admission")
	}
	select {
	case <-forwardDone:
	case <-ctx.Done():
		t.Fatal("forwarded call did not return its owner receipt")
	}
	if response.Code != http.StatusOK {
		t.Fatalf("forwarded call status = %d, want 200", response.Code)
	}
	select {
	case <-snapshotDone:
	case <-ctx.Done():
		t.Fatal("snapshot writer did not finish after owner reauthorization")
	}
}

func TestNodeConnUnavailableKeepsAuthAdmissionForFallback(t *testing.T) {
	for _, closed := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "closed"}[closed], func(t *testing.T) {
			writer := nodeConnWriterNode(t, "writer-node")
			if closed {
				conn := &nodeConn{name: "shell:member-a", done: make(chan struct{})}
				conn.closeOnce.Do(func() { close(conn.done) })
				writer.nodeConns.put(conn)
			}
			handler := writer.authSnapshotGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if writer.forwardOverNodeConn(w, r, "shell:member-a", "https://unreachable.invalid",
					[]byte(`{}`), "/mcp-relay/call", false) {
					t.Error("unavailable live connection consumed the fallback")
				}
				if writer.authMu.TryLock() {
					writer.authMu.Unlock()
					t.Error("unsent fallback lost the auth admission gate")
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			request := httptest.NewRequest(http.MethodPost, "/mcp-relay/call", nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusNoContent {
				t.Fatalf("fallback status = %d, want 204", response.Code)
			}
			if !writer.authMu.TryLock() {
				t.Fatal("auth admission was not released when the fallback returned")
			}
			writer.authMu.Unlock()
		})
	}
}
