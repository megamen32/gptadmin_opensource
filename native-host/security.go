// Security helpers for path validation and binary file detection.
package main

import (
        "bytes"
        "errors"
        "fmt"
        "io"
        "os"
        "path/filepath"
        "strings"
)

// ValidatePath ensures that the given path does not escape the home directory
// via symlinks or ".." traversal. It resolves symlinks and checks that the
// final, resolved absolute path starts with homeDir.
func ValidatePath(path string, homeDir string) error {
        // Clean the path first to resolve any .. components.
        cleaned := filepath.Clean(path)

        // If the path is relative, make it absolute relative to the home directory
        // so we don't accidentally resolve to an unexpected location.
        if !filepath.IsAbs(cleaned) {
                cleaned = filepath.Join(homeDir, cleaned)
        }

        // Resolve symlinks to get the true path on disk.
        resolved, err := filepath.EvalSymlinks(cleaned)
        if err != nil {
                // If the file doesn't exist yet (e.g., for writes), EvalSymlinks will
                // fail. In that case, resolve the parent directory instead and check
                // that it's within homeDir.
                if os.IsNotExist(err) {
                        return validateParentPath(cleaned, homeDir)
                }
                return fmt.Errorf("resolving path %q: %w", path, err)
        }

        // Ensure both paths end with a separator so that /home/user2 is not
        // treated as a child of /home/user.
        homeDir = strings.TrimSuffix(homeDir, string(filepath.Separator))
        resolved = strings.TrimSuffix(resolved, string(filepath.Separator))

        if !strings.HasPrefix(resolved, homeDir+string(filepath.Separator)) && resolved != homeDir {
                return fmt.Errorf("path %q resolves outside home directory %q", path, homeDir)
        }

        return nil
}

// validateParentPath checks that the parent directory of a non-existent path
// is within homeDir. This is used when ValidatePath encounters a path that
// doesn't yet exist (common for file writes).
func validateParentPath(path string, homeDir string) error {
        parent := filepath.Dir(path)

        resolved, err := filepath.EvalSymlinks(parent)
        if err != nil {
                // If the parent also doesn't exist, walk up until we find one that does.
                for {
                        if os.IsNotExist(err) {
                                parent = filepath.Dir(parent)
                                if parent == "." || parent == string(filepath.Separator) {
                                        return fmt.Errorf("cannot resolve any ancestor of %q", path)
                                }
                                resolved, err = filepath.EvalSymlinks(parent)
                                        continue
                        }
                        return fmt.Errorf("resolving parent of %q: %w", path, err)
                }
        }

        homeDir = strings.TrimSuffix(homeDir, string(filepath.Separator))
        resolved = strings.TrimSuffix(resolved, string(filepath.Separator))

        if !strings.HasPrefix(resolved, homeDir+string(filepath.Separator)) && resolved != homeDir {
                return fmt.Errorf("path %q would be created outside home directory %q", path, homeDir)
        }

        return nil
}

// IsBinaryFile checks whether a file appears to be binary by reading its first
// 512 bytes and looking for NUL bytes. This is the same heuristic used by git.
func IsBinaryFile(path string) (bool, error) {
        f, err := os.Open(path)
        if err != nil {
                return false, fmt.Errorf("opening file %q: %w", path, err)
        }
        defer f.Close()

        buf := make([]byte, 512)
        n, err := f.Read(buf)
        if err != nil && !errors.Is(err, io.EOF) {
                return false, fmt.Errorf("reading file %q: %w", path, err)
        }

        // A NUL byte anywhere in the first 512 bytes is a strong indicator
        // that the file is binary.
        return bytes.IndexByte(buf[:n], 0) >= 0, nil
}
