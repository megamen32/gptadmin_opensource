package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/hub"
)

// Deliberately fill a single shard to exercise its hard disk bound independently
// of random distribution. All IDs are normal immutable task handles.
func timedTestIDs(shard byte, issued time.Time, n int) []string {
	var ids []string
	for seq := 0; len(ids) < n; seq++ {
		id := fmt.Sprintf("ot1.YQ.t1_%d_%032x", issued.Unix(), seq)
		if sha256.Sum256([]byte(id))[0] == shard {
			ids = append(ids, id)
		}
	}
	return ids
}

func TestDispatchBudgetExpiryAndClockRollback(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	shard := byte(now.Add(dispatchWindow).Unix() / 15)
	cfg := Config{IdentityDir: t.TempDir()}
	s := New(cfg)
	ids := timedTestIDs(shard, now, dispatchBucketSlots+1)
	// Leave the first task unresolved, all others receive their owner ACK.
	for i, id := range ids[:dispatchBucketSlots] {
		if seen, err := s.updateDispatch(id, now, false); err != nil || seen {
			t.Fatalf("claim %d: %v %v", i, seen, err)
		}
		if i != 0 {
			if _, err := s.updateDispatch(id, now, true); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := s.updateDispatch(ids[dispatchBucketSlots], now, false); !errors.Is(err, errDispatchCapacity) {
		t.Fatalf("full shard: %v", err)
	}
	if seen, err := s.updateDispatch(ids[0], now, false); err != nil || !seen {
		t.Fatal("capacity displaced an unresolved execution")
	}
	if seen, err := s.updateDispatch(ids[1], now.Add(dispatchWindow-time.Second), false); err != nil || !seen {
		t.Fatal("premature expiry of ACK")
	}
	path := filepath.Join(cfg.IdentityDir, "shellmcp-dispatch", fmt.Sprintf("%02x.v2", shard))
	info, err := os.Stat(path)
	if err != nil || info.Size() > dispatchBucketMaxBytes {
		t.Fatal("shard exceeded its byte budget")
	}
	t.Logf("%d exact entries in one shard, %d bytes", dispatchBucketSlots, info.Size())
	if _, err := s.updateDispatch(ids[1], now.Add(dispatchWindow), false); !errors.Is(err, errStaleDispatch) {
		t.Fatalf("expiry boundary: %v", err)
	}
	future := timedTestIDs(shard, now.Add(dispatchWindow), 1)[0]
	if _, err := s.updateDispatch(future, now, false); !errors.Is(err, errInvalidDispatch) {
		t.Fatalf("future issuance: %v", err)
	}
	if err := s.pruneDispatchShard(now.Add(dispatchWindow)); err != nil {
		t.Fatal(err)
	}
	pruned, err := loadDispatchBucket(path)
	if err != nil || len(pruned.Claims) != 1 {
		t.Fatal("idle maintenance did not reclaim confirmed IDs at expiry")
	}
	if seen, err := s.updateDispatch(future, now.Add(dispatchWindow), false); err != nil || seen {
		t.Fatal("expired ACKs blocked new work")
	}
	b, err := loadDispatchBucket(path)
	if err != nil {
		t.Fatal(err)
	}
	key := sha256.Sum256([]byte(ids[0]))
	deadline, kept := b.Claims[hex.EncodeToString(key[:])]
	if len(b.Claims) != 2 || !kept || deadline != 0 {
		t.Fatal("expiry deleted unknown evidence or kept terminal IDs")
	}
	s.Close()
	restarted := New(cfg)
	defer restarted.Close()
	if _, err := restarted.updateDispatch(ids[1], now, false); !errors.Is(err, errStaleDispatch) {
		t.Fatalf("clock rollback admitted forgotten ID: %v", err)
	}
	if _, err := restarted.updateDispatch(ids[0], now.Add(dispatchWindow), true); err != nil {
		t.Fatal("late owner ACK:", err)
	}
	b, err = loadDispatchBucket(path)
	if err != nil || len(b.Claims) != 1 {
		t.Fatal("late ACK did not release the unresolved slot")
	}
	for _, id := range []string{"ot1.YQ.t1_bad_random", "t1_0001_00000000000000000000000000000000"} {
		if _, err := restarted.updateDispatch(id, now, false); !errors.Is(err, errInvalidDispatch) {
			t.Fatal("malformed issuance accepted as legacy")
		}
	}
}

func TestLegacyDispatchJournalMigration(t *testing.T) {
	const id = "ot1.YQ.legacy"
	key := sha256.Sum256([]byte(id))
	cfg := Config{IdentityDir: t.TempDir()}
	journal := filepath.Join(cfg.IdentityDir, "shellmcp-dispatch")
	if err := os.MkdirAll(journal, 0700); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(journal, fmt.Sprintf("%02x", key[0]))
	if err := os.WriteFile(base+".ids", key[:], 0600); err != nil {
		t.Fatal(err)
	}
	s := New(cfg)
	defer s.Close()
	if _, err := s.updateDispatch(id, time.Now(), false); !errors.Is(err, errStaleDispatch) {
		t.Fatalf("v1 replay: %v", err)
	}
	b, err := loadDispatchBucket(base + ".v2")
	if err != nil || b.Imported != 32 || !b.V1.match(key, false) {
		t.Fatal("missing v1 migration fence")
	}
	if _, err := s.updateDispatch(id, time.Now(), true); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(base + ".ids"); err != nil || !bytes.Equal(raw, key[:]) {
		t.Fatal("v1 evidence changed")
	}
	// An opaque ACK changes only a fixed-size membership summary. The
	// representation has no false negatives, even when every bit is set.
	for range 8 {
		if _, err := s.updateDispatch(id, time.Now(), false); !errors.Is(err, errStaleDispatch) {
			t.Fatal("accepted opaque ACK replay")
		}
	}
	b.Legacy = bytes.Repeat([]byte{255}, legacyFilterBytes)
	if err := b.save(base + ".v2"); err != nil {
		t.Fatal(err)
	}
	freshLegacy := ""
	for n := 0; ; n++ {
		freshLegacy = fmt.Sprintf("legacy-%d", n)
		if sha256.Sum256([]byte(freshLegacy))[0] == key[0] {
			break
		}
	}
	if _, err := s.updateDispatch(freshLegacy, time.Now(), false); !errors.Is(err, errStaleDispatch) {
		t.Fatal("ambiguous legacy ID admitted")
	}
	dated := timedTestIDs(key[0], time.Now(), 1)[0]
	if seen, err := s.updateDispatch(dated, time.Now(), false); err != nil || seen {
		t.Fatal("full legacy filter blocked a fresh dated ID")
	}
}

func TestRetrySidecarPreservesLegacyPayloadAndLateOwnerACK(t *testing.T) {
	const id = "ot1.YQ.old-outbox"
	data := strings.Repeat("a", 4<<20)
	legacy, err := json.Marshal(map[string]any{"attempts": 4, "next_attempt_at": 0, "created_at": 1, "job_id": id, "payload": map[string]any{"id": id, "result": map[string]any{"stdout": data}}})
	if err != nil {
		t.Fatal(err)
	}
	var n atomic.Int32
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var result struct {
			ID     string
			Result struct{ Stdout string }
		}
		if r.URL.Query().Get("job_id") != id || json.NewDecoder(r.Body).Decode(&result) != nil || result.ID != id || result.Result.Stdout != data {
			t.Error("invalid receipt body")
		}
		switch n.Add(1) {
		case 1, 2:
			w.WriteHeader(503)
		case 3:
			io.WriteString(w, `{"ok":true,"id":"foreign","owner_id":"b"}`)
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "id": id, "owner_id": "a"})
		}
	}))
	defer peer.Close()
	cfg := Config{Name: "phone", HubURL: peer.URL, IdentityDir: t.TempDir(), OutboxDir: t.TempDir(), OutboxBackoffBase: time.Millisecond, OutboxBackoffCap: time.Millisecond}
	path := filepath.Join(cfg.OutboxDir, id+".json")
	if err := os.WriteFile(path, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	old, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		s := New(cfg)
		if s.sendOutbox(context.Background(), id) {
			t.Fatal("missing owner ACK treated as delivered")
		}
		s.Close()
		info, err := os.Stat(path)
		if err != nil || !os.SameFile(info, old) || info.ModTime() != old.ModTime() {
			t.Fatal("retry replaced or modified the payload inode")
		}
		raw, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(raw, legacy) {
			t.Fatal("retried payload changed")
		}
		retry, err := os.Stat(retryPath(path))
		if err != nil || retry.Size() > 128 {
			t.Fatal("retry state exceeded 128 bytes")
		}
		attempts, next, err := outboxSchedule(path)
		if err != nil || attempts != 5+i {
			t.Fatalf("attempts after restart: %d %v", attempts, err)
		}
		if wait := time.Until(time.Unix(next, 0)); wait > 0 {
			time.Sleep(wait)
		}
	}
	s := New(cfg)
	defer s.Close()
	if !s.sendOutbox(context.Background(), id) {
		t.Fatal("valid late ACK failed")
	}
	for _, p := range []string{path, retryPath(path)} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("ACK retained %s", filepath.Base(p))
		}
	}
	if ran, err := s.updateDispatch(id, time.Now(), false); ran || !errors.Is(err, errStaleDispatch) {
		t.Fatal("legacy outbox replay after ACK")
	}
	t.Logf("%d payload bytes retained the same inode, mtime and content across two 503s and one foreign ACK", len(legacy))
}

// If the journal cannot record the owner ACK, the payload itself must
// continue fencing dispatch, including on process restart.
func TestAcknowledgementFailureRetainsOutbox(t *testing.T) {
	const id = "ot1.YQ.retained"
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "id": id, "owner_id": "a"})
	}))
	defer peer.Close()
	cfg := Config{Name: "phone", HubURL: peer.URL, IdentityDir: t.TempDir(), OutboxDir: t.TempDir()}
	s := New(cfg)
	defer s.Close()
	if err := s.storeOutbox(hub.TaskResult{ID: id, Result: "undelivered"}); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(cfg.IdentityDir, "shellmcp-dispatch")
	if err := os.WriteFile(journal, []byte("unavailable"), 0600); err != nil {
		t.Fatal(err)
	}
	if s.sendOutbox(context.Background(), id) {
		t.Fatal("outbox removed before terminal fence")
	}
	if _, err := os.Stat(filepath.Join(cfg.OutboxDir, id+".json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(journal); err != nil {
		t.Fatal(err)
	}
	if seen, err := s.updateDispatch(id, time.Now(), false); err != nil || !seen {
		t.Fatal("pending outbox did not fence the retry")
	}
}
