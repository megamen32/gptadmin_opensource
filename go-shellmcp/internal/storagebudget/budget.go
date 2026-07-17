package storagebudget

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
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
	limit, err := FilesystemLimit(root)
	if err != nil {
		return Result{}, err
	}
	return EnforceLimit(root, limit, protected)
}

func EnforceLimit(root string, limit int64, protected map[string]bool) (Result, error) {
	result := Result{LimitBytes: limit}
	if limit < 0 {
		limit = 0
		result.LimitBytes = 0
	}
	var files []fileEntry
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
		abs, _ := filepath.Abs(path)
		files = append(files, fileEntry{path: abs, size: info.Size(), mod: info.ModTime().UnixNano()})
		result.RemainingBytes += info.Size()
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].mod == files[j].mod {
			return files[i].path < files[j].path
		}
		return files[i].mod < files[j].mod
	})
	normalizedProtected := map[string]bool{}
	for p, yes := range protected {
		if yes {
			a, _ := filepath.Abs(p)
			normalizedProtected[a] = true
		}
	}
	for _, f := range files {
		if result.RemainingBytes <= limit {
			break
		}
		if normalizedProtected[f.path] {
			continue
		}
		if err := os.Remove(f.path); err != nil {
			continue
		}
		result.RemovedFiles++
		result.RemovedBytes += f.size
		result.RemainingBytes -= f.size
	}
	removeEmptyDirs(root)
	return result, nil
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
