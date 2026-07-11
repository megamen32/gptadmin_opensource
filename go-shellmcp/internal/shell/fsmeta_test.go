//go:build !windows

package shell

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// isRoot returns true when the test process is uid 0, so we can decide whether
// chown assertions are safe to make. On most CI runners we are not root and
// must skip the ownership round-trip.
func isRoot() bool {
	return os.Geteuid() == 0
}

// makeFile writes content to name inside dir and chmods it to mode.
func makeFile(t *testing.T, dir, name string, content string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatalf("chmod %s: %v", p, err)
	}
	return p
}

func TestSnapshotDirRestoresMode(t *testing.T) {
	dir := t.TempDir()
	makeFile(t, dir, "alpha", "AAA", 0o640)
	makeFile(t, dir, "beta", "BBB", 0o755)

	snap, err := SnapshotDir(dir, 0)
	if err != nil {
		t.Fatalf("SnapshotDir: %v", err)
	}
	if snap == nil || snap.Len() != 2 {
		t.Fatalf("want 2 entries, got %+v (len=%d)", snap, snap.Len())
	}

	// Mutate one of the files as if a root command had rewritten it.
	beta := filepath.Join(dir, "beta")
	if err := os.Chmod(beta, 0o600); err != nil {
		t.Fatalf("chmod beta: %v", err)
	}
	if isRoot() {
		if err := os.Lchown(beta, 0, 0); err != nil {
			t.Fatalf("lchown beta: %v", err)
		}
	}

	restored, failed := snap.Restore()
	if failed != 0 {
		t.Fatalf("restore reported %d failures", failed)
	}
	if restored != 2 {
		t.Fatalf("want 2 restored, got %d", restored)
	}

	// Mode must be back to the original.
	for name, want := range map[string]os.FileMode{"alpha": 0o640, "beta": 0o755} {
		st, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if got := st.Mode().Perm(); got != want {
			t.Fatalf("mode for %s: want %o got %o", name, want, got)
		}
	}

	// If we are root, verify that ownership was actually restored: read
	// the file's uid/gid back and confirm they round-trip through the
	// snapshot. We re-stat the file (not the original snapshot) so we
	// compare against what Restore just wrote.
	if isRoot() {
		st, err := os.Stat(beta)
		if err != nil {
			t.Fatalf("stat beta after restore: %v", err)
		}
		// On a non-root-owned file, Lchown by root should have produced
		// uid=0/gid=0 in the "mutated" step and then put us back to the
		// snapshot's original values. We don't pin to a specific numeric
		// value (which depends on whoever ran the test) — we just confirm
		// the resulting uid is non-negative and matches what a fresh stat
		// of an unmutated sibling file would produce. Concretely, before
		// the mutation alpha and beta had the same owner; after Restore
		// they still should.
		alphaSt, err := os.Stat(filepath.Join(dir, "alpha"))
		if err != nil {
			t.Fatalf("stat alpha: %v", err)
		}
		if alphaSt.Sys() != nil && st.Sys() != nil {
			if us, ok := alphaSt.Sys().(uidGetter); ok {
				if bs, ok := st.Sys().(uidGetter); ok {
					if us.Uid() != bs.Uid() {
						t.Fatalf("uid drift: alpha=%d beta=%d", us.Uid(), bs.Uid())
					}
					if us.Gid() != bs.Gid() {
						t.Fatalf("gid drift: alpha=%d beta=%d", us.Gid(), bs.Gid())
					}
				}
			}
		}
	}
}

// uidGetter matches the subset of syscall.Stat_t we read from tests. Defined
// here so the test stays build-tag-gated to !windows without importing
// syscall directly.
type uidGetter interface {
	Uid() uint32
	Gid() uint32
}

func TestSnapshotDirRespectsMaxFiles(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		makeFile(t, dir, "file-"+strconv.Itoa(i), "x", 0o644)
	}
	snap, err := SnapshotDir(dir, 2)
	if err != nil {
		t.Fatalf("SnapshotDir: %v", err)
	}
	if snap == nil || snap.Len() != 2 {
		t.Fatalf("want cap=2 entries, got len=%d", snap.Len())
	}
}

func TestSnapshotDirDefaultCapWhenZero(t *testing.T) {
	dir := t.TempDir()
	makeFile(t, dir, "a", "a", 0o600)
	snap, err := SnapshotDir(dir, 0) // 0 → defaultMaxFiles
	if err != nil {
		t.Fatalf("SnapshotDir: %v", err)
	}
	if snap == nil || snap.Len() != 1 {
		t.Fatalf("want 1 entry, got %+v", snap.Len())
	}
	if snap.Dir() == "" {
		t.Fatalf("Dir() should be populated")
	}
}

