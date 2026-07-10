package server

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const fileBackupManifestName = "manifest.jsonl"

type fileBackupArgs struct {
	Action     string `json:"action"`
	Path       string `json:"path"`
	BackupID   string `json:"backup_id"`
	TTLDays    *int   `json:"ttl_days"`
	Label      string `json:"label"`
	UseSudo    bool   `json:"use_sudo"`
	Overwrite  bool   `json:"overwrite"`
	Limit      int    `json:"limit"`
	MaxAgeDays *int   `json:"max_age_days"`
}

type fileBackupMeta struct {
	BackupID   string `json:"backup_id"`
	Path       string `json:"path"`
	BackupPath string `json:"backup_path"`
	Artifact   string `json:"artifact"`
	Kind       string `json:"kind"`
	Label      string `json:"label,omitempty"`
	Host       string `json:"host,omitempty"`
	UseSudo    bool   `json:"use_sudo,omitempty"`
	SizeBytes  int64  `json:"size_bytes,omitempty"`
	Mode       uint32 `json:"mode,omitempty"`
	CreatedAt  string `json:"created_at"`
	ExpiresAt  string `json:"expires_at,omitempty"`
	RestoredAt string `json:"restored_at,omitempty"`
}

func (s *Server) mcpFileBackup(args map[string]any) (map[string]any, error) {
	var req fileBackupArgs
	b, _ := json.Marshal(args)
	if err := json.Unmarshal(b, &req); err != nil {
		return nil, err
	}
	req.Action = strings.ToLower(strings.TrimSpace(req.Action))
	if req.Action == "" {
		req.Action = "backup"
	}
	switch req.Action {
	case "backup":
		meta, err := s.fileBackupCreate(req)
		if err != nil {
			return nil, err
		}
		return mcpText("file_backup created "+meta.BackupID, map[string]any{"ok": true, "action": "backup", "backup": meta, "backup_id": meta.BackupID, "artifact": meta.Artifact, "backup_path": meta.BackupPath}), nil
	case "list":
		items, err := s.fileBackupList(req.Limit)
		if err != nil {
			return nil, err
		}
		return mcpText(fmt.Sprintf("%d managed backup(s)", len(items)), map[string]any{"ok": true, "action": "list", "count": len(items), "backups": items, "root": s.fileBackupRoot()}), nil
	case "cleanup":
		removed, err := s.fileBackupCleanup(req)
		if err != nil {
			return nil, err
		}
		return mcpText(fmt.Sprintf("removed %d managed backup(s)", removed), map[string]any{"ok": true, "action": "cleanup", "removed": removed, "root": s.fileBackupRoot()}), nil
	case "restore":
		meta, err := s.fileBackupRestore(req)
		if err != nil {
			return nil, err
		}
		return mcpText("file_backup restored "+meta.BackupID, map[string]any{"ok": true, "action": "restore", "backup": meta, "backup_id": meta.BackupID, "path": meta.Path}), nil
	default:
		return nil, fmt.Errorf("unknown file_backup action %q", req.Action)
	}
}

func (s *Server) fileBackupRoot() string {
	if v := strings.TrimSpace(os.Getenv("SHELLMCP_FILE_BACKUP_ROOT")); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("GPTADMIN_FILE_BACKUP_ROOT")); v != "" {
		return v
	}
	home := strings.TrimSpace(s.cfg.DefaultHome)
	if home == "" {
		if u, err := user.Current(); err == nil && u.HomeDir != "" {
			home = u.HomeDir
		}
	}
	if home == "" {
		home = "/var/lib/gptadmin"
	}
	return filepath.Join(home, ".gptadmin", "file-backups")
}

