package node

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/megamen32/gptadmin/go-hub/internal/hub"
	shell "github.com/megamen32/gptadmin/go-shellmcp/runtime"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func findTransfer(value any) map[string]any {
	switch v := value.(type) {
	case map[string]any:
		if x, ok := v["transfer"].(map[string]any); ok {
			return x
		}
		for _, x := range v {
			if r := findTransfer(x); r != nil {
				return r
			}
		}
	case []any:
		for _, x := range v {
			if r := findTransfer(x); r != nil {
				return r
			}
		}
	case string:
		if strings.HasPrefix(v, "{") {
			var x any
			if json.Unmarshal([]byte(v), &x) == nil {
				return findTransfer(x)
			}
		}
	}
	return nil
}
func TestRealPrivateBinaryTransferThroughHub(t *testing.T) {
	// macOS temporary directories may be reached through /var symlink aliases.
	// The transfer contract intentionally accepts canonical roots only.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	router := hub.New(hub.Config{ConfigDir: filepath.Join(dir, "hub"), CtlToken: "binary-test-control-token", ShellToken: "binary-test-shell-token", DefaultTimeout: 20 * time.Second})
	defer router.Close()
	web := httptest.NewServer(router.Handler())
	defer web.Close()
	cfg := shell.Config{Name: "binary-transfer-integration", Token: "binary-test-local-token-12345678901234567890", HubURL: web.URL, IdentityDir: filepath.Join(dir, "identity"), SpillDir: filepath.Join(dir, "spill"), OutboxDir: filepath.Join(dir, "outbox"), QueueEnabled: true, Mode: "long_poll", QueueTimeout: 1, HeartbeatEnabled: true, HeartbeatInterval: time.Second, PollInterval: time.Second, ExecTimeout: 20, LogLimit: 8192, DisableSelfUpdate: true, DefaultHome: dir, DefaultCwd: dir, InspectRoots: []string{dir}}
	executor := shell.New(cfg)
	defer executor.Close()
	name, identity, err := executor.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if err = router.RegisterLocalExecutor(name, identity, cfg.Token); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- executor.ListenAndServeContext(ctx) }()
	defer func() { cancel(); <-done }()
	call := func(args map[string]any) (map[string]any, string) {
		t.Helper()
		a, _ := json.Marshal(args)
		code := fmt.Sprintf("return await mcp.call(%q,%q,%s);", "file:"+name, "file_transfer", a)
		payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "exec", "arguments": map[string]any{"code": code, "max_output_bytes": 65536, "timeout_ms": 30000}}})
		request, _ := http.NewRequest("POST", web.URL+"/mcp", bytes.NewReader(payload))
		request.Header.Set("Authorization", "Bearer binary-test-control-token")
		request.Header.Set("Content-Type", "application/json")
		response, e := http.DefaultClient.Do(request)
		if e != nil {
			t.Fatal(e)
		}
		raw, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatalf("MCP status=%d body=%s", response.StatusCode, raw)
		}
		var value any
		if e = json.Unmarshal(raw, &value); e != nil {
			t.Fatalf("response=%s error=%v", raw, e)
		}
		return findTransfer(value), string(raw)
	}
	must := func(args map[string]any) map[string]any {
		t.Helper()
		r, raw := call(args)
		if r == nil {
			t.Fatalf("transfer missing: %s", raw)
		}
		return r
	}
	data := bytes.Repeat([]byte{0, 255, 128, 1, 13, 10, 42}, 22000)
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	path := filepath.Join(dir, "real-binary.bin")
	begin := must(map[string]any{"action": "upload_begin", "path": path, "size": len(data), "sha256": digest})
	id := begin["transfer_id"]
	for offset := 0; offset < len(data); {
		end := offset + 32768
		if end > len(data) {
			end = len(data)
		}
		args := map[string]any{"action": "upload_chunk", "path": path, "transfer_id": id, "offset": offset, "data_base64": base64.StdEncoding.EncodeToString(data[offset:end])}
		must(args)
		if offset == 0 {
			must(args)
		}
		offset = end
	}
	if _, e := os.Stat(path); !os.IsNotExist(e) {
		t.Fatal("target visible before commit")
	}
	must(map[string]any{"action": "upload_commit", "path": path, "transfer_id": id})
	disk, e := os.ReadFile(path)
	if e != nil || !bytes.Equal(disk, data) {
		t.Fatal("real target differs", e)
	}
	snap := must(map[string]any{"action": "download_begin", "path": path})
	if snap["sha256"] != digest {
		t.Fatal("download digest", snap)
	}
	var downloaded []byte
	for offset := 0; offset < len(data); {
		r := must(map[string]any{"action": "download_chunk", "path": path, "transfer_id": snap["transfer_id"], "offset": offset, "max_bytes": 32768})
		chunk, e := base64.StdEncoding.Strict().DecodeString(r["data_base64"].(string))
		if e != nil {
			t.Fatal(e)
		}
		downloaded = append(downloaded, chunk...)
		offset = int(r["next_offset"].(float64))
	}
	if !bytes.Equal(downloaded, data) {
		t.Fatal("real downloaded bytes differ")
	}
	r, raw := call(map[string]any{"action": "download_begin", "path": "/etc/hosts"})
	if r != nil || !strings.Contains(raw, "outside configured filesystem ACL roots") {
		t.Fatalf("outside-root result: %s", raw)
	}
	must(map[string]any{"action": "download_abort", "path": path, "transfer_id": snap["transfer_id"]})
	t.Logf("Real authenticated MCP exec -> Hub queue -> Node -> filesystem -> download: bytes=%d sha256=%s", len(data), digest)
}
