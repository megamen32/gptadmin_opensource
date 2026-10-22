package hub

// A minimal ShellMCP-like executor for the code_mode tests.
//
// It speaks the real agent transport: it polls the Hub's task queue, answers
// the queued tool call locally and posts the result back. A test that uses it
// therefore exercises the same dispatch, authorization and receipt path a real
// host uses, instead of a stubbed shortcut.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// codeModeShellAgent answers queued tool calls for one shell host.
type codeModeShellAgent struct {
	name    string
	token   string
	base    string
	stop    chan struct{}
	once    sync.Once
	mu      sync.Mutex
	calls   []map[string]any
	handler func(tool string, args map[string]any) (any, error)
}

// codeModeShellHost is a live Hub with one registered shell host.
type codeModeShellHost struct {
	server *Server
	http   *httptest.Server
	agent  *codeModeShellAgent
}

// startCodeModeShellHost registers one shell host and starts its executor.
func startCodeModeShellHost(t *testing.T, handler func(tool string, args map[string]any) (any, error)) *codeModeShellHost {
	t.Helper()
	s := New(Config{
		ConfigDir:      t.TempDir(),
		CtlToken:       "ctl",
		ShellToken:     "shell-secret",
		DefaultTimeout: 5 * time.Second,
		PollMaxTimeout: time.Second,
		ActionSyncWait: 5 * time.Second,
	})
	t.Cleanup(func() { _ = s.Close() })
	s.mu.Lock()
	s.agents["shell:fixture"] = &Agent{
		AgentID: "shell:fixture", Name: "fixture", Kind: "shell", Transport: "long_poll", Status: "online",
		Meta: map[string]any{"approved": true, "build_version": 400, "enabled": true},
	}
	s.mu.Unlock()
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	agent := &codeModeShellAgent{name: "fixture", token: "shell-secret", base: ts.URL, stop: make(chan struct{}), handler: handler}
	go agent.run()
	t.Cleanup(agent.close)
	return &codeModeShellHost{server: s, http: ts, agent: agent}
}

func (a *codeModeShellAgent) close() {
	a.once.Do(func() { close(a.stop) })
}

// callCount reports how many tool calls the executor answered.
func (a *codeModeShellAgent) callCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.calls)
}

// callsFor returns the recorded calls of one tool.
func (a *codeModeShellAgent) callsFor(tool string) []map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := []map[string]any{}
	for _, call := range a.calls {
		if firstString(call, "tool") == tool {
			out = append(out, call)
		}
	}
	return out
}

func (a *codeModeShellAgent) record(tool string, args map[string]any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, map[string]any{"tool": tool, "args": args})
}

func (a *codeModeShellAgent) run() {
	client := &http.Client{Timeout: 30 * time.Second}
	for {
		select {
		case <-a.stop:
			return
		default:
		}
		a.pollOnce(client)
	}
}

func (a *codeModeShellAgent) pollOnce(client *http.Client) {
	endpoint := a.base + "/queue/" + url.PathEscape(a.name) + "?timeout=1"
	request, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return
	}
	request.Header.Set("Authorization", "Bearer "+a.token)
	response, err := client.Do(request)
	if err != nil {
		time.Sleep(20 * time.Millisecond)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		time.Sleep(20 * time.Millisecond)
		return
	}
	var job map[string]any
	if json.Unmarshal(body, &job) != nil {
		time.Sleep(20 * time.Millisecond)
		return
	}
	id := firstString(job, "id")
	tool := firstString(job, "tool_name")
	if id == "" || tool == "" {
		// The queue answered with its runtime settings only.
		time.Sleep(20 * time.Millisecond)
		return
	}
	args := mapValue(job["arguments"])
	a.record(tool, args)
	payload := map[string]any{"id": id}
	if a.handler == nil {
		payload["error"] = "no handler"
	} else {
		result, err := a.handler(tool, args)
		if err != nil {
			payload["error"] = err.Error()
		} else {
			payload["result"] = result
		}
	}
	encoded, _ := json.Marshal(payload)
	post, err := http.NewRequest(http.MethodPost, a.base+"/queue/"+url.PathEscape(a.name)+"/result", bytes.NewReader(encoded))
	if err != nil {
		return
	}
	post.Header.Set("Authorization", "Bearer "+a.token)
	post.Header.Set("Content-Type", "application/json")
	answer, err := client.Do(post)
	if err == nil {
		io.Copy(io.Discard, answer.Body)
		answer.Body.Close()
	}
}

// codeModeShellResult wraps a tool payload the way a ShellMCP agent does.
func codeModeShellResult(structured map[string]any) map[string]any {
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}, "structuredContent": structured}
}

