//go:build !windows

package storagebudget

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

func FilesystemLimit(root string) (int64, error) {
	probe := root
	for {
		var st syscall.Statfs_t
		if err := syscall.Statfs(probe, &st); err == nil {
			return LimitForCapacity(int64(st.Blocks) * int64(st.Bsize)), nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return 0, err
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return 0, os.ErrNotExist
		}
		probe = parent
	}
}
