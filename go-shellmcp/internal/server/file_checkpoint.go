package server

import (
	"compress/gzip"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	checkpointManifestVersion = 1
	checkpointMaxFiles        = 100000
	checkpointMaxFileBytes    = int64(128 << 20)
	checkpointMaxTotalBytes   = int64(512 << 20)
)

var checkpointMu sync.Mutex

type fileCheckpointArgs struct {
	Action       string   `json:"action"`
	Path         string   `json:"path"`
	Paths        []string `json:"paths"`
	CheckpointID string   `json:"checkpoint_id"`
	Name         string   `json:"name"`
	TTLDays      *int     `json:"ttl_days"`
	Limit        int      `json:"limit"`
	MaxAgeDays   *int     `json:"max_age_days"`
}

type checkpointManifest struct {
	Version      int               `json:"version"`
	CheckpointID string            `json:"checkpoint_id"`
	Name         string            `json:"name,omitempty"`
	Kind         string            `json:"kind"`
	Host         string            `json:"host,omitempty"`
	Paths        []string          `json:"paths"`
	Entries      []checkpointEntry `json:"entries"`
	FileCount    int               `json:"file_count"`
	TotalBytes   int64             `json:"total_bytes"`
	CreatedAt    string            `json:"created_at"`
	ExpiresAt    string            `json:"expires_at,omitempty"`
}