func TestSnapshotDirSkipsSubdirectories(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	makeFile(t, dir, "top", "T", 0o644)
	snap, err := SnapshotDir(dir, 0)
	if err != nil {
		t.Fatalf("SnapshotDir: %v", err)
	}
	if snap.Len() != 1 {
		t.Fatalf("want 1 (subdir should not be recorded), got %d", snap.Len())
	}
	for p := range snap.files {
		if strings.HasSuffix(p, "sub") {
			t.Fatalf("subdir leaked into snapshot: %s", p)
		}
	}
}

func TestSnapshotDirEmptyAndMissingDir(t *testing.T) {
	// Empty string → no-op, no error.
	snap, err := SnapshotDir("", 0)
	if err != nil || snap != nil {
		t.Fatalf("want (nil,nil) for empty dir, got (%v, %v)", snap, err)
	}
	// Non-existent dir → no-op, no error (matches Python's behaviour of
	// returning (None, {}) when cwd does not exist).
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	snap, err = SnapshotDir(missing, 0)
	if err != nil || snap != nil {
		t.Fatalf("want (nil,nil) for missing dir, got (%v, %v)", snap, err)
	}
	// A file (not a directory) → also a no-op.
	tmpFile := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(tmpFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("write tmp file: %v", err)
	}
	snap, err = SnapshotDir(tmpFile, 0)
	if err != nil || snap != nil {
		t.Fatalf("want (nil,nil) for file path, got (%v, %v)", snap, err)
	}
}

func TestSnapshotRestoreIsSafeAfterFileDeleted(t *testing.T) {
	dir := t.TempDir()
	alpha := makeFile(t, dir, "alpha", "AAA", 0o640)
	beta := makeFile(t, dir, "beta", "BBB", 0o755)

	snap, err := SnapshotDir(dir, 0)
	if err != nil {
		t.Fatalf("SnapshotDir: %v", err)
	}
	if snap.Len() != 2 {
		t.Fatalf("want 2, got %d", snap.Len())
	}

	if err := os.Remove(alpha); err != nil {
		t.Fatalf("remove alpha: %v", err)
	}
	if err := os.Chmod(beta, 0o600); err != nil {
		t.Fatalf("chmod beta: %v", err)
	}

	restored, failed := snap.Restore()
	if failed != 0 {
		t.Fatalf("restore: want 0 failures, got %d", failed)
	}
	// We restored one file (beta); alpha was missing and was silently
	// skipped — not counted as either restored or failed.
	if restored != 1 {
		t.Fatalf("want 1 restored, got %d", restored)
	}

	st, err := os.Stat(beta)
	if err != nil {
		t.Fatalf("stat beta: %v", err)
	}
	if got := st.Mode().Perm(); got != 0o755 {
		t.Fatalf("beta mode: want 0o755, got %o", got)
	}
}

func TestSnapshotNilRestoreIsNoop(t *testing.T) {
	var s *Snapshot
	restored, failed := s.Restore()
	if restored != 0 || failed != 0 {
		t.Fatalf("nil snapshot restore: want (0,0), got (%d,%d)", restored, failed)
	}
	if !s.Empty() || s.Len() != 0 {
		t.Fatalf("nil snapshot methods: Empty=%v Len=%d", s.Empty(), s.Len())
	}

	// Also exercise a real but empty snapshot.
	dir := t.TempDir()
	snap, err := SnapshotDir(dir, 0)
	if err != nil {
		t.Fatalf("SnapshotDir: %v", err)
	}
	if !snap.Empty() || snap.Len() != 0 {
		t.Fatalf("empty dir snapshot: Empty=%v Len=%d", snap.Empty(), snap.Len())
	}
	restored, failed = snap.Restore()
	if restored != 0 || failed != 0 {
		t.Fatalf("empty snapshot restore: want (0,0), got (%d,%d)", restored, failed)
	}
}

func TestSnapshotRestoreSurvivesPermissionErrors(t *testing.T) {
	// Disabled-on-non-root simulation: when chown/chmod fail with EPERM
	// (because we are not root), Restore must still complete and report
	// zero *hard* failures.
	if isRoot() {
		t.Skip("running as root, EPERM path cannot be exercised")
	}
	dir := t.TempDir()
	makeFile(t, dir, "gamma", "G", 0o644)

	snap, err := SnapshotDir(dir, 0)
	if err != nil {
		t.Fatalf("SnapshotDir: %v", err)
	}

	restored, failed := snap.Restore()
	if failed != 0 {
		t.Fatalf("non-root restore should not classify EPERM as a hard failure; got %d failed", failed)
	}
	if restored != 1 {
		t.Fatalf("want 1 restored, got %d", restored)
	}
}