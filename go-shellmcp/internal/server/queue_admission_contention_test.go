package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/megamen32/gptadmin/go-shellmcp/internal/hub"
)

// The owner issues this command once. The executor must retain it after a
// missed lock acquisition and permit a normal drain to observe the handoff.
func TestIssuedQueueJobSurvivesBusyClaimJournal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("command uses sh")
	}
	dir := t.TempDir()
	journal := filepath.Join(dir, "shellmcp-dispatch")
	if err := os.Mkdir(journal, 0700); err != nil {
		t.Fatal(err)
	}
	lock := flock.New(filepath.Join(journal, "claims.lock"))
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	const id = "ot1.YQ.busy-claim"
	const cancelledID = "ot1.YQ.already-cancelled"
	job := hub.QueueJob{ID: id, Cmd: "printf x >> effects", Cwd: dir, Arguments: map[string]any{"run_as_user": currentTestUser()}}
	jobs := make(chan hub.QueueJob)
	result := make(chan hub.TaskResult, 2)
	var polls atomic.Int32
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			polls.Add(1)
			select {
			case j := <-jobs:
				_ = json.NewEncoder(w).Encode(j)
			case <-r.Context().Done():
			}
			return
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/result") {
			var v hub.TaskResult
			if json.NewDecoder(r.Body).Decode(&v) != nil || (v.ID != id && v.ID != cancelledID) {
				http.Error(w, "bad receipt", 400)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "id": v.ID, "owner_id": "a"})
			result <- v
			return
		}
		http.NotFound(w, r)
	}))
	defer owner.Close()
	cfg := Config{Name: "local", HubURL: owner.URL, IdentityDir: dir, SpillDir: filepath.Join(dir, "spill"), OutboxDir: filepath.Join(dir, "outbox"), QueueEnabled: true, DisableSelfUpdate: true}
	s := New(cfg)
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.queueLoop(ctx) }()
	defer func() { cancel(); <-done; s.StopCallbacks() }()
	select {
	case jobs <- job:
	case <-time.After(4 * time.Second):
		t.Fatal("no initial poll")
	}
	time.Sleep(2500 * time.Millisecond)
	if n := polls.Load(); n != 1 {
		t.Fatalf("busy admission accepted another poll: %d", n)
	}
	s.SetQueueDraining(true)
	if n := s.ActiveCallbacks(); n == 0 {
		t.Fatal("drain missed pending admission")
	}
	if _, err := os.Stat(filepath.Join(dir, "effects")); !os.IsNotExist(err) {
		t.Fatal("command ran before its durable claim")
	}
	if err := lock.Unlock(); err != nil {
		t.Fatal(err)
	}
	select {
	case v := <-result:
		if v.ID != id {
			t.Fatal("wrong receipt")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("issued command was discarded when the journal stayed busy")
	}
	assertEventually(t, func() bool { _, err := os.Stat(filepath.Join(cfg.OutboxDir, id+".json")); return os.IsNotExist(err) })
	assertEventually(t, func() bool { return s.ActiveCallbacks() == 0 })
	if n := polls.Load(); n != 1 {
		t.Fatal("drain admitted another poll")
	}
	s.SetQueueDraining(false)
	// Replay only in this isolated test after the owner ACK. A following poll
	// proves the duplicate already passed the executor's admission boundary.
	for _, j := range []hub.QueueJob{job, {}} {
		select {
		case jobs <- j:
		case <-time.After(4 * time.Second):
			t.Fatal("poll blocked after ACK")
		}
	}
	// A real owner control delivered before this lease must still win
	// after disk contention. Its response certifies no execution.
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	cancelledJob := job
	cancelledJob.ID = cancelledID
	for _, j := range []hub.QueueJob{{ID: "control_" + cancelledID, ToolName: "__gptadmin_cancel_task", Arguments: map[string]any{"task_id": cancelledID}}, cancelledJob} {
		select {
		case jobs <- j:
		case <-time.After(4 * time.Second):
			t.Fatal("owner control delivery stalled")
		}
	}
	assertEventually(t, func() bool {
		s.callbackMu.Lock()
		defer s.callbackMu.Unlock()
		return s.pendingClaim != nil && s.pendingClaim.id == cancelledID
	})
	if err := lock.Unlock(); err != nil {
		t.Fatal(err)
	}
	select {
	case receipt := <-result:
		failure, ok := receipt.Error.(map[string]any)
		if receipt.ID != cancelledID || !ok || failure["code"] != "cancelled" || failure["executed"] != false {
			t.Fatal("delivered cancellation lost")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("missing durable cancellation receipt")
	}
	s.callbackWG.Wait()
	b, err := os.ReadFile(filepath.Join(dir, "effects"))
	if err != nil || string(b) != "x" {
		t.Fatalf("duplicate or lost effect %q, %v", b, err)
	}
}

func TestDelayedAdmissionHonorsCancelAndShutdown(t *testing.T) {
	for _, mode := range []string{"cancel", "shutdown", "queue-stop", "acl"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, "shellmcp-dispatch"), 0700); err != nil {
				t.Fatal(err)
			}
			lock := flock.New(filepath.Join(dir, "shellmcp-dispatch", "claims.lock"))
			if err := lock.Lock(); err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			const id = "ot1.YQ.pending-policy"
			got := make(chan hub.TaskResult, 1)
			owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/result") {
					http.NotFound(w, r)
					return
				}
				var v hub.TaskResult
				if json.NewDecoder(r.Body).Decode(&v) != nil || v.ID != id {
					w.WriteHeader(400)
					return
				}
				got <- v
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "id": id, "owner_id": "a"})
			}))
			defer owner.Close()
			s := New(Config{Name: "local", HubURL: owner.URL, IdentityDir: dir, SpillDir: filepath.Join(dir, "spill"), OutboxDir: filepath.Join(dir, "outbox"), DisableSelfUpdate: true})
			defer s.Close()
			var starts atomic.Int32
			run := func(context.Context) { starts.Add(1) }
			job := hub.QueueJob{ID: id, Cmd: "printf should-never-run > unexpected", Cwd: dir, Arguments: map[string]any{"run_as_user": currentTestUser()}}
			if mode == "acl" {
				job = hub.QueueJob{ID: id, ToolName: "mcp_call", FilesystemACL: &hub.FilesystemACL{Version: 1, JobID: "a-different-job", Tool: "mcp_call"}, Arguments: map[string]any{}}
			}
			queue, cancel := context.WithCancel(context.Background())
			defer cancel()
			returned := make(chan error, 1)
			go func() { returned <- s.startPolledJob(queue, job) }()
			assertEventually(t, func() bool {
				s.callbackMu.Lock()
				defer s.callbackMu.Unlock()
				return s.pendingClaim != nil && s.pendingClaim.id == id
			})
			if s.ActiveCallbacks() != 1 {
				t.Fatal("drain lost waiting callback")
			}
			if _, err := s.startBoundCallback(id, false, run); !errors.Is(err, errDispatchBusy) {
				t.Fatalf("direct request acknowledged an uncommitted dispatch: %v", err)
			}
			if seen, err := s.startBoundCallback(id, true, run); err != nil || !seen {
				t.Fatal("second delivery adopted the held envelope")
			}
			s.SetQueueDraining(true)
			if mode == "shutdown" || mode == "queue-stop" {
				stopped := make(chan struct{})
				go func() {
					if mode == "queue-stop" {
						cancel()
					} else {
						s.StopCallbacks()
					}
					<-returned
					close(stopped)
				}()
				select {
				case <-stopped:
				case <-time.After(time.Second):
					t.Fatal("shutdown depends on disk lock holder")
				}
				if s.ActiveCallbacks() != 0 || starts.Load() != 0 {
					t.Fatal("unclaimed job executed during stop")
				}
				return
			}
			if mode == "cancel" {
				s.cancelCallbackJob(id)
			}
			if err := lock.Unlock(); err != nil {
				t.Fatal(err)
			}
			select {
			case v := <-got:
				if mode == "cancel" {
					failure, ok := v.Error.(map[string]any)
					if !ok || failure["code"] != "cancelled" || failure["executed"] != false {
						t.Fatalf("unproven cancellation result: %v", v.Error)
					}
				} else {
					failure, ok := v.Result.(map[string]any)
					if !ok || failure["error"] != "invalid filesystem ACL queue binding" {
						t.Fatalf("invalid queue ACL reached child: %v", v.Result)
					}
				}
			case <-time.After(3 * time.Second):
				t.Fatal("original authorized receipt missing")
			}
			if err := <-returned; err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(dir, "unexpected")); !os.IsNotExist(err) {
				t.Fatal("cancelled command produced an effect")
			}
			s.callbackWG.Wait()
			if s.ActiveCallbacks() != 0 || starts.Load() != 0 {
				t.Fatal("cancelled command executed or drain stuck")
			}
			// A new process using the same old-format journal cannot re-run it.
			reopened := New(s.cfg)
			defer reopened.Close()
			if seen, err := reopened.startBoundCallback(id, true, run); !seen && !errors.Is(err, errStaleDispatch) {
				t.Fatalf("stale callback after restart: seen=%v err=%v", seen, err)
			}
			reopened.StopCallbacks()
			if starts.Load() != 0 {
				t.Fatal("terminal task executed after restart")
			}
		})
	}
}
