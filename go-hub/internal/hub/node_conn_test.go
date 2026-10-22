package hub

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The node connection tests exercise the real WebSocket surface between an
// in-process writer and member: outbound dial, writer-side registration,
// forwarded execution against the member's own listener, and the delivery
// contract that a written frame is never retried over the HTTP origin.

func waitForNodeConn(t *testing.T, server *Server, name string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if conn := server.nodeConns.get(name); conn != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("node connection for %s never registered", name)
}

// nodeConnWriterNode mirrors membershipWriterNode but preserves env-seeded
// GPTADMIN_*_MCP_BEARER values: the transport tests need a caller bearer the
// member accepts through the replicated snapshot (control credentials are
// deliberately excluded from snapshots).
func nodeConnWriterNode(t *testing.T, name string) *Server {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("GPTADMIN_AUTH_MODE", "writer")
	t.Setenv("GPTADMIN_CONFIG_DIR", dir)
	cfg := FromEnv()
	cfg.CtlToken = "fixture-owner"
	cfg.OAuthClientSecret = "fixture-signing"
	cfg.PublicOrigin = "https://writer.example"
	cfg.MCPResource = "https://writer.example/mcp"
	s := New(cfg)
	t.Cleanup(func() { s.Close() })
	if err := s.RegisterLocalExecutor(name, map[string]string{"server_id": name + "-identity", "public_key": name + "-public", "fingerprint": name + "-fingerprint"}, strings.Repeat("x", 32)); err != nil {
		t.Fatal(err)
	}
	if err := s.InitializeAuthContinuity(); err != nil {
		t.Fatal(err)
	}
	s.ensureWriterSelfEntry()
	return s
}

