//go:build windows

package server

import "os"

type fileOwnership struct {
	UID   int
	GID   int
	Valid bool
}

func ownershipFromInfo(info os.FileInfo) fileOwnership                            { return fileOwnership{} }
func ownershipForWrite(path string) fileOwnership                                 { return fileOwnership{} }
func applyOpenFileOwnership(f *os.File, ownership fileOwnership) error            { return nil }
func applyPathOwnership(path string, ownership fileOwnership, symlink bool) error { return nil }
func processIsPrivileged() bool                                                   { return false }