type checkpointEntry struct {
	Path       string `json:"path"`
	Kind       string `json:"kind"`
	Mode       uint32 `json:"mode,omitempty"`
	UID        int    `json:"uid,omitempty"`
	GID        int    `json:"gid,omitempty"`
	OwnerValid bool   `json:"owner_valid,omitempty"`
	SizeBytes  int64  `json:"size_bytes,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
	LinkTarget string `json:"link_target,omitempty"`
}

type checkpointDiff struct {
	Added     []string `json:"added"`
	Modified  []string `json:"modified"`
	Deleted   []string `json:"deleted"`
	Truncated bool     `json:"truncated"`
}

func (s *Server) mcpFileCheckpoint(args map[string]any) (map[string]any, error) {
	checkpointMu.Lock()
	defer checkpointMu.Unlock()

	var req fileCheckpointArgs
	b, _ := json.Marshal(args)
	if err := json.Unmarshal(b, &req); err != nil {
		return nil, err
	}
	req.Action = strings.ToLower(strings.TrimSpace(req.Action))
	if req.Action == "" {
		req.Action = "create"
	}
	if strings.TrimSpace(req.Path) != "" {
		req.Paths = append(req.Paths, req.Path)
	}

	switch req.Action {
	case "create":
		manifest, err := s.checkpointCreate(req.Paths, req.Name, req.TTLDays, "manual", false)
		if err != nil {
			return nil, err
		}
		return mcpText(fmt.Sprintf("checkpoint %s saved: %d files, %d bytes", manifest.CheckpointID, manifest.FileCount, manifest.TotalBytes), map[string]any{
			"ok": true, "action": "create", "checkpoint": manifest, "checkpoint_id": manifest.CheckpointID, "root": s.fileCheckpointRoot(),
		}), nil
	case "list":
		items, err := s.checkpointList(req.Limit)
		if err != nil {
			return nil, err
		}
		return mcpText(fmt.Sprintf("%d checkpoint(s)", len(items)), map[string]any{"ok": true, "action": "list", "count": len(items), "checkpoints": items, "root": s.fileCheckpointRoot()}), nil
	case "diff":
		manifest, err := s.checkpointLoad(req.CheckpointID)
		if err != nil {
			return nil, err
		}
		diff, err := s.checkpointDiffLive(manifest, 200)
		if err != nil {
			return nil, err
		}
		return mcpText(formatCheckpointDiff(manifest.CheckpointID, diff), map[string]any{"ok": true, "action": "diff", "checkpoint_id": manifest.CheckpointID, "diff": diff}), nil
	case "restore":
		manifest, err := s.checkpointLoad(req.CheckpointID)
		if err != nil {
			return nil, err
		}
		safetyName := "restore-safety-" + time.Now().UTC().Format("20060102-150405.000")
		ttl := 30
		safety, err := s.checkpointCreate(manifest.Paths, safetyName, &ttl, "restore-safety", true)
		if err != nil {
			return nil, fmt.Errorf("restore aborted: could not create safety checkpoint: %w", err)
		}
		if err := s.checkpointRestore(manifest); err != nil {
			return nil, fmt.Errorf("restore failed after safety checkpoint %s was saved: %w", safety.CheckpointID, err)
		}
		return mcpText(fmt.Sprintf("restored %s; pre-restore state is safety checkpoint %s", manifest.CheckpointID, safety.CheckpointID), map[string]any{
			"ok": true, "action": "restore", "checkpoint_id": manifest.CheckpointID, "safety_checkpoint_id": safety.CheckpointID, "safety_checkpoint_created": true,
		}), nil
	case "delete":
		manifest, err := s.checkpointLoad(req.CheckpointID)
		if err != nil {
			return nil, err
		}
		if err := os.Remove(s.checkpointManifestPath(manifest.CheckpointID)); err != nil {
			return nil, err
		}
		removed, reclaimed, err := s.checkpointGC()
		if err != nil {
			return nil, err
		}
		return mcpText(fmt.Sprintf("deleted checkpoint %s; GC removed %d object(s)", manifest.CheckpointID, removed), map[string]any{"ok": true, "action": "delete", "checkpoint_id": manifest.CheckpointID, "gc_removed_objects": removed, "gc_reclaimed_bytes": reclaimed}), nil
	case "cleanup":
		removedCP, err := s.checkpointCleanup(req.MaxAgeDays)
		if err != nil {
			return nil, err
		}
		removedObj, reclaimed, err := s.checkpointGC()
		if err != nil {
			return nil, err
		}
		return mcpText(fmt.Sprintf("cleanup removed %d checkpoint(s) and %d unreferenced object(s)", removedCP, removedObj), map[string]any{"ok": true, "action": "cleanup", "removed_checkpoints": removedCP, "gc_removed_objects": removedObj, "gc_reclaimed_bytes": reclaimed}), nil
	case "gc":
		removed, reclaimed, err := s.checkpointGC()
		if err != nil {
			return nil, err
		}
		return mcpText(fmt.Sprintf("GC removed %d unreferenced object(s)", removed), map[string]any{"ok": true, "action": "gc", "gc_removed_objects": removed, "gc_reclaimed_bytes": reclaimed}), nil
	default:
		return nil, fmt.Errorf("unknown file_checkpoint action %q", req.Action)
	}
}

func (s *Server) fileCheckpointRoot() string {
	if v := strings.TrimSpace(os.Getenv("SHELLMCP_FILE_CHECKPOINT_ROOT")); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("GPTADMIN_FILE_CHECKPOINT_ROOT")); v != "" {
		return v
	}
	home := strings.TrimSpace(s.cfg.DefaultHome)
	if home == "" {
		if u, err := user.Current(); err == nil {
			home = u.HomeDir
		}
	}
	if home == "" {
		home = "/var/lib/gptadmin"
	}
	return filepath.Join(home, ".gptadmin", "file-checkpoints")
}

func (s *Server) checkpointCreate(paths []string, name string, ttlDays *int, kind string, allowMissing bool) (checkpointManifest, error) {
	paths, err := normalizeCheckpointRoots(paths)
	if err != nil {
		return checkpointManifest{}, err
	}
	if len(paths) == 0 {
		return checkpointManifest{}, errors.New("file_checkpoint create requires path or paths")
	}
	root := s.fileCheckpointRoot()
	rootAbs, _ := filepath.Abs(root)
	for _, path := range paths {
		if path == rootAbs || strings.HasPrefix(rootAbs, path+string(os.PathSeparator)) {
			return checkpointManifest{}, fmt.Errorf("refusing to checkpoint a root containing the checkpoint store itself: %s", path)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "objects"), 0o700); err != nil {
		return checkpointManifest{}, err
	}
	if err := os.MkdirAll(filepath.Join(root, "checkpoints"), 0o700); err != nil {
		return checkpointManifest{}, err
	}
	if name != "" && kind == "manual" {
		items, err := s.checkpointList(0)
		if err != nil {
			return checkpointManifest{}, err
		}
		for _, item := range items {
			if item.Name == name {
				return checkpointManifest{}, fmt.Errorf("checkpoint name %q already exists; choose another name or delete the old checkpoint", name)
			}
		}
	}

	entries, fileCount, totalBytes, err := s.captureCheckpointEntries(paths, true, allowMissing)
	if err != nil {
		return checkpointManifest{}, err
	}
	now := time.Now().UTC()
	id := "cp_" + now.Format("20060102_150405.000000") + "_" + randomHex(4)
	manifest := checkpointManifest{
		Version: checkpointManifestVersion, CheckpointID: id, Name: strings.TrimSpace(name), Kind: kind,
		Host: s.cfg.Name, Paths: paths, Entries: entries, FileCount: fileCount, TotalBytes: totalBytes, CreatedAt: now.Format(time.RFC3339Nano),
	}
	ttl := 30
	if ttlDays != nil {
		ttl = *ttlDays
	}
	if ttl > 0 {
		manifest.ExpiresAt = now.Add(time.Duration(ttl) * 24 * time.Hour).Format(time.RFC3339Nano)
	}
	if err := writeJSONAtomic(s.checkpointManifestPath(id), manifest, 0o600); err != nil {
		return checkpointManifest{}, err
	}
	return manifest, nil
}

func normalizeCheckpointRoots(paths []string) ([]string, error) {
	seen := map[string]bool{}
	var roots []string
	for _, raw := range paths {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		abs, err := filepath.Abs(raw)
		if err != nil {
			return nil, err
		}
		abs = filepath.Clean(abs)
		if !seen[abs] {
			seen[abs] = true
			roots = append(roots, abs)
		}
	}
	sort.Slice(roots, func(i, j int) bool { return len(roots[i]) < len(roots[j]) })
	out := roots[:0]
	for _, candidate := range roots {
		covered := false
		for _, root := range out {
			if candidate == root || strings.HasPrefix(candidate, root+string(os.PathSeparator)) {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, candidate)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (s *Server) captureCheckpointEntries(paths []string, storeObjects, allowMissing bool) ([]checkpointEntry, int, int64, error) {
	entriesByPath := map[string]checkpointEntry{}
	fileCount := 0
	var totalBytes int64
	for _, root := range paths {
		info, err := os.Lstat(root)
		if err != nil {
			if allowMissing && errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, 0, 0, err
		}
		if !info.IsDir() {
			entry, err := s.captureCheckpointEntry(root, info, storeObjects)
			if err != nil {
				return nil, 0, 0, err
			}
			entriesByPath[root] = entry
			if entry.Kind == "file" {
				fileCount++
				totalBytes += entry.SizeBytes
			}
			continue
		}
		err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if len(entriesByPath) >= checkpointMaxFiles {
				return fmt.Errorf("checkpoint exceeds %d entries", checkpointMaxFiles)
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			entry, err := s.captureCheckpointEntry(path, info, storeObjects)
			if err != nil {
				return err
			}
			entriesByPath[path] = entry
			if entry.Kind == "file" {
				fileCount++
				totalBytes += entry.SizeBytes
				if totalBytes > checkpointMaxTotalBytes {
					return fmt.Errorf("checkpoint exceeds %d bytes of file content", checkpointMaxTotalBytes)
				}
			}
			return nil
		})
		if err != nil {
			return nil, 0, 0, err
		}
	}
	entries := make([]checkpointEntry, 0, len(entriesByPath))
	for _, entry := range entriesByPath {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, fileCount, totalBytes, nil
}

func (s *Server) captureCheckpointEntry(path string, info os.FileInfo, storeObject bool) (checkpointEntry, error) {
	owner := ownershipFromInfo(info)
	entry := checkpointEntry{Path: path, Mode: uint32(info.Mode().Perm()), UID: owner.UID, GID: owner.GID, OwnerValid: owner.Valid}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(path)
		if err != nil {
			return checkpointEntry{}, err
		}
		entry.Kind = "symlink"
		entry.LinkTarget = target
	case info.IsDir():
		entry.Kind = "directory"
	case info.Mode().IsRegular():
		if info.Size() > checkpointMaxFileBytes {
			return checkpointEntry{}, fmt.Errorf("file %s exceeds checkpoint per-file limit of %d bytes", path, checkpointMaxFileBytes)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return checkpointEntry{}, err
		}
		sum := sha256.Sum256(data)
		entry.Kind = "file"
		entry.SizeBytes = int64(len(data))
		entry.SHA256 = hex.EncodeToString(sum[:])
		if storeObject {
			if err := s.checkpointStoreObject(entry.SHA256, data); err != nil {
				return checkpointEntry{}, err
			}
		}
	default:
		return checkpointEntry{}, fmt.Errorf("checkpoint does not support special file %s (%s)", path, info.Mode())
	}
	return entry, nil
}

func (s *Server) checkpointStoreObject(hash string, data []byte) error {
	path, err := s.checkpointObjectPath(hash)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".object-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	keep := false
	defer func() {
		_ = tmp.Close()
		if !keep {
			_ = os.Remove(tmpPath)
		}
	}()
	_ = tmp.Chmod(0o600)
	gz, err := gzip.NewWriterLevel(tmp, gzip.BestSpeed)
	if err != nil {
		return err
	}
	if _, err := gz.Write(data); err != nil {
		_ = gz.Close()
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		if _, statErr := os.Stat(path); statErr == nil {
			keep = true
			return nil
		}
		return err
	}
	keep = true
	return nil
}

func (s *Server) checkpointReadObject(hash string) ([]byte, error) {
	path, err := s.checkpointObjectPath(hash)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	data, err := io.ReadAll(io.LimitReader(gz, checkpointMaxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > checkpointMaxFileBytes {
		return nil, errors.New("checkpoint object exceeds decompression limit")
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != hash {
		return nil, fmt.Errorf("checkpoint object integrity mismatch for %s", hash)
	}
	return data, nil
}

func (s *Server) checkpointObjectPath(hash string) (string, error) {
	if len(hash) != 64 {
		return "", errors.New("invalid checkpoint object hash")
	}
	if _, err := hex.DecodeString(hash); err != nil {
		return "", errors.New("invalid checkpoint object hash")
	}
	return filepath.Join(s.fileCheckpointRoot(), "objects", hash[:2], hash[2:]+".gz"), nil
}

func (s *Server) checkpointManifestPath(id string) string {
	return filepath.Join(s.fileCheckpointRoot(), "checkpoints", id+".json")
}

func (s *Server) checkpointLoad(id string) (checkpointManifest, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return checkpointManifest{}, errors.New("checkpoint_id is required")
	}
	if strings.ContainsAny(id, `/\\`) || strings.Contains(id, "..") {
		return checkpointManifest{}, errors.New("invalid checkpoint_id")
	}
	data, err := os.ReadFile(s.checkpointManifestPath(id))
	if err != nil {
		return checkpointManifest{}, err
	}
	var manifest checkpointManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return checkpointManifest{}, err
	}
	if manifest.Version != checkpointManifestVersion || manifest.CheckpointID != id {
		return checkpointManifest{}, errors.New("invalid checkpoint manifest")
	}
	return manifest, nil
}

func (s *Server) checkpointList(limit int) ([]checkpointManifest, error) {
	dir := filepath.Join(s.fileCheckpointRoot(), "checkpoints")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []checkpointManifest{}, nil
	}
	if err != nil {
		return nil, err
	}
	var out []checkpointManifest
	for _, d := range entries {
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, d.Name()))
		if err != nil {
			continue
		}
		var manifest checkpointManifest
		if json.Unmarshal(data, &manifest) == nil && manifest.Version == checkpointManifestVersion {
			out = append(out, manifest)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *Server) checkpointDiffLive(manifest checkpointManifest, limit int) (checkpointDiff, error) {
	live, _, _, err := s.captureCheckpointEntries(manifest.Paths, false, true)
	if err != nil {
		return checkpointDiff{}, err
	}
	before := map[string]checkpointEntry{}
	after := map[string]checkpointEntry{}
	for _, e := range manifest.Entries {
		before[e.Path] = e
	}
	for _, e := range live {
		after[e.Path] = e
	}
	var diff checkpointDiff
	for path, cur := range after {
		old, ok := before[path]
		if !ok {
			diff.Added = append(diff.Added, path)
		} else if checkpointEntryChanged(old, cur) {
			diff.Modified = append(diff.Modified, path)
		}
	}
	for path := range before {
		if _, ok := after[path]; !ok {
			diff.Deleted = append(diff.Deleted, path)
		}
	}
	sort.Strings(diff.Added)
	sort.Strings(diff.Modified)
	sort.Strings(diff.Deleted)
	if limit > 0 {
		total := len(diff.Added) + len(diff.Modified) + len(diff.Deleted)
		if total > limit {
			diff.Truncated = true
		}
		diff.Added, diff.Modified, diff.Deleted = truncateDiffLists(diff.Added, diff.Modified, diff.Deleted, limit)
	}
	return diff, nil
}

func checkpointEntryChanged(a, b checkpointEntry) bool {
	return a.Kind != b.Kind || a.Mode != b.Mode || a.UID != b.UID || a.GID != b.GID || a.OwnerValid != b.OwnerValid || a.SizeBytes != b.SizeBytes || a.SHA256 != b.SHA256 || a.LinkTarget != b.LinkTarget
}

func truncateDiffLists(a, m, d []string, limit int) ([]string, []string, []string) {
	if limit <= 0 {
		return a, m, d
	}
	remaining := limit
	cut := func(in []string) []string {
		if remaining <= 0 {
			return nil
		}
		if len(in) > remaining {
			in = in[:remaining]
		}
		remaining -= len(in)
		return in
	}
	return cut(a), cut(m), cut(d)
}

func formatCheckpointDiff(id string, diff checkpointDiff) string {
	return fmt.Sprintf("checkpoint %s vs live: +%d ~%d -%d%s", id, len(diff.Added), len(diff.Modified), len(diff.Deleted), map[bool]string{true: " (truncated)", false: ""}[diff.Truncated])
}

func (s *Server) checkpointRestore(manifest checkpointManifest) error {
	desired := map[string]checkpointEntry{}
	for _, e := range manifest.Entries {
		desired[e.Path] = e
	}

	// Remove state that is absent from the checkpoint, deepest paths first.
	var removals []string
	for _, root := range manifest.Paths {
		rootEntry, wantsRoot := desired[root]
		info, err := os.Lstat(root)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !wantsRoot {
			removals = append(removals, root)
			continue
		}
		if info.IsDir() && rootEntry.Kind == "directory" {
			err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if _, ok := desired[path]; !ok {
					removals = append(removals, path)
				}
				return nil
			})
			if err != nil {
				return err
			}
		} else if currentKind(info) != rootEntry.Kind {
			removals = append(removals, root)
		}
	}
	sort.Slice(removals, func(i, j int) bool { return pathDepth(removals[i]) > pathDepth(removals[j]) })
	seen := map[string]bool{}
	for _, path := range removals {
		if seen[path] {
			continue
		}
		seen[path] = true
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.IsDir() {
			if err := os.RemoveAll(path); err != nil {
				return err
			}
		} else {
			if err := os.Remove(path); err != nil {
				return err
			}
		}
	}

	entries := append([]checkpointEntry(nil), manifest.Entries...)
	sort.Slice(entries, func(i, j int) bool {
		di, dj := pathDepth(entries[i].Path), pathDepth(entries[j].Path)
		if entries[i].Kind == "directory" && entries[j].Kind != "directory" {
			return true
		}
		if entries[i].Kind != "directory" && entries[j].Kind == "directory" {
			return false
		}
		if di == dj {
			return entries[i].Path < entries[j].Path
		}
		return di < dj
	})
	for _, e := range entries {
		switch e.Kind {
		case "directory":
			if info, err := os.Lstat(e.Path); err == nil && !info.IsDir() {
				if err := os.Remove(e.Path); err != nil {
					return err
				}
			}
			if err := os.MkdirAll(e.Path, os.FileMode(e.Mode)); err != nil {
				return err
			}
			if err := os.Chmod(e.Path, os.FileMode(e.Mode)); err != nil {
				return err
			}
			if err := applyPathOwnership(e.Path, fileOwnership{UID: e.UID, GID: e.GID, Valid: e.OwnerValid}, false); err != nil {
				return err
			}
		case "file":
			if err := ensureNonDirectoryTarget(e.Path); err != nil {
				return err
			}
			data, err := s.checkpointReadObject(e.SHA256)
			if err != nil {
				return err
			}
			if err := atomicWriteFile(e.Path, data, os.FileMode(e.Mode)); err != nil {
				return err
			}
			if err := applyPathOwnership(e.Path, fileOwnership{UID: e.UID, GID: e.GID, Valid: e.OwnerValid}, false); err != nil {
				return err
			}
		case "symlink":
			if err := ensureNonDirectoryTarget(e.Path); err != nil {
				return err
			}
			if err := os.Symlink(e.LinkTarget, e.Path); err != nil {
				return err
			}
			if err := applyPathOwnership(e.Path, fileOwnership{UID: e.UID, GID: e.GID, Valid: e.OwnerValid}, true); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported checkpoint entry kind %q", e.Kind)
		}
	}
	return nil
}

func ensureNonDirectoryTarget(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return os.MkdirAll(filepath.Dir(path), 0o755)
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	} else {
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return os.MkdirAll(filepath.Dir(path), 0o755)
}

func currentKind(info os.FileInfo) string {
	if info.Mode()&os.ModeSymlink != 0 {
		return "symlink"
	}
	if info.IsDir() {
		return "directory"
	}
	if info.Mode().IsRegular() {
		return "file"
	}
	return "special"
}
func pathDepth(path string) int { return strings.Count(filepath.Clean(path), string(os.PathSeparator)) }

func (s *Server) checkpointCleanup(maxAgeDays *int) (int, error) {
	items, err := s.checkpointList(0)
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	removed := 0
	for _, m := range items {
		remove := false
		if m.ExpiresAt != "" {
			if t, err := time.Parse(time.RFC3339Nano, m.ExpiresAt); err == nil && !t.After(now) {
				remove = true
			}
		}
		if maxAgeDays != nil && *maxAgeDays >= 0 {
			if t, err := time.Parse(time.RFC3339Nano, m.CreatedAt); err == nil && now.Sub(t) >= time.Duration(*maxAgeDays)*24*time.Hour {
				remove = true
			}
		}
		if remove {
			if err := os.Remove(s.checkpointManifestPath(m.CheckpointID)); err == nil || errors.Is(err, os.ErrNotExist) {
				removed++
			} else {
				return removed, err
			}
		}
	}
	return removed, nil
}

func (s *Server) checkpointGC() (int, int64, error) {
	items, err := s.checkpointList(0)
	if err != nil {
		return 0, 0, err
	}
	live := map[string]bool{}
	for _, m := range items {
		for _, e := range m.Entries {
			if e.SHA256 != "" {
				live[e.SHA256] = true
			}
		}
	}
	root := filepath.Join(s.fileCheckpointRoot(), "objects")
	removed := 0
	var bytes int64
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		name := strings.TrimSuffix(d.Name(), ".gz")
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) != 2 {
			return nil
		}
		hash := parts[0] + name
		if !live[hash] {
			if info, err := d.Info(); err == nil {
				bytes += info.Size()
			}
			if err := os.Remove(path); err == nil {
				removed++
			}
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	return removed, bytes, err
}

func writeJSONAtomic(path string, value any, mode os.FileMode) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return atomicWriteFile(path, data, mode)
}

func randomHex(bytesN int) string {
	b := make([]byte, bytesN)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
