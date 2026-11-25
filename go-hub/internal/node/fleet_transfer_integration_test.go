package node

import (
	"bytes"
	"context"
	"crypto/sha256"
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

func fleetTransferSchemaReady(t *testing.T, web *httptest.Server, name string) bool {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"target": "file:" + name, "detail": "full"})
	req, _ := http.NewRequest(http.MethodPost, web.URL+"/mcp-relay/tools", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer fleet-transfer-control-token")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK && strings.Contains(string(raw), `"file_transfer"`)
}

func TestRealFleetTransferThroughAuthenticatedHub(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	router := hub.New(hub.Config{
		ConfigDir:                  filepath.Join(root, "hub"),
		CtlToken:                   "fleet-transfer-control-token",
		ShellToken:                 "fleet-transfer-shell-token",
		DefaultTimeout:             20 * time.Second,
		FleetTransferAllowlistJSON: `[{"source":"file:source-node","destination":"file:destination-node"}]`,
	})
	defer router.Close()
	web := httptest.NewServer(router.Handler())
	defer web.Close()

	data := bytes.Repeat([]byte{0, 1, 255, 128, '\n'}, (16<<20+1+4)/5)
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	sourceDir := filepath.Join(root, "source")
	destinationDir := filepath.Join(root, "destination")
	if err := os.MkdirAll(sourceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(destinationDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(sourceDir, "source.bin")
	destinationPath := filepath.Join(destinationDir, "destination.bin")
	if err := os.WriteFile(sourcePath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	type runningNode struct {
		executor *shell.Server
		cancel   context.CancelFunc
		done     chan error
	}
	nodes := make([]runningNode, 0, 2)
	start := func(name, dir string) {
		cfg := shell.Config{
			Name:              name,
			Token:             name + "-token-12345678901234567890",
			HubURL:            web.URL,
			IdentityDir:       filepath.Join(dir, "identity"),
			SpillDir:          filepath.Join(dir, "spill"),
			OutboxDir:         filepath.Join(dir, "outbox"),
			QueueEnabled:      true,
			Mode:              "long_poll",
			QueueTimeout:      1,
			HeartbeatEnabled:  true,
			HeartbeatInterval: time.Second,
			PollInterval:      100 * time.Millisecond,
			ExecTimeout:       20,
			LogLimit:          8192,
			DisableSelfUpdate: true,
			DefaultHome:       dir,
			DefaultCwd:        dir,
			InspectRoots:      []string{dir},
		}
		executor := shell.New(cfg)
		identityName, identity, err := executor.LocalIdentity()
		if err != nil {
			t.Fatal(err)
		}
		if identityName != name {
			t.Fatalf("identity name=%q want %q", identityName, name)
		}
		if err := router.RegisterLocalExecutor(name, identity, cfg.Token); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			defer cancel()
			done <- executor.ListenAndServeContext(ctx)
		}()
		nodes = append(nodes, runningNode{executor: executor, cancel: cancel, done: done})
	}
	start("source-node", sourceDir)
	start("destination-node", destinationDir)
	defer func() {
		for _, node := range nodes {
			node.cancel()
			node.executor.Close()
			select {
			case <-node.done:
			case <-time.After(5 * time.Second):
				t.Error("ShellMCP did not stop")
			}
		}
	}()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && (!fleetTransferSchemaReady(t, web, "source-node") || !fleetTransferSchemaReady(t, web, "destination-node")) {
		time.Sleep(200 * time.Millisecond)
	}
	if !fleetTransferSchemaReady(t, web, "source-node") || !fleetTransferSchemaReady(t, web, "destination-node") {
		t.Fatal("paired file_transfer capability did not become ready")
	}

	arguments := map[string]any{
		"source":           "file:source-node",
		"destination":      "file:destination-node",
		"source_path":      sourcePath,
		"destination_path": destinationPath,
	}
	encoded, _ := json.Marshal(arguments)
	code := fmt.Sprintf("return await mcp.call(%q,%q,%s);", "hub", "fleet_transfer", encoded)
	payload, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "exec",
			"arguments": map[string]any{
				"code":             code,
				"max_output_bytes": 65536,
				"timeout_ms":       60000,
			},
		},
	})
	req, _ := http.NewRequest(http.MethodPost, web.URL+"/mcp", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer fleet-transfer-control-token")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"verified":true`) || !strings.Contains(string(raw), `"status":"completed"`) || !strings.Contains(string(raw), digest) {
		t.Fatalf("fleet transfer failed: status=%d body=%s", resp.StatusCode, raw)
	}
	got, err := os.ReadFile(destinationPath)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("destination bytes differ: err=%v bytes=%d", err, len(got))
	}
	t.Logf("authenticated Hub fleet transfer receipt verified: bytes=%d sha256=%s", len(got), digest)
}
