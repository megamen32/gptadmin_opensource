package hub

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var errInstructionFileLockBusy = errors.New("instruction file lock busy")

func acquireInstructionFileLock(path string) (*os.File, error) {
	if path == "" {
		return nil, errors.New("startup instructions file path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create startup instructions directory: %w", err)
	}
	lock, err := AcquireUpdateLock(path + ".lock")
	if err != nil {
		if isInstructionLockBusy(err) {
			return nil, fmt.Errorf("%w: %v", errInstructionFileLockBusy, err)
		}
		return nil, fmt.Errorf("acquire startup instructions file lock: %w", err)
	}
	return lock, nil
}

func releaseInstructionFileLock(lock *os.File) error {
	return ReleaseUpdateLock(lock)
}
