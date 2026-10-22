package server

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileEditorRawPreservesExactBytes(t *testing.T) {
	for _, text := range []string{"", "alpha\nbeta\n", "alpha\nbeta", "alpha\r\nbeta\r\n", "Привет\n末尾"} {
		t.Run(text, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "x")
			if err := os.WriteFile(path, []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			s := New(Config{DefaultHome: dir})
			r, err := s.mcpFileEditor(map[string]any{"action": "view", "path": path, "raw": true, "max_bytes": 4096})
			if err != nil {
				t.Fatal(err)
			}
			m := r["structuredContent"].(map[string]any)
			if m["text"] != text || m["text_exact"] != true || m["bytes"] != len(text) || m["file_bytes"] != len(text) || m["truncated"] != false {
				t.Fatalf("bad raw metadata %#v", m)
			}
			if editorTextResult(t, r) != text {
				t.Fatal("content and structured text differ")
			}
		})
	}
}
func TestFileEditorRawRangeAndBudget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x")
	os.WriteFile(path, []byte("alpha\r\nbeta\r\ngamma\n"), 0600)
	s := New(Config{DefaultHome: dir})
	r, err := s.mcpFileEditor(map[string]any{"action": "view", "path": path, "raw": true, "start_line": 2, "end_line": 2, "max_bytes": 4096})
	if err != nil {
		t.Fatal(err)
	}
	m := r["structuredContent"].(map[string]any)
	if m["text"] != "beta\r\n" || m["truncated"] != true || m["start_line"] != 2 || m["end_line"] != 2 {
		t.Fatalf("range mismatch %#v", m)
	}
	r, err = s.mcpFileEditor(map[string]any{"action": "view", "path": path, "raw": true, "max_bytes": 1})
	if err != nil {
		t.Fatal(err)
	}
	m = r["structuredContent"].(map[string]any)
	if m["text"] != "" || m["truncated"] != true || m["end_line"] != 0 {
		t.Fatalf("budget mismatch %#v", m)
	}
}
