package server

import (
	"testing"
	"time"
)

// A planned stop (ShutdownGrace) must let already-running callbacks finish;
// only stragglers past the window are cancelled.
func TestStopCallbacksGraceRunsCallbacksToCompletion(t *testing.T) {
	s := New(Config{ShutdownGrace: 2 * time.Second})
	ctx, finish, ok := s.beginCallback("graceful-cb", false)
	if !ok {
		t.Fatal("beginCallback refused")
	}
	completed := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			// forced cancel before natural completion
		case <-time.After(150 * time.Millisecond):
			close(completed)
		}
		finish()
	}()
	start := time.Now()
	s.StopCallbacks()
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Fatalf("StopCallbacks returned before the callback finished: %s", elapsed)
	}
	select {
	case <-completed:
	default:
		t.Fatal("running callback was cancelled instead of finishing within the grace window")
	}
}

// Legacy default (no grace) keeps today's immediate-cancel behavior.
func TestStopCallbacksWithoutGraceCancelsImmediately(t *testing.T) {
	s := New(Config{})
	ctx, finish, ok := s.beginCallback("legacy-cb", false)
	if !ok {
		t.Fatal("beginCallback refused")
	}
	cancelled := make(chan struct{})
	go func() {
		<-ctx.Done()
		close(cancelled)
		finish()
	}()
	s.StopCallbacks()
	select {
	case <-cancelled:
	default:
		t.Fatal("legacy stop did not cancel the running callback")
	}
}