func (s *Server) fileBackupCreate(req fileBackupArgs) (fileBackupMeta, error) {
	path := strings.TrimSpace(req.Path)
	if path == "" {
		return fileBackupMeta{}, errors.New("file_backup backup requires path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return fileBackupMeta{}, err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return fileBackupMeta{}, err
	}
	now := time.Now().UTC()
	ttl := 30
	if req.TTLDays != nil {
		ttl = *req.TTLDays
	}
	host := s.cfg.Name
	if host == "" {
		host, _ = os.Hostname()
	}
	id := now.Format("20060102_150405") + "_" + sanitizeBackupPart(host, 24) + "_" + shortHash(abs) + suffixLabel(req.Label)
	dir := filepath.Join(s.fileBackupRoot(), id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fileBackupMeta{}, err
	}
	kind := "file"
	artifact := filepath.Join(dir, "artifact")
	if info.IsDir() {
		kind = "directory"
		artifact += ".tar.gz"
		if req.UseSudo {
			return fileBackupMeta{}, errors.New("use_sudo directory backups are not supported yet")
		}
		if err := tarGzipDirectory(abs, artifact); err != nil {
			return fileBackupMeta{}, err
		}
	} else {
		artifact += filepath.Ext(abs)
		if err := copyFileForBackup(abs, artifact, req.UseSudo, info.Mode()); err != nil {
			return fileBackupMeta{}, err
		}
	}
	meta := fileBackupMeta{BackupID: id, Path: abs, BackupPath: dir, Artifact: artifact, Kind: kind, Label: req.Label, Host: host, UseSudo: req.UseSudo, SizeBytes: info.Size(), Mode: uint32(info.Mode().Perm()), CreatedAt: now.Format(time.RFC3339)}
	if ttl > 0 {
		meta.ExpiresAt = now.Add(time.Duration(ttl) * 24 * time.Hour).Format(time.RFC3339)
	}
	if err := writeBackupMeta(dir, meta); err != nil {
		return fileBackupMeta{}, err
	}
	_ = appendBackupManifest(s.fileBackupRoot(), map[string]any{"event": "backup", "backup": meta, "time": now.Format(time.RFC3339)})
	return meta, nil
}

func copyFileForBackup(src, dst string, useSudo bool, mode os.FileMode) error {
	if useSudo {
		cmd := exec.Command("sudo", "-n", "cat", src)
		out, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("sudo cat %s: %w", src, err)
		}
		if err := os.WriteFile(dst, out, mode.Perm()); err != nil {
			return err
		}
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode.Perm())
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func tarGzipDirectory(root, dst string) error {
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	gz := gzip.NewWriter(out)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()
	base := filepath.Dir(root)
	return filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(base, path)
		if err != nil {
			return err
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			link, _ = os.Readlink(path)
		}
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			_, err = io.Copy(tw, f)
			_ = f.Close()
			return err
		}
		return nil
	})
}

func (s *Server) fileBackupList(limit int) ([]fileBackupMeta, error) {
	root := s.fileBackupRoot()
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	items := make([]fileBackupMeta, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		meta, err := readBackupMeta(filepath.Join(root, e.Name()))
		if err == nil {
			items = append(items, meta)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt > items[j].CreatedAt })
	if limit > 0 && limit < len(items) {
		items = items[:limit]
	}
	return items, nil
}

func (s *Server) fileBackupCleanup(req fileBackupArgs) (int, error) {
	items, err := s.fileBackupList(0)
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	removed := 0
	for _, meta := range items {
		remove := false
		if meta.ExpiresAt != "" {
			if exp, err := time.Parse(time.RFC3339, meta.ExpiresAt); err == nil && !exp.After(now) {
				remove = true
			}
		}
		if req.MaxAgeDays != nil && *req.MaxAgeDays >= 0 {
			if created, err := time.Parse(time.RFC3339, meta.CreatedAt); err == nil && created.Before(now.Add(-time.Duration(*req.MaxAgeDays)*24*time.Hour)) {
				remove = true
			}
		}
		if remove {
			if err := os.RemoveAll(meta.BackupPath); err != nil {
				return removed, err
			}
			removed++
			_ = appendBackupManifest(s.fileBackupRoot(), map[string]any{"event": "cleanup", "backup_id": meta.BackupID, "time": now.Format(time.RFC3339)})
		}
	}
	return removed, nil
}

