package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func writeBridgeChild(t *testing.T, dir string) string {
	t.Helper()
	script := filepath.Join(dir, "child.py")
	body := `import json, os, sys, time
with open(os.environ["BRIDGE_STARTS"], "a", encoding="utf-8") as f:
    f.write("start\\n")
for line in sys.stdin:
    msg = json.loads(line)
    method = msg.get("method")
    if method == "initialize":
        out = {"jsonrpc":"2.0", "id":msg.get("id"), "result":{"protocolVersion":"2025-03-26", "capabilities":{"tools":{}}, "serverInfo":{"name":"bridge-fixture","version":"1"}}}
        print(json.dumps(out), flush=True)
    elif method == "notifications/initialized":
        path = os.environ.get("BRIDGE_INITIALIZED")
        if path:
            with open(path, "a", encoding="utf-8") as f:
                f.write("initialized\\n")
    elif method == "tools/list":
        path = os.environ.get("BRIDGE_LIST_STARTED")
        if path:
            with open(path, "a", encoding="utf-8") as f:
                f.write("listed\\n")
        block = os.environ.get("BRIDGE_LIST_BLOCK")
        while block and not os.path.exists(block):
            time.sleep(0.01)
        out = {"jsonrpc":"2.0", "id":msg.get("id"), "result":{"tools":[{"name":"echo","inputSchema":{"type":"object"}}]}}
        print(json.dumps(out), flush=True)
`
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return script
}

func bridgePost(t *testing.T, url, auth, session string, body map[string]any) *http.Response {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Content-Type", "application/json")
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func bridgeDelete(t *testing.T, url, auth, session string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Mcp-Session-Id", session)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestManagedBridgeLazilyStartsAndReusesOwnerChild(t *testing.T) {
	dir := t.TempDir()
	starts := filepath.Join(dir, "starts")
	child := writeBridgeChild(t, dir)
	now := time.Unix(100, 0)
	host := New(Config{
		Children: map[string]Child{"pty": {Command: "python3", Args: []string{child}, Env: map[string]string{"BRIDGE_STARTS": starts}}},
		IdleTTL:  5 * time.Second,
		Now:      func() time.Time { return now },
	})
	server := httptest.NewServer(host)
	defer server.Close()
	defer func() { _ = host.Close(context.Background()) }()
	url := server.URL + "/mcp/pty"
	initBody := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}}}

	if response := bridgePost(t, url, "Bearer owner-a", "", initBody); response.StatusCode != http.StatusOK {
		response.Body.Close()
		t.Fatalf("initialize status=%d", response.StatusCode)
	} else {
		response.Body.Close()
	}
	firstStarts, _ := os.ReadFile(starts)
	if got := strings.Count(string(firstStarts), "start"); got != 1 {
		t.Fatalf("lazy start count=%d", got)
	}

	second := bridgePost(t, url, "Bearer owner-a", "", initBody)
	if second.StatusCode != http.StatusOK {
		t.Fatalf("second initialize status=%d", second.StatusCode)
	}
	second.Body.Close()
	if data, _ := os.ReadFile(starts); strings.Count(string(data), "start") != 1 {
		t.Fatal("same owner created a second stdio child")
	}

	other := bridgePost(t, url, "Bearer owner-b", "", initBody)
	if other.StatusCode != http.StatusOK {
		t.Fatalf("other owner initialize status=%d", other.StatusCode)
	}
	other.Body.Close()
	if data, _ := os.ReadFile(starts); strings.Count(string(data), "start") != 2 {
		t.Fatal("different owners shared a stdio child")
	}

	now = now.Add(6 * time.Second)
	if err := host.EvictIdle(t.Context()); err != nil {
		t.Fatal(err)
	}
	third := bridgePost(t, url, "Bearer owner-a", "", initBody)
	if third.StatusCode != http.StatusOK {
		t.Fatalf("new generation initialize status=%d", third.StatusCode)
	}
	third.Body.Close()
	if data, _ := os.ReadFile(starts); strings.Count(string(data), "start") != 3 {
		t.Fatal("new generation did not create a replacement child")
	}
}

