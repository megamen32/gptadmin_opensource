package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	nodehub "github.com/megamen32/gptadmin/go-shellmcp/internal/hub"
)

func transferTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	// macOS temporary directories may be reached through /var symlink aliases.
	// The transfer contract intentionally accepts canonical roots only.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: Config{InspectRoots: []string{dir}}}
	t.Cleanup(s.closeBinaryTransfers)
	return s, dir
}
func transferTestCall(t *testing.T, s *Server, ctx context.Context, args map[string]any) map[string]any {
	t.Helper()
	r, e := s.mcpFileTransfer(ctx, args)
	if e != nil {
		t.Fatal(e)
	}
	return r["structuredContent"].(map[string]any)["transfer"].(map[string]any)
}
func transferTestBegin(t *testing.T, s *Server, path string, data []byte) string {
	r := transferTestCall(t, s, context.Background(), map[string]any{"action": "upload_begin", "path": path, "size": len(data), "sha256": transferDigest(data)})
	return r["transfer_id"].(string)
}
func transferTestChunk(t *testing.T, s *Server, path, id string, offset int, data []byte) map[string]any {
	return transferTestCall(t, s, context.Background(), map[string]any{"action": "upload_chunk", "path": path, "transfer_id": id, "offset": offset, "data_base64": base64.StdEncoding.EncodeToString(data), "sha256": transferDigest(data)})
}
func TestFileTransferBinaryRoundtripResumeAndAtomicCreate(t *testing.T) {
	s, dir := transferTestServer(t)
	ctx := context.Background()
	path := filepath.Join(dir, "binary.dat")
	data := bytes.Repeat([]byte{0, 1, 2, 255, 128, 13, 10}, 17000)
	id := transferTestBegin(t, s, path, data)
	transferTestChunk(t, s, path, id, 0, data[:transferChunkLimit])
	got := transferTestChunk(t, s, path, id, 0, data[:transferChunkLimit])
	if got["received"] != int64(transferChunkLimit) {
		t.Fatal(got)
	}
	if _, e := os.Stat(path); !os.IsNotExist(e) {
		t.Fatal("partial target is visible", e)
	}
	if _, e := s.mcpFileTransfer(ctx, map[string]any{"action": "upload_commit", "path": path, "transfer_id": id}); e == nil {
		t.Fatal("incomplete upload committed")
	}
	for offset := transferChunkLimit; offset < len(data); {
		end := offset + transferChunkLimit
		if end > len(data) {
			end = len(data)
		}
		transferTestChunk(t, s, path, id, offset, data[offset:end])
		offset = end
	}
	done := transferTestCall(t, s, ctx, map[string]any{"action": "upload_commit", "path": path, "transfer_id": id})
	if done["completed"] != true {
		t.Fatal(done)
	}
	// A lost commit response may be retried without creating or replacing again.
	transferTestCall(t, s, ctx, map[string]any{"action": "upload_commit", "path": path, "transfer_id": id})
	disk, e := os.ReadFile(path)
	if e != nil || !bytes.Equal(disk, data) {
		t.Fatal("binary differs", e)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm()&0077 != 0 {
		t.Fatalf("mode %o", info.Mode())
	}
	snap := transferTestCall(t, s, ctx, map[string]any{"action": "download_begin", "path": path})
	downloadID := snap["transfer_id"].(string)
	if snap["sha256"] != transferDigest(data) {
		t.Fatal(snap)
	}
	if e = os.WriteFile(path, []byte("changed after snapshot"), 0600); e != nil {
		t.Fatal(e)
	}
	var all []byte
	for off := 0; off < len(data); {
		r := transferTestCall(t, s, ctx, map[string]any{"action": "download_chunk", "path": path, "transfer_id": downloadID, "offset": off, "max_bytes": 20000})
		part, e := base64.StdEncoding.Strict().DecodeString(r["data_base64"].(string))
		if e != nil {
			t.Fatal(e)
		}
		if transferDigest(part) != r["chunk_sha256"] {
			t.Fatal("chunk digest")
		}
		all = append(all, part...)
		off = int(r["next_offset"].(int64))
	}
	if !bytes.Equal(all, data) {
		t.Fatal("snapshot changed")
	}
	transferTestCall(t, s, ctx, map[string]any{"action": "download_abort", "path": path, "transfer_id": downloadID})
}
func TestFileTransferRejectsInvalidInputAndOverwrite(t *testing.T) {
	s, dir := transferTestServer(t)
	ctx := context.Background()
	path := filepath.Join(dir, "new.bin")
	data := []byte{0, 255, 3}
	id := transferTestBegin(t, s, path, data)
	for _, args := range []map[string]any{
		{"action": "upload_chunk", "path": path, "transfer_id": id, "offset": 1, "data_base64": "AA=="},
		{"action": "upload_chunk", "path": path, "transfer_id": id, "offset": int64(1<<63 - 1), "data_base64": "AA=="},
		{"action": "upload_chunk", "path": path, "transfer_id": id, "data_base64": "AA==\n"},
		{"action": "upload_chunk", "path": path, "transfer_id": id, "data_base64": "AA==", "sha256": strings.Repeat("0", 64)},
		{"action": "upload_chunk", "path": path, "transfer_id": id, "data_base64": base64.StdEncoding.EncodeToString(make([]byte, transferChunkLimit+1))},
		{"action": "upload_begin", "path": path, "size": transferFileLimit + 1, "sha256": transferDigest(nil)},
		{"action": "download_chunk", "path": path, "transfer_id": id},
		{"action": "upload_status", "path": filepath.Join(dir, "other"), "transfer_id": id},
	} {
		if _, e := s.mcpFileTransfer(ctx, args); e == nil {
			t.Errorf("accepted invalid action %#v", args["action"])
		}
	}
	transferTestChunk(t, s, path, id, 0, data)
	if _, e := s.mcpFileTransfer(ctx, map[string]any{"action": "upload_chunk", "path": path, "transfer_id": id, "data_base64": "AAAA"}); e == nil {
		t.Fatal("accepted divergent retry")
	}
	if e := os.WriteFile(path, []byte("do not replace"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := s.mcpFileTransfer(ctx, map[string]any{"action": "upload_commit", "path": path, "transfer_id": id}); e == nil {
		t.Fatal("overwrote concurrent target")
	}
	disk, _ := os.ReadFile(path)
	if string(disk) != "do not replace" {
		t.Fatal(string(disk))
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatal("temporary files leaked", entries)
	}
	transferTestCall(t, s, ctx, map[string]any{"action": "upload_abort", "path": path, "transfer_id": id})
}
func TestFileTransferDigestMismatchAndEmptyFile(t *testing.T) {
	s, dir := transferTestServer(t)
	ctx := context.Background()
	path := filepath.Join(dir, "bad.bin")
	id := transferTestBegin(t, s, path, []byte("good"))
	transferTestChunk(t, s, path, id, 0, []byte("evil"))
	if _, e := s.mcpFileTransfer(ctx, map[string]any{"action": "upload_commit", "path": path, "transfer_id": id}); e == nil {
		t.Fatal("bad digest committed")
	}
	if _, e := os.Stat(path); !os.IsNotExist(e) {
		t.Fatal("bad digest target exists")
	}
	empty := filepath.Join(dir, "empty")
	eid := transferTestBegin(t, s, empty, nil)
	transferTestCall(t, s, ctx, map[string]any{"action": "upload_commit", "path": empty, "transfer_id": eid})
	data, e := os.ReadFile(empty)
	if e != nil || len(data) != 0 {
		t.Fatal(e)
	}
}
func TestFileTransferRootsSymlinksAndProfileIsolation(t *testing.T) {
	s, dir := transferTestServer(t)
	outside := t.TempDir()
	ctx := context.Background()
	if e := os.Symlink(outside, filepath.Join(dir, "escape")); e != nil {
		t.Skip(e)
	}
	for _, path := range []string{filepath.Join(outside, "x"), dir + "/../x", filepath.Join(dir, "escape", "x"), dir, "relative"} {
		if _, e := s.mcpFileTransfer(ctx, map[string]any{"action": "upload_begin", "path": path, "size": 0, "sha256": transferDigest(nil)}); e == nil {
			t.Error("accepted path", path)
		}
	}
	roots := []string{dir}
	p := &nodehub.FilesystemACL{ProfileID: "a", ProfileVersion: 1, RootsDigest: "v1", ReadRoots: &roots, WriteRoots: &roots}
	profileCtx := withFilesystemACL(ctx, p)
	path := filepath.Join(dir, "scoped.bin")
	begin := transferTestCall(t, s, profileCtx, map[string]any{"action": "upload_begin", "path": path, "size": 0, "sha256": transferDigest(nil)})
	args := map[string]any{"action": "upload_commit", "path": path, "transfer_id": begin["transfer_id"]}
	q := *p
	q.ProfileID = "b"
	if _, e := s.mcpFileTransfer(withFilesystemACL(ctx, &q), args); e == nil {
		t.Fatal("cross-profile transfer")
	}
	q = *p
	q.ProfileVersion++
	if _, e := s.mcpFileTransfer(withFilesystemACL(ctx, &q), args); e == nil {
		t.Fatal("changed-profile transfer")
	}
	q = *p
	q.WriteRoots = nil
	if _, e := s.mcpFileTransfer(withFilesystemACL(ctx, &q), args); e == nil {
		t.Fatal("read-only transfer write")
	}
	transferTestCall(t, s, profileCtx, args)
}
func TestFileTransferLimitsAndClose(t *testing.T) {
	s, dir := transferTestServer(t)
	for n := 0; n < transferSlots; n++ {
		transferTestBegin(t, s, filepath.Join(dir, string(rune('a'+n))), nil)
	}
	if _, e := s.mcpFileTransfer(context.Background(), map[string]any{"action": "upload_begin", "path": filepath.Join(dir, "overflow"), "size": 0, "sha256": transferDigest(nil)}); e == nil {
		t.Fatal("slot limit not enforced")
	}
	s.closeBinaryTransfers()
	if len(s.transfers.items) != 0 {
		t.Fatal("transfers leaked on close")
	}
}
