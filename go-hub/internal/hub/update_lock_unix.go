//go:build !windows

package hub

import (
	"fmt"
	"os"
	"syscall"
)

// AcquireUpdateLock takes a non-blocking exclusive lock on the update lock file.
func AcquireUpdateLock(lockPath string) (*os.File, error) {
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, fmt.Errorf("acquire lock: %w", err)
	}
	return file, nil
}

// ReleaseUpdateLock unlocks and closes an update lock file.
func ReleaseUpdateLock(file *os.File) error {
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_UN); err != nil {
		file.Close()
		return fmt.Errorf("release lock: %w", err)
	}
	return file.Close()
}
