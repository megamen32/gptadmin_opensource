package node

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/megamen32/gptadmin/go-hub/internal/hub"
	shell "github.com/megamen32/gptadmin/go-shellmcp/runtime"
)

func TestRealExecutorDrainsAJobQueuedBeforeDrain(t *testing.T) {
	dir := t.TempDir()
	router := hub.New(hub.Config{ConfigDir: dir, CtlToken: "test-control-token", ShellToken: "test-shell-token", DefaultTimeout: 10 * time.Second})
	defer router.Close()
	web := httptest.NewServer(router.Handler())
	defer web.Close()
	cfg := shell.FromEnv()
	cfg.Name = "drain-integration"
	cfg.Token = "test-local-executor-token-123456"
	cfg.HubURL = web.URL
	cfg.IdentityDir = filepath.Join(dir, "identity")
	cfg.SpillDir = filepath.Join(dir, "spill")
	cfg.OutboxDir = filepath.Join(dir, "outbox")
	cfg.QueueEnabled = true
	cfg.Mode = "long_poll"
	cfg.QueueTimeout = 1
	cfg.HeartbeatEnabled = true
	cfg.DisableSelfUpdate = true
	cfg.DefaultHome = dir
	cfg.DefaultCwd = dir
	executor := shell.New(cfg)
	defer executor.Close()
	name, identity, err := executor.LocalIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if err = router.RegisterLocalExecutor(name, identity, cfg.Token); err != nil {
		t.Fatal(err)
	}
	router.SetNodeRuntimeCounters(executor)
	payload := []byte(`{"target":"shell:drain-integration","tool":"shell_exec","arguments":{"cmd":"printf before-drain"},"background":true}`)
	request, err := http.NewRequest("POST", web.URL+"/mcp-relay/call", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer test-control-token")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("enqueue %d %s", response.StatusCode, raw)
	}
	var submitted map[string]any
	if err = json.Unmarshal(raw, &submitted); err != nil {
		t.Fatal(err)
	}
	id, _ := submitted["job_id"].(string)
	if id == "" {
		t.Fatalf("no job id: %s", raw)
	}
	router.EnableNodeDrain("integration")
	if router.NodeDrainStatus().ReadyToStop {
		t.Fatal("ready before queued job runs")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- executor.ListenAndServeContext(ctx) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(75 * time.Second)
	for time.Now().Before(deadline) {
		if router.NodeDrainStatus().ReadyToStop {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if s := router.NodeDrainStatus(); !s.ReadyToStop {
		t.Fatalf("drain stranded work: %+v", s)
	}
	get, err := http.NewRequest("GET", web.URL+"/mcp-relay/job/"+id, nil)
	if err != nil {
		t.Fatal(err)
	}
	get.Header.Set("Authorization", "Bearer test-control-token")
	result, err := http.DefaultClient.Do(get)
	if err != nil {
		t.Fatal(err)
	}
	resultBody, _ := io.ReadAll(result.Body)
	result.Body.Close()
	if result.StatusCode != 200 || !bytes.Contains(resultBody, []byte("before-drain")) {
		t.Fatal(fmt.Sprintf("job result %d %s", result.StatusCode, resultBody))
	}
}
