// Filesystem operations: list, read, write.
//
// All file access is restricted to the user's home directory by default
// via the security.go path validation.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// FileEntry represents a single file or directory in a listing.
type FileEntry struct {
	Name     string `json:"name"`
	Type     string `json:"type"`     // "file" or "dir"
	Size     int64  `json:"size"`     // bytes; 0 for directories
	Modified string `json:"modified"` // RFC 3339 timestamp
}

// filesListParams is the expected JSON params for files.list.
type filesListParams struct {
	Path       string `json:"path"`
	ShowHidden bool   `json:"show_hidden"`
}

// filesReadResult is returned by files.read.
type filesReadResult struct {
	Content  string `json:"content"`
	Encoding string `json:"encoding"` // "utf-8" or "base64"
}

// filesWriteParams is the expected JSON params for files.write.
type filesWriteParams struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// filesWriteResult is returned by files.write.
type filesWriteResult struct {
	BytesWritten int64  `json:"bytes_written"`
	Path         string `json:"path"`
}

// filesWatchResult is returned by files.watch (placeholder).
type filesWatchResult struct {
	Message string `json:"message"`
}

const maxReadSize = 10 * 1024 * 1024 // 10 MB

// handleFilesList lists directory contents.
func handleFilesList(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var p filesListParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	// Default to home directory if no path specified.
	if p.Path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolving home directory: %w", err)
		}
		p.Path = home
	}

	// Security: ensure path is within home directory.
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolving home directory: %w", err)
	}
	if err := ValidatePath(p.Path, home); err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(p.Path)
	if err != nil {
		return nil, fmt.Errorf("reading directory %q: %w", p.Path, err)
	}

	var result []FileEntry
	for _, e := range entries {
		name := e.Name()

		// Skip hidden files unless explicitly requested.
		if !p.ShowHidden && strings.HasPrefix(name, ".") {
			continue
		}

		info, err := e.Info()
		if err != nil {
			// Skip entries we can't stat rather than failing entirely.
			continue
		}

		ftype := "file"
		if e.IsDir() {
			ftype = "dir"
		}

		result = append(result, FileEntry{
			Name:     name,
			Type:     ftype,
			Size:     info.Size(),
			Modified: info.ModTime().Format(time.RFC3339),
		})
	}

	// Sort: directories first, then alphabetically by name.
	sort.Slice(result, func(i, j int) bool {
		if result[i].Type != result[j].Type {
			return result[i].Type == "dir" // dirs before files
		}
		return result[i].Name < result[j].Name
	})

	return result, nil
}

// handleFilesRead reads file content, returning it as UTF-8 or base64 for binary files.
func handleFilesRead(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolving home directory: %w", err)
	}
	if err := ValidatePath(p.Path, home); err != nil {
		return nil, err
	}

	// Check file size before reading.
	info, err := os.Stat(p.Path)
	if err != nil {
		return nil, fmt.Errorf("stat %q: %w", p.Path, err)
	}
	if info.Size() > maxReadSize {
		return nil, fmt.Errorf("file %q is %d bytes, exceeding %d-byte limit", p.Path, info.Size(), maxReadSize)
	}

	// Determine if the file is binary.
	isBinary, err := IsBinaryFile(p.Path)
	if err != nil {
		return nil, fmt.Errorf("detecting binary %q: %w", p.Path, err)
	}

	f, err := os.Open(p.Path)
	if err != nil {
		return nil, fmt.Errorf("opening file %q: %w", p.Path, err)
	}
	defer f.Close()

	// Read at most maxReadSize + 1 bytes to detect overflow.
	reader := io.LimitReader(f, int64(maxReadSize)+1)
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("reading file %q: %w", p.Path, err)
	}

	if isBinary {
		return filesReadResult{
			Content:  base64.StdEncoding.EncodeToString(data),
			Encoding: "base64",
		}, nil
	}

	return filesReadResult{
		Content:  string(data),
		Encoding: "utf-8",
	}, nil
}

// handleFilesWrite writes content to a file, creating parent directories as needed.
func handleFilesWrite(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var p filesWriteParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	if p.Path == "" {
		return nil, fmt.Errorf("path is required")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolving home directory: %w", err)
	}
	if err := ValidatePath(p.Path, home); err != nil {
		return nil, err
	}

	// Create parent directories if they don't exist.
	parent := filepath.Dir(p.Path)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return nil, fmt.Errorf("creating parent directories for %q: %w", p.Path, err)
	}

	content := []byte(p.Content)
	if err := os.WriteFile(p.Path, content, 0644); err != nil {
		return nil, fmt.Errorf("writing file %q: %w", p.Path, err)
	}

	return filesWriteResult{
		BytesWritten: int64(len(content)),
		Path:         p.Path,
	}, nil
}

// handleFilesWatch is a placeholder that returns a not-implemented error.
func handleFilesWatch(ctx context.Context, params json.RawMessage) (interface{}, error) {
	return filesWatchResult{
		Message: "files.watch is not yet implemented. Use polling as a workaround.",
	}, nil
}
