package hub

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A separate Hub process uses the real ledger and HTTP queue; the ShellMCP
// executable polls it and executes the command. Only the test-only /run
// endpoint supplies fixture claims; it does not simulate execution or results.
func TestCodeModeDurableRealProcesses(t *testing.T) {
	if dir := os.Getenv("GPTADMIN_DURABLE_PROCESS_FIXTURE"); dir != "" {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		s := New(Config{ConfigDir: dir, ShellToken: "fixture-shell", DefaultTimeout: time.Second, PollMaxTimeout: time.Second})
		codeModeRuns = newCodeModeGate(1)
		s.mu.Lock()
		s.agents["shell:fixture"] = &Agent{AgentID: "shell:fixture", Name: "Fixture", Kind: "virtual_shell", Transport: "long_poll", Status: "online", LastSeen: nowFloat(), Meta: map[string]any{"approved": true}}
		s.mu.Unlock()
		mux := http.NewServeMux()
		mux.HandleFunc("/test/age", func(w http.ResponseWriter, r *http.Request) {
			var a struct {
				ID  string
				Age int
			}
			if json.NewDecoder(r.Body).Decode(&a) != nil {
				http.Error(w, "bad input", 400)
				return
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			j := s.shellJobs[a.ID]
			if j == nil {
				http.Error(w, "missing task", 404)
				return
			}
			change := s.beginTaskMutationLocked(a.ID)
			j.CreatedAt = nowFloat() - float64(a.Age)
			if err := change.commit(); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			if _, err := s.maintainTaskStateLocked(); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			w.WriteHeader(200)
		})
		mux.HandleFunc("/test/run", func(w http.ResponseWriter, r *http.Request) {
			var a map[string]any
			if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			request := httptest.NewRequest("POST", "/mcp", nil)
			request = requestWithAuthClaims(request, map[string]any{"sub": "code-mode-client", "client_id": "code-mode-client", "scope": "gptadmin.read gptadmin.exec", "access_mode": accessModeFull})
			out, status := s.callCodeModeTool(request, a)
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(out)
		})
		mux.Handle("/", s.Handler())
		fmt.Println("http://" + l.Addr().String())
		if err := http.Serve(l, mux); err != nil {
			t.Fatal(err)
		}
		return
	}
	node := os.Getenv("GPTADMIN_TEST_SHELLMCP")
	if node == "" {
		t.Skip("set GPTADMIN_TEST_SHELLMCP to a freshly built ShellMCP executable")
	}
	dir := t.TempDir()
	startHub := func() (*exec.Cmd, string) {
		c := exec.Command(os.Args[0], "-test.run=^TestCodeModeDurableRealProcesses$")
		c.Env = append(os.Environ(), "GPTADMIN_DURABLE_PROCESS_FIXTURE="+dir)
		stdout, err := c.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		c.Stderr = os.Stderr
		if err = c.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if c.ProcessState == nil {
				_ = c.Process.Kill()
				_ = c.Wait()
			}
		})
		line, err := bufio.NewReader(stdout).ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		return c, strings.TrimSpace(line)
	}
	call := func(origin, code string) map[string]any {
		b, _ := json.Marshal(map[string]any{"code": code, "timeout_ms": 5000})
		res, err := http.Post(origin+"/test/run", "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil || res.StatusCode != 200 {
			t.Fatalf("code run HTTP %d %s: %v", res.StatusCode, raw, err)
		}
		return out
	}
	hub, origin := startHub()
	n := exec.Command(node)
	n.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "HUB_URL=" + origin, "SHELL_TOKEN=fixture-shell", "SHELL_NAME=fixture", "SHELLMCP_QUEUE=1", "QUEUE_LONG_POLL_TIMEOUT_S=1", "SHELL_IDENTITY_DIR=" + filepath.Join(dir, "identity"), "SHELL_SPOOL_DIR=" + filepath.Join(dir, "spool")}
	n.Stderr = os.Stderr
	if err := n.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Process.Kill(); _ = n.Wait() })
	marker := filepath.Join(dir, "started")
	command, _ := json.Marshal("printf started > " + marker + "; sleep 3; printf DURABLE_REAL_MARKER")
	out := call(origin, fmt.Sprintf(`return await mcp.tasks.start('shell:fixture','shell_exec',{cmd:%s,timeout:15});`, command))
	id := firstString(mapValue(out["result"]), "taskId")
	if id == "" {
		t.Fatalf("missing handle: %v", out)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("real command did not start")
		}
	}
	out = call(origin, fmt.Sprintf(`return {task:mcp.tasks.get(%q),independent:42};`, id))
	if mapValue(out["result"])["independent"] != float64(42) || firstString(mapValue(mapValue(out["result"])["task"]), "status") != "working" {
		t.Fatalf("task not running independently: %v", out)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		out = call(origin, fmt.Sprintf(`return mcp.tasks.get(%q);`, id))
		task := mapValue(out["result"])
		if firstString(task, "status") == "completed" {
			raw, _ := json.Marshal(task)
			if !strings.Contains(string(raw), "DURABLE_REAL_MARKER") {
				t.Fatalf("missing result: %s", raw)
			}
			result := call(origin, fmt.Sprintf(`return mcp.tasks.result(%q);`, id))
			encoded, _ := json.Marshal(result)
			if !strings.Contains(string(encoded), "DURABLE_REAL_MARKER") {
				t.Fatalf("result method: %s", encoded)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("real node did not finish: %v", out)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err := hub.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = hub.Wait()
	_ = n.Process.Kill()
	_ = n.Wait()
	restartedHub, origin := startHub()
	out = call(origin, fmt.Sprintf(`return mcp.tasks.get(%q);`, id))
	raw, _ := json.Marshal(out)
	if !strings.Contains(string(raw), "DURABLE_REAL_MARKER") {
		t.Fatalf("result lost across Hub process restart: %s", raw)
	}
	start := func() string {
		o := call(origin, `return await mcp.tasks.start('shell:fixture','shell_exec',{cmd:'printf never-dispatched',timeout:15});`)
		id := firstString(mapValue(o["result"]), "taskId")
		if id == "" {
			t.Fatalf("missing task id: %v", o)
		}
		return id
	}
	cancelID := start()
	out = call(origin, fmt.Sprintf(`mcp.tasks.cancel(%q); return mcp.tasks.get(%q);`, cancelID, cancelID))
	if firstString(mapValue(out["result"]), "status") != "cancelled" {
		t.Fatalf("cancel: %v", out)
	}
	ttlID := start()
	age := func(seconds int) {
		b, _ := json.Marshal(map[string]any{"ID": ttlID, "Age": seconds})
		res, err := http.Post(origin+"/test/age", "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			data, _ := io.ReadAll(res.Body)
			t.Fatalf("age: %d %s", res.StatusCode, data)
		}
	}
	age(10790)
	out = call(origin, fmt.Sprintf(`return mcp.tasks.get(%q);`, ttlID))
	if firstString(mapValue(out["result"]), "status") != "working" {
		t.Fatalf("TTL expired early: %v", out)
	}
	age(10801)
	out = call(origin, fmt.Sprintf(`return mcp.tasks.get(%q);`, ttlID))
	if firstString(mapValue(out["result"]), "status") != "cancelled" {
		t.Fatalf("TTL did not expire: %v", out)
	}
	if err := restartedHub.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = restartedHub.Wait()
	_, origin = startHub()
	for _, persisted := range []string{cancelID, ttlID} {
		out = call(origin, fmt.Sprintf(`return mcp.tasks.get(%q);`, persisted))
		if firstString(mapValue(out["result"]), "status") != "cancelled" {
			t.Fatalf("cancelled state lost on restart: %v", out)
		}
	}
	t.Log("one JS slot released while real process ran; cancellation and simulated age 10790/10801 seconds verified")
	t.Logf("real ShellMCP completed %s after JS; persisted result recovered after Hub process restart", id)
}

func TestCodeModeTaskResultRequiresCompletedOwnedTask(t *testing.T) {
	s, r := taskBridgeFixture(t)
	id := startBridgeTask(t, s, r)
	code := fmt.Sprintf(`return mcp.tasks.result(%q);`, id)
	out, status := codeModeRun(t, s, r, map[string]any{"code": code})
	if status == http.StatusOK || firstString(mapValue(out["error"]), "code") != "task_not_completed" {
		t.Fatalf("unfinished result: %d %v", status, out)
	}
	foreign := requestWithAuthClaims(r, map[string]any{"sub": "foreign", "client_id": "foreign", "scope": "gptadmin.read gptadmin.exec", "access_mode": accessModeFull})
	out, status = codeModeRun(t, s, foreign, map[string]any{"code": code})
	if status == http.StatusOK || firstString(mapValue(out["error"]), "code") != "task_unavailable" {
		t.Fatalf("foreign result: %d %v", status, out)
	}
}
