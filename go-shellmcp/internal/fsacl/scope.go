// Package fsacl opens a user path relative to an explicitly authorized
// directory. Root methods keep path resolution and the resulting I/O in one
// operation, so a symlink swap cannot escape the selected tree.
package fsacl

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Policy struct {
	ReadRoots             *[]string
	WriteRoots            *[]string
	RequireCanonicalRoots bool
}

func (p Policy) Active() bool { return p.ReadRoots != nil || p.WriteRoots != nil }

// Open maps an absolute caller path beneath one configured root and opens the
// root-relative name. A missing side is a denial, even if the other side was
// explicitly configured.
func (p Policy) Open(path string, write bool) (*os.Root, string, error) {
	roots := p.ReadRoots
	if write {
		roots = p.WriteRoots
	}
	if roots == nil {
		return nil, "", errors.New("filesystem ACL denies this access class")
	}
	if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
		return nil, "", errors.New("filesystem ACL requires an absolute path")
	}
	cleanPath := filepath.Clean(path)
	for _, configured := range *roots {
		if strings.TrimSpace(configured) == "" {
			continue
		}
		cleanRoot := filepath.Clean(configured)
		if !filepath.IsAbs(cleanRoot) {
			if p.RequireCanonicalRoots {
				continue
			}
			absoluteRoot, absErr := filepath.Abs(cleanRoot)
			if absErr != nil {
				continue
			}
			cleanRoot = absoluteRoot
		}
		rel, err := filepath.Rel(cleanRoot, cleanPath)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
			continue
		}
		resolvedRoot, err := filepath.EvalSymlinks(cleanRoot)
		if err != nil || (p.RequireCanonicalRoots && resolvedRoot != cleanRoot) {
			return nil, "", errors.New("filesystem ACL root must be an existing canonical path without symlink aliases")
		}
		before, err := os.Lstat(resolvedRoot)
		if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.IsDir() {
			return nil, "", errors.New("filesystem ACL root must be a real directory")
		}
		root, err := os.OpenRoot(resolvedRoot)
		if err != nil {
			return nil, "", fmt.Errorf("open authorized root: %w", err)
		}
		resolvedAfter, resolveErr := filepath.EvalSymlinks(cleanRoot)
		after, statErr := root.Stat(".")
		if resolveErr != nil || resolvedAfter != resolvedRoot || statErr != nil || !os.SameFile(before, after) {
			_ = root.Close()
			return nil, "", errors.New("filesystem ACL root changed while it was opened")
		}
		if rel == "." {
			return root, rel, nil
		}
		return root, rel, nil
	}
	return nil, "", errors.New("path is outside configured filesystem ACL roots")
}

// RejectSymlinkComponents refuses symlink traversal inside a root. Root.Open
// still supplies the race-safe containment boundary if a component changes
// after this usability check.
func RejectSymlinkComponents(root *os.Root, relative string) error {
	if root == nil || relative == "." {
		return nil
	}
	current := ""
	for _, part := range strings.Split(filepath.Clean(relative), string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := root.Lstat(current)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("filesystem ACL denies symlink path component %q", current)
		}
	}
	return nil
}
