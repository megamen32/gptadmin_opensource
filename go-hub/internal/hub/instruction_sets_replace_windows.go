//go:build windows

package hub

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"
)

const (
	moveFileReplaceExisting = 0x1
	moveFileWriteThrough    = 0x8
)

var moveFileExW = syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileExW")

var replaceInstructionFile = replaceInstructionFileWindows
var syncInstructionDirectory = syncInstructionDirectoryWindows

func replaceInstructionFileWindows(source, destination string) error {
	sourcePath, err := syscall.UTF16PtrFromString(source)
	if err != nil {
		return fmt.Errorf("encode source path: %w", err)
	}
	destinationPath, err := syscall.UTF16PtrFromString(destination)
	if err != nil {
		return fmt.Errorf("encode destination path: %w", err)
	}
	result, _, callErr := moveFileExW.Call(
		uintptr(unsafe.Pointer(sourcePath)),
		uintptr(unsafe.Pointer(destinationPath)),
		moveFileReplaceExisting|moveFileWriteThrough,
	)
	if result == 0 {
		return callErr
	}
	return nil
}

func syncInstructionDirectoryWindows(string) error {
	// MoveFileExW with MOVEFILE_WRITE_THROUGH flushes the replacement before returning.
	return nil
}

func isInstructionLockBusy(err error) bool {
	// LockFileEx reports ERROR_LOCK_VIOLATION for a held non-blocking lock.
	return errors.Is(err, syscall.Errno(33))
}