func TestManagedBridgeDeleteClosesSessionButKeepsChild(t *testing.T) {
	dir := t.TempDir()
	starts := filepath.Join(dir, "starts")
	initializedPath := filepath.Join(dir, "initialized")
	child := writeBridgeChild(t, dir)
	host := New(Config{
		Children: map[string]Child{"pty": {Command: "python3", Args: []string{child}, Env: map[string]string{"BRIDGE_STARTS": starts, "BRIDGE_INITIALIZED": initializedPath}}},
		IdleTTL:  time.Hour,
	})
	server := httptest.NewServer(host)
	defer server.Close()
	defer func() { _ = host.Close(context.Background()) }()
	url := server.URL + "/mcp/pty"
	initBody := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}}}

	initialized := bridgePost(t, url, "Bearer owner", "", initBody)
	if initialized.StatusCode != http.StatusOK {
		t.Fatalf("initialize status=%d", initialized.StatusCode)
	}
	sessionID := initialized.Header.Get("Mcp-Session-Id")
	initialized.Body.Close()
	if sessionID == "" {
		t.Fatal("initialize did not return Mcp-Session-Id")
	}

	notification := bridgePost(t, url, "Bearer owner", sessionID, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized", "params": map[string]any{}})
	if notification.StatusCode != http.StatusAccepted {
		t.Fatalf("initialized notification status=%d", notification.StatusCode)
	}
	notification.Body.Close()

	listed := bridgePost(t, url, "Bearer owner", sessionID, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": map[string]any{}})
	defer listed.Body.Close()
	if listed.StatusCode != http.StatusOK {
		t.Fatalf("tools/list status=%d", listed.StatusCode)
	}
	var reply struct {
		Result struct {
			Tools []struct{ Name string }
		}
	}
	if err := json.NewDecoder(listed.Body).Decode(&reply); err != nil {
		t.Fatal(err)
	}
	if tools := reply.Result.Tools; len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("unexpected tools/list result: %+v", tools)
	}

	// The child processes stdin in order, so its tools/list reply follows the marker write.
	data, err := os.ReadFile(initializedPath)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(data), "initialized"); count != 1 {
		t.Fatalf("child recorded %d initialized notifications, want 1", count)
	}

	deleted := bridgeDelete(t, url, "Bearer owner", sessionID)
	if deleted.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE status=%d", deleted.StatusCode)
	}
	deleted.Body.Close()

	stale := bridgePost(t, url, "Bearer owner", sessionID, map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/list", "params": map[string]any{}})
	if stale.StatusCode != http.StatusNotFound {
		t.Fatalf("stale session status=%d", stale.StatusCode)
	}
	stale.Body.Close()

	second := bridgePost(t, url, "Bearer owner", "", initBody)
	if second.StatusCode != http.StatusOK {
		t.Fatalf("second initialize status=%d", second.StatusCode)
	}
	second.Body.Close()
	if data, _ := os.ReadFile(starts); strings.Count(string(data), "start") != 1 {
		t.Fatal("closing the HTTP session restarted the stdio child")
	}
}

func TestManagedBridgeDoesNotEvictInFlightCall(t *testing.T) {
	dir := t.TempDir()
	starts := filepath.Join(dir, "starts")
	listed := filepath.Join(dir, "listed")
	block := filepath.Join(dir, "release")
	child := writeBridgeChild(t, dir)
	var unixNow atomic.Int64
	unixNow.Store(100)
	host := New(Config{
		Children: map[string]Child{"pty": {Command: "python3", Args: []string{child}, Env: map[string]string{
			"BRIDGE_STARTS": starts, "BRIDGE_LIST_STARTED": listed, "BRIDGE_LIST_BLOCK": block,
		}}},
		IdleTTL: 5 * time.Second,
		Now:     func() time.Time { return time.Unix(unixNow.Load(), 0) },
	})
	server := httptest.NewServer(host)
	defer server.Close()
	defer func() { _ = host.Close(context.Background()) }()
	url := server.URL + "/mcp/pty"
	initBody := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}}
	initialized := bridgePost(t, url, "Bearer owner", "", initBody)
	if initialized.StatusCode != http.StatusOK {
		t.Fatalf("initialize status=%d", initialized.StatusCode)
	}
	sessionID := initialized.Header.Get("Mcp-Session-Id")
	initialized.Body.Close()

	result := make(chan *http.Response, 1)
	go func() {
		result <- bridgePost(t, url, "Bearer owner", sessionID, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": map[string]any{}})
	}()
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		if _, err := os.Stat(listed); err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := os.Stat(listed); err != nil {
		t.Fatal("in-flight child call did not start")
	}

	unixNow.Store(106)
	if err := host.EvictIdle(t.Context()); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(starts); strings.Count(string(data), "start") != 1 {
		t.Fatal("active child was evicted while a request was in flight")
	}
	if err := os.WriteFile(block, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	response := <-result
	if response.StatusCode != http.StatusOK {
		t.Fatalf("in-flight tools/list status=%d", response.StatusCode)
	}
	response.Body.Close()

	unixNow.Store(112)
	if err := host.EvictIdle(t.Context()); err != nil {
		t.Fatal(err)
	}
	replacement := bridgePost(t, url, "Bearer owner", "", initBody)
	if replacement.StatusCode != http.StatusOK {
		t.Fatalf("replacement initialize status=%d", replacement.StatusCode)
	}
	replacement.Body.Close()
	if data, _ := os.ReadFile(starts); strings.Count(string(data), "start") != 2 {
		t.Fatalf("idle reap did not create a replacement generation: %q", data)
	}
}

func TestOwnerKeyDoesNotRetainAuthorizationValue(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/mcp/pty", nil)
	req.Header.Set("Authorization", "Bearer secret-owner-token")
	key, ok := ownerKey(req, "pty")
	if !ok || key == "" {
		t.Fatal("owner key was not derived")
	}
	if strings.Contains(key, "secret-owner-token") || strings.Contains(key, "Bearer") {
		t.Fatalf("owner key retained credential material: %q", key)
	}
}
