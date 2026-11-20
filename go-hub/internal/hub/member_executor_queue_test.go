package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	shell "github.com/megamen32/gptadmin/go-shellmcp/runtime"
)

// The parent fixture supplies a real leader and follower with an initialized
// task baseline. Use the actual scoped MCP, member queue, embedded ShellMCP
// and SQLite on both voters. Bootstrap has its own integration coverage.
func testNonvoterExecutorQueue(t *testing.T, leader, replica *Server) {
	dir := t.TempDir()
	member := New(Config{ConfigDir: filepath.Join(dir, "ordinary"), PollMaxTimeout: time.Second})
	defer member.Close()
	local := httptest.NewServer(member.Handler())
	defer local.Close()
	mux := http.NewServeMux()
	mux.HandleFunc(memberQueuePrefix, leader.memberExecutorQueue)
	mux.HandleFunc("/server/", leader.serverMCPEndpoint)
	cluster := httptest.NewTLSServer(mux)
	defer cluster.Close()
	member.nodePeerClient = cluster.Client()
	member.memberQuorumHubs.Set([]string{cluster.URL})
	member.nodeIdentity = &nodeIdentity{NodeID: "mac", NodeKey: strings.Repeat("m", 32)}
	worker := shell.New(shell.Config{Name: "mac", Token: strings.Repeat("w", 32), HubURL: local.URL,
		IdentityDir: filepath.Join(dir, "identity"), OutboxDir: filepath.Join(dir, "outbox"), SpillDir: filepath.Join(dir, "spill"),
		DefaultCwd: dir, DefaultHome: dir, QueueEnabled: true, Mode: "long_poll", QueueTimeout: 1, QueueRetry: time.Second,
		ExecTimeout: 10, LogLimit: 1024, DisableSelfUpdate: true})
	defer worker.Close()
	name, identity, err := worker.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if err := member.RegisterLocalExecutor(name, identity, strings.Repeat("w", 32)); err != nil {
		t.Fatal(err)
	}
	enrolled := leader.membership.Nodes["mac"]
	enrolled.LocalID, enrolled.LocalFingerprint = identity["server_id"], identity["fingerprint"]
	enrolled.KeyHash = hashNodeSecret(member.nodeIdentity.NodeKey)
	// No polling goroutines yet. Exercise authorization before letting the
	// real executor obtain any work.
	for _, tc := range []struct{ suffix, key, state string }{
		{"other", member.nodeIdentity.NodeKey, nodeStateReady},
		{"mac", "wrong-fixture-credential", nodeStateReady},
		{"mac", member.nodeIdentity.NodeKey, nodeStateDisabled},
		{"mac?server_id=wrong", member.nodeIdentity.NodeKey, nodeStateReady},
	} {
		enrolled.State = tc.state
		r := httptest.NewRequest("GET", memberQueuePrefix+tc.suffix, nil)
		r.Header.Set("Authorization", "Bearer "+tc.key)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("unauthorized member poll returned HTTP %d", w.Code)
		}
	}
	enrolled.State = nodeStateReady
	leader.cfg.PublicOrigin, leader.cfg.MCPResource = cluster.URL, cluster.URL+"/mcp"
	leader.cfg.OAuthClientSecret = strings.Repeat("c", 32)
	caller, err := leader.issueOAuthAccessToken("fixture-client", leader.cfg.MCPResource, "gptadmin.read gptadmin.exec")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	done := make(chan error, 1)
	go func() { done <- worker.ListenAndServeContext(ctx) }()
	defer func() { cancel(); <-done }()
	var slug string
	for ctx.Err() == nil && slug == "" {
		leader.mu.Lock()
		if a := leader.agents["shell:mac"]; a != nil && a.Status == "online" {
			slug = exposedAgentSlug(*a)
		}
		leader.mu.Unlock()
		if slug == "" {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if slug == "" {
		t.Fatal("member's TLS queue poll did not register its enrolled executor")
	}
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"shell_exec","arguments":{"cmd":"printf x >> invocations; printf done"},"_meta":{"com.gptadmin/idempotencyKey":"one-process","io.modelcontextprotocol/clientCapabilities":{"extensions":{"io.modelcontextprotocol/tasks":{}}}}}}`)
	submit := func() string {
		t.Helper()
		r, err := http.NewRequestWithContext(ctx, "POST", local.URL+"/server/"+slug+"/mcp", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+caller)
		r.Header.Set("Content-Type", "application/json")
		resp, err := local.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var rpc struct {
			Result struct {
				TaskID string `json:"taskId"`
			}
		}
		raw, _ := io.ReadAll(resp.Body)
		if err = json.Unmarshal(raw, &rpc); err != nil || resp.StatusCode != 200 || rpc.Result.TaskID == "" {
			t.Fatalf("ordinary ingress failed to return a task ID (HTTP %d): %s", resp.StatusCode, string(raw))
		}
		return rpc.Result.TaskID
	}
	id := submit()
	var completed persistedShellTask
	for ctx.Err() == nil {
		state, err := replica.readTaskRecords(id)
		if err == nil && state.Shell[id].Status == "completed" {
			completed = state.Shell[id]
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if completed.ID != id || completed.OwnerID == "" || completed.Server != "mac" || completed.Caller == nil {
		t.Fatalf("follower receipt: status=%q server=%q owner=%t caller=%t", completed.Status, completed.Server, completed.OwnerID != "", completed.Caller != nil)
	}
	if submit() != id {
		t.Fatal("retry at ordinary ingress created another task")
	}
	state, err := member.readTaskRecords(id)
	if err != nil || state.Shell[id].ID != "" {
		t.Fatal("cluster task was recorded in the nonvoter's independent history")
	}
	data, err := os.ReadFile(filepath.Join(dir, "invocations"))
	if err != nil || string(data) != "x" {
		t.Fatal("expected one physical command execution on the nonvoter")
	}
	// A delayed duplicate result with a different body cannot rewrite the
	// first majority-committed terminal record.
	post, _ := http.NewRequest("POST", local.URL+"/queue/mac/result", strings.NewReader(`{"id":"`+id+`","result":"late duplicate"}`))
	post.Header.Set("Authorization", "Bearer "+strings.Repeat("w", 32))
	resp, err := local.Client().Do(post)
	if err != nil {
		t.Fatal(err)
	}
	answer, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != 200 || !bytes.Contains(answer, []byte(`"already_recorded":true`)) {
		t.Fatal("duplicate outbox result was not acknowledged at its owner")
	}
	state, err = leader.readTaskRecords(id)
	if err != nil || state.Shell[id].Revision != completed.Revision || state.Shell[id].DoneAt != completed.DoneAt {
		t.Fatal("duplicate result changed the committed terminal receipt")
	}
}
