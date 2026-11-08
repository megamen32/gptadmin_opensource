package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestFileTransferStatAndList(t *testing.T) {
	s, dir := transferTestServer(t)
	ctx := context.Background()

	data := []byte("hello stat\n")
	if err := os.WriteFile(filepath.Join(dir, "alpha.txt"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}

	stat := transferTestCall(t, s, ctx, map[string]any{"action": "stat", "path": filepath.Join(dir, "alpha.txt")})
	if stat["is_dir"] != false || stat["is_regular"] != true || stat["size"] != int64(len(data)) {
		t.Fatal(stat)
	}
	if _, ok := stat["sha256"]; ok {
		t.Fatal("hash must be opt-in")
	}
	statHash := transferTestCall(t, s, ctx, map[string]any{"action": "stat", "path": filepath.Join(dir, "alpha.txt"), "hash": true})
	if statHash["sha256"] != transferDigest(data) {
		t.Fatal(statHash)
	}
	statDir := transferTestCall(t, s, ctx, map[string]any{"action": "stat", "path": filepath.Join(dir, "sub")})
	if statDir["is_dir"] != true {
		t.Fatal(statDir)
	}
	if _, err := s.mcpFileTransfer(ctx, map[string]any{"action": "stat", "path": filepath.Join(dir, "missing.txt")}); err == nil {
		t.Fatal("stat of a missing file must fail")
	}

	list := transferTestCall(t, s, ctx, map[string]any{"action": "list", "path": dir})
	entries, _ := list["entries"].([]map[string]any)
	if list["count"] != len(entries) || len(entries) != 2 {
		t.Fatal(list)
	}
	if entries[0]["name"] != "alpha.txt" || entries[1]["name"] != "sub" {
		t.Fatal("entries must be sorted", entries)
	}
	if entries[0]["size"] != int64(len(data)) || entries[1]["is_dir"] != true {
		t.Fatal(entries)
	}

	filtered := transferTestCall(t, s, ctx, map[string]any{"action": "list", "path": dir, "pattern": "*.txt"})
	filteredEntries, _ := filtered["entries"].([]map[string]any)
	if len(filteredEntries) != 1 || filteredEntries[0]["name"] != "alpha.txt" {
		t.Fatal(filtered)
	}

	if _, err := s.mcpFileTransfer(ctx, map[string]any{"action": "list", "path": filepath.Join(dir, "alpha.txt")}); err == nil {
		t.Fatal("list of a file must fail")
	}
	if _, err := s.mcpFileTransfer(ctx, map[string]any{"action": "list", "path": dir, "pattern": "["}); err == nil {
		t.Fatal("invalid glob must fail")
	}
}

func TestFileTransferUploadReplaceAtomicCAS(t *testing.T) {
	s, dir := transferTestServer(t)
	ctx := context.Background()
	path := filepath.Join(dir, "cas.bin")

	original := []byte("original content")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}

	// A wrong expected digest must be rejected up front.
	if _, err := s.mcpFileTransfer(ctx, map[string]any{"action": "upload_replace", "path": path, "size": 4, "sha256": transferDigest([]byte("new!")), "expected_sha256": transferDigest([]byte("stale"))}); err == nil {
		t.Fatal("stale expected_sha256 must fail at begin")
	}

	replacement := []byte("replacement content with more bytes")
	begin := transferTestCall(t, s, ctx, map[string]any{"action": "upload_replace", "path": path, "size": len(replacement), "sha256": transferDigest(replacement), "expected_sha256": transferDigest(original)})
	id := begin["transfer_id"].(string)
	if begin["replace"] != true || begin["expected_sha256"] != transferDigest(original) {
		t.Fatal(begin)
	}

	// The old file stays intact and readable while the transfer is open.
	if disk, err := os.ReadFile(path); err != nil || !bytes.Equal(disk, original) {
		t.Fatal("destination must not change before commit", err)
	}

	transferTestChunk(t, s, path, id, 0, replacement)
	done := transferTestCall(t, s, ctx, map[string]any{"action": "upload_commit", "path": path, "transfer_id": id})
	if done["completed"] != true {
		t.Fatal(done)
	}
	if disk, err := os.ReadFile(path); err != nil || !bytes.Equal(disk, replacement) {
		t.Fatal("replaced content differs", err)
	}

	// A commit must refuse when the destination changes between begin and commit.
	newer := []byte("someone else wrote this")
	if err := os.WriteFile(path, newer, 0o600); err != nil {
		t.Fatal(err)
	}
	begin2 := transferTestCall(t, s, ctx, map[string]any{"action": "upload_replace", "path": path, "size": 3, "sha256": transferDigest([]byte("zzz")), "expected_sha256": transferDigest(newer)})
	id2 := begin2["transfer_id"].(string)
	transferTestChunk(t, s, path, id2, 0, []byte("zzz"))
	concurrent := []byte("changed after begin")
	if err := os.WriteFile(path, concurrent, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.mcpFileTransfer(ctx, map[string]any{"action": "upload_commit", "path": path, "transfer_id": id2}); err == nil {
		t.Fatal("commit must refuse when the destination changed since begin")
	}
	if disk, err := os.ReadFile(path); err != nil || !bytes.Equal(disk, concurrent) {
		t.Fatal("failed CAS commit must leave the current destination intact", err)
	}
	// A stale expected digest must be rejected up front at begin.
	if _, err := s.mcpFileTransfer(ctx, map[string]any{"action": "upload_replace", "path": path, "size": 4, "sha256": transferDigest([]byte("abcd")), "expected_sha256": transferDigest(replacement)}); err == nil {
		t.Fatal("stale expected_sha256 must fail at begin")
	}
}

func TestFileTransferUploadReplaceRejectsMissingDestination(t *testing.T) {
	s, dir := transferTestServer(t)
	path := filepath.Join(dir, "absent.bin")
	if _, err := s.mcpFileTransfer(context.Background(), map[string]any{"action": "upload_replace", "path": path, "size": 2, "sha256": transferDigest([]byte("ab")), "expected_sha256": transferDigest([]byte("cd"))}); err == nil {
		t.Fatal("upload_replace requires an existing destination")
	}
}

func TestFileTransferStatListRespectPatternAndLimits(t *testing.T) {
	s, dir := transferTestServer(t)
	ctx := context.Background()
	for _, name := range []string{"a.go", "b.go", "c.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	list := transferTestCall(t, s, ctx, map[string]any{"action": "list", "path": dir, "pattern": "*.go"})
	entries, _ := list["entries"].([]map[string]any)
	if len(entries) != 2 {
		t.Fatal(list)
	}
	for _, entry := range entries {
		name := entry["name"].(string)
		matched, err := filepath.Match("*.go", name)
		if err != nil || !matched {
			t.Fatal(name, err)
		}
	}
}

func TestFileTransferChunkedReplaceRoundtrip(t *testing.T) {
	s, dir := transferTestServer(t)
	ctx := context.Background()
	path := filepath.Join(dir, "chunked.bin")
	original := bytes.Repeat([]byte("0123456789"), 4000) // 40000 bytes, 2 chunks
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	replacement := bytes.Repeat([]byte{0xff, 0x1b, 0x5b, 0x6d}, 9000) // 36000 bytes, 2 chunks
	begin := transferTestCall(t, s, ctx, map[string]any{"action": "upload_replace", "path": path, "size": len(replacement), "sha256": transferDigest(replacement), "expected_sha256": transferDigest(original)})
	id := begin["transfer_id"].(string)
	for offset := 0; offset < len(replacement); {
		end := offset + transferChunkLimit
		if end > len(replacement) {
			end = len(replacement)
		}
		transferTestChunk(t, s, path, id, offset, replacement[offset:end])
		offset = end
	}
	transferTestCall(t, s, ctx, map[string]any{"action": "upload_commit", "path": path, "transfer_id": id})
	if disk, err := os.ReadFile(path); err != nil || !bytes.Equal(disk, replacement) {
		t.Fatal("chunked replace differs", err)
	}
	_ = base64.StdEncoding // keep the import aligned with the file's other tests
}
