package node

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/megamen32/gptadmin/go-hub/internal/hub"
	shell "github.com/megamen32/gptadmin/go-shellmcp/runtime"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Real Node, HTTP, durable queue and shell subprocess across a renamed restart.
func TestStableTargetNodeRenameAndForeignKey(t *testing.T) {
	t.Setenv(diagnosticsSocketEnv, "")
	dir := t.TempDir()
	cfg := hub.Config{ConfigDir: filepath.Join(dir, "hub"), CtlToken: "stable-control", ShellToken: "stable-shell", DefaultTimeout: 5 * time.Second}
	executor := shell.Config{Name: "mac-before", IdentityDir: filepath.Join(dir, "identity"), SpillDir: filepath.Join(dir, "spill"), OutboxDir: filepath.Join(dir, "outbox"), ExecTimeout: 5, QueueTimeout: 1, PollInterval: time.Second, HeartbeatInterval: time.Second, DefaultHome: dir, DefaultCwd: dir}
	target := ""
	var previousJob string
	for _, label := range []string{"mac-before", "mac-after"} {
		func() {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			base := "http://" + listener.Addr().String()
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			executor.Name = label
			go func() { done <- Run(ctx, listener, cfg, executor) }()
			defer func() {
				cancel()
				if err := <-done; err != nil {
					t.Error(err)
				}
			}()
			client := &http.Client{Timeout: 8 * time.Second}
			request := func(method, path string, body []byte) []byte {
				t.Helper()
				req, _ := http.NewRequest(method, base+path, bytes.NewReader(body))
				req.Header.Set("Authorization", "Bearer stable-control")
				req.Header.Set("Content-Type", "application/json")
				res, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer res.Body.Close()
				raw, _ := io.ReadAll(res.Body)
				if res.StatusCode != 200 {
					t.Fatalf("%s %d: %s", path, res.StatusCode, raw)
				}
				return raw
			}
			discovered := request("GET", "/mcp-relay/servers", nil)
			var catalog struct {
				Servers []struct {
					ID string `json:"server_id"`
				} `json:"servers"`
			}
			if err := json.Unmarshal(discovered, &catalog); err != nil {
				t.Fatal(err)
			}
			current := ""
			for _, a := range catalog.Servers {
				if len(a.ID) == 13 && strings.HasPrefix(a.ID, "h") {
					if current != "" {
						t.Fatal("multiple executor identities")
					}
					current = a.ID
				}
			}
			if current == "" {
				t.Fatalf("missing identity-derived host target: %s", discovered)
			}
			if target == "" {
				target = current
			} else if current != target {
				t.Fatalf("rename changed alias: %s -> %s", target, current)
			}
			raw := request("POST", "/mcp-relay/tools", []byte(fmt.Sprintf(`{"target":%q,"tool":"shell_exec"}`, target)))
			if !bytes.Contains(raw, []byte("shell_exec")) {
				t.Fatalf("schema missing: %s", raw)
			}
			if previousJob != "" {
				raw = request("GET", "/mcp-relay/job/"+previousJob, nil)
				if !bytes.Contains(raw, []byte("stable-node-result")) {
					t.Fatalf("history lost: %s", raw)
				}
			}
			raw = request("POST", "/mcp-relay/call", []byte(fmt.Sprintf(`{"target":%q,"tool":"shell_exec","arguments":{"cmd":"printf stable-node-result"},"background":true}`, target)))
			var job struct {
				JobID string `json:"job_id"`
			}
			json.Unmarshal(raw, &job)
			if job.JobID == "" {
				t.Fatalf("no job: %s", raw)
			}
			previousJob = job.JobID
			deadline := time.Now().Add(12 * time.Second)
			for {
				raw = request("GET", "/mcp-relay/job/"+job.JobID, nil)
				if bytes.Contains(raw, []byte("stable-node-result")) {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("queue did not deliver after rename: %s", raw)
				}
				time.Sleep(50 * time.Millisecond)
			}

			raw = request("GET", "/mcp-relay/servers", nil)
			var registry struct {
				Servers []struct {
					ID   string `json:"server_id"`
					Name string `json:"name"`
				} `json:"servers"`
			}
			if err := json.Unmarshal(raw, &registry); err != nil {
				t.Fatal(err)
			}
			hosts := 0
			for _, a := range registry.Servers {
				if len(a.ID) == 13 && strings.HasPrefix(a.ID, "h") {
					hosts++
					if a.ID != target || a.Name != label {
						t.Fatalf("wrong stable target or display name: %+v", a)
					}
				}
			}
			if hosts != 1 {
				t.Fatalf("rename duplicated/lost executor: %s", raw)
			}
		}()
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	executor.Name = "mac-before"
	executor.IdentityDir = filepath.Join(dir, "foreign-identity")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err = Run(ctx, listener, cfg, executor)
	if err == nil || !strings.Contains(err.Error(), "identity differs") {
		t.Fatalf("foreign identity not rejected: %v", err)
	}
}
