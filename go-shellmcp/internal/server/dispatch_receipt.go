package server

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

// A dated local ID carries the time of original issuance through every retry,
// including a retry on a different transport. The full ID is the identity.
// A later HTTP Date, a fresh poll or a field in tool arguments cannot renew it.
const (
	dispatchWindow         = 24 * time.Hour
	dispatchClockSkew      = 5 * time.Minute
	dispatchBucketSlots    = 128
	legacyFilterBytes      = 4096
	dispatchBucketMaxBytes = 24 << 10
)

var (
	errStaleDispatch    = errors.New("executor dispatch expired or already acknowledged")
	errDispatchCapacity = errors.New("executor receipt bucket is full")
	errInvalidDispatch  = errors.New("invalid executor dispatch issuance")
)

func dispatchExpiry(id string) (int64, error) {
	local := id
	if rest, scoped := strings.CutPrefix(id, "ot1."); scoped {
		_, local, _ = strings.Cut(rest, ".")
	}
	stamp, timed := strings.CutPrefix(local, "t1_")
	if !timed {
		return 0, nil // pre-upgrade opaque ID
	}
	stamp, nonce, ok := strings.Cut(stamp, "_")
	issued, err := strconv.ParseInt(stamp, 10, 64)
	if !ok || err != nil || issued <= 0 || issued > 253402300799 || stamp != strconv.FormatInt(issued, 10) || len(nonce) != 32 {
		return 0, errInvalidDispatch
	}
	if _, err := hex.DecodeString(nonce); err != nil {
		return 0, errInvalidDispatch
	}
	return issued + int64(dispatchWindow/time.Second), nil
}

// One shard holds at most 128 exact pending/unexpired claims. A zero deadline
// means unresolved and cannot be evicted. Opaque legacy IDs cannot prove age:
// preserve their acknowledged membership in a fixed, non-rotating Bloom filter.
// A possible collision gives an explicit refusal, never a successful dispatch.
type dispatchBucket struct {
	Clock    int64            `json:"clock"`
	Imported int64            `json:"imported,omitempty"`
	Legacy   dispatchFilter   `json:"legacy,omitempty"`
	V1       dispatchFilter   `json:"v1,omitempty"`
	Claims   map[string]int64 `json:"claims"`
}

type dispatchFilter []byte

func (filter *dispatchFilter) match(key [32]byte, add bool) bool {
	if len(*filter) == 0 {
		if !add {
			return false
		}
		*filter = make([]byte, legacyFilterBytes)
	}
	match := true
	for i := 1; i <= 11; i += 2 { // independent 15-bit locations in this shard
		bit := int(binary.BigEndian.Uint16(key[i:i+2])) % (legacyFilterBytes * 8)
		mask := byte(1 << (bit % 8))
		if (*filter)[bit/8]&mask == 0 {
			match = false
		}
		if add {
			(*filter)[bit/8] |= mask
		}
	}
	return match
}

func (b *dispatchBucket) save(path string) error {
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	if len(raw) > dispatchBucketMaxBytes {
		return errDispatchCapacity
	}
	return writeOutboxFile(path, raw)
}

func loadDispatchBucket(path string) (*dispatchBucket, error) {
	b := new(dispatchBucket)
	f, err := os.Open(path)
	if err == nil {
		defer f.Close()
		raw, err := io.ReadAll(io.LimitReader(f, dispatchBucketMaxBytes+1))
		if err != nil {
			return nil, err
		}
		if len(raw) > dispatchBucketMaxBytes || json.Unmarshal(raw, b) != nil || b.Claims == nil || b.Clock < 0 ||
			len(b.Claims) > dispatchBucketSlots || b.Imported < 0 || b.Imported%sha256.Size != 0 ||
			(len(b.Legacy) != 0 && len(b.Legacy) != legacyFilterBytes) || (len(b.V1) != 0 && len(b.V1) != legacyFilterBytes) || (b.Imported > 0 && len(b.V1) == 0) {
			return nil, errors.New("invalid executor receipt bucket")
		}
	} else if os.IsNotExist(err) {
		b.Claims = make(map[string]int64)
	} else {
		return nil, err
	}

	// v1 did not distinguish acknowledged from unknown. Keep the original
	// evidence untouched, stream each digest once under the same claims.lock.
	legacy, err := os.Open(strings.TrimSuffix(path, ".v2") + ".ids")
	if os.IsNotExist(err) {
		return b, nil
	}
	if err != nil {
		return nil, err
	}
	defer legacy.Close()
	info, err := legacy.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() < b.Imported || info.Size()%sha256.Size != 0 {
		return nil, errors.New("incomplete v1 dispatch journal")
	}
	if info.Size() == b.Imported {
		return b, nil
	}
	if _, err = legacy.Seek(b.Imported, io.SeekStart); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(legacy)
	var key [sha256.Size]byte
	for b.Imported < info.Size() {
		if _, err = io.ReadFull(reader, key[:]); err != nil {
			return nil, err
		}
		b.V1.match(key, true)
		b.Imported += sha256.Size
	}
	return b, b.save(path)
}

