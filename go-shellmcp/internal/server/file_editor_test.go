package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func editorTextResult(t *testing.T, result map[string]any) string {
	t.Helper()
	content, ok := result["content"].([]map[string]any)
	if !ok || len(content) == 0 {
		t.Fatalf("missing MCP text content: %#v", result)
	}
	text, _ := content[0]["text"].(string)
	return text
}

func TestFileEditorReplaceReturnsFreshIDs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.conf")
	if err := os.WriteFile(path, []byte("alpha\nport=80\nomega\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	s := New(Config{DefaultHome: dir})

	result, err := s.mcpFileEditor(map[string]any{"action": "str_replace", "path": path, "old_text": "port=80", "new_text": "port=8080"})
	if err != nil {
		t.Fatal(err)
	}
	text := editorTextResult(t, result)
	if !strings.Contains(text, "+2:") || !strings.Contains(text, "|port=8080") {
		t.Fatalf("successful edit did not return fresh id/diff: %s", text)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "alpha\nport=8080\nomega\n" {
		t.Fatalf("content=%q", got)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
}

func TestFileEditorNoMatchIsSelfHealingAndDoesNotWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	original := "one\nlisten 8080;\nthree\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	s := New(Config{DefaultHome: dir})

	_, err := s.mcpFileEditor(map[string]any{"action": "str_replace", "path": path, "old_text": "listen 80;", "new_text": "listen 443;"})
	if err == nil {
		t.Fatal("expected no-match error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "nothing was changed") || !strings.Contains(msg, "listen 8080;") || !strings.Contains(msg, "2:") || !strings.Contains(msg, "no separate read is required") {
		t.Fatalf("error is not self-healing: %s", msg)
	}
	got, _ := os.ReadFile(path)
	if string(got) != original {
		t.Fatalf("file changed: %q", got)
	}
}

func TestFileEditorMultipleMatchReturnsTargetsWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	original := "x=1\nkeep\nx=1\n"
	_ = os.WriteFile(path, []byte(original), 0o644)
	s := New(Config{DefaultHome: dir})
	_, err := s.mcpFileEditor(map[string]any{"action": "str_replace", "path": path, "old_text": "x=1", "new_text": "x=2"})
	if err == nil {
		t.Fatal("expected ambiguity")
	}
	if !strings.Contains(err.Error(), "found 2 exact matches") || !strings.Contains(err.Error(), "1:") || !strings.Contains(err.Error(), "3:") {
		t.Fatalf("missing actionable candidates: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != original {
		t.Fatal("ambiguous replace wrote file")
	}
}

func TestFileEditorBatchRejectsStaleIDWithFreshContextAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	original := "alpha\nbeta\ngamma\n"
	_ = os.WriteFile(path, []byte(original), 0o644)
	s := New(Config{DefaultHome: dir})
	stale := "2:" + lineHash("old-beta")
	_, err := s.mcpFileEditor(map[string]any{"action": "batch_edit", "path": path, "operations": []any{
		map[string]any{"action": "replace", "start": stale, "content": "BETA"},
		map[string]any{"action": "replace", "start": lineID(3, "gamma"), "content": "GAMMA"},
	}})
	if err == nil {
		t.Fatal("expected stale target")
	}
	if !strings.Contains(err.Error(), "no operations were applied") || !strings.Contains(err.Error(), "beta") || !strings.Contains(err.Error(), "Retry with these fresh ids") {
		t.Fatalf("stale diagnostic=%v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != original {
		t.Fatalf("batch was not atomic: %q", got)
	}
}

func TestFileEditorBatchChainsWithFreshIDs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	_ = os.WriteFile(path, []byte("alpha\nbeta\ngamma\n"), 0o644)
	s := New(Config{DefaultHome: dir})
	result, err := s.mcpFileEditor(map[string]any{"action": "batch_edit", "path": path, "operations": []any{
		map[string]any{"action": "replace", "start": lineID(2, "beta"), "content": "BETA"},
		map[string]any{"action": "insert", "start": lineID(3, "gamma"), "content": "delta"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	text := editorTextResult(t, result)
	if !strings.Contains(text, "|BETA") || !strings.Contains(text, "|delta") {
		t.Fatalf("diff lacks fresh result: %s", text)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "alpha\nBETA\ngamma\ndelta\n" {
		t.Fatalf("content=%q", got)
	}
}

func TestFileEditorPreservesCRLFAndRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	_ = os.WriteFile(path, []byte("a\r\nb\r\n"), 0o644)
	s := New(Config{DefaultHome: dir})
	if _, err := s.mcpFileEditor(map[string]any{"action": "str_replace", "path": path, "old_text": "b", "new_text": "B"}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "a\r\nB\r\n" {
		t.Fatalf("CRLF lost: %q", got)
	}
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := s.mcpFileEditor(map[string]any{"action": "str_replace", "path": link, "old_text": "B", "new_text": "C"}); err == nil || !strings.Contains(err.Error(), "refuses symlinks") {
		t.Fatalf("symlink edit err=%v", err)
	}
}

func TestFileEditorLineIDContract(t *testing.T) {
	if got := lineID(2, "beta"); got != "2:4b46" {
		t.Fatalf("lineID contract=%q want 2:4b46", got)
	}
}

func TestFileEditorViewReturnsReusableLineIDs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\ngamma\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	s := New(Config{DefaultHome: dir})
	result, err := s.mcpFileEditor(map[string]any{"action": "view", "path": path, "start_line": 2, "end_line": 3})
	if err != nil {
		t.Fatal(err)
	}
	text := editorTextResult(t, result)
	if !strings.Contains(text, lineID(2, "beta")+"|beta") || !strings.Contains(text, lineID(3, "gamma")+"|gamma") {
		t.Fatalf("view did not return reusable ids: %s", text)
	}
}

func TestFileEditorViewReturnsBoundedFreshIDs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\ngamma\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	s := New(Config{DefaultHome: dir})
	result, err := s.mcpFileEditor(map[string]any{"action": "view", "path": path, "start_line": 2, "end_line": 3})
	if err != nil {
		t.Fatal(err)
	}
	text := editorTextResult(t, result)
	if !strings.Contains(text, "2:4b46|beta") || !strings.Contains(text, "3:") || strings.Contains(text, "alpha") {
		t.Fatalf("unexpected view: %s", text)
	}
	structured := result["structuredContent"].(map[string]any)
	if structured["truncated"].(bool) {
		t.Fatalf("unexpected truncation: %#v", structured)
	}
}
