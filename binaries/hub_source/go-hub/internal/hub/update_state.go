package hub

import (
	"encoding/json"
	"fmt"
	"os"
	"syscall"
)

// UpdateState represents the persistent update state file.
type UpdateState struct {
	Current    UpdateCurrent `json:"current"`
	LastResult *UpdateResult `json:"last_result"`
}

// UpdateCurrent tracks right-now update activity.
type UpdateCurrent struct {
	Status string `json:"status"` // "idle" | "running"
}

// UpdateResult records the outcome of the last completed update.
type UpdateResult struct {
	Status      string `json:"status"` // "done" | "error"
	Message     string `json:"message"`
	StartedAt   int64  `json:"started_at"`
	FinishedAt  int64  `json:"finished_at"`
	FromVersion int    `json:"from_version"`
	ToVersion   int    `json:"to_version"`
}

// ReadUpdateState reads and parses the update state file.
// Returns nil, nil if the file does not exist.
func ReadUpdateState(path string) (*UpdateState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read update state: %w", err)
	}
	var s UpdateState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse update state: %w", err)
	}
	return &s, nil
}

// WriteUpdateState atomically writes the update state file.
func WriteUpdateState(path string, s *UpdateState) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal update state: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("write update state tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename update state: %w", err)
	}
	return nil
}

// AcquireUpdateLock takes an exclusive flock on the lock file.
// Returns the open file handle (caller must ReleaseUpdateLock).
func AcquireUpdateLock(lockPath string) (*os.File, error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("acquire lock: %w", err)
	}
	return f, nil
}

// ReleaseUpdateLock releases the flock and closes the file.
func ReleaseUpdateLock(f *os.File) error {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		f.Close()
		return fmt.Errorf("release lock: %w", err)
	}
	return f.Close()
}

// EnsureDefaultUpdateState returns a state with idle current if s is nil.
func EnsureDefaultUpdateState(s *UpdateState) *UpdateState {
	if s == nil {
		return &UpdateState{
			Current: UpdateCurrent{Status: "idle"},
		}
	}
	if s.Current.Status == "" {
		s.Current.Status = "idle"
	}
	return s
}
