package hub

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func peerTestHub(t *testing.T, token string, routes map[string]string) *Server {
	t.Helper()
	raw, err := json.Marshal(routes)
	if err != nil {
		t.Fatal(err)
	}
	s := New(Config{ConfigDir: t.TempDir(), CtlToken: token, NodePeersJSON: string(raw)})
	t.Cleanup(func() { s.Close() })
	return s
}

func peerTestCall(s *Server, body, token, hop string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/mcp-relay/call", strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if hop != "" {
		r.Header.Set(nodePeerHopHeader, hop)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func TestNodePeersPreserveCallerAndOriginalBody(t *testing.T) {
	body := ` { "target":"shell:nodeB", "tool":"shell_exec", "arguments":{"cmd":"printf test","approval_id":"approved","number":9007199254740993}, "background":true, "idempotency_key":"same-key", "detail":"full" } `
	var calls atomic.Int32
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		data, _ := io.ReadAll(r.Body)
		if string(data) != body || r.URL.Path != "/mcp-relay/call" || r.Header.Get("Authorization") != "Bearer caller-token" || r.Header.Get(nodePeerHopHeader) != "1" {
			t.Errorf("request changed: path=%s body=%s", r.URL.Path, data)
		}
		writeJSON(w, http.StatusOK, map[string]any{"job_id": "owned-by-b", "status": "running"})
	}))
	defer peer.Close()
	s := peerTestHub(t, "caller-token", map[string]string{"shell:nodeB": peer.URL})
	w := peerTestCall(s, body, "caller-token", "")
	if w.Code != 200 || calls.Load() != 1 || !strings.Contains(w.Body.String(), `"owner_target":"shell:nodeB"`) || !strings.Contains(w.Body.String(), `"owner_endpoint":"`+peer.URL+`"`) {
		t.Fatalf("forward failed: %d %s", w.Code, w.Body.String())
	}
	if len(s.shellJobs) != 0 || len(s.shellQueues) != 0 {
		t.Fatal("ingress reserved a local execution")
	}
	w = peerTestCall(s, body, "", "")
	if w.Code != 401 || calls.Load() != 1 {
		t.Fatalf("anonymous call reached peer: %d", w.Code)
	}
	w = peerTestCall(s, strings.Replace(body, "shell:nodeB", "shell:unconfigured", 1), "caller-token", "")
	if w.Code != 404 || calls.Load() != 1 {
		t.Fatalf("unconfigured target forwarded: %d", w.Code)
	}
}

func TestNodePeersDestinationAuthorizesOriginalBearer(t *testing.T) {
	b := peerTestHub(t, "different-owner-token", map[string]string{})
	peer := httptest.NewServer(b.Handler())
	defer peer.Close()
	a := peerTestHub(t, "caller-token", map[string]string{"shell:nodeB": peer.URL})
	w := peerTestCall(a, `{"target":"shell:nodeB","tool":"shell_exec","arguments":{"cmd":"true"}}`, "caller-token", "")
	if w.Code != 401 || len(b.shellJobs) != 0 {
		t.Fatalf("destination auth bypassed: %d %s", w.Code, w.Body.String())
	}
}

func TestNodePeersRejectSecondHop(t *testing.T) {
	var calls atomic.Int32
	c := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer c.Close()
	b := peerTestHub(t, "token", map[string]string{"shell:nodeB": c.URL})
	peer := httptest.NewServer(b.Handler())
	defer peer.Close()
	a := peerTestHub(t, "token", map[string]string{"shell:nodeB": peer.URL})
	w := peerTestCall(a, `{"target":"shell:nodeB","tool":"shell_exec","arguments":{"cmd":"true"}}`, "token", "")
	if w.Code != 508 || calls.Load() != 0 || len(b.shellJobs) != 0 {
		t.Fatalf("second hop accepted: %d %s", w.Code, w.Body.String())
	}
}

func TestNodePeersNeverOverridePinnedLocalExecutor(t *testing.T) {
	s := peerTestHub(t, "token", map[string]string{"shell:local": "http://127.0.0.1:1"})
	identity := map[string]string{"server_id": "local-id", "public_key": "local-key", "fingerprint": "local-fingerprint"}
	if err := s.RegisterLocalExecutor("local", identity, strings.Repeat("t", 32)); err != nil {
		t.Fatal(err)
	}
	w := peerTestCall(s, `{"target":"shell:local","tool":"shell_exec","arguments":{"cmd":"true"},"background":true}`, "token", "1")
	if w.Code != 200 || len(s.shellJobs) != 1 {
		t.Fatalf("local executor overridden: %d %s", w.Code, w.Body.String())
	}
}

