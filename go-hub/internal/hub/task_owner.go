package hub

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/gofrs/flock"
)

// A runtime has a unique, never-reused name and holds an OS lock for its
// lifetime. No clock/heartbeat timeout may declare a paused live owner dead.
// This is local shared-disk ownership, not distributed consensus.
type taskOwner struct {
	id   string
	lock *flock.Flock
}

func (s *Server) ensureTaskOwnerLocked() (string, error) {
	if s.taskRuntimeClosed {
		return "", fmt.Errorf("task runtime is closed")
	}
	if s.cfg.ConfigDir == "" {
		return "", nil
	}
	if s.taskOwner != nil {
		return s.taskOwner.id, nil
	}
	dir := filepath.Join(s.cfg.ConfigDir, "task-owners")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	owner := &taskOwner{id: newID()}
	owner.lock = flock.New(filepath.Join(dir, owner.id+".lock"))
	held, err := owner.lock.TryLock()
	if err != nil {
		return "", err
	}
	if !held {
		return "", fmt.Errorf("task owner identity collision")
	}
	runtime.SetFinalizer(owner, func(o *taskOwner) { _ = o.lock.Close() })
	s.taskOwner = owner
	return owner.id, nil
}

func (s *Server) taskOwnerAlive(owner string) (bool, error) {
	if owner == "" || s.cfg.ConfigDir == "" {
		return false, nil
	}
	if strings.ContainsAny(owner, "/\\.") || len(owner) > 128 {
		return false, fmt.Errorf("invalid task owner identifier")
	}
	if s.taskOwner != nil && s.taskOwner.id == owner {
		return !s.taskRuntimeClosed, nil
	}
	probe := flock.New(filepath.Join(s.cfg.ConfigDir, "task-owners", owner+".lock"))
	if _, err := os.Stat(probe.Path()); os.IsNotExist(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	held, err := probe.TryLock()
	if err != nil {
		return false, err
	}
	defer probe.Close()
	return !held, nil
}

// Close releases task ownership after HTTP serving and in-flight operations
// have stopped. It is idempotent; a closed runtime cannot create new tasks.
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.taskRuntimeClosed {
		return nil
	}
	s.taskRuntimeClosed = true
	if s.taskRuntimeStop != nil {
		close(s.taskRuntimeStop)
	}
	if s.taskOwner != nil {
		runtime.SetFinalizer(s.taskOwner, nil)
		return s.taskOwner.lock.Close()
	}
	return nil
}