func TestNodeConnForwardsCallOverLiveConnection(t *testing.T) {
	// The original caller bearer is an MCP-family token: control credentials
	// are deliberately excluded from the replicated snapshot, so members
	// accept exactly this class from the writer.
	t.Setenv("GPTADMIN_TEST_MCP_BEARER", "fixture-mcp-bearer-0123456789abcdefghijklmnop")
	writer := nodeConnWriterNode(t, "writer-node")
	writerServer := httptest.NewServer(writer.Handler())
	defer writerServer.Close()
	writer.cfg.NodePublicURL = writerServer.URL
	writer.ensureWriterSelfEntry()

	member, nodeID, nodeKey := membershipMemberNode(t, "member-a", writerServer.URL, "writer-node-identity")
	// Production managers pin GPTADMIN_AUTH_SOURCE_ID to the writer identity;
	// the snapshot gate refuses reader traffic otherwise.
	member.cfg.AuthSourceID = "writer-node-identity"
	// Context portability requires the same signing authority. Preserve the
	// distinct control credentials and replicate the opaque caller bearer as
	// before; only this portability fixture shares its instruction signer.
	member.cfg.OAuthClientSecret = writer.cfg.OAuthClientSecret
	// Observe the real loopback request without replacing its handler.
	forwardedHeaders := make(chan http.Header, 2)
	memberHandler := member.Handler()
	memberServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mcp-relay/call" {
			select {
			case forwardedHeaders <- r.Header.Clone():
			default:
			}
		}
		memberHandler.ServeHTTP(w, r)
	}))
	defer memberServer.Close()
	// The live-connection executor replays calls against the node's own
	// configured listener; point it at the test server. A short sync wait
	// returns the owner's queue receipt promptly (the fixture has no shell
	// worker; inline execution is proven by the live deployment checks).
	member.cfg.Addr = memberServer.Listener.Addr().String()
	member.cfg.ActionSyncWait = 2 * time.Second
	member.cfg.DefaultTimeout = 3 * time.Second

	grant := membershipIssueGrant(t, writer, "shell:member-a")
	// The advertised origin must be unusable: the live connection, not the
	// HTTP dial, has to carry the call for this test to pass.
	joined := membershipJoin(t, writer, grant, nodeID, nodeKey, "shell:member-a", "https://unreachable.invalid")
	if joined["__status"] != http.StatusOK || joined["state"] != nodeStateJoining {
		t.Fatalf("join failed: %v", joined)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	member.membershipMemberTick(client, true)
	member.membershipMemberTick(client, false)

	// The member dials out through the production loop entry point (it also
	// creates the stop channel the dial loop exits on).
	member.startMembershipLoops()
	waitForNodeConn(t, writer, "shell:member-a")

	// The production cross-node path: the original MCP bearer travels to the
	// member's own relay endpoint, which reauthorizes it against the
	// replicated snapshot token store and executes through its local queue.
	// The direct execute facade is retired; the authenticated relay route is
	// the surviving submission path over a live node connection.
	body := []byte(`{"target":"shell:member-a","tool":"shell_exec","arguments":{"command":"echo node-conn-live-ok"},"background":true}`)
	// A valid bearer alone must not enqueue a command. Missing instructions
	// are an explicit, side-effect-free refusal, not an accepted receipt.
	direct := httptest.NewRequest(http.MethodPost, "/mcp-relay/call", bytes.NewReader(body))
	direct.Header.Set("Authorization", "Bearer fixture-mcp-bearer-0123456789abcdefghijklmnop")
	direct.Header.Set("Content-Type", "application/json")
	directW := httptest.NewRecorder()
	member.Handler().ServeHTTP(directW, direct)
	var refusal map[string]any
	if err := json.Unmarshal(directW.Body.Bytes(), &refusal); err != nil {
		t.Fatal(err)
	}
	if directW.Code != http.StatusPreconditionRequired || refusal["executed"] != false ||
		firstString(mapValue(refusal["error"]), "code") != "instructions_required" {
		t.Fatalf("member admitted execution without instructions: %d %s", directW.Code, directW.Body.String())
	}
	member.mu.Lock()
	jobsBefore := len(member.shellJobs)
	member.mu.Unlock()
	if jobsBefore != 0 {
		t.Fatalf("instruction refusal created %d commands", jobsBefore)
	}

	// Bootstrap with the same caller through actual authenticated discovery;
	// no manually signed receipts and no instruction-gate opt-out.
	bootstrap := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"discover","arguments":{}}}`))
	bootstrap.Header.Set("Authorization", "Bearer fixture-mcp-bearer-0123456789abcdefghijklmnop")
	bootstrap.Header.Set("Content-Type", "application/json")
	bootstrap.Header.Set("MCP-Protocol-Version", mcpProtocolVersion)
	bootstrap.Header.Set("Mcp-Method", "tools/call")
	bootstrap.Header.Set("Mcp-Name", "discover")
	bootstrapW := httptest.NewRecorder()
	writer.Handler().ServeHTTP(bootstrapW, bootstrap)
	var bootstrapRPC map[string]any
	if err := json.Unmarshal(bootstrapW.Body.Bytes(), &bootstrapRPC); err != nil {
		t.Fatal(err)
	}
	instructions := mapValue(mapValue(bootstrapRPC["result"])["structuredContent"])
	instructionContext := firstString(instructions, "context")
	if bootstrapW.Code != http.StatusOK || instructionContext == "" || firstString(instructions, "instructions") == "" {
		t.Fatalf("writer did not deliver instructions: %d %s", bootstrapW.Code, bootstrapW.Body.String())
	}

	r := httptest.NewRequest(http.MethodPost, "/mcp-relay/call", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer fixture-mcp-bearer-0123456789abcdefghijklmnop")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(instructionContextHeader, instructionContext)
	w := httptest.NewRecorder()
	// Include the writer's authentication and admission checks, then traverse
	// the existing live WebSocket connection to the member's HTTP handler.
	writer.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("forwarded call failed: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"owner_target":"shell:member-a"`) {
		t.Fatalf("receipt missing owner metadata contract: %s", w.Body.String())
	}
	var receipt map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	jobID := firstString(receipt, "job_id")
	if receipt["error"] != nil || jobID == "" ||
		(firstString(receipt, "status") != "queued" && firstString(receipt, "status") != "running") {
		t.Fatalf("expected the owner's pending job receipt: %s", w.Body.String())
	}
	member.mu.Lock()
	job := member.shellJobs[jobID]
	ownerAdmitted := len(member.shellJobs) == 1 && job != nil && job.Server == "member-a" &&
		job.Cmd == "echo node-conn-live-ok" && job.Status == "queued"
	member.mu.Unlock()
	if !ownerAdmitted {
		t.Fatal("receipt does not identify exactly one command queued at its owner")
	}
	writer.mu.Lock()
	writerJobs := len(writer.shellJobs)
	writer.mu.Unlock()
	if writerJobs != 0 {
		t.Fatalf("writer duplicated owner work: %d local commands", writerJobs)
	}
	select {
	case observed := <-forwardedHeaders:
		if observed.Get("Authorization") != r.Header.Get("Authorization") || observed.Get(instructionContextHeader) != instructionContext {
			t.Fatal("forwarder changed the original caller or lost the instruction receipt")
		}
	default:
		t.Fatal("request never reached the member HTTP handler")
	}
}

// Fast local transport regression (<1s, max5s); live fleet acceptance is
// separate. Arbitrary frame headers must not cross the loopback boundary.
func TestNodeConnLocalExecuteKeepsOnlyMCPRoutingHeaders(t *testing.T) {
	observed := make(chan http.Header, 2)
	listener := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observed <- r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer listener.Close()
	s := &Server{cfg: Config{Addr: listener.Listener.Addr().String()}}
	for _, endpoint := range []string{"/mcp", "/admin/api/mcp/execute"} {
		frame := nodeConnFrame{Endpoint: endpoint, Auth: "Bearer original", Body: []byte(`{}`), Headers: map[string]string{
			"Accept": "application/json", "MCP-Protocol-Version": mcpProtocolVersion,
			"Mcp-Method": "tools/call", "Mcp-Name": "execute", "Cookie": "foreign-cookie",
			"Mcp-Session-Id": "foreign-session", "Authorization": "Bearer replacement", "X-User": "foreign-user",
		}}
		status, _, _ := s.nodeConnLocalExecute(frame)
		if status != http.StatusOK {
			t.Fatalf("loopback status %d", status)
		}
		headers := <-observed
		if headers.Get("Authorization") != frame.Auth || headers.Get("Cookie") != "" || headers.Get("Mcp-Session-Id") != "" || headers.Get("X-User") != "" {
			t.Fatal("caller or session isolation changed")
		}
		for _, name := range []string{"MCP-Protocol-Version", "Mcp-Method", "Mcp-Name"} {
			want := ""
			if endpoint == "/mcp" {
				want = frame.Headers[name]
			}
			if headers.Get(name) != want {
				t.Fatalf("endpoint %s lost routing header %s", endpoint, name)
			}
		}
	}
}

