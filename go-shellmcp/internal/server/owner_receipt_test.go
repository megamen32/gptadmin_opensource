package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/hub"
	"github.com/megamen32/gptadmin/go-shellmcp/internal/shell"
)

func TestBoundDispatchAfterExecutorDiesWithUnknownOutcome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test command uses sh")
	}
	const childEnv = "GPTADMIN_TEST_CRASH_DISPATCH"
	const job = "ot1.YQ.unknown"
	if dir := os.Getenv(childEnv); dir != "" {
		s := New(Config{IdentityDir: dir, SpillDir: filepath.Join(dir, "spool")})
		_, err := s.startBoundCallback(job, true, func(context.Context) {
			if err := exec.Command("sh", "-c", "printf x >> count").Run(); err != nil {
				os.Exit(2)
			}
			// The test kills this executor after the actual side effect and
			// before any delivery or terminal record.
			select {}
		})
		if err != nil {
			t.Fatal(err)
		}
		select {}
	}
	dir := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(self, "-test.run=^TestBoundDispatchAfterExecutorDiesWithUnknownOutcome$")
	child.Env = append(os.Environ(), childEnv+"="+dir)
	child.Dir = dir
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	assertEventually(t, func() bool { b, _ := os.ReadFile(filepath.Join(dir, "count")); return string(b) == "x" })
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	s := New(Config{IdentityDir: dir, SpillDir: filepath.Join(dir, "spool")})
	defer s.Close()
	for range 2 {
		seen, err := s.startCallbackJob(job, shell.Request{Cmd: "printf x >> count", Cwd: dir}, true)
		if err != nil || !seen {
			t.Fatalf("restarted dispatch duplicate=%v err=%v", seen, err)
		}
	}
	s.StopCallbacks()
	b, err := os.ReadFile(filepath.Join(dir, "count"))
	if err != nil || string(b) != "x" {
		t.Fatal("ambiguous execution replayed")
	}
}

func assertEventually(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for operation")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestOwnerOutboxDoesNotBlockPollOrOtherOwner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test command uses sh")
	}
	dir := t.TempDir()
	const offline = "ot1.YQ.first"
	const live = "ot1.Yg.next"
	stuck, release, liveACK := make(chan struct{}), make(chan struct{}), make(chan struct{})
	deferRelease := sync.OnceFunc(func() { close(release) })
	var onceStuck, onceLive sync.Once
	jobs := make(chan hub.QueueJob)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			select {
			case job := <-jobs:
				_ = json.NewEncoder(w).Encode(job)
			case <-r.Context().Done():
			}
		case http.MethodPost:
			var receipt hub.TaskResult
			if json.NewDecoder(r.Body).Decode(&receipt) != nil || r.URL.Query().Get("job_id") != receipt.ID {
				w.WriteHeader(400)
				return
			}
			switch receipt.ID {
			case offline:
				onceStuck.Do(func() { close(stuck) })
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				w.WriteHeader(503)
			case live:
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "id": live, "owner_id": "b"})
				onceLive.Do(func() { close(liveACK) })
			default:
				w.WriteHeader(400)
			}
		}
	}))
	defer server.Close()
	defer deferRelease()
	cfg := Config{Name: "android-poll", IdentityDir: dir, SpillDir: filepath.Join(dir, "spool"), OutboxDir: filepath.Join(dir, "outbox"), DefaultCwd: dir, HubURL: server.URL, QueueEnabled: true, DisableSelfUpdate: true}
	s := New(cfg)
	defer s.Close()
	if err := s.storeOutbox(hub.TaskResult{ID: offline, Result: map[string]any{"output": "pending"}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = s.ListenAndServeContext(ctx) }()
	defer func() { deferRelease(); cancel(); <-done; s.StopCallbacks() }()
	select {
	case <-stuck:
	case <-time.After(5 * time.Second):
		t.Fatal("old receipt was not attempted")
	}
	j := hub.QueueJob{ID: live, Cmd: "printf x >> counter", Cwd: dir, Arguments: map[string]any{"run_as_user": currentTestUser()}}
	jobs <- j
	select {
	case <-liveACK:
	case <-time.After(4 * time.Second):
		t.Fatal("blocked owner stalled a new job")
	}
	assertEventually(t, func() bool { _, err := os.Stat(filepath.Join(cfg.OutboxDir, live+".json")); return os.IsNotExist(err) })
	// Deliver the same job only after the executor removed its outbox on ACK.
	// An empty following poll is a barrier: the duplicate has left admission.
	for _, replay := range []hub.QueueJob{j, {}} {
		select {
		case jobs <- replay:
		case <-time.After(4 * time.Second):
			t.Fatal("poll did not progress beyond the post-ACK replay")
		}
	}
	s.callbackWG.Wait()
	if b, err := os.ReadFile(filepath.Join(dir, "counter")); err != nil || string(b) != "x" {
		t.Fatal("expected exactly one real execution")
	}
	if b, err := os.ReadFile(filepath.Join(cfg.OutboxDir, offline+".json")); err != nil || !strings.Contains(string(b), "pending") {
		t.Fatal("unconfirmed receipt lost")
	}
}

func currentTestUser() string {
	if os.Getuid() == 0 {
		return "root"
	}
	return ""
}