func (s *Server) fileBackupRestore(req fileBackupArgs) (fileBackupMeta, error) {
	id := strings.TrimSpace(req.BackupID)
	if id == "" {
		return fileBackupMeta{}, errors.New("file_backup restore requires backup_id")
	}
	meta, err := readBackupMeta(filepath.Join(s.fileBackupRoot(), id))
	if err != nil {
		return fileBackupMeta{}, err
	}
	if meta.Kind == "directory" {
		if req.UseSudo || meta.UseSudo {
			return fileBackupMeta{}, errors.New("sudo directory restore is not supported yet")
		}
		if !req.Overwrite {
			if _, err := os.Lstat(meta.Path); err == nil {
				return fileBackupMeta{}, errors.New("target exists; pass overwrite=true to restore")
			}
		}
		if req.Overwrite {
			if err := os.RemoveAll(meta.Path); err != nil {
				return fileBackupMeta{}, err
			}
		}
		if err := untarGzip(meta.Artifact, filepath.Dir(meta.Path)); err != nil {
			return fileBackupMeta{}, err
		}
	} else {
		if !req.Overwrite {
			if _, err := os.Lstat(meta.Path); err == nil {
				return fileBackupMeta{}, errors.New("target exists; pass overwrite=true to restore")
			}
		}
		if err := restoreFile(meta.Artifact, meta.Path, req.UseSudo || meta.UseSudo, os.FileMode(meta.Mode)); err != nil {
			return fileBackupMeta{}, err
		}
	}
	meta.RestoredAt = time.Now().UTC().Format(time.RFC3339)
	_ = writeBackupMeta(meta.BackupPath, meta)
	_ = appendBackupManifest(s.fileBackupRoot(), map[string]any{"event": "restore", "backup_id": meta.BackupID, "path": meta.Path, "time": meta.RestoredAt})
	return meta, nil
}

func restoreFile(src, dst string, useSudo bool, mode os.FileMode) error {
	if mode == 0 {
		mode = 0o600
	}
	if useSudo {
		data, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		cmd := exec.Command("sudo", "-n", "tee", dst)
		cmd.Stdin = strings.NewReader(string(data))
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("sudo tee %s: %w: %s", dst, err, strings.TrimSpace(string(out)))
		}
		_ = exec.Command("sudo", "-n", "chmod", strconv.FormatInt(int64(mode.Perm()), 8), dst).Run()
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode.Perm())
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func untarGzip(src, dest string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		clean := filepath.Clean(hdr.Name)
		if clean == "." || strings.HasPrefix(clean, "../") || filepath.IsAbs(clean) {
			return fmt.Errorf("unsafe tar path %q", hdr.Name)
		}
		target := filepath.Join(dest, clean)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode)); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(out, tr)
			closeErr := out.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		}
	}
}

func writeBackupMeta(dir string, meta fileBackupMeta) error {
	b, _ := json.MarshalIndent(meta, "", "  ")
	return os.WriteFile(filepath.Join(dir, "meta.json"), append(b, '\n'), 0o600)
}

func readBackupMeta(dir string) (fileBackupMeta, error) {
	var meta fileBackupMeta
	b, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return meta, err
	}
	err = json.Unmarshal(b, &meta)
	return meta, err
}

func appendBackupManifest(root string, event map[string]any) error {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(root, fileBackupManifestName), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	b, _ := json.Marshal(event)
	_, err = f.Write(append(b, '\n'))
	return err
}

func sanitizeBackupPart(v string, max int) string {
	v = strings.TrimSpace(v)
	if v == "" {
		v = "host"
	}
	var b strings.Builder
	for _, r := range v {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
		if max > 0 && b.Len() >= max {
			break
		}
	}
	return strings.Trim(b.String(), "._-")
}

func suffixLabel(label string) string {
	label = sanitizeBackupPart(label, 40)
	if label == "" || label == "host" {
		return ""
	}
	return "_" + label
}

func shortHash(v string) string {
	// FNV-1a keeps backup ids stable enough without pulling in long hashes.
	var h uint32 = 2166136261
	for i := 0; i < len(v); i++ {
		h ^= uint32(v[i])
		h *= 16777619
	}
	return fmt.Sprintf("%08x", h)
}
