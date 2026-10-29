package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDrainTracksPollUntilDeliveredCallbackFinishes(t *testing.T) {
	started := make(chan struct{}, 1)
	deliver := make(chan struct{})
	results := make(chan struct{}, 1)
	var polls atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/queue/"):
			polls.Add(1)
			started <- struct{}{}
			<-deliver
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "drain-race", "cmd": "sleep 1", "tool_name": "shell_exec", "arguments": map[string]any{}})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/result"):
			results <- struct{}{}
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer remote.Close()
	s := New(Config{Name: "queue-agent", HubURL: remote.URL, QueueEnabled: true, QueueTimeout: 2, IdentityDir: t.TempDir(), SpillDir: t.TempDir(), OutboxDir: t.TempDir()})
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { s.queueLoop(ctx); close(done) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("poll did not start")
	}
	s.SetQueueDraining(true)
	if n := s.ActiveCallbacks(); n != 1 {
		t.Fatalf("open poll invisible: %d", n)
	}
	if s.beginQueuePoll() {
		t.Fatal("drain admitted another poll")
	}
	close(deliver)
	select {
	case <-results:
	case <-time.After(5 * time.Second):
		t.Fatal("delivered callback did not finish")
	}
	deadline := time.Now().Add(2 * time.Second)
	for s.ActiveCallbacks() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := s.ActiveCallbacks(); n != 0 {
		t.Fatalf("work still counted: %d", n)
	}
	time.Sleep(300 * time.Millisecond)
	if n := polls.Load(); n != 1 {
		t.Fatalf("polls during drain: %d", n)
	}
	cancel()
	<-done
}

func TestDrainHandoffNeverReportsZero(t *testing.T) {
	s := New(Config{SpillDir: t.TempDir()})
	defer s.Close()
	if !s.beginQueuePoll() {
		t.Fatal("poll denied")
	}
	s.SetQueueDraining(true)
	if _, _, ok := s.beginCallback("new-direct", false); ok {
		t.Fatal("new callback accepted during drain")
	}
	_, finish, ok := s.beginCallback("accepted", true)
	if !ok {
		t.Fatal("accepted callback denied")
	}
	s.endQueuePoll()
	if n := s.ActiveCallbacks(); n != 1 {
		t.Fatalf("handoff invisible: %d", n)
	}
	finish()
	if n := s.ActiveCallbacks(); n != 0 {
		t.Fatalf("finished work counted: %d", n)
	}
	s.SetQueueDraining(false)
	if !s.beginQueuePoll() {
		t.Fatal("poll not resumed")
	}
	s.endQueuePoll()
}
