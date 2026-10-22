package server

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestRawFileViewDigest(t *testing.T) {
	for _, text := range []string{"", "alpha\r\nbeta\r\n", "Привет\nend"} {
		dir := t.TempDir()
		path := filepath.Join(dir, "sample.txt")
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		s := New(Config{DefaultHome: dir})
		result, err := s.mcpFileEditor(map[string]any{"action": "view", "path": path, "raw": true})
		if err != nil {
			t.Fatal(err)
		}
		data := result["structuredContent"].(map[string]any)
		digest := sha256.Sum256([]byte(text))
		if data["source_sha256"] != fmt.Sprintf("%x", digest) {
			t.Fatal("source digest differs from exact file bytes")
		}
	}
}
