package hub

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// The subprocess runs the actual HTTP handler and storage with fresh fixture
// credentials. No production environment/configuration or shell is executed.
func TestArchitectureTwoHubProcessesHTTP(t *testing.T) {
	if dir := os.Getenv("GPTADMIN_ARCHITECTURE_HTTP_FIXTURE"); dir != "" {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		origin := "http://" + listener.Addr().String()
		s := New(Config{ConfigDir: dir, AdminPassword: "fixture-admin", ShellToken: "fixture-shell", PublicOrigin: origin, DefaultTimeout: time.Second, PollMaxTimeout: time.Second})
		s.agents["shell:fixture"] = &Agent{AgentID: "shell:fixture", Name: "Fixture", Kind: "virtual_shell", Status: "online", Transport: "long_poll", LastSeen: nowFloat(), Meta: map[string]any{"approved": true}}
		fmt.Println(origin)
		if err := http.Serve(listener, s.Handler()); err != nil {
			t.Fatal(err)
		}
		return
	}
	dir := t.TempDir()
	start := func() (*exec.Cmd, string, *http.Client) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestArchitectureTwoHubProcessesHTTP$")
		command.Env = append(os.Environ(), "GPTADMIN_ARCHITECTURE_HTTP_FIXTURE="+dir)
		output, err := command.StdoutPipe()
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		if err = command.Start(); err != nil {
			cancel()
			t.Fatal(err)
		}
		stopped := false
		t.Cleanup(func() {
			if !stopped {
				_ = command.Process.Kill()
				_ = command.Wait()
			}
			cancel()
		})
		line, err := bufio.NewReader(output).ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		origin := strings.TrimSpace(line)
		if !strings.HasPrefix(origin, "http://127.0.0.1:") {
			t.Fatalf("unexpected fixture startup: %q", origin)
		}
		jar, _ := cookiejar.New(nil)
		client := &http.Client{Jar: jar, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		response, err := client.PostForm(origin+"/admin/login", url.Values{"password": {"fixture-admin"}})
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 302 {
			t.Fatalf("fixture login: %d", response.StatusCode)
		}
		return command, origin, client
	}
	call := func(client *http.Client, origin, method, path string, body any, shell bool) map[string]any {
		t.Helper()
		var reader io.Reader
		if body != nil {
			payload, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			reader = bytes.NewReader(payload)
		}
		request, err := http.NewRequest(method, origin+path, reader)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		if shell {
			request.Header.Set("Authorization", "Bearer fixture-shell")
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != 200 {
			t.Fatalf("%s %s: HTTP %d: %s", method, path, response.StatusCode, data)
		}
		var result map[string]any
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatalf("%s: %v: %s", path, err, data)
		}
		return result
	}
	primary, primaryURL, primaryClient := start()
	body := map[string]any{"target": "shell:fixture", "tool": "shell_exec", "args": map[string]any{"cmd": "fixture-not-executed"}, "background": true, "idempotency_key": "http-once"}
	first := call(primaryClient, primaryURL, http.MethodPost, "/mcp-relay/call", body, false)
	id := firstString(first, "job_id", "task_id")
	if id == "" {
		t.Fatalf("missing task: %v", first)
	}
	_, standbyURL, standbyClient := start()
	state := call(standbyClient, standbyURL, http.MethodGet, "/mcp-relay/job/"+id, nil, false)
	if state["status"] != "queued" {
		t.Fatalf("standby failed a live owner's task: %v", state)
	}
	replay := call(standbyClient, standbyURL, http.MethodPost, "/mcp-relay/call", body, false)
	if firstString(replay, "job_id", "task_id") != id {
		t.Fatalf("cross-process duplicate: %v", replay)
	}
	dispatched := call(primaryClient, primaryURL, http.MethodGet, "/queue/fixture?timeout=1", nil, true)
	if dispatched["id"] != id {
		t.Fatalf("unexpected dispatch: %v", dispatched)
	}
	call(standbyClient, standbyURL, http.MethodPost, "/queue/fixture/result", map[string]any{"id": id, "result": map[string]any{"stdout": "cross-process-result"}}, true)
	completed := call(primaryClient, primaryURL, http.MethodGet, "/mcp-relay/job/"+id, nil, false)
	if completed["status"] != "completed" {
		t.Fatalf("primary cannot see standby result: %v", completed)
	}
	body["idempotency_key"] = "http-owner-crash"
	queued := call(primaryClient, primaryURL, http.MethodPost, "/mcp-relay/call", body, false)
	queuedID := firstString(queued, "job_id", "task_id")
	if err := primary.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = primary.Wait()
	liveRecovered := call(standbyClient, standbyURL, http.MethodGet, "/mcp-relay/job/"+queuedID, nil, false)
	if liveRecovered["status"] != "failed" {
		t.Fatalf("already-running standby did not notice dead owner: %v", liveRecovered)
	}
	_, recoveryURL, recoveryClient := start()
	recovered := call(recoveryClient, recoveryURL, http.MethodGet, "/mcp-relay/job/"+queuedID, nil, false)
	if recovered["status"] != "failed" {
		t.Fatalf("dead-owner task not reconciled: %v", recovered)
	}
	t.Log("HTTP proof: two live processes, same-key replay, owner-only dispatch, cross-process result, OS-lock recovery after process death")
}
