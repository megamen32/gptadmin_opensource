//go:build !linux && !darwin && !windows

package hub

import "errors"

func authFilesystemAvailable(string) (authFilesystemSpace, error) {
	return authFilesystemSpace{}, errors.New("reader filesystem preflight unsupported on this platform")
}