func TestNodePeersDoNotFollowRedirectOrRetryDroppedWrite(t *testing.T) {
	for _, mode := range []string{"redirect", "dropped"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if mode == "redirect" {
					w.Header().Set("Location", "/redirected")
					w.WriteHeader(http.StatusTemporaryRedirect)
				} else {
					io.Copy(io.Discard, r.Body)
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					conn.Close()
				}
			}))
			defer peer.Close()
			s := peerTestHub(t, "token", map[string]string{"shell:nodeB": peer.URL})
			w := peerTestCall(s, `{"target":"shell:nodeB","tool":"shell_exec","arguments":{"cmd":"true"}}`, "token", "")
			if w.Code != 502 || calls.Load() != 1 || !strings.Contains(w.Body.String(), `"outcome":"unknown"`) {
				t.Fatalf("unsafe delivery: calls=%d %d %s", calls.Load(), w.Code, w.Body.String())
			}
		})
	}
}

func TestNodePeersConfiguration(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `{"shell:x":123}`, `{"hub":"http://localhost"}`, `{"shell: x":"http://localhost"}`, `{"shell:home-assistant":"http://localhost"}`, `{"shell:x":"http://user:secret@localhost"}`, `{"shell:x":"https://host/call"}`, `{"shell:x":"https://host?token=secret"}`, `{"shell:x":"file:///tmp/a"}`} {
		if _, err := parseNodePeers(raw); err == nil {
			t.Errorf("accepted invalid config %s", raw)
		}
	}
	t.Setenv("GPTADMIN_NODE_PEERS", `{"shell:nodeB":"https://node-b.example/"}`)
	routes, err := parseNodePeers(FromEnv().NodePeersJSON)
	if err != nil || routes["shell:nodeB"].URL != "https://node-b.example" {
		t.Fatalf("env route not loaded: %v %v", routes, err)
	}
}

func TestNodePeersPhysicalPinConfiguration(t *testing.T) {
	if _, err := parseNodePeers(`{"shell:nodeB":{"url":"https://peer.example","connect_to":"127.0.0.1"}}`); err != nil {
		t.Fatalf("physical peer route rejected: %v", err)
	}
	for _, raw := range []string{
		`{"shell:nodeB":{"url":"http://peer.example","connect_to":"127.0.0.1"}}`,
		`{"shell:nodeB":{"url":"https://peer.example","connect_to":"other.example"}}`,
		`{"shell:nodeB":{"url":"https://peer.example","connect_to":"127.0.0.1:443"}}`,
		`{"shell:nodeB":{"url":"https://peer.example","insecure":true}}`,
	} {
		if _, err := parseNodePeers(raw); err == nil {
			t.Errorf("invalid physical peer accepted: %s", raw)
		}
	}
}

func peerTestMCPCall(s *Server, body, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("MCP-Protocol-Version", mcpProtocolVersion)
	r.Header.Set("Mcp-Method", "tools/call")
	var envelope map[string]any
	_ = json.Unmarshal([]byte(body), &envelope)
	r.Header.Set("Mcp-Name", firstString(mapValue(envelope["params"]), "name"))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func TestNodePeersJobOwnerRouteAndLocalPriority(t *testing.T) {
	b := peerTestHub(t, "token", map[string]string{"shell:nodeB": "http://127.0.0.1:1"})
	if err := b.RegisterLocalExecutor("nodeB", map[string]string{"server_id": "b-id", "public_key": "b-key", "fingerprint": "b-fingerprint"}, strings.Repeat("b", 32)); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	b.shellJobs["owned"] = &shellJob{ID: "owned", Server: "nodeB", Status: "completed", CreatedAt: nowFloat(), DoneAt: nowFloat(), Result: map[string]any{"stdout": "owner-only-marker"}}
	b.shellJobs["not-owned"] = &shellJob{ID: "not-owned", Server: "other", Status: "completed", CreatedAt: nowFloat(), DoneAt: nowFloat(), Result: map[string]any{"stdout": "must-not-leak"}}
	err := b.saveTaskStateLocked("owned", "not-owned")
	b.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	owner := httptest.NewServer(b.Handler())
	defer owner.Close()
	a := peerTestHub(t, "token", map[string]string{"shell:nodeB": owner.URL})
	body := `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"job","arguments":{"id":"owned","owner_target":"shell:nodeB"}}}`
	for i := 0; i < 2; i++ {
		w := peerTestMCPCall(a, body, "token")
		if w.Code != 200 || !strings.Contains(w.Body.String(), "owner-only-marker") || len(a.shellJobs) != 0 {
			t.Fatalf("receipt route failed: %d %s", w.Code, w.Body.String())
		}
	}
	for _, id := range []string{"not-owned", "missing"} {
		w := peerTestMCPCall(a, strings.Replace(body, `"id":"owned"`, `"id":"`+id+`"`, 1), "token")
		if w.Code != 404 || strings.Contains(w.Body.String(), "must-not-leak") {
			t.Fatalf("nonowned receipt leaked: %d %s", w.Code, w.Body.String())
		}
	}
	// A stale/colliding local record cannot be overridden by a supplied hint.
	a.mu.Lock()
	a.shellJobs["owned"] = &shellJob{ID: "owned", Server: "nodeA", Status: "completed", CreatedAt: nowFloat(), DoneAt: nowFloat()}
	err = a.saveTaskStateLocked("owned")
	a.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if w := peerTestMCPCall(a, body, "token"); w.Code != 404 || strings.Contains(w.Body.String(), "owner-only-marker") {
		t.Fatalf("hint overrode local job: %d %s", w.Code, w.Body.String())
	}
}

func TestNodePeersJobReadRejectsSecondHopAndForeignBearer(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"job","arguments":{"id":"missing","owner_target":"shell:nodeB"}}}`
	for _, token := range []string{"token", "other"} {
		b := peerTestHub(t, token, map[string]string{"shell:nodeB": "http://127.0.0.1:1"})
		peer := httptest.NewServer(b.Handler())
		a := peerTestHub(t, "token", map[string]string{"shell:nodeB": peer.URL})
		w := peerTestMCPCall(a, body, "token")
		peer.Close()
		want := 508
		if token != "token" {
			want = 401
		}
		if w.Code != want || strings.Contains(w.Body.String(), `"outcome":"unknown"`) {
			t.Fatalf("unsafe job read: %d %s", w.Code, w.Body.String())
		}
	}
}

func peerTestLocalRelayJob(t *testing.T, routes map[string]string) *Server {
	t.Helper()
	s := peerTestHub(t, "token", routes)
	if err := s.RegisterLocalExecutor("mac-mini-2012.lan", map[string]string{"server_id": "mini-id", "public_key": "mini-key", "fingerprint": "mini-fingerprint"}, strings.Repeat("m", 32)); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.relayJobs["relay-owned"] = &relayJob{ID: "relay-owned", AgentID: "shell:mac-mini-2012.lan", Status: "completed", CreatedAt: nowFloat(), DoneAt: nowFloat(), Result: map[string]any{"stdout": "relay-owner-marker"}}
	err := s.saveTaskStateLocked("relay-owned")
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestNodePeersLocalRelayOwnerAndRepeatedReadsMatchNoHint(t *testing.T) {
	s := peerTestLocalRelayJob(t, nil)
	noHint := `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"job","arguments":{"id":"relay-owned"}}}`
	hinted := strings.Replace(noHint, `"id":"relay-owned"`, `"id":"relay-owned","owner_target":"shell:mac-mini-2012.lan"`, 1)
	baseline := peerTestMCPCall(s, noHint, "token")
	if baseline.Code != http.StatusOK || !strings.Contains(baseline.Body.String(), "relay-owner-marker") {
		t.Fatalf("no-hint relay read failed: %d %s", baseline.Code, baseline.Body.String())
	}
	for i := 0; i < 2; i++ {
		for _, body := range []string{hinted, noHint} {
			w := peerTestMCPCall(s, body, "token")
			if w.Code != http.StatusOK || w.Body.String() != baseline.Body.String() {
				t.Fatalf("relay owner read changed: %d %s", w.Code, w.Body.String())
			}
		}
	}
}

func TestNodePeersLocalRelayCannotBeOverriddenByForeignOwnerHint(t *testing.T) {
	var calls atomic.Int32
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer peer.Close()
	s := peerTestLocalRelayJob(t, map[string]string{"shell:remote": peer.URL})
	if err := s.RegisterLocalExecutor("other", map[string]string{"server_id": "other-id", "public_key": "other-key", "fingerprint": "other-fingerprint"}, strings.Repeat("o", 32)); err != nil {
		t.Fatal(err)
	}
	for _, owner := range []string{"shell:other", "shell:remote"} {
		body := `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"job","arguments":{"id":"relay-owned","owner_target":"` + owner + `"}}}`
		w := peerTestMCPCall(s, body, "token")
		if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "relay-owner-marker") || calls.Load() != 0 {
			t.Fatalf("foreign hint overrode local relay: %d %s, peer calls=%d", w.Code, w.Body.String(), calls.Load())
		}
	}
}

func TestNodePeersRelayOwnerEnrollmentSurvivesWithoutProcessLocalPin(t *testing.T) {
	s := peerTestLocalRelayJob(t, nil)
	s.mu.Lock()
	delete(s.localExecutors, "shell:mac-mini-2012.lan")
	s.mu.Unlock()
	body := `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"job","arguments":{"id":"relay-owned","owner_target":"shell:mac-mini-2012.lan"}}}`
	w := peerTestMCPCall(s, body, "token")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "relay-owner-marker") {
		t.Fatalf("approved queued owner rejected without local pin: %d %s", w.Code, w.Body.String())
	}
	s.mu.Lock()
	s.agents["shell:mac-mini-2012.lan"].Meta["approved"] = false
	s.mu.Unlock()
	w = peerTestMCPCall(s, body, "token")
	if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "relay-owner-marker") {
		t.Fatalf("unapproved owner leaked relay receipt: %d %s", w.Code, w.Body.String())
	}
}

func TestNodePeersLocalRelayUsesSameKindPriorityAsJobReader(t *testing.T) {
	s := peerTestLocalRelayJob(t, nil)
	// In-memory collision: the reader chooses relay, so a matching shell record
	// must not authorize reading a different relay owner.
	s.mu.Lock()
	s.shellJobs["relay-owned"] = &shellJob{ID: "relay-owned", Server: "mac-mini-2012.lan"}
	s.relayJobs["relay-owned"].AgentID = "shell:other"
	s.mu.Unlock()
	body := `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"job","arguments":{"id":"relay-owned","owner_target":"shell:mac-mini-2012.lan"}}}`
	r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	w := httptest.NewRecorder()
	args := map[string]any{"id": "relay-owned", "owner_target": "shell:mac-mini-2012.lan"}
	// Persist both kinds so refresh reads the same collision as the job reader.
	s.mu.Lock()
	err := s.saveTaskStateLocked("relay-owned")
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if !s.routeNodePeerJob(w, r, args, []byte(body)) || w.Code != http.StatusNotFound {
		t.Fatalf("shell collision authorized relay receipt: %d %s", w.Code, w.Body.String())
	}
}

func TestNodePeersLocalRelayOwnerDeniedByProfile(t *testing.T) {
	s := peerTestLocalRelayJob(t, nil)
	r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	r = requestWithAccessProfile(r, AccessProfile{ID: "restricted", AllowedTargets: []string{"shell:other"}})
	w := httptest.NewRecorder()
	args := map[string]any{"id": "relay-owned", "owner_target": "shell:mac-mini-2012.lan"}
	if !s.routeNodePeerJob(w, r, args, []byte(`{"jsonrpc":"2.0","id":9}`)) || w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "relay-owner-marker") {
		t.Fatalf("profile denial bypassed: %d %s", w.Code, w.Body.String())
	}
}

func TestNodePeersRelayChildOwnerHintsRemainUnsupported(t *testing.T) {
	s := peerTestLocalRelayJob(t, nil)
	for _, owner := range []string{"mcp:shell:mac-mini-2012.lan:AgentBrowser", "file:mac-mini-2012.lan", "shell:mac-mini-2012.lan/AgentBrowser"} {
		body := `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"job","arguments":{"id":"relay-owned","owner_target":"` + owner + `"}}}`
		w := peerTestMCPCall(s, body, "token")
		if w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), "relay-owner-marker") {
			t.Fatalf("unsupported child hint accepted: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestNodePeersJobHintSchema(t *testing.T) {
	for _, tool := range appsSDKTools() {
		if tool["name"] == "job" {
			if mapValue(mapValue(tool["inputSchema"])["properties"])["owner_target"] == nil {
				t.Fatal("job schema omits peer owner hint")
			}
			return
		}
	}
	t.Fatal("job tool missing")
}

func TestNodePeersMCPPreservesProtocolAndBearer(t *testing.T) {
	body := ` {"jsonrpc":"2.0","id":"original-id","method":"tools/call","params":{"name":"execute","arguments":{"target":"shell:nodeB","tool":"shell_exec","arguments":{"cmd":"true","approval_id":"owner-approval"},"background":true,"idempotency_key":"same-key"}}} `
	var calls atomic.Int32
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		data, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/mcp" || string(data) != body || r.Header.Get("Authorization") != "Bearer caller-token" || r.Header.Get("MCP-Protocol-Version") != mcpProtocolVersion || r.Header.Get("Mcp-Method") != "tools/call" || r.Header.Get("Mcp-Name") != "execute" {
			t.Errorf("MCP request was converted: %s %s", r.URL.Path, data)
		}
		writeJSON(w, 200, map[string]any{"jsonrpc": "2.0", "id": "original-id", "result": mcpToolResult(map[string]any{"job_id": "owner-job"})})
	}))
	defer peer.Close()
	s := peerTestHub(t, "caller-token", map[string]string{"shell:nodeB": peer.URL})
	w := peerTestMCPCall(s, body, "caller-token")
	var envelope map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	result := mapValue(envelope["result"])
	if w.Code != 200 || calls.Load() != 1 || envelope["id"] != "original-id" || mapValue(result["_meta"])["owner_target"] != "shell:nodeB" || mapValue(result["structuredContent"])["job_id"] != "owner-job" || len(s.shellJobs) != 0 {
		t.Fatalf("MCP forwarding failed: %d %s", w.Code, w.Body.String())
	}
	// A accepts the caller, but B's independent MCP admission rejects it.
	b := peerTestHub(t, "owner-only-token", map[string]string{})
	owner := httptest.NewServer(b.Handler())
	defer owner.Close()
	a := peerTestHub(t, "caller-token", map[string]string{"shell:nodeB": owner.URL})
	w = peerTestMCPCall(a, body, "caller-token")
	if w.Code != 401 || w.Header().Get("WWW-Authenticate") == "" || len(b.shellJobs) != 0 {
		t.Fatalf("MCP destination authorization bypassed: %d %s", w.Code, w.Body.String())
	}
}

func TestNodePeersMCPRejectsSecondHop(t *testing.T) {
	b := peerTestHub(t, "token", map[string]string{"shell:nodeB": "http://127.0.0.1:1"})
	owner := httptest.NewServer(b.Handler())
	defer owner.Close()
	a := peerTestHub(t, "token", map[string]string{"shell:nodeB": owner.URL})
	w := peerTestMCPCall(a, `{"jsonrpc":"2.0","id":42,"method":"tools/call","params":{"name":"execute","arguments":{"target":"shell:nodeB","tool":"shell_exec","arguments":{"cmd":"true"}}}}`, "token")
	var envelope map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if w.Code != 508 || envelope["id"] != float64(42) || envelope["error"] == nil || len(b.shellJobs) != 0 {
		t.Fatalf("MCP second hop accepted: %d %s", w.Code, w.Body.String())
	}
}

func TestNodePeersReuseConnectionWithoutReplayingDeliveredPOST(t *testing.T) {
	var calls, connections atomic.Int32
	peer := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		io.Copy(io.Discard, r.Body)
		if r.Header.Get("Idempotency-Key") != "" || r.Header.Get("X-Idempotency-Key") != "" {
			t.Error("HTTP retry-enabling headers were forwarded")
		}
		if call == 2 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			conn.Close() // Body accepted on a reused connection; no receipt delivered.
			return
		}
		writeJSON(w, 200, map[string]any{"job_id": "owner-job"})
	}))
	peer.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	peer.Start()
	defer peer.Close()
	s := peerTestHub(t, "token", map[string]string{"shell:nodeB": peer.URL})
	body := `{"target":"shell:nodeB","tool":"shell_exec","arguments":{"cmd":"true"},"idempotency_key":"owner-key"}`
	if w := peerTestCall(s, body, "token", ""); w.Code != 200 {
		t.Fatalf("warmup: %d %s", w.Code, w.Body.String())
	}
	r := httptest.NewRequest(http.MethodPost, "/mcp-relay/call", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer token")
	r.Header.Set("Idempotency-Key", "do-not-forward")
	r.Header.Set("X-Idempotency-Key", "do-not-forward")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 502 || calls.Load() != 2 || connections.Load() != 1 || !strings.Contains(w.Body.String(), `"outcome":"unknown"`) {
		t.Fatalf("connection not reused or delivered POST replayed: calls=%d connections=%d status=%d %s", calls.Load(), connections.Load(), w.Code, w.Body.String())
	}
}

func TestNodePeersCloseReleasesIdleConnections(t *testing.T) {
	closed := make(chan struct{}, 1)
	peer := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		writeJSON(w, 200, map[string]any{"job_id": "owner-job"})
	}))
	peer.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateClosed {
			select {
			case closed <- struct{}{}:
			default:
			}
		}
	}
	peer.Start()
	defer peer.Close()
	s := peerTestHub(t, "token", map[string]string{"shell:nodeB": peer.URL})
	w := peerTestCall(s, `{"target":"shell:nodeB","tool":"shell_exec","arguments":{"cmd":"true"}}`, "token", "")
	if w.Code != 200 {
		t.Fatalf("warmup: %d %s", w.Code, w.Body.String())
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("peer idle connection survived Server.Close")
	}
}
