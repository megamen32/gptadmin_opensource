//go:build windows

package hub

import "sync"

var stateFileProcessLock sync.Mutex

func withStateFileLock(_ string, fn func() error) error {
	stateFileProcessLock.Lock()
	defer stateFileProcessLock.Unlock()
	return fn()
}
