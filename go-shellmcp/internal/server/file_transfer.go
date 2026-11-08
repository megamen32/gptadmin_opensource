package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/fsacl"
)

const transferFileLimit = 16 << 20
const transferChunkLimit = 32 << 10
const transferSlots = 4
const transferListLimit = 1000
const transferTTL = 15 * time.Minute

type binaryTransfer struct {
	path, owner, direction, digest string
	expected                       string
	replace                        bool
	size, received                 int64
	data                           []byte
	parent                         *os.Root
	parentInfo                     os.FileInfo
	name                           string
	completed                      bool
	timer                          *time.Timer
	expires                        time.Time
}
type binaryTransfers struct {
	sync.Mutex
	items  map[string]*binaryTransfer
	closed bool
}
type transferArgs struct {
	Action   string `json:"action"`
	Path     string `json:"path"`
	ID       string `json:"transfer_id"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
	Offset   int64  `json:"offset"`
	Data     string `json:"data_base64"`
	MaxBytes int    `json:"max_bytes"`
	Pattern  string `json:"pattern"`
	Expected string `json:"expected_sha256"`
	Hash     bool   `json:"hash"`
}

// A transfer handle is not an authorization grant. Every operation rechecks
// current roots, profile identity/version, direction and exact caller path.
func (s *Server) transferScope(ctx context.Context) (*fsacl.Policy, string) {
	if p, ok := filesystemACLFromContext(ctx); ok {
		return &fsacl.Policy{ReadRoots: p.ReadRoots, WriteRoots: p.WriteRoots, RequireCanonicalRoots: true},
			fmt.Sprintf("%s:%d:%s", p.ProfileID, p.ProfileVersion, p.RootsDigest)
	}
	roots := append([]string(nil), s.cfg.InspectRoots...)
	return &fsacl.Policy{ReadRoots: &roots, WriteRoots: &roots, RequireCanonicalRoots: true}, "authenticated-node-owner"
}

func transferOpen(policy *fsacl.Policy, path string, write bool) (*os.Root, string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, "", errors.New("file_transfer requires a clean absolute path without traversal")
	}
	root, rel, err := policy.Open(path, write)
	if err != nil {
		return nil, "", err
	}
	if rel == "." {
		root.Close()
		return nil, "", errors.New("file_transfer requires a file")
	}
	if err = fsacl.RejectSymlinkComponents(root, rel); err != nil {
		root.Close()
		return nil, "", err
	}
	return root, rel, nil
}
func transferDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func transferReadAction(action string) bool { return strings.HasPrefix(action, "download_") }
func (t *binaryTransfer) close() {
	if t.timer != nil {
		t.timer.Stop()
	}
	if t.parent != nil {
		t.parent.Close()
		t.parent = nil
	}
	t.data = nil
}
func (s *Server) closeBinaryTransfers() {
	s.transfers.Lock()
	defer s.transfers.Unlock()
	s.transfers.closed = true
	for id, t := range s.transfers.items {
		t.close()
		delete(s.transfers.items, id)
	}
}
func (s *Server) transferReceipt(id string, t *binaryTransfer) map[string]any {
	receipt := map[string]any{"transfer_id": id, "path": t.path, "direction": t.direction,
		"size": t.size, "sha256": t.digest, "received": t.received, "completed": t.completed,
		"chunk_bytes": transferChunkLimit, "expires_at": t.expires.UTC().Format(time.RFC3339),
		"resume": "transport reconnect only; node restart requires a new transfer"}
	if t.replace {
		receipt["replace"] = true
		receipt["expected_sha256"] = t.expected
	}
	return receipt
}
func (s *Server) mcpFileTransfer(ctx context.Context, args map[string]any) (map[string]any, error) {
	var req transferArgs
	b, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(b, &req); err != nil {
		return nil, err
	}
	switch req.Action {
	case "upload_begin", "upload_replace", "upload_chunk", "upload_status", "upload_commit", "upload_abort",
		"download_begin", "download_chunk", "download_abort", "stat", "list":
	default:
		return nil, errors.New("unknown file_transfer action")
	}
	read := transferReadAction(req.Action)
	policy, owner := s.transferScope(ctx)
	switch req.Action {
	case "stat":
		return s.transferStat(policy, req)
	case "list":
		return s.transferList(policy, req)
	}
	root, rel, err := transferOpen(policy, req.Path, !read)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if req.Offset < 0 || req.Offset > transferFileLimit {
		return nil, errors.New("offset must not be negative")
	}
	s.transfers.Lock()
	defer s.transfers.Unlock()
	if s.transfers.closed {
		return nil, errors.New("file transfer service is closed")
	}
	if s.transfers.items == nil {
		s.transfers.items = make(map[string]*binaryTransfer)
	}
	if req.Action == "upload_begin" || req.Action == "upload_replace" || req.Action == "download_begin" {
		if len(s.transfers.items) >= transferSlots {
			return nil, errors.New("file transfer capacity reached; abort unused transfers or wait for expiry")
		}
		if req.Size < 0 || req.Size > transferFileLimit {
			return nil, errors.New("file size exceeds 16 MiB limit")
		}
		t := &binaryTransfer{path: req.Path, owner: owner, direction: "upload", size: req.Size, digest: req.SHA256}
		if read {
			t.direction = "download"
			before, e := root.Lstat(rel)
			if e != nil {
				return nil, e
			}
			if !before.Mode().IsRegular() || before.Size() > transferFileLimit {
				return nil, errors.New("download requires a regular file of at most 16 MiB")
			}
			f, e := transferOpenRead(root, rel)
			if e != nil {
				return nil, e
			}
			info, e := f.Stat()
			if e != nil || !info.Mode().IsRegular() || !os.SameFile(before, info) {
				f.Close()
				return nil, errors.New("source changed while opening")
			}
			data, e := io.ReadAll(io.LimitReader(f, transferFileLimit+1))
			after, se := f.Stat()
			f.Close()
			if e != nil {
				return nil, e
			}
			if se != nil || len(data) > transferFileLimit || int64(len(data)) != info.Size() || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
				return nil, errors.New("source changed during snapshot")
			}
			t.data = data
			t.size = int64(len(data))
			t.received = t.size
			t.digest = transferDigest(data)
		} else {
			digest, e := hex.DecodeString(req.SHA256)
			if e != nil || len(digest) != sha256.Size || req.SHA256 != strings.ToLower(req.SHA256) {
				return nil, errors.New("upload requires lowercase SHA-256")
			}
			if req.Action == "upload_replace" {
				// Replace mode: the destination must exist now and match the
				// caller's expected_sha256 digest; commit re-verifies it.
				if ed, ee := hex.DecodeString(req.Expected); ee != nil || len(ed) != sha256.Size || req.Expected != strings.ToLower(req.Expected) {
					return nil, errors.New("upload_replace requires lowercase expected_sha256")
				}
				current, ee := transferDestinationDigest(root, rel)
				if ee != nil {
					return nil, ee
				}
				if current != req.Expected {
					return nil, errors.New("expected_sha256 does not match the current destination")
				}
				t.replace = true
				t.expected = req.Expected
			} else if _, e := root.Lstat(rel); !errors.Is(e, os.ErrNotExist) {
				return nil, errors.New("destination already exists or cannot be checked")
			}
			parent, e := root.OpenRoot(filepath.Dir(rel))
			if e != nil {
				return nil, e
			}
			pi, e := parent.Stat(".")
			if e != nil {
				parent.Close()
				return nil, e
			}
			t.parent = parent
			t.parentInfo = pi
			t.name = filepath.Base(rel)
			t.data = make([]byte, 0, int(req.Size))
		}
		var random [24]byte
		if _, e := rand.Read(random[:]); e != nil {
			t.close()
			return nil, e
		}
		id := hex.EncodeToString(random[:])
		t.expires = time.Now().Add(transferTTL)
		s.transfers.items[id] = t
		t.timer = time.AfterFunc(transferTTL, func() {
			s.transfers.Lock()
			defer s.transfers.Unlock()
			if current := s.transfers.items[id]; current == t {
				t.close()
				delete(s.transfers.items, id)
			}
		})
		return mcpText("Private file transfer opened.", map[string]any{"transfer": s.transferReceipt(id, t)}), nil
	}
	t := s.transfers.items[req.ID]
	direction := "upload"
	if read {
		direction = "download"
	}
	if t == nil || t.path != req.Path || t.owner != owner || t.direction != direction {
		return nil, errors.New("unknown, expired or unauthorized transfer")
	}
	receipt := s.transferReceipt(req.ID, t)
	switch req.Action {
	case "upload_status":
	case "upload_abort", "download_abort":
		t.close()
		delete(s.transfers.items, req.ID)
		receipt["aborted"] = true
	case "upload_chunk":
		if t.completed {
			return nil, errors.New("upload is already committed")
		}
		if len(req.Data) > base64.StdEncoding.EncodedLen(transferChunkLimit) {
			return nil, errors.New("chunk exceeds 32 KiB")
		}
		data, e := base64.StdEncoding.Strict().DecodeString(req.Data)
		if e != nil || len(data) == 0 || len(data) > transferChunkLimit || base64.StdEncoding.EncodeToString(data) != req.Data {
			return nil, errors.New("chunk requires canonical nonempty base64")
		}
		if req.SHA256 != "" && transferDigest(data) != req.SHA256 {
			return nil, errors.New("chunk SHA-256 mismatch")
		}
		end := req.Offset + int64(len(data))
		if end > t.size {
			return nil, errors.New("chunk exceeds declared file size")
		}
		if req.Offset < t.received {
			if end > t.received || !bytes.Equal(t.data[int(req.Offset):int(end)], data) {
				return nil, errors.New("retry does not match accepted bytes")
			}
		} else if req.Offset == t.received {
			t.data = append(t.data, data...)
			t.received = end
		} else {
			return nil, errors.New("out-of-order chunk; resume from received offset")
		}
		receipt = s.transferReceipt(req.ID, t)
	case "upload_commit":
		if t.completed {
			break
		}
		if t.received != t.size || transferDigest(t.data) != t.digest {
			return nil, errors.New("upload size or SHA-256 mismatch")
		}
		current, e := root.Stat(filepath.Dir(rel))
		if e != nil || !os.SameFile(t.parentInfo, current) {
			return nil, errors.New("destination parent changed")
		}
		if t.replace {
			now, e := transferDestinationDigest(root, rel)
			if e != nil {
				return nil, e
			}
			if now != t.expected {
				return nil, errors.New("destination changed since upload_replace began")
			}
		}
		if e = transferCommit(t); e != nil {
			return nil, e
		}
		t.completed = true
		t.data = nil
		t.parent.Close()
		t.parent = nil
		receipt = s.transferReceipt(req.ID, t)
	case "download_chunk":
		if req.Offset > t.size {
			return nil, errors.New("offset exceeds snapshot size")
		}
		count := req.MaxBytes
		if count == 0 {
			count = transferChunkLimit
		}
		if count < 1 || count > transferChunkLimit {
			return nil, errors.New("max_bytes must be 1..32768")
		}
		end := req.Offset + int64(count)
		if end > t.size {
			end = t.size
		}
		data := t.data[int(req.Offset):int(end)]
		receipt["offset"] = req.Offset
		receipt["next_offset"] = end
		receipt["eof"] = end == t.size
		receipt["data_base64"] = base64.StdEncoding.EncodeToString(data)
		receipt["chunk_sha256"] = transferDigest(data)
	}
	return mcpText("Private file transfer operation completed.", map[string]any{"transfer": receipt}), nil
}

// Publish with an atomic no-replace hard link. A destination is never
// overwritten, including when it appears concurrently with the final commit.
func transferCommit(t *binaryTransfer) error {
	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	temp := ".gptadmin-transfer-" + hex.EncodeToString(nonce[:]) + ".tmp"
	f, err := t.parent.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer t.parent.Remove(temp)
	before, err := f.Stat()
	if err == nil {
		_, err = f.Write(t.data)
	}
	if err == nil {
		// Match file_editor: a privileged Node must leave the new private file
		// readable by the destination directory's ordinary OS owner.
		err = applyOpenFileOwnership(f, ownershipFromInfo(t.parentInfo))
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	current, err := t.parent.Lstat(temp)
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(before, current) {
		return errors.New("temporary upload file changed")
	}
	if t.replace {
		if err = t.parent.Rename(temp, t.name); err != nil {
			return fmt.Errorf("atomic replace failed: %w", err)
		}
	} else if err = t.parent.Link(temp, t.name); err != nil {
		return fmt.Errorf("atomic create failed (destination must not exist): %w", err)
	}
	// Linking publishes already-fsynced contents. Directory sync support differs
	// across platforms; do not turn an already-published result into a retry.
	if dir, e := t.parent.Open("."); e == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

// transferDestinationDigest reads a regular file through the open root and
// returns its SHA-256, refusing destinations that change while being read.
func transferDestinationDigest(root *os.Root, rel string) (string, error) {
	before, e := root.Lstat(rel)
	if e != nil {
		return "", e
	}
	if !before.Mode().IsRegular() || before.Size() > transferFileLimit {
		return "", errors.New("upload_replace requires a regular destination of at most 16 MiB")
	}
	f, e := transferOpenRead(root, rel)
	if e != nil {
		return "", e
	}
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() || !os.SameFile(before, info) {
		f.Close()
		return "", errors.New("destination changed while opening")
	}
	data, e := io.ReadAll(io.LimitReader(f, transferFileLimit+1))
	after, se := f.Stat()
	f.Close()
	if e != nil {
		return "", e
	}
	if se != nil || len(data) > transferFileLimit || int64(len(data)) != info.Size() || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return "", errors.New("destination changed while reading")
	}
	return transferDigest(data), nil
}

// Stateless metadata reads share the transfer policy without consuming a
// transfer slot: stat describes one path, list enumerates one directory.
func (s *Server) transferStat(policy *fsacl.Policy, req transferArgs) (map[string]any, error) {
	if !filepath.IsAbs(req.Path) || filepath.Clean(req.Path) != req.Path {
		return nil, errors.New("file_transfer requires a clean absolute path without traversal")
	}
	root, rel, err := policy.Open(req.Path, false)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err = fsacl.RejectSymlinkComponents(root, rel); err != nil {
		return nil, err
	}
	info, err := root.Lstat(rel)
	if err != nil {
		return nil, err
	}
	entry := map[string]any{"path": req.Path, "name": filepath.Base(rel), "is_dir": info.IsDir(),
		"is_regular": info.Mode().IsRegular(), "mode": info.Mode().String(),
		"size": info.Size(), "mtime": info.ModTime().UTC().Format(time.RFC3339Nano)}
	if req.Hash && info.Mode().IsRegular() {
		if info.Size() > transferFileLimit {
			return nil, errors.New("stat hash requires a regular file of at most 16 MiB")
		}
		digest, err := transferDestinationDigest(root, rel)
		if err != nil {
			return nil, err
		}
		entry["sha256"] = digest
	}
	return mcpText("Private file transfer operation completed.", map[string]any{"transfer": entry}), nil
}

func (s *Server) transferList(policy *fsacl.Policy, req transferArgs) (map[string]any, error) {
	if req.Pattern != "" {
		if len(req.Pattern) > 256 {
			return nil, errors.New("pattern must be at most 256 bytes")
		}
		if _, err := filepath.Match(req.Pattern, "probe"); err != nil {
			return nil, errors.New("pattern is not a valid glob")
		}
	}
	if !filepath.IsAbs(req.Path) || filepath.Clean(req.Path) != req.Path {
		return nil, errors.New("file_transfer requires a clean absolute path without traversal")
	}
	root, rel, err := policy.Open(req.Path, false)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err = fsacl.RejectSymlinkComponents(root, rel); err != nil {
		return nil, err
	}
	info, err := root.Lstat(rel)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("list requires a directory")
	}
	dir, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	entries := []map[string]any{}
	truncated := false
	entryName := func(m map[string]any) string {
		n, _ := m["name"].(string)
		return n
	}
	for {
		batch, err := dir.ReadDir(64)
		for _, e := range batch {
			if req.Pattern != "" {
				matched, me := filepath.Match(req.Pattern, e.Name())
				if me != nil || !matched {
					continue
				}
			}
			if len(entries) >= transferListLimit {
				truncated = true
				break
			}
			entry := map[string]any{"name": e.Name(), "is_dir": e.IsDir()}
			if fi, ie := e.Info(); ie == nil && fi.Mode().IsRegular() {
				entry["size"] = fi.Size()
			}
			entries = append(entries, entry)
		}
		if truncated || err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entryName(entries[i]) < entryName(entries[j]) })
	transfer := map[string]any{"path": req.Path, "entries": entries, "count": len(entries)}
	if truncated {
		transfer["truncated"] = true
	}
	if req.Pattern != "" {
		transfer["pattern"] = req.Pattern
	}
	return mcpText("Private file transfer operation completed.", map[string]any{"transfer": transfer}), nil
}

func fileTransferToolDefinition() map[string]any {
	return map[string]any{"name": "file_transfer", "description": "Private binary file exchange through existing authenticated MCP, without public URLs. Paths must stay in configured roots. 16 MiB per file, four transfers per node, 32 KiB chunks, 15-minute TTL. Upload begin declares size and SHA256; chunk retries are idempotent; commit atomically creates a NEW file (never overwrites). upload_replace instead atomically replaces an existing file at commit, and only while its digest still equals expected_sha256. Download begin snapshots and hashes the file. stat and list are stateless metadata reads within the same roots; stat can hash with hash=true. Resume after transport loss using transfer_id and offsets, not after node restart. Abort to release memory. Binary data is base64; do not print it into chat.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"action":          map[string]any{"type": "string", "enum": []string{"upload_begin", "upload_replace", "upload_chunk", "upload_status", "upload_commit", "upload_abort", "download_begin", "download_chunk", "download_abort", "stat", "list"}},
			"path":            map[string]any{"type": "string"},
			"transfer_id":     map[string]any{"type": "string"},
			"size":            map[string]any{"type": "integer", "minimum": 0, "maximum": transferFileLimit},
			"sha256":          map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"},
			"expected_sha256": map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$", "description": "upload_replace: current destination digest; commit aborts when it no longer matches"},
			"pattern":         map[string]any{"type": "string", "maxLength": 256, "description": "list: glob matched against entry names"},
			"hash":            map[string]any{"type": "boolean", "description": "stat: include sha256 of a regular file"},
			"offset":          map[string]any{"type": "integer", "minimum": 0, "maximum": transferFileLimit},
			"data_base64":     map[string]any{"type": "string", "maxLength": base64.StdEncoding.EncodedLen(transferChunkLimit)},
			"max_bytes":       map[string]any{"type": "integer", "minimum": 1, "maximum": transferChunkLimit},
		}, "required": []string{"action", "path"}, "additionalProperties": false}}
}
