//go:build linux || darwin

package hub

import "golang.org/x/sys/unix"

func authFilesystemAvailable(path string) (authFilesystemSpace, error) {
	var fs unix.Statfs_t
	if err := unix.Statfs(path, &fs); err != nil {
		return authFilesystemSpace{}, err
	}
	return authFilesystemSpace{Available: uint64(fs.Bavail) * uint64(fs.Bsize), Block: uint64(fs.Bsize), Inodes: uint64(fs.Ffree), HasInodes: true}, nil
}
