//go:build !windows

package hub

import (
	"errors"
	"os"
	"syscall"
)

var replaceInstructionFile = os.Rename
var syncInstructionDirectory = syncInstructionDirectoryOS

func syncInstructionDirectoryOS(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	if err := directory.Sync(); err != nil {
		directory.Close()
		return err
	}
	return directory.Close()
}

func isInstructionLockBusy(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
}