// Split-horizon DNS (the writer hostname resolving to this node's own proxy,
// as under ingress failover) must not break the persistent connection: the
// member dials the writer through the pinned physical address exactly like
// its heartbeat and snapshot traffic.
func TestNodeConnDialHonorsWriterConnectTo(t *testing.T) {
	t.Setenv("GPTADMIN_TEST_MCP_BEARER", "fixture-mcp-bearer-0123456789abcdefghijklmnop")
	writer := nodeConnWriterNode(t, "writer-node")
	writerServer := httptest.NewServer(writer.Handler())
	defer writerServer.Close()
	writer.cfg.NodePublicURL = writerServer.URL
	writer.ensureWriterSelfEntry()

	_, port, err := net.SplitHostPort(writerServer.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	// The writer hostname is deliberately unresolvable; only the pinned
	// physical address can reach the real listener.
	member, nodeID, nodeKey := membershipMemberNode(t, "member-a", "http://writer-loopback.invalid:"+port, "writer-node-identity")
	member.cfg.AuthSourceID = "writer-node-identity"
	member.cfg.NodeWriterConnectTo = "127.0.0.1"
	memberServer := httptest.NewServer(member.Handler())
	defer memberServer.Close()
	member.cfg.Addr = memberServer.Listener.Addr().String()
	member.cfg.ActionSyncWait = 2 * time.Second
	member.cfg.DefaultTimeout = 3 * time.Second

	grant := membershipIssueGrant(t, writer, "shell:member-a")
	joined := membershipJoin(t, writer, grant, nodeID, nodeKey, "shell:member-a", "https://unreachable.invalid")
	if joined["__status"] != http.StatusOK || joined["state"] != nodeStateJoining {
		t.Fatalf("join failed: %v", joined)
	}
	client := newNodeMembershipClient(member.cfg.NodeWriterConnectTo)
	member.membershipMemberTick(client, true)
	member.membershipMemberTick(client, false)

	member.startMembershipLoops()
	waitForNodeConn(t, writer, "shell:member-a")

	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"execute","arguments":{"target":"shell:member-a","tool":"shell_exec","arguments":{"command":"echo node-conn-connect-to-ok"}}}}`)
	r := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer fixture-mcp-bearer-0123456789abcdefghijklmnop")
	w := httptest.NewRecorder()
	writer.forwardNodePeerRequest(w, r, "shell:member-a", body, "/mcp")
	if w.Code != http.StatusOK {
		t.Fatalf("forwarded call failed: %d %s", w.Code, w.Body.String())
	}
}

func TestNodeConnectAuth(t *testing.T) {
	writer := membershipWriterNode(t, "writer-node")
	if response := membershipHTTP(writer, http.MethodGet, "/admin/api/nodes/connect", "", nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous connect accepted: %d", response.Code)
	}
	if response := membershipHTTP(writer, http.MethodGet, "/admin/api/nodes/connect", "wrong-key", nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("bogus node key accepted: %d", response.Code)
	}

	writerServer := httptest.NewServer(writer.Handler())
	defer writerServer.Close()
	writer.cfg.NodePublicURL = writerServer.URL
	writer.ensureWriterSelfEntry()

	_, nodeID, nodeKey := membershipMemberNode(t, "member-a", writerServer.URL, "")
	grant := membershipIssueGrant(t, writer, "shell:member-a")
	joined := membershipJoin(t, writer, grant, nodeID, nodeKey, "shell:member-a", "http://127.0.0.1:1")
	if joined["__status"] != http.StatusOK {
		t.Fatalf("join failed: %v", joined)
	}
	// A disabled member must be refused before the upgrade.
	if response := membershipHTTP(writer, http.MethodPost, "/admin/api/nodes/disable", "fixture-owner",
		[]byte(`{"node_id":"`+nodeID+`"}`)); response.Code != http.StatusOK {
		t.Fatalf("disable failed: %d %s", response.Code, response.Body.String())
	}
	if response := membershipHTTP(writer, http.MethodGet, "/admin/api/nodes/connect", nodeKey, nil); response.Code != http.StatusForbidden {
		t.Fatalf("disabled member connect accepted: %d", response.Code)
	}
}
