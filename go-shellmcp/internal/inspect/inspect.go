// Package inspect provides bounded, read-only host inspection without
// exposing an arbitrary command interpreter.
package inspect

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/redact"
)

const (
	defaultMaxBytes = 64 * 1024
	maximumMaxBytes = 1024 * 1024
	maximumEntries  = 500
)

// Request describes one typed read-only inspection operation.
type Request struct {
	Action       string   `json:"action"`
	Path         string   `json:"path,omitempty"`
	MaxBytes     int64    `json:"max_bytes,omitempty"`
	AllowedRoots []string `json:"-"`
}

// Entry is bounded file metadata returned by list_directory.
type Entry struct {
	Name    string    `json:"name"`
	Type    string    `json:"type"`
	Size    int64     `json:"size"`
	Mode    string    `json:"mode"`
	ModTime time.Time `json:"modified_at"`
}

// Result contains only typed inspection data and redacted text.
type Result struct {
	Action    string  `json:"action"`
	Path      string  `json:"path"`
	Content   string  `json:"content,omitempty"`
	Entries   []Entry `json:"entries,omitempty"`
	Truncated bool    `json:"truncated,omitempty"`
}

// Run executes a supported inspection operation. Unsupported operations fail
// closed instead of falling back to a shell.
func Run(req Request) (Result, error) {
	switch req.Action {
	case "read_file":
		return readFile(req)
	case "list_directory":
		return listDirectory(req)
	default:
		return Result{}, fmt.Errorf("unsupported read-only inspection action %q", req.Action)
	}
}

func readFile(req Request) (Result, error) {
	if req.Path == "" {
		return Result{}, errors.New("read_file requires path")
	}
	limit := req.MaxBytes
	if limit <= 0 {
		limit = defaultMaxBytes
	}
	if limit > maximumMaxBytes {
		limit = maximumMaxBytes
	}
	resolved, err := resolveAllowedPath(req.Path, req.AllowedRoots)
	if err != nil {
		return Result{}, err
	}
	f, err := os.Open(resolved)
	if err != nil {
		return Result{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return Result{}, err
	}
	if !info.Mode().IsRegular() {
		return Result{}, fmt.Errorf("read_file requires a regular file, got %s", info.Mode().Type())
	}
	content, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return Result{}, err
	}
	truncated := int64(len(content)) > limit
	if truncated {
		content = content[:limit]
	}
	return Result{Action: req.Action, Path: redact.Secrets(filepath.Clean(req.Path)), Content: redact.Secrets(string(content)), Truncated: truncated}, nil
}

func listDirectory(req Request) (Result, error) {
	if req.Path == "" {
		return Result{}, errors.New("list_directory requires path")
	}
	resolved, err := resolveAllowedPath(req.Path, req.AllowedRoots)
	if err != nil {
		return Result{}, err
	}
	items, err := os.ReadDir(resolved)
	if err != nil {
		return Result{}, err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name() < items[j].Name() })
	truncated := len(items) > maximumEntries
	if truncated {
		items = items[:maximumEntries]
	}
	entries := make([]Entry, 0, len(items))
	for _, item := range items {
		info, infoErr := item.Info()
		if infoErr != nil {
			return Result{}, infoErr
		}
		kind := "file"
		if item.IsDir() {
			kind = "directory"
		} else if item.Type()&os.ModeSymlink != 0 {
			kind = "symlink"
		}
		entries = append(entries, Entry{Name: redact.Secrets(item.Name()), Type: kind, Size: info.Size(), Mode: info.Mode().String(), ModTime: info.ModTime()})
	}
	return Result{Action: req.Action, Path: redact.Secrets(filepath.Clean(req.Path)), Entries: entries, Truncated: truncated}, nil
}

var deniedCredentialDirectories = map[string]bool{
	".aws": true, ".azure": true, ".docker": true, ".gnupg": true, ".kube": true,
	".password-store": true, ".ssh": true, "credentials": true,
}

func resolveAllowedPath(path string, roots []string) (string, error) {
	if len(roots) == 0 {
		return "", errors.New("read-only inspection has no allowed roots configured")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	for _, part := range strings.FieldsFunc(strings.ToLower(filepath.Clean(resolved)), func(r rune) bool { return r == '/' || r == '\\' }) {
		if deniedCredentialDirectories[part] {
			return "", fmt.Errorf("read-only inspection denies credential directory %q", part)
		}
	}
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		resolvedRoot, rootErr := filepath.EvalSymlinks(root)
		if rootErr != nil {
			continue
		}
		resolvedRoot, rootErr = filepath.Abs(resolvedRoot)
		if rootErr != nil {
			continue
		}
		rel, relErr := filepath.Rel(resolvedRoot, resolved)
		if relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel) {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("path %q is outside configured read-only inspection roots", path)
}
