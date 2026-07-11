//go:build windows

// Windows stub for the file-metadata snapshot/restore helpers. Windows does
// not expose stable uid/gid in syscall.Stat_t the way unix does, so this
// build records only file mode. The exported API mirrors fsmeta.go so callers
// can be platform-agnostic.
package shell

import (
	"os"
	"path/filepath"
)

const defaultMaxFiles = 1000

type entry struct {
	mode os.FileMode
	uid  int
	gid  int
}

type Snapshot struct {
	dir   string
	files map[string]entry
}

func (s *Snapshot) Empty() bool {
	return s == nil || len(s.files) == 0
}

func (s *Snapshot) Len() int {
	if s == nil {
		return 0
	}
	return len(s.files)
}

func (s *Snapshot) Dir() string {
	if s == nil {
		return ""
	}
	return s.dir
}

func SnapshotDir(dir string, maxFiles int) (*Snapshot, error) {
	if dir == "" {
		return nil, nil
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if !info.IsDir() {
		return nil, nil
	}
	if maxFiles <= 0 {
		maxFiles = defaultMaxFiles
	}

	snap := &Snapshot{dir: abs, files: make(map[string]entry, maxFiles)}

	f, err := os.Open(abs)
	if err != nil {
		return nil, err
	}
	names, err := f.Readdirnames(maxFiles + 1)
	_ = f.Close()
	if err != nil && len(names) == 0 {
		return nil, nil
	}
	if len(names) > maxFiles {
		names = names[:maxFiles]
	}

	for _, name := range names {
		path := filepath.Join(abs, name)
		st, err := os.Lstat(path)
		if err != nil {
			continue
		}
		mode := st.Mode()
		if mode.IsDir() {
			continue
		}
		if mode&os.ModeSymlink == 0 && !mode.IsRegular() {
			continue
		}
		snap.files[path] = entry{mode: mode.Perm(), uid: -1, gid: -1}
	}
	return snap, nil
}

func (s *Snapshot) Restore() (restored, failed int) {
	if s == nil || len(s.files) == 0 {
		return 0, 0
	}
	for path, e := range s.files {
		st, err := os.Lstat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			failed++
			continue
		}
		if st.Mode()&os.ModeSymlink == 0 {
			if cmerr := os.Chmod(path, e.mode); cmerr != nil {
				failed++
				continue
			}
		}
		restored++
	}
	return restored, failed
}