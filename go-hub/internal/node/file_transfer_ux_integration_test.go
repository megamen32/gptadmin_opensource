package node

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/megamen32/gptadmin/go-hub/internal/hub"
	shell "github.com/megamen32/gptadmin/go-shellmcp/runtime"
)

// transferUXCall invokes file_transfer on the paired file target through the
// real Hub exec path, mirroring the production client flow.
func transferUXCall(t *testing.T, web *httptest.Server, name string, args map[string]any) (map[string]any, string) {
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

func TestRealTransferStatListAndUploadReplaceThroughHub(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	router := hub.New(hub.Config{ConfigDir: filepath.Join(dir, "hub"), CtlToken: "binary-test-control-token", ShellToken: "binary-test-shell-token", DefaultTimeout: 20 * time.Second})
	defer router.Close()
	web := httptest.NewServer(router.Handler())
	defer web.Close()
	cfg := shell.Config{Name: "transfer-ux-integration", Token: "binary-test-local-token-12345678901234567890", HubURL: web.URL, IdentityDir: filepath.Join(dir, "identity"), SpillDir: filepath.Join(dir, "spill"), OutboxDir: filepath.Join(dir, "outbox"), QueueEnabled: true, Mode: "long_poll", QueueTimeout: 1, HeartbeatEnabled: true, HeartbeatInterval: time.Second, PollInterval: time.Second, ExecTimeout: 20, LogLimit: 8192, DisableSelfUpdate: true, DefaultHome: dir, DefaultCwd: dir, InspectRoots: []string{dir}}
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

	must := func(args map[string]any) map[string]any {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for {
			r, raw := transferUXCall(t, web, name, args)
			if r != nil {
				return r
			}
			if strings.Contains(raw, "file_transfer_version") || strings.Contains(raw, "still running") || strings.Contains(raw, "no route") {
				if time.Now().After(deadline) {
					t.Fatalf("transfer never became available: %s", raw)
				}
				time.Sleep(500 * time.Millisecond)
				continue
			}
			t.Fatalf("transfer missing: %s", raw)
			return nil
		}
	}
	fail := func(args map[string]any) string {
		t.Helper()
		r, raw := transferUXCall(t, web, name, args)
		if r != nil {
			t.Fatalf("expected failure, got transfer: %v", r)
		}
		return raw
	}

	// stat: opt-in hash, directory vs regular file
	seed := []byte("integration seed\n")
	seedPath := filepath.Join(dir, "seed.txt")
	if err = os.WriteFile(seedPath, seed, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(seed)
	digest := hex.EncodeToString(sum[:])
	stat := must(map[string]any{"action": "stat", "path": seedPath})
	if stat["is_regular"] != true || stat["size"] != float64(len(seed)) {
		t.Fatal("stat payload", stat)
	}
	if _, ok := stat["sha256"]; ok {
		t.Fatal("stat hash must be opt-in")
	}
	statHash := must(map[string]any{"action": "stat", "path": seedPath, "hash": true})
	if statHash["sha256"] != digest {
		t.Fatal("stat hash", statHash)
	}
	statDir := must(map[string]any{"action": "stat", "path": dir})
	if statDir["is_dir"] != true {
		t.Fatal("stat dir", statDir)
	}

	// list: entries + glob filter
	if err = os.Mkdir(filepath.Join(dir, "pkg"), 0o700); err != nil {
		t.Fatal(err)
	}
	list := must(map[string]any{"action": "list", "path": dir})
	names, _ := list["entries"].([]any)
	found := map[string]bool{}
	for _, n := range names {
		entry, _ := n.(map[string]any)
		found[entry["name"].(string)] = true
	}
	if !found["seed.txt"] || !found["pkg"] {
		t.Fatal("list must include seed.txt and pkg", list)
	}
	filtered := must(map[string]any{"action": "list", "path": dir, "pattern": "*.txt"})
	fn, _ := filtered["entries"].([]any)
	if len(fn) != 1 {
		t.Fatal("filtered entries", filtered)
	}
	if name, _ := fn[0].(map[string]any)["name"].(string); name != "seed.txt" {
		t.Fatal("filtered name", fn)
	}
	fail(map[string]any{"action": "list", "path": dir, "pattern": "["})

	// upload_replace: full CAS flow over two chunks
	replacement := bytes.Repeat([]byte{0x1b, 0x5b, 0x6d, 0x00, 0xff}, 8000) // 40000 bytes
	rsum := sha256.Sum256(replacement)
	rdigest := hex.EncodeToString(rsum[:])
	begin := must(map[string]any{"action": "upload_replace", "path": seedPath, "size": len(replacement), "sha256": rdigest, "expected_sha256": digest})
	id := begin["transfer_id"]
	if begin["replace"] != true || begin["expected_sha256"] != digest {
		t.Fatal("replace begin receipt", begin)
	}
	if disk, e := os.ReadFile(seedPath); e != nil || !bytes.Equal(disk, seed) {
		t.Fatal("destination must stay intact before commit", e)
	}
	for offset := 0; offset < len(replacement); {
		end := offset + 32768
		if end > len(replacement) {
			end = len(replacement)
		}
		must(map[string]any{"action": "upload_chunk", "path": seedPath, "transfer_id": id, "offset": offset, "data_base64": encodeChunk(replacement[offset:end])})
		offset = end
	}
	must(map[string]any{"action": "upload_commit", "path": seedPath, "transfer_id": id})
	if disk, e := os.ReadFile(seedPath); e != nil || !bytes.Equal(disk, replacement) {
		t.Fatal("replaced bytes differ", e)
	}

	// stale expected digest is rejected at begin
	fail(map[string]any{"action": "upload_replace", "path": seedPath, "size": 3, "sha256": rdigest, "expected_sha256": digest})

	// commit refuses when the destination changes between begin and commit
	four := sha256.Sum256([]byte{0, 0, 0, 0})
	begin2 := must(map[string]any{"action": "upload_replace", "path": seedPath, "size": 4, "sha256": hex.EncodeToString(four[:]), "expected_sha256": rdigest})
	id2 := begin2["transfer_id"]
	must(map[string]any{"action": "upload_chunk", "path": seedPath, "transfer_id": id2, "offset": 0, "data_base64": encodeChunk([]byte{0, 0, 0, 0})})
	if err = os.WriteFile(seedPath, []byte("concurrent writer"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw := fail(map[string]any{"action": "upload_commit", "path": seedPath, "transfer_id": id2})
	if !strings.Contains(raw, "destination changed since upload_replace began") {
		t.Fatalf("expected CAS refusal, got %s", raw)
	}
	if disk, e := os.ReadFile(seedPath); e != nil || !bytes.Equal(disk, []byte("concurrent writer")) {
		t.Fatal("failed CAS commit must leave the concurrent content", e)
	}
}

func encodeChunk(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}
