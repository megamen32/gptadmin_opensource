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
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestSharedAccessTwoProcessesHTTP(t *testing.T) {
	if dir := os.Getenv("GPTADMIN_SHARED_ACCESS_FIXTURE"); dir != "" {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		s := New(Config{ConfigDir: dir, CtlToken: "fixture-owner", OAuthClientSecret: "fixture-signing", PublicOrigin: "https://hub.example"})
		fmt.Println("http://" + listener.Addr().String())
		if err = http.Serve(listener, s.Handler()); err != nil {
			t.Fatal(err)
		}
		return
	}
	dir := t.TempDir()
	start := func() string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSharedAccessTwoProcessesHTTP$")
		cmd.Env = append(os.Environ(), "GPTADMIN_SHARED_ACCESS_FIXTURE="+dir)
		output, err := cmd.StdoutPipe()
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		if err = cmd.Start(); err != nil {
			cancel()
			t.Fatal(err)
		}
		t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait(); cancel() })
		line, err := bufio.NewReader(output).ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		origin := strings.TrimSpace(line)
		if !strings.HasPrefix(origin, "http://127.0.0.1:") {
			t.Fatal("bad fixture address")
		}
		return origin
	}
	client := &http.Client{Timeout: 5 * time.Second}
	call := func(origin, token, method, path string, body any) (map[string]any, int) {
		t.Helper()
		data, _ := json.Marshal(body)
		r, err := http.NewRequest(method, origin+path, bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err = io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		if err = json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		return result, response.StatusCode
	}
	a, b := start(), start()
	issued, status := call(a, "fixture-owner", "POST", "/admin/api/mcp/issue-token", map[string]any{"client_id": "http-admin", "role": "admin", "access_mode": "full"})
	if status != 200 {
		t.Fatal(status)
	}
	token, id := firstString(issued, "access_token"), firstString(issued, "token_id")
	if _, status = call(b, token, "POST", "/mcp-relay/call", map[string]any{"target": "hub", "tool": "access_profiles", "args": map[string]any{"action": "create", "id": "http-policy", "profile": map[string]any{"access_mode": "readonly", "allowed_targets": []string{"*"}, "allowed_tools": []string{"*"}}}}); status != 200 {
		t.Fatalf("native admin management on second process: %d", status)
	}
	fresh := start()
	value, status := call(fresh, token, "GET", "/admin/api/mcp/tokens/"+id+"/value", nil)
	if status != 200 || value["access_token"] != token {
		t.Fatalf("saved token unavailable after fresh process: %d", status)
	}
	if _, status = call(b, "fixture-owner", "PUT", "/admin/api/client-bindings/"+id, map[string]any{"profile_id": "http-policy"}); status != 200 {
		t.Fatal(status)
	}
	if _, status = call(a, token, "GET", "/admin/api/clients", nil); status != 403 {
		t.Fatalf("other process ignored read-only profile: %d", status)
	}
	if _, status = call(a, "fixture-owner", "DELETE", "/admin/api/client-bindings/"+id, nil); status != 200 {
		t.Fatal(status)
	}
	if _, status = call(b, token, "GET", "/admin/api/clients", nil); status != 200 {
		t.Fatalf("explicit unbind failed across processes: %d", status)
	}
	if _, status = call(b, "fixture-owner", "DELETE", "/admin/api/clients/"+id, nil); status != 200 {
		t.Fatal(status)
	}
	for _, origin := range []string{a, b, fresh} {
		if _, status = call(origin, token, "GET", "/mcp-relay/servers", nil); status != 401 {
			t.Fatalf("revoked token still valid through process: %d", status)
		}
	}
	t.Log("separate processes: issue, native admin profile creation, saved value, readonly bind, unbind, revoke all PASS")
}
