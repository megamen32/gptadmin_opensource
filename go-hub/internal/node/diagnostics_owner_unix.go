//go:build unix

package node

import (
	"os"
	"syscall"
)

func diagnosticsOwnedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Geteuid()
}