func (s *Server) ackDispatch(id string) error {
	_, err := s.updateDispatch(id, time.Now(), true)
	return err
}

func (s *Server) withDispatchBucket(shard byte, create bool, change func(*dispatchBucket) (bool, error)) error {
	dir := s.cfg.IdentityDir
	if dir == "" {
		dir = s.cfg.OutboxDir
	}
	if dir == "" {
		dir = s.cfg.SpillDir
	}
	if dir == "" {
		return errors.New("no durable executor receipt directory")
	}
	journal := filepath.Join(dir, "shellmcp-dispatch")
	path := filepath.Join(journal, fmt.Sprintf("%02x.v2", shard))
	if !create {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return nil
		} else if err != nil {
			return err
		}
	}
	if err := os.MkdirAll(journal, 0700); err != nil {
		return err
	}
	lock := flock.New(filepath.Join(journal, "claims.lock"), flock.SetPermissions(0600))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	defer lock.Close()
	if held, err := lock.TryLockContext(ctx, 10*time.Millisecond); err != nil {
		return err
	} else if !held {
		return errors.New("executor receipt journal busy")
	}
	b, err := loadDispatchBucket(path)
	if err != nil {
		return err
	}
	dirty, err := change(b)
	if err != nil || !dirty {
		return err
	}
	if err := b.save(path); err != nil {
		return err
	}
	return syncReceiptDirectory(dir)
}

func (b *dispatchBucket) expire(clock int64) bool {
	before := len(b.Claims)
	for key, deadline := range b.Claims {
		if deadline != 0 && deadline <= clock {
			delete(b.Claims, key)
		}
	}
	if before == len(b.Claims) {
		return false
	}
	b.Clock = max(clock, b.Clock)
	return true
}

// One existing shard each 15 seconds bounds idle maintenance to one small read
// per tick. Using wall time spreads cleanup over shards across process restarts.
func (s *Server) pruneDispatchShard(now time.Time) error {
	return s.withDispatchBucket(byte(now.Unix()/15), false, func(b *dispatchBucket) (bool, error) {
		return b.expire(max(now.Unix(), b.Clock)), nil
	})
}

func (s *Server) updateDispatch(id string, now time.Time, ack bool) (bool, error) {
	if !safeReceiptID(id) {
		return false, errors.New("invalid executor job ID")
	}
	expiry, err := dispatchExpiry(id)
	if err != nil {
		return false, err
	}
	key := sha256.Sum256([]byte(id))
	var seen bool
	err = s.withDispatchBucket(key[0], true, func(b *dispatchBucket) (bool, error) {
		fingerprint := hex.EncodeToString(key[:])
		clock := max(now.Unix(), b.Clock)
		if !ack {
			if expiry != 0 && expiry <= clock {
				return false, errStaleDispatch
			}
			if _, exists := b.Claims[fingerprint]; exists {
				seen = true
				return false, nil
			}
			if b.V1.match(key, false) || expiry == 0 && b.Legacy.match(key, false) {
				return false, errStaleDispatch
			}
			if expiry > now.Add(dispatchClockSkew+dispatchWindow).Unix() {
				return false, errInvalidDispatch
			}
			if s.cfg.OutboxDir != "" {
				_, err := os.Lstat(filepath.Join(s.cfg.OutboxDir, id+".json"))
				if err == nil {
					seen = true
					return false, nil
				}
				if !os.IsNotExist(err) {
					return false, err
				}
			}
		}
		b.expire(clock)
		// Persist the high-water clock in the same atomic replacement as any
		// eviction. Rolling the wall clock back cannot make that ID executable.
		b.Clock = clock
		switch {
		case ack && expiry == 0:
			b.Legacy.match(key, true)
			delete(b.Claims, fingerprint)
		case ack && expiry <= clock:
			delete(b.Claims, fingerprint)
		default:
			if _, exists := b.Claims[fingerprint]; !exists && len(b.Claims) >= dispatchBucketSlots {
				return false, errDispatchCapacity
			}
			b.Claims[fingerprint] = 0
			if ack {
				b.Claims[fingerprint] = expiry
			}
		}
		return true, nil
	})
	return seen, err
}

func safeReceiptID(id string) bool {
	return id != "" && len(id) <= 245 && filepath.IsLocal(id) && filepath.Base(id) == id &&
		id != "." && !strings.ContainsAny(id, "/\\\x00\r\n")
}

func syncReceiptDirectory(dir string) error {
	// Windows does not support FlushFileBuffers on directory handles.
	if runtime.GOOS == "windows" {
		return nil
	}
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// Keep admission and callback registration in the same drain critical section.
func (s *Server) startBoundCallback(id string, acceptedPoll bool, run func(context.Context)) (bool, error) {
	s.callbackMu.Lock()
	defer s.callbackMu.Unlock()
	if s.callbackClosed || (s.queueDraining && !acceptedPoll) {
		return false, errors.New("executor is stopping")
	}
	seen, err := s.updateDispatch(id, time.Now(), false)
	if err != nil || seen {
		return seen, err
	}
	ctx, finish, _ := s.beginCallbackLocked(id, acceptedPoll)
	go func() { defer finish(); run(ctx) }()
	return false, nil
}
