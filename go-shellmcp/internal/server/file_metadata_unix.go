//go:build !windows

package server

import (
	"os"
	"syscall"
)

type fileOwnership struct {
	UID   int
	GID   int
	Valid bool
}

func ownershipFromInfo(info os.FileInfo) fileOwnership {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return fileOwnership{}
	}
	return fileOwnership{UID: int(st.Uid), GID: int(st.Gid), Valid: true}
}

func ownershipForWrite(path string) fileOwnership {
	if info, err := os.Lstat(path); err == nil {
		return ownershipFromInfo(info)
	}
	if info, err := os.Stat(filepathDir(path)); err == nil {
		return ownershipFromInfo(info)
	}
	return fileOwnership{}
}

func applyOpenFileOwnership(f *os.File, ownership fileOwnership) error {
	if !ownership.Valid || os.Geteuid() != 0 {
		return nil
	}
	return f.Chown(ownership.UID, ownership.GID)
}

func applyPathOwnership(path string, ownership fileOwnership, symlink bool) error {
	if !ownership.Valid || os.Geteuid() != 0 {
		return nil
	}
	if symlink {
		return os.Lchown(path, ownership.UID, ownership.GID)
	}
	return os.Chown(path, ownership.UID, ownership.GID)
}

func processIsPrivileged() bool { return os.Geteuid() == 0 }
