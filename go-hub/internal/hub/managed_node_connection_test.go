package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// Narrow protocol units: test endpoints and claims are local fixtures, not
// evidence of native Codex/ZCode or a deployed remote node acceptance.
func managedPeerUnitRequest(method string) *http.Request {
	r := connectionTestRequest("unit-owner-credential", "unit-profile", 1)
	r.Method = method
	r.URL.Path = "/connection/leaf/mcp"
	r = requestWithAccessProfile(r, AccessProfile{ID: "unit-profile", Version: 1, AllowedTargets: []string{"mcp:shell:peer:leaf"}})
	return r
}
func managedPeerUnitServer(conn *nodeConn) *Server {
	s := &Server{nodeConns: newNodeConnRegistry()}
	s.nodeConns.put(conn)
	return s
}
func managedPeerUnitAgent() Agent {
	return Agent{AgentID: "mcp:shell:peer:leaf", Transport: "streamable-http", Meta: map[string]any{"parent_server_id": "shell:peer", "connection_scopes_version": 0}}
}

func TestManagedNodeConnectionPath(t *testing.T) {
	if !managedConnectionPath("/connection/leaf/mcp") {
		t.Fatal("managed path rejected")
	}
	for _, path := range []string{"/mcp", "/connection//mcp", "/connection/../mcp", "/connection/leaf/mcp?redirect=1", "/connection/leaf%2fmcp/mcp", "/connection/leaf/mcp/extra"} {
		if managedConnectionPath(path) {
			t.Fatalf("unsafe path accepted: %q", path)
		}
	}
}
func TestManagedNodeConnectionRefusesLegacyAndBusyPeers(t *testing.T) {
	conn := &nodeConn{name: "shell:peer", done: make(chan struct{}), pending: map[string]chan nodeConnResult{}}
	s := managedPeerUnitServer(conn)
	w := httptest.NewRecorder()
	if !s.forwardManagedConnection(w, managedPeerUnitRequest(http.MethodPost), managedPeerUnitAgent(), []byte(`{"id":1}`)) || w.Code != http.StatusServiceUnavailable {
		t.Fatal("legacy peer accepted")
	}
	conn.managedHTTP = true
	for n := 0; n < managedNodePendingLimit; n++ {
		conn.pending[string(rune('a'+n))] = make(chan nodeConnResult, 1)
	}
	w = httptest.NewRecorder()
	s.forwardManagedConnection(w, managedPeerUnitRequest(http.MethodPost), managedPeerUnitAgent(), []byte(`{"id":1}`))
	if w.Code != http.StatusServiceUnavailable || len(conn.pending) != managedNodePendingLimit {
		t.Fatal("capacity was exceeded or foreign requests changed")
	}
}
func TestManagedNodeConnectionRefusesForeignProfileAndSecondHop(t *testing.T) {
	conn := &nodeConn{name: "shell:peer", managedHTTP: true, done: make(chan struct{}), pending: map[string]chan nodeConnResult{}}
	s := managedPeerUnitServer(conn)
	r := requestWithAccessProfile(managedPeerUnitRequest(http.MethodPost), AccessProfile{ID: "foreign", AllowedTargets: []string{"other"}})
	w := httptest.NewRecorder()
	s.forwardManagedConnection(w, r, managedPeerUnitAgent(), nil)
	if w.Code != http.StatusForbidden || len(conn.pending) != 0 {
		t.Fatal("foreign profile reached peer")
	}
	r = managedPeerUnitRequest(http.MethodPost)
	r.Header.Set(nodePeerHopHeader, "1")
	w = httptest.NewRecorder()
	s.forwardManagedConnection(w, r, managedPeerUnitAgent(), nil)
	if w.Code != http.StatusLoopDetected {
		t.Fatal("recursive route accepted")
	}
}
func TestManagedNodeConnectionPreservesOwnerSessionAndNoContent(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			received := make(chan nodeConnFrame, 1)
			fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ws, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer ws.Close(websocket.StatusNormalClosure, "")
				_, data, err := ws.Read(ctx)
				if err != nil {
					return
				}
				var frame nodeConnFrame
				if json.Unmarshal(data, &frame) != nil {
					return
				}
				received <- frame
				result := nodeConnFrame{Op: "result", ID: frame.ID, Status: http.StatusOK, Headers: map[string]string{"Mcp-Session-Id": "owner-session", "Set-Cookie": "foreign-cookie"}, Body: json.RawMessage(`{"jsonrpc":"2.0","id":"request-exact","result":{"ok":true}}`)}
				if method == http.MethodDelete {
					result.Status = http.StatusNoContent
					result.Body = nil
				}
				reply, _ := json.Marshal(result)
				_ = ws.Write(ctx, websocket.MessageText, reply)
			}))
			defer fixture.Close()
			ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(fixture.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			conn := &nodeConn{name: "shell:peer", managedHTTP: true, ws: ws, done: make(chan struct{}), pending: map[string]chan nodeConnResult{}}
			s := managedPeerUnitServer(conn)
			done := make(chan struct{})
			go func() { s.nodeConnReadLoop(conn, conn.name); close(done) }()
			r := managedPeerUnitRequest(method)
			r.Header.Set("Mcp-Session-Id", "owner-session")
			r.Header.Set("Cookie", "private-cookie")
			r.Header.Set("X-Untrusted", "private-header")
			w := httptest.NewRecorder()
			body := []byte(`{"jsonrpc":"2.0","id":"request-exact","method":"tools/list"}`)
			if method == http.MethodDelete {
				body = nil
			}
			if !s.forwardManagedConnection(w, r, managedPeerUnitAgent(), body) {
				t.Fatal("managed peer not selected")
			}
			frame := <-received
			if frame.Method != method || frame.Auth != "Bearer unit-owner-credential" || frame.Headers["Mcp-Session-Id"] != "owner-session" || !bytes.Equal(frame.Body, body) || frame.Headers["Cookie"] != "" || frame.Headers["X-Untrusted"] != "" {
				t.Fatal("method/session/principal/body isolation changed")
			}
			if w.Header().Get("Set-Cookie") != "" || w.Header().Get("Mcp-Session-Id") != "owner-session" {
				t.Fatal("response headers crossed boundaries")
			}
			if method == http.MethodDelete && (w.Code != http.StatusNoContent || w.Body.Len() != 0) || method == http.MethodPost && (w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte(`"id":"request-exact"`))) {
				t.Fatalf("response changed: %d", w.Code)
			}
			if len(s.connectionSessions) != 0 || len(conn.pending) != 0 {
				t.Fatal("gateway replicated session or retained pending request")
			}
			conn.close()
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("own websocket cleanup unconfirmed")
			}
		})
	}
}
func TestManagedNodeLocalExecuteMethodAndHeaders(t *testing.T) {
	seen := make(chan *http.Request, 1)
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Clone(r.Context())
		w.Header().Set("Mcp-Session-Id", "same-owner-session")
		w.Header().Set("Set-Cookie", "owner-private-cookie")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer owner.Close()
	s := &Server{cfg: Config{Addr: owner.Listener.Addr().String()}}
	status, headers, body := s.nodeConnLocalExecute(nodeConnFrame{Endpoint: "/connection/leaf/mcp", Method: http.MethodDelete, Auth: "Bearer owner", Headers: map[string]string{"Mcp-Session-Id": "same-owner-session", "Cookie": "do-not-forward", "X-Untrusted": "do-not-forward"}})
	r := <-seen
	if status != http.StatusNoContent || len(body) != 0 || r.Method != http.MethodDelete || r.Header.Get("Mcp-Session-Id") != "same-owner-session" || r.Header.Get("Authorization") != "Bearer owner" || r.Header.Get("Cookie") != "" || r.Header.Get("X-Untrusted") != "" || headers.Get("Set-Cookie") != "" || headers.Get("Mcp-Session-Id") != "same-owner-session" {
		t.Fatal("owner method/header contract changed")
	}
	status, _, _ = s.nodeConnLocalExecute(nodeConnFrame{Endpoint: "/connection/leaf/mcp", Method: http.MethodGet})
	if status != http.StatusBadRequest {
		t.Fatal("unsupported managed method accepted")
	}
}
