package hub

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNodeRuntimeStopsListenerOnCancellation(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir(), CtlToken: "node-test-control", ShellToken: "node-test-shell"})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.ServeContext(ctx, listener) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("node listener did not stop")
	}
	conn, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err == nil {
		conn.Close()
		t.Fatal("listener remains open")
	}
}

func TestNodeLocalQueueRejectsFleetCredential(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir(), ShellToken: "fleet-token"})
	defer s.Close()
	identity := map[string]string{"server_id": "local-id", "public_key": "local-key", "fingerprint": "local-fingerprint"}
	if err := s.RegisterLocalExecutor("local", identity, strings.Repeat("t", 32)); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/queue/local", "/queue/local/result", "/queue/%256cocal/result", "/queue/%20local%20/result"} {
		r := httptest.NewRequest("POST", path, nil)
		r.Header.Set("Authorization", "Bearer fleet-token")
		w := httptest.NewRecorder()
		called := false
		s.requireShell(func(http.ResponseWriter, *http.Request) { called = true })(w, r)
		if called || w.Code != http.StatusUnauthorized {
			t.Fatalf("fleet token reached local queue %s", path)
		}
	}
}

func TestNodeLocalEnrollmentRejectsReplacementIdentity(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir()})
	defer s.Close()
	identity := map[string]string{"server_id": "local-id", "public_key": "local-key", "fingerprint": "local-fingerprint"}
	if err := s.RegisterLocalExecutor("local", identity, strings.Repeat("t", 32)); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/queue/local?public_key=other", nil)
	s.mu.Lock()
	accepted := s.touchShellPollLocked("local", request)
	gotKey := s.agents["shell:local"].Meta["public_key"]
	s.mu.Unlock()
	if accepted || gotKey != "local-key" {
		t.Fatal("remote poll replaced pinned local identity")
	}
	if err := s.RegisterLocalExecutor("local", identity, strings.Repeat("t", 32)); err != nil {
		t.Fatal(err)
	}
	identity["public_key"] = "different-key"
	if err := s.RegisterLocalExecutor("local", identity, strings.Repeat("t", 32)); err == nil {
		t.Fatal("silently replaced local identity")
	}
}

func TestNodeLocalResultCannotArriveThroughAnotherQueue(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir()})
	defer s.Close()
	result := s.callShellTool("shell:local", "shell_exec", map[string]any{"cmd": "printf local"}, true, time.Second)
	id := firstString(result, "job_id")
	if id == "" {
		t.Fatalf("missing job id: %v", result)
	}
	r := httptest.NewRequest("POST", "/queue/other/result", strings.NewReader(`{"id":"`+id+`","result":{"return_code":0}}`))
	w := httptest.NewRecorder()
	s.shellQueueResult(w, r, "other")
	if w.Code != http.StatusForbidden {
		t.Fatalf("foreign result accepted: %d %s", w.Code, w.Body.String())
	}
}