// codeModeFileEditorHandler answers file_editor calls against a real directory.
// It is deliberately small: a view reads the file, a create writes it.
func codeModeFileEditorHandler(t *testing.T, root string) func(tool string, args map[string]any) (any, error) {
	t.Helper()
	return func(tool string, args map[string]any) (any, error) {
		if tool != "file_editor" {
			return nil, nil
		}
		path := firstString(args, "path")
		action := firstString(args, "action")
		switch action {
		case "view":
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			return codeModeShellResult(map[string]any{
				"text": string(data), "text_exact": true, "bytes": len(data),
				"line_count": strings.Count(string(data), "\n"), "file_bytes": len(data), "truncated": false,
			}), nil
		case "create":
			content := firstString(args, "content")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return nil, err
			}
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				return nil, err
			}
			return codeModeShellResult(map[string]any{"text": content, "text_exact": true, "bytes": len(content)}), nil
		default:
			return nil, nil
		}
	}
}

// codeModeHostRequest builds a caller request for the live host.
func codeModeHostRequest(t *testing.T, host *codeModeShellHost) *http.Request {
	t.Helper()
	r := httptest.NewRequest("POST", "/mcp", nil)
	return requestWithAuthClaims(r, map[string]any{
		"sub": "code-mode-client", "client_id": "code-mode-client",
		"scope": "gptadmin.read gptadmin.exec", "access_mode": accessModeFull,
	})
}

// codeModeReadOnlyRun runs one read-only script against the live host.
func codeModeReadOnlyRun(t *testing.T, host *codeModeShellHost, r *http.Request, code string) (map[string]any, int) {
	t.Helper()
	return host.server.callCodeModeToolMode(r, map[string]any{"code": code}, true)
}

// codeModeExecRun runs one ordinary script against the live host.
func codeModeExecRun(t *testing.T, host *codeModeShellHost, r *http.Request, code string, extra map[string]any) (map[string]any, int) {
	t.Helper()
	args := map[string]any{"code": code}
	for key, value := range extra {
		args[key] = value
	}
	return host.server.callCodeModeTool(r, args)
}

// ---------------------------------------------------------------------------
// The read-only execution ceiling
// ---------------------------------------------------------------------------

