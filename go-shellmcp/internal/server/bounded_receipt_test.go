package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/hub"
	"github.com/megamen32/gptadmin/go-shellmcp/internal/shell"
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

func TestCallbackOnFullAcknowledgedV2Shard(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test command uses sh")
	}
	dir := t.TempDir()
	cfg := Config{IdentityDir: dir, OutboxDir: filepath.Join(dir, "outbox"), SpillDir: filepath.Join(dir, "spool")}
	now := time.Now().Truncate(time.Second)
	const shard = 17
	ids := timedTestIDs(shard, now, dispatchBucketSlots+2)
	claims := make(map[string]int64)
	for _, id := range ids[:dispatchBucketSlots] {
		claims[fmt.Sprintf("%x", sha256.Sum256([]byte(id)))] = now.Add(dispatchWindow).Unix()
	}
	// Write the installed v2 layout directly, including 128 unexpired ACKs.
	raw, err := json.Marshal(map[string]any{"clock": now.Unix(), "claims": claims})
	if err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(dir, "shellmcp-dispatch")
	if err := os.Mkdir(journal, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(journal, fmt.Sprintf("%02x.v2", shard)), raw, 0600); err != nil {
		t.Fatal(err)
	}
	s := New(cfg)
	defer s.Close()
	pending, fresh := ids[dispatchBucketSlots], ids[dispatchBucketSlots+1]
	if err := s.storeOutbox(hub.TaskResult{ID: pending, Result: "awaiting owner ACK"}); err != nil {
		t.Fatal(err)
	}
	outbox := filepath.Join(cfg.OutboxDir, pending+".json")
	before, err := os.ReadFile(outbox)
	if err != nil {
		t.Fatal(err)
	}
	cmd := shell.Request{Cmd: "printf x >> counter", Cwd: dir, RunAsUser: currentTestUser()}
	if seen, err := s.startCallbackJob(fresh, cmd, true); seen || err != nil {
		t.Fatalf("fresh callback on an ACK-only v2 shard: duplicate=%v err=%v", seen, err)
	}
	s.callbackWG.Wait()
	restarted := New(cfg)
	defer restarted.Close()
	for _, id := range ids[:dispatchBucketSlots] {
		if _, err := restarted.startCallbackJob(id, cmd, false); !errors.Is(err, errStaleDispatch) {
			t.Fatalf("compacted ACK %s: %v", id, err)
		}
	}
	for _, id := range []string{pending, fresh} {
		if seen, err := restarted.startCallbackJob(id, cmd, false); err != nil || !seen {
			t.Fatalf("pending callback %s: duplicate=%v err=%v", id, seen, err)
		}
	}
	restarted.callbackWG.Wait()
	if b, err := os.ReadFile(filepath.Join(dir, "counter")); err != nil || string(b) != "x" {
		t.Fatalf("physical executions: %q, %v", b, err)
	}
	if after, err := os.ReadFile(outbox); err != nil || !bytes.Equal(after, before) {
		t.Fatal("pending outbox changed during admission or restart")
	}
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
	if seen, err := s.updateDispatch(ids[dispatchBucketSlots], now, false); err != nil || seen {
		t.Fatalf("ACKs blocked a new claim: duplicate=%v err=%v", seen, err)
	}
	if seen, err := s.updateDispatch(ids[0], now, false); err != nil || !seen {
		t.Fatal("capacity displaced an unresolved execution")
	}
	if _, err := s.updateDispatch(ids[1], now.Add(dispatchWindow-time.Second), false); !errors.Is(err, errStaleDispatch) {
		t.Fatal("premature expiry of ACK")
	}
	path := filepath.Join(cfg.IdentityDir, "shellmcp-dispatch", fmt.Sprintf("%02x.v2", shard))
	info, err := os.Stat(path)
	if err != nil || info.Size() > dispatchBucketMaxBytes {
		t.Fatal("shard exceeded its byte budget")
	}
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
	if err != nil || len(pruned.Claims) != 2 {
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
	if len(b.Claims) != 3 || !kept || deadline != 0 {
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
	if err != nil || len(b.Claims) != 2 {
		t.Fatal("late ACK did not release the unresolved slot")
	}
	for _, id := range []string{"ot1.YQ.t1_bad_random", "t1_0001_00000000000000000000000000000000"} {
		if _, err := restarted.updateDispatch(id, now, false); !errors.Is(err, errInvalidDispatch) {
			t.Fatal("malformed issuance accepted as legacy")
		}
	}
}

func TestFullUnresolvedShardCanCommitOutboxACK(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	const shard = 19
	ids := timedTestIDs(shard, now, dispatchBucketSlots+2)
	outboxID, fresh := ids[dispatchBucketSlots], ids[dispatchBucketSlots+1]
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "id": outboxID, "owner_id": "a"})
	}))
	defer owner.Close()
	s := New(Config{Name: "test", IdentityDir: t.TempDir(), OutboxDir: t.TempDir(), HubURL: owner.URL})
	defer s.Close()
	if err := s.withDispatchBucket(shard, true, func(b *dispatchBucket) (bool, error) {
		for _, id := range ids[:dispatchBucketSlots] {
			b.Claims[fmt.Sprintf("%x", sha256.Sum256([]byte(id)))] = 0
		}
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.updateDispatch(fresh, now, false); !errors.Is(err, errDispatchCapacity) {
		t.Fatalf("new claim displaced unresolved work: %v", err)
	}
	if err := s.storeOutbox(hub.TaskResult{ID: outboxID, Result: "completed before journal upgrade"}); err != nil {
		t.Fatal(err)
	}
	if !s.sendOutbox(context.Background(), outboxID) {
		t.Fatal("owner ACK needed an exact slot")
	}
	if _, err := os.Stat(filepath.Join(s.cfg.OutboxDir, outboxID+".json")); !os.IsNotExist(err) {
		t.Fatalf("acknowledged outbox retained: %v", err)
	}
	if _, err := s.updateDispatch(outboxID, now, false); !errors.Is(err, errStaleDispatch) {
		t.Fatalf("outbox ACK replay: %v", err)
	}
	for _, id := range ids[:dispatchBucketSlots] {
		if seen, err := s.updateDispatch(id, now, false); err != nil || !seen {
			t.Fatalf("lost unresolved claim %s: %v", id, err)
		}
	}
	if err := s.ackDispatch(ids[0]); err != nil {
		t.Fatal(err)
	}
	if seen, err := s.updateDispatch(fresh, now, false); err != nil || seen {
		t.Fatalf("owner ACK failed to release capacity: duplicate=%v err=%v", seen, err)
	}
}

func TestPolledDispatchCapacityReportsDurableRefusal(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	const shard = 23
	ids := timedTestIDs(shard, now, dispatchBucketSlots+1)
	refused := ids[dispatchBucketSlots]
	delivered := make(chan map[string]any, 1)
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			ID    string         `json:"id"`
			Error map[string]any `json:"error"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.ID != refused {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		delivered <- payload.Error
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "id": refused, "owner_id": "a"})
	}))
	defer owner.Close()
	cfg := Config{Name: "test", IdentityDir: t.TempDir(), OutboxDir: t.TempDir(), HubURL: owner.URL}
	s := New(cfg)
	if err := s.withDispatchBucket(shard, true, func(b *dispatchBucket) (bool, error) {
		for _, id := range ids[:dispatchBucketSlots] {
			b.Claims[fmt.Sprintf("%x", sha256.Sum256([]byte(id)))] = 0
		}
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	var starts atomic.Int32
	run := func(context.Context) { starts.Add(1) }
	if _, err := s.startBoundCallback(refused, true, run); !errors.Is(err, errDispatchCapacity) {
		t.Fatalf("unresolved dispatch did not refuse capacity: %v", err)
	}
	s.Close()

	restarted := New(cfg)
	defer restarted.Close()
	if seen, err := restarted.startBoundCallback(refused, true, run); err != nil || !seen {
		t.Fatalf("pending refusal did not fence replay after restart: seen=%v err=%v", seen, err)
	}
	if !restarted.sendOutbox(context.Background(), refused) {
		t.Fatal("owner acknowledgement of refusal failed")
	}
	select {
	case failure := <-delivered:
		if failure["code"] != "executor_dispatch_capacity" || failure["executed"] != false {
			t.Fatalf("unexpected refusal: %v", failure)
		}
	default:
		t.Fatal("owner did not receive refusal")
	}
	// Later capacity must not make the original request executable.
	if err := restarted.ackDispatch(ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.startBoundCallback(refused, true, run); !errors.Is(err, errStaleDispatch) {
		t.Fatalf("acknowledged refusal did not fence replay: %v", err)
	}
	restarted.callbackWG.Wait()
	if got := starts.Load(); got != 0 {
		t.Fatalf("refused task started %d times", got)
	}
}

func TestTimedACKWindowsDiskBoundsAndRollback(t *testing.T) {
	// Two adjacent expiries cross an epoch using full-width timestamp fields.
	const boundary = 2900000 * int64((dispatchWindow+dispatchClockSkew)/time.Second)
	now := time.Unix(boundary, 0).Add(-dispatchWindow)
	shard := byte((boundary - 1) / 15 % 256)
	s := New(Config{IdentityDir: t.TempDir()})
	defer s.Close()
	if err := s.withDispatchBucket(shard, true, func(b *dispatchBucket) (bool, error) {
		b.Clock = now.Unix()
		b.Legacy = make(dispatchFilter, legacyFilterBytes)
		b.V1 = make(dispatchFilter, legacyFilterBytes)
		for _, id := range timedTestIDs(shard, now, dispatchBucketSlots) {
			b.Claims[fmt.Sprintf("%x", sha256.Sum256([]byte(id)))] = boundary
		}
		// Saturate both windows to exercise collisions and maximum file size.
		for _, expiry := range []int64{boundary - 1, boundary} {
			b.rememberTimedACK([sha256.Size]byte{}, expiry)
			b.ACKs[ackWindowIndex(expiry)].Bits = bytes.Repeat([]byte{0xff}, timedFilterBytes)
		}
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.cfg.IdentityDir, "shellmcp-dispatch", fmt.Sprintf("%02x.v2", shard))
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > dispatchBucketMaxBytes {
		t.Fatalf("maximum shard: %d bytes, %v", len(raw), err)
	}
	t.Logf("128 exact ACKs, two timed windows and both legacy filters: %d bytes", len(raw))
	var v2 struct {
		Clock int64
	}
	if err := json.Unmarshal(raw, &v2); err != nil || v2.Clock != boundary {
		t.Fatalf("rollback reader clock: %d, %v", v2.Clock, err)
	}
	if err := s.pruneDispatchShard(time.Unix(boundary-1, 0)); err != nil {
		t.Fatal(err)
	}
	b, err := loadDispatchBucket(path)
	if err != nil {
		t.Fatal(err)
	}
	if b.Clock != boundary-1 || b.ACKs[ackWindowIndex(boundary-1)].Until != 0 || b.ACKs[ackWindowIndex(boundary)].Until != boundary {
		t.Fatal("partial rotation lost the admission clock or a live window")
	}
	for _, issued := range []time.Time{now.Add(-time.Second), now} {
		// Avoid the fixture's exact IDs. Roll the wall clock back after pruning.
		ids := timedTestIDs(shard, issued, dispatchBucketSlots+1)
		if _, err := s.updateDispatch(ids[dispatchBucketSlots], now, false); !errors.Is(err, errStaleDispatch) {
			t.Fatalf("expired ID or possible filter match admitted: %v", err)
		}
	}
	fresh := timedTestIDs(shard, now.Add(time.Second), 1)[0]
	if seen, err := s.updateDispatch(fresh, now.Add(time.Second), false); err != nil || seen {
		t.Fatalf("full filter blocked later issuance: duplicate=%v err=%v", seen, err)
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

type importedV2Shard struct {
	digests []byte
	bits    [4096]byte
	example string
}

// The v2 maintenance writer moved ACK digests into .ids and published their
// already-imported offset and v1 bitmap. Build that wire format independently.
func writeImportedV2Journal(t *testing.T, dir string, issued time.Time) []importedV2Shard {
	t.Helper()
	const total = 32723
	shards := make([]importedV2Shard, 256)
	for seq, stored := 0, 0; stored < total; seq++ {
		id := fmt.Sprintf("ot1.YQ.t1_%d_%032x", issued.Unix(), seq)
		key := sha256.Sum256([]byte(id))
		shard := &shards[key[0]]
		limit := total / len(shards)
		if int(key[0]) < total%len(shards) {
			limit++
		}
		if len(shard.digests) == limit*sha256.Size {
			continue
		}
		shard.digests = append(shard.digests, key[:]...)
		for i := 1; i < 12; i += 2 {
			bit := binary.BigEndian.Uint16(key[i:i+2]) & 0x7fff
			shard.bits[bit/8] |= 1 << (bit % 8)
		}
		shard.example = id
		stored++
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for n, shard := range shards {
		base := filepath.Join(dir, fmt.Sprintf("%02x", n))
		if err := os.WriteFile(base+".ids", shard.digests, 0600); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(map[string]any{
			"clock": issued.Unix(), "imported": len(shard.digests), "v1": shard.bits[:], "claims": map[string]int64{},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(base+".v2", raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return shards
}

func TestDispatchOnPreviouslyImportedV2Journal(t *testing.T) {
	now := time.Unix(1800000000, 0)
	cfg := Config{IdentityDir: t.TempDir()}
	dir := filepath.Join(cfg.IdentityDir, "shellmcp-dispatch")
	shards := writeImportedV2Journal(t, dir, now.Add(-time.Minute))
	s := New(cfg)
	defer s.Close()
	// Run a second wave through the maintenance result until one shard must compact.
	for i, id := range timedTestIDs(17, now, dispatchBucketSlots+1) {
		if seen, err := s.updateDispatch(id, now, false); err != nil || seen {
			t.Fatalf("new dispatch %d after v1 import: duplicate=%v err=%v", i+1, seen, err)
		}
		if _, err := s.updateDispatch(id, now, true); err != nil {
			t.Fatal(err)
		}
	}
	restarted := New(cfg)
	defer restarted.Close()
	for n, shard := range shards {
		if _, err := restarted.updateDispatch(shard.example, now, false); !errors.Is(err, errStaleDispatch) {
			t.Fatalf("dated ID from an already-imported .ids replayed on shard %02x: %v", n, err)
		}
		base := filepath.Join(dir, fmt.Sprintf("%02x", n))
		b, err := loadDispatchBucket(base + ".v2")
		if err != nil || b.Imported != int64(len(shard.digests)) || !bytes.Equal(b.V1, shard.bits[:]) {
			t.Fatalf("imported offset or v1 bitmap changed on shard %02x: %v", n, err)
		}
		if raw, err := os.ReadFile(base + ".ids"); err != nil || !bytes.Equal(raw, shard.digests) {
			t.Fatalf("exact .ids changed on shard %02x: %v", n, err)
		}
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
