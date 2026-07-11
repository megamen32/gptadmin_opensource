package hub

import (
	"path/filepath"
	"testing"
)

func TestUpdateStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "update_state.json")
	lockPath := filepath.Join(dir, "update.lock")

	// 1. Start idle.
	s, _ := ReadUpdateState(statePath)
	if s != nil {
		t.Fatal("expected nil for missing file")
	}

	// 2. Write running.
	state := &UpdateState{Current: UpdateCurrent{Status: "running"}}
	if err := WriteUpdateState(statePath, state); err != nil {
		t.Fatalf("write: %v", err)
	}

	// 3. Second "request" sees running.
	s2, _ := ReadUpdateState(statePath)
	if s2.Current.Status != "running" {
		t.Errorf("expected running, got %q", s2.Current.Status)
	}

	// 4. Lock test.
	f, err := AcquireUpdateLock(lockPath)
	if err != nil {
		t.Fatalf("acquire lock: %v", err)
	}
	// Second acquire should fail.
	_, err2 := AcquireUpdateLock(lockPath)
	if err2 == nil {
		t.Fatal("expected lock conflict")
	}
	ReleaseUpdateLock(f)

	// 5. Write done.
	state.Current.Status = "idle"
	state.LastResult = &UpdateResult{
		Status:     "done",
		Message:    "ok",
		FinishedAt: 999,
		ToVersion:  120,
	}
	WriteUpdateState(statePath, state)

	// 6. Read done.
	s3, _ := ReadUpdateState(statePath)
	if s3.Current.Status != "idle" {
		t.Errorf("expected idle, got %q", s3.Current.Status)
	}
	if s3.LastResult.ToVersion != 120 {
		t.Errorf("expected to_version 120, got %d", s3.LastResult.ToVersion)
	}
}