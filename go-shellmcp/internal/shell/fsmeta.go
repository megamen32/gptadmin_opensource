//go:build !windows

// Package shell file-metadata preservation helpers.
//
// Ported from client/shellmcp_{linux,mac}.py: snapshot mode/uid/gid for files in
// a working directory before running a command so that, if the command rewrites
// a file (e.g. temp-file + rename) and accidentally chowns/chmods it, we can
// restore the original metadata afterward.
//
// All operations are best-effort and never panic. This file holds the
// unix implementation; the windows variant lives in fsmeta_windows.go.
package shell

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// defaultMaxFiles is the cap used when the caller passes <= 0. Mirrors the
// Python default (SHELLMCP_PRESERVE_METADATA_MAX_FILES default of 50000) but
// kept smaller here so a misuse on a huge directory cannot stall a command.
const defaultMaxFiles = 1000

// entry is a single recorded file's metadata. uid/gid are -1 when the caller
// could not determine them.
type entry struct {
	mode os.FileMode
	uid  int
	gid  int
}

// Snapshot is the recorded metadata for files directly in a directory. Files
// in subdirectories are not recorded (matching the deliverable's "shallow,
// non-recursive" walk) — the Go port intentionally diverges from the Python
// reference which descends with skip-dirs; that behavior will be wired in by
// the agent that integrates this into the exec flow.
type Snapshot struct {
	dir   string
	files map[string]entry
}

// Empty reports whether the snapshot recorded nothing (either the dir was
// missing, the feature was disabled, or the walk found no files).
func (s *Snapshot) Empty() bool {
	return s == nil || len(s.files) == 0
}

// Len returns the number of recorded entries.
func (s *Snapshot) Len() int {
	if s == nil {
		return 0
	}
	return len(s.files)
}

// Dir returns the directory the snapshot was taken against.
func (s *Snapshot) Dir() string {
	if s == nil {
		return ""
	}
	return s.dir
}

// SnapshotDir records the metadata of every regular file or symlink directly
// in dir (no descent into subdirectories). If maxFiles <= 0 a default cap is
// applied. The walk is best-effort: errors on individual files are silently
// skipped so a transient stat failure cannot abort the snapshot.
//
// If dir is empty, missing, or not a directory, a nil snapshot with no error
// is returned — this mirrors the Python helper which simply returns
// (None, {}) in that case.
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
	names, err := f.Readdirnames(maxFiles + 1) // +1 to detect "more"
	// Always close; ignore the close error.
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
			continue // best-effort
		}
		mode := st.Mode()
		// Match Python: only record files and symlinks; skip subdirectories,
		// sockets, devices, etc.
		if mode.IsDir() {
			continue
		}
		if mode&os.ModeSymlink == 0 && !mode.IsRegular() {
			continue
		}
		e := entry{mode: mode.Perm()}
		if sys, ok := st.Sys().(*syscall.Stat_t); ok {
			e.uid = int(sys.Uid)
			e.gid = int(sys.Gid)
		}
		snap.files[path] = e
	}
	return snap, nil
}

// Restore re-applies the recorded mode/uid/gid for each file in the snapshot.
// Files that no longer exist are silently skipped and NOT counted as
// failures. Other errors on individual files are counted but never
// propagated; Restore is always best-effort.
//
// Returns (restored, failed), mirroring the {"restored": N, "failed": N}
// dict from the Python helper so a future integration can surface those
// counts in the Result.
func (s *Snapshot) Restore() (restored, failed int) {
	if s == nil || len(s.files) == 0 {
		return 0, 0
	}
	for path, e := range s.files {
		st, err := os.Lstat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue // file vanished — not a failure
			}
			failed++
			continue
		}
		if e.uid >= 0 || e.gid >= 0 {
			uid := e.uid
			if uid < 0 {
				uid = -1 // "do not change"
			}
			gid := e.gid
			if gid < 0 {
				gid = -1
			}
			if lerr := os.Lchown(path, uid, gid); lerr != nil && !isPermissionError(lerr) {
				// EPERM on a non-root process is expected (not a hard failure);
				// any other error counts but we still attempt the chmod below.
				failed++
			}
		}
		if st.Mode()&os.ModeSymlink == 0 {
			if cmerr := os.Chmod(path, e.mode); cmerr != nil && !isPermissionError(cmerr) {
				failed++
				continue
			}
		}
		restored++
	}
	return restored, failed
}

// isPermissionError reports whether err is a permission-denied error. Used to
// downgrade expected EPERM (e.g. when running as a non-root user trying to
// chown files they don't own) from a hard failure to a soft skip.
func isPermissionError(err error) bool {
	if err == nil {
		return false
	}
	var pe *os.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	return err == syscall.EPERM || err == syscall.EACCES || err == os.ErrPermission
}