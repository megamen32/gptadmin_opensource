package hub

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func childPeerRouteRegister(t *testing.T, s *Server) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	parent := &Agent{AgentID: "shell:peer", Kind: "virtual_shell", Status: "online", Meta: map[string]any{"approved": true, "mcp_agents": []any{map[string]any{"ref": "sample", "name": "sample", "enabled": true, "transport": "streamable-http"}}}}
	s.agents[parent.AgentID] = parent
	return childMCPAgents(*parent)[0].AgentID
}

func TestChildPeerRouteUsesParentPreservesChildBody(t *testing.T) {
	for _, enrolled := range []bool{false, true} {
		t.Run(map[bool]string{false: "static", true: "enrolled"}[enrolled], func(t *testing.T) {
			body := ` { "target":"mcp:shell:peer:sample", "tool":"sample_tool", "arguments":{"number":9007199254740993}, "background":true, "idempotency_key":"one-call" } `
			var calls atomic.Int32
			peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				got, _ := io.ReadAll(r.Body)
				if string(got) != body || r.Header.Get("Authorization") != "Bearer token" || r.Header.Get(nodePeerHopHeader) != "1" || r.URL.Path != "/mcp-relay/call" {
					t.Errorf("child payload or credential changed: %s", got)
				}
				writeJSON(w, http.StatusOK, map[string]any{"status": "queued", "job_id": "owner-job"})
			}))
			defer peer.Close()
			s := peerTestHub(t, "token", map[string]string{"shell:peer": peer.URL})
			child := childPeerRouteRegister(t, s)
			if enrolled {
				s.mu.Lock()
				delete(s.nodePeers, "shell:peer")
				s.membershipRoutes = map[string]nodePeerRoute{"shell:peer": {URL: peer.URL}}
				s.mu.Unlock()
			}
			r := httptest.NewRequest(http.MethodPost, "/mcp-relay/call", nil)
			r.Header.Set("Authorization", "Bearer token")
			w := httptest.NewRecorder()
			if !s.forwardNodePeerRequest(w, r, child, []byte(body), "/mcp-relay/call") {
				t.Fatal("child fell through to legacy queue instead of parent Node")
			}
			if w.Code != http.StatusOK || calls.Load() != 1 || !strings.Contains(w.Body.String(), `"owner_target":"shell:peer"`) || !strings.Contains(w.Body.String(), `"job_id":"owner-job"`) {
				t.Fatalf("wrong owner/duplicate delivery: calls=%d HTTP %d %s", calls.Load(), w.Code, w.Body.String())
			}
			if len(s.shellJobs) != 0 || len(s.relayJobs) != 0 || len(s.shellQueues) != 0 {
				t.Fatal("ingress allocated a legacy job")
			}
		})
	}
}

func TestChildPeerRouteRejectsWrongOwnerWithoutSecondForward(t *testing.T) {
	var forwarded atomic.Int32
	next := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded.Add(1) }))
	defer next.Close()
	wrong := peerTestHub(t, "token", map[string]string{"shell:peer": next.URL})
	childPeerRouteRegister(t, wrong)
	peer := httptest.NewServer(wrong.Handler())
	defer peer.Close()
	s := peerTestHub(t, "token", map[string]string{"shell:peer": peer.URL})
	child := childPeerRouteRegister(t, s)
	r := httptest.NewRequest(http.MethodPost, "/mcp-relay/call", nil)
	r.Header.Set("Authorization", "Bearer token")
	w := httptest.NewRecorder()
	if !s.forwardNodePeerRequest(w, r, child, []byte(`{"target":"mcp:shell:peer:sample","tool":"sample_tool","background":true}`), "/mcp-relay/call") || w.Code != http.StatusLoopDetected || forwarded.Load() != 0 {
		t.Fatalf("wrong owner accepted or forwarded: calls=%d HTTP %d %s", forwarded.Load(), w.Code, w.Body.String())
	}
	if len(wrong.shellJobs) != 0 || len(s.shellJobs) != 0 {
		t.Fatal("wrong-owner request queued")
	}
}

func TestChildPeerRouteWrittenFrameNeverFallsBackToHTTP(t *testing.T) {
	var calls atomic.Int32
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer fallback.Close()
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		_, _, _ = ws.Read(r.Context())
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
	child := childPeerRouteRegister(t, s)
	s.nodeConns.put(conn)
	r := httptest.NewRequest(http.MethodPost, "/mcp-relay/call", nil).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer token")
	w := httptest.NewRecorder()
	if !s.forwardNodePeerRequest(w, r, child, []byte(`{"target":"mcp:shell:peer:sample","tool":"sample_tool"}`), "/mcp-relay/call") || w.Code != http.StatusBadGateway || calls.Load() != 0 || !strings.Contains(w.Body.String(), `"outcome":"unknown"`) || !strings.Contains(w.Body.String(), `"owner_target":"shell:peer"`) {
		t.Fatalf("lost parent connection or replay: calls=%d HTTP %d %s", calls.Load(), w.Code, w.Body.String())
	}
}

func TestChildPeerRouteExecRetainsChildACL(t *testing.T) {
	var calls atomic.Int32
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var args map[string]any
		if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
			t.Error(err)
		}
		if firstString(args, "target") != "mcp:shell:peer:sample" || r.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("exec lost child or bearer: %#v", args)
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "completed", "response": map[string]any{"marker": "child-owner"}})
	}))
	defer peer.Close()
	s := peerTestHub(t, "token", map[string]string{"shell:peer": peer.URL})
	child := childPeerRouteRegister(t, s)
	r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	r.Header.Set("Authorization", "Bearer token")
	r = requestWithAuthClaims(r, map[string]any{"sub": "test-caller", "scope": "gptadmin.read gptadmin.exec", "access_mode": accessModeFull})
	allowed := requestWithAccessProfile(r, AccessProfile{ID: "child-only", AccessMode: "full", AllowedTargets: []string{"hub", child}, AllowedTools: []string{"exec", "sample_tool"}})
	code := `return await mcp.call('mcp:shell:peer:sample','sample_tool',{});`
	got, status := codeModeRun(t, s, allowed, map[string]any{"code": code})
	if status != http.StatusOK || calls.Load() != 1 || firstString(mapValue(got["result"]), "marker") != "child-owner" {
		t.Fatalf("exec child route failed: %d %#v calls=%d", status, got, calls.Load())
	}
	denied := requestWithAccessProfile(r, AccessProfile{ID: "other-child", AccessMode: "full", AllowedTargets: []string{"hub", "mcp:shell:peer:other"}, AllowedTools: []string{"exec", "sample_tool"}})
	got, status = codeModeRun(t, s, denied, map[string]any{"code": code})
	if status == http.StatusOK || calls.Load() != 1 {
		t.Fatalf("denied child reached parent: %d %#v calls=%d", status, got, calls.Load())
	}
	if len(s.shellJobs) != 0 || len(s.relayJobs) != 0 {
		t.Fatal("exec allocated ingress job")
	}
}
