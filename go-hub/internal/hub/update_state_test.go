package hub

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadWriteUpdateState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "update_state.json")

	// Read non-existent file returns nil.
	s, err := ReadUpdateState(path)
	if err != nil {
		t.Fatalf("ReadUpdateState on missing file: %v", err)
	}
	if s != nil {
		t.Fatalf("expected nil state for missing file, got %+v", s)
	}

	// Write and read back.
	state := &UpdateState{
		Current: UpdateCurrent{Status: "idle"},
		LastResult: &UpdateResult{
			Status:      "done",
			Message:     "Updated build 119 → 120",
			StartedAt:   time.Now().Unix(),
			FinishedAt:  time.Now().Unix(),
			FromVersion: 119,
			ToVersion:   120,
		},
	}
	if err := WriteUpdateState(path, state); err != nil {
		t.Fatalf("WriteUpdateState: %v", err)
	}

	got, err := ReadUpdateState(path)
	if err != nil {
		t.Fatalf("ReadUpdateState: %v", err)
	}
	if got == nil {
		t.Fatal("expected state, got nil")
	}
	if got.Current.Status != "idle" {
		t.Errorf("expected idle, got %q", got.Current.Status)
	}
	if got.LastResult == nil {
		t.Fatal("expected last_result")
	}
	if got.LastResult.Message != "Updated build 119 → 120" {
		t.Errorf("unexpected message: %q", got.LastResult.Message)
	}
}

func TestEnsureDefaultUpdateState(t *testing.T) {
	// nil -> default idle.
	s := EnsureDefaultUpdateState(nil)
	if s.Current.Status != "idle" {
		t.Errorf("expected idle, got %q", s.Current.Status)
	}

	// empty status -> idle.
	s2 := EnsureDefaultUpdateState(&UpdateState{Current: UpdateCurrent{}})
	if s2.Current.Status != "idle" {
		t.Errorf("expected idle, got %q", s2.Current.Status)
	}
}

func TestAcquireReleaseLock(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "update.lock")

	f, err := AcquireUpdateLock(lockPath)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	// Second acquire should fail (lock held).
	_, err2 := AcquireUpdateLock(lockPath)
	if err2 == nil {
		t.Fatal("expected second acquire to fail with lock held")
	}

	// Release and re-acquire.
	if err := ReleaseUpdateLock(f); err != nil {
		t.Fatalf("release: %v", err)
	}

	f3, err := AcquireUpdateLock(lockPath)
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	ReleaseUpdateLock(f3)

	// Verify lock file still exists.
	if _, err := os.Stat(lockPath); os.IsNotExist(err) {
		t.Error("lock file should persist after release")
	}
}
