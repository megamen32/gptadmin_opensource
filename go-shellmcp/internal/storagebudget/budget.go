package storagebudget

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const maxBudgetBytes int64 = 500 << 20

type Result struct {
	LimitBytes     int64
	RemovedFiles   int
	RemovedBytes   int64
	RemainingBytes int64
}

type fileEntry struct {
	path string
	size int64
	mod  int64
}

func LimitForCapacity(capacity int64) int64 {
	if capacity <= 0 {
		return maxBudgetBytes
	}
	fivePercent := capacity / 20
	if fivePercent < maxBudgetBytes {
		return fivePercent
	}
	return maxBudgetBytes
}

func Enforce(root string, protected map[string]bool) (Result, error) {
	return EnforceRoots([]string{root}, protected)
}

// EnforceRoots applies one filesystem-derived allowance across every supplied
// ShellMCP-owned root. The smallest underlying-filesystem allowance wins so a
// split audit/spool layout cannot consume a full allowance on each path.
func EnforceRoots(roots []string, protected map[string]bool) (Result, error) {
	normalized := normalizeRoots(roots)
	limit := maxBudgetBytes
	for _, root := range normalized {
		rootLimit, err := FilesystemLimit(root)
		if err != nil {
			return Result{}, err
		}
		if rootLimit < limit {
			limit = rootLimit
		}
	}
	return EnforceRootsLimit(normalized, limit, protected)
}

func EnforceLimit(root string, limit int64, protected map[string]bool) (Result, error) {
	return EnforceRootsLimit([]string{root}, limit, protected)
}

// EnforceRootsLimit is the deterministic form used by tests and callers that
// already resolved a capacity limit. Nested roots are collapsed before the
// walk so an outbox inside a spool directory is counted exactly once.
func EnforceRootsLimit(roots []string, limit int64, protected map[string]bool) (Result, error) {
	result := Result{LimitBytes: limit}
	if limit < 0 {
		limit = 0
		result.LimitBytes = 0
	}
	normalizedRoots := normalizeRoots(roots)
	filesByPath := make(map[string]fileEntry)
	for _, root := range normalizedRoots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				if errors.Is(walkErr, os.ErrNotExist) {
					return nil
				}
				return walkErr
			}
			if d.IsDir() {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			abs, err := filepath.Abs(path)
			if err != nil {
				return nil
			}
			filesByPath[abs] = fileEntry{path: abs, size: info.Size(), mod: info.ModTime().UnixNano()}
			return nil
		})
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return result, err
		}
	}
	files := make([]fileEntry, 0, len(filesByPath))
	for _, entry := range filesByPath {
		files = append(files, entry)
		result.RemainingBytes += entry.size
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].mod == files[j].mod {
			return files[i].path < files[j].path
		}
		return files[i].mod < files[j].mod
	})
	normalizedProtected := map[string]bool{}
	for path, yes := range protected {
		if !yes {
			continue
		}
		if abs, err := filepath.Abs(path); err == nil {
			normalizedProtected[abs] = true
		}
	}
	for _, entry := range files {
		if result.RemainingBytes <= limit {
			break
		}
		if normalizedProtected[entry.path] {
			continue
		}
		if err := os.Remove(entry.path); err != nil {
			continue
		}
		result.RemovedFiles++
		result.RemovedBytes += entry.size
		result.RemainingBytes -= entry.size
	}
	for _, root := range normalizedRoots {
		removeEmptyDirs(root)
	}
	return result, nil
}

func normalizeRoots(roots []string) []string {
	unique := make(map[string]bool, len(roots))
	for _, root := range roots {
		if root == "" {
			continue
		}
		abs, err := filepath.Abs(root)
		if err == nil {
			unique[filepath.Clean(abs)] = true
		}
	}
	normalized := make([]string, 0, len(unique))
	for root := range unique {
		normalized = append(normalized, root)
	}
	sort.Slice(normalized, func(i, j int) bool {
		if len(normalized[i]) == len(normalized[j]) {
			return normalized[i] < normalized[j]
		}
		return len(normalized[i]) < len(normalized[j])
	})
	outer := normalized[:0]
	for _, candidate := range normalized {
		nested := false
		for _, root := range outer {
			if candidate == root || strings.HasPrefix(candidate, root+string(os.PathSeparator)) {
				nested = true
				break
			}
		}
		if !nested {
			outer = append(outer, candidate)
		}
	}
	return outer
}

func removeEmptyDirs(root string) {
	var dirs []string
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && path != root {
			dirs = append(dirs, path)
		}
		return nil
	})
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, dir := range dirs {
		_ = os.Remove(dir)
	}
}
