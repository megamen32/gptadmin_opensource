//go:build windows

package hub

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

const (
	lockFileFailImmediately = 0x1
	lockFileExclusiveLock   = 0x2
)

var (
	lockFileEx   = syscall.NewLazyDLL("kernel32.dll").NewProc("LockFileEx")
	unlockFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("UnlockFileEx")
)

// AcquireUpdateLock takes a non-blocking exclusive lock on the update lock file.
func AcquireUpdateLock(lockPath string) (*os.File, error) {
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	overlapped := new(syscall.Overlapped)
	result, _, callErr := lockFileEx.Call(
		file.Fd(),
		lockFileFailImmediately|lockFileExclusiveLock,
		0,
		1,
		0,
		uintptr(unsafe.Pointer(overlapped)),
	)
	if result == 0 {
		file.Close()
		return nil, fmt.Errorf("acquire lock: %w", callErr)
	}
	return file, nil
}

// ReleaseUpdateLock unlocks and closes an update lock file.
func ReleaseUpdateLock(file *os.File) error {
	overlapped := new(syscall.Overlapped)
	result, _, callErr := unlockFileEx.Call(file.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(overlapped)))
	if result == 0 {
		file.Close()
		return fmt.Errorf("release lock: %w", callErr)
	}
	return file.Close()
}
