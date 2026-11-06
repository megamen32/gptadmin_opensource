//go:build !windows

package server

import (
	"os"
	"syscall"
)

func transferOpenRead(root *os.Root, path string) (*os.File, error) {
	return root.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