// A read-only script reads a real file and a permitted Hub facade, and every
// refused operation leaves the target untouched.
func TestReadOnlyExecReadsFileAndRefusesWrites(t *testing.T) {
	root := t.TempDir()
	readable := filepath.Join(root, "readable.txt")
	if err := os.WriteFile(readable, []byte("alpha\nbeta\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	forbidden := filepath.Join(root, "forbidden.txt")
	host := startCodeModeShellHost(t, codeModeFileEditorHandler(t, root))
	r := codeModeHostRequest(t, host)

	read, status := codeModeReadOnlyRun(t, host, r, `const f=file('file:fixture'); const r=await f('read',`+strconv.Quote(readable)+`,{max_bytes:4096}); return r.text;`)
	if status != http.StatusOK || firstString(read, "status") != "completed" {
		t.Fatalf("read-only file read failed: %d %#v", status, read)
	}
	got, _ := read["result"].(string)
	if got != "alpha\nbeta\n" {
		t.Fatalf("read-only file read returned %q", got)
	}

	// A permitted Hub read stays allowed.
	hubRead, status := codeModeReadOnlyRun(t, host, r, `const d=await mcp.call('hub','settings_schema',{}); return typeof d;`)
	if status != http.StatusOK || firstString(hubRead, "status") != "completed" {
		t.Fatalf("read-only Hub read failed: %d %#v", status, hubRead)
	}

	before := host.agent.callCount()
	for _, code := range []string{
		`const f=file('file:fixture'); await f('create',` + strconv.Quote(forbidden) + `,{text:'x'}); return 1;`,
		`await mcp.call('file:fixture','file_editor',{action:'create',path:` + strconv.Quote(forbidden) + `,content:'x'}); return 1;`,
		`await mcp.call('file:fixture','file_checkpoint',{action:'create',path:` + strconv.Quote(forbidden) + `}); return 1;`,
		`await mcp.call('file:fixture','file_backup',{action:'backup',path:` + strconv.Quote(forbidden) + `}); return 1;`,
		`await mcp.call('shell:fixture','shell_exec',{cmd:'echo denied'}); return 1;`,
		`await mcp.call('shell:fixture','shell_exec',{cmd:'cat /etc/hosts'}); return 1;`,
		`await mcp.call('control:fixture','mcp_manage',{action:'restart'}); return 1;`,
		`await mcp.call('control:fixture','mcp_manage',{action:'upsert',config:{}}); return 1;`,
		`await mcp.call('hub','settings_set',{settings:{}}); return 1;`,
		`await mcp.call('hub','approve_pending_server',{server_id:'x'}); return 1;`,
		`await mcp.call('hub','handover_start',{}); return 1;`,
		`await mcp.call('hub','exec',{code:'return 1;'}); return 1;`,
		`await mcp.call('hub','ro',{code:'return 1;'}); return 1;`,
		`await mcp.call('control:fixture','mcp_call',{ref:'x',name:'y',arguments:{}}); return 1;`,
		`await mcp.call('control:fixture','mcp_request',{method:'x'}); return 1;`,
		`await mcp.call('hub','fleetExec',{servers:['shell:fixture'],cmd:'echo denied'}); return 1;`,
		`await mcp.call('hub','fleet_exec',{servers:['shell:fixture'],cmd:'echo denied'}); return 1;`,
		`await index('persistent'); return 1;`,
		`await drop('persistent'); return 1;`,
		`await mcp.call('shell:fixture','shell_exec',{cmd:'echo denied'},{index:true}); return 1;`,
	} {
		got, st := codeModeReadOnlyRun(t, host, r, code)
		if st == http.StatusOK || firstString(got, "status") == "completed" {
			t.Fatalf("read-only ceiling allowed %q: %#v", code, got)
		}
	}
	if _, err := os.Stat(forbidden); !os.IsNotExist(err) {
		t.Fatalf("a refused read-only write created %s", forbidden)
	}
	if after := host.agent.callCount(); after != before {
		t.Fatalf("refused read-only operations reached the executor: %d calls", after-before)
	}
}

// A caught refusal does not lift the restriction: the next forbidden call is
// refused too and the run does not report completed.
func TestReadOnlyExecCaughtRefusalStaysRefused(t *testing.T) {
	root := t.TempDir()
	forbidden := filepath.Join(root, "forbidden.txt")
	host := startCodeModeShellHost(t, codeModeFileEditorHandler(t, root))
	r := codeModeHostRequest(t, host)
	before := host.agent.callCount()

	code := `try { await mcp.call('shell:fixture','shell_exec',{cmd:'echo denied'}); } catch(e) {} ` +
		`try { await mcp.call('file:fixture','file_editor',{action:'create',path:` + strconv.Quote(forbidden) + `,content:'x'}); } catch(e) {} ` +
		`try { await mcp.call('shell:fixture','shell_exec',{cmd:'echo again'}); } catch(e) {} return 42;`
	got, status := codeModeReadOnlyRun(t, host, r, code)
	if status == http.StatusOK || firstString(got, "status") == "completed" {
		t.Fatalf("a caught refusal let the run report completed: %d %#v", status, got)
	}
	if _, err := os.Stat(forbidden); !os.IsNotExist(err) {
		t.Fatalf("a caught refusal created %s", forbidden)
	}
	if after := host.agent.callCount(); after != before {
		t.Fatalf("a caught refusal reached the executor: %d calls", after-before)
	}
	if calls := intFromAny(got["calls"]); calls == 0 {
		t.Fatalf("the failed run did not report the executed operations: %#v", got)
	}
}

// A read-only script may build the search cache of a source it may read, but
// it may not create, change or delete a stored source.
func TestReadOnlyExecContentHelpersAreCacheOnly(t *testing.T) {
	s, r := codeModeFixture(t)
	defer s.Close()
	store, err := s.contentStoreForExec()
	if err != nil {
		t.Fatal(err)
	}
	principal := operationPrincipal(r)
	source, err := store.SaveFormatted(principal, "fixture", contentFreeTextOrigin, "text", "CACHE_ONLY_MARKER\nsecond line\n")
	if err != nil {
		t.Fatal(err)
	}
	quoted, _ := json.Marshal(source.ID)

	// The search cache for an allowed read may be built.
	built, status := s.callCodeModeToolMode(r, map[string]any{"code": `return await mcp.content.search('CACHE_ONLY_MARKER',{source:` + string(quoted) + `});`}, true)
	if status != http.StatusOK || firstString(built, "status") != "completed" {
		t.Fatalf("read-only search failed: %d %#v", status, built)
	}
	meta, err := store.Source(principal, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !meta.Indexed {
		t.Fatal("the read-only search did not build the search cache of an allowed read")
	}

	// Creating and deleting a stored source stay refused.
	for _, code := range []string{
		`await mcp.content.index('new source'); return 1;`,
		`await mcp.content.delete(` + string(quoted) + `); return 1;`,
		`await index('new source'); return 1;`,
		`await drop(` + string(quoted) + `); return 1;`,
	} {
		got, st := s.callCodeModeToolMode(r, map[string]any{"code": code}, true)
		if st == http.StatusOK || firstString(got, "status") == "completed" {
			t.Fatalf("read-only ceiling allowed %q: %#v", code, got)
		}
	}
}
