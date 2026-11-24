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
	"github.com/megamen32/gptadmin/go-shellmcp/internal/hub"
)

// A dated local ID carries the time of original issuance through every retry,
// including a retry on a different transport. The full ID is the identity.
// A later HTTP Date, a fresh poll or a field in tool arguments cannot renew it.
const (
	dispatchWindow         = 24 * time.Hour
	dispatchClockSkew      = 5 * time.Minute
	dispatchBucketSlots    = 128
	legacyFilterBytes      = 4096
	timedFilterBytes       = 1024 // two encoded windows fit alongside both legacy filters and 128 exact claims
	dispatchBucketMaxBytes = 24 << 10
)

var (
	errDispatchBusy     = errors.New("executor receipt journal busy")
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

// Zero deadlines stay exact until the owner ACKs. Under pressure, only positive
// deadlines move into bounded Bloom filters. A possible match refuses execution.
// Opaque legacy ACKs cannot prove age and keep their non-rotating filter.
type dispatchBucket struct {
	Clock    int64               `json:"clock"`
	Imported int64               `json:"imported,omitempty"`
	Legacy   dispatchFilter      `json:"legacy,omitempty"`
	V1       dispatchFilter      `json:"v1,omitempty"`
	Claims   map[string]int64    `json:"claims"`
	ACKs     []dispatchACKWindow `json:"ack_windows,omitempty"`
}

type dispatchACKWindow struct {
	Until int64          `json:"until"` // largest immutable expiry inserted
	Bits  dispatchFilter `json:"bits,omitempty"`
}

// An old v2 writer drops unknown fields. Its clock must fence every compacted
// ACK even after that rewrite. Current readers can use the actual admission
// clock while the windows are present. Both clocks and all claims commit together.
type dispatchBucketFile struct {
	dispatchBucket
	AdmissionClock *int64 `json:"admission_clock,omitempty"`
}

// At most two adjacent epochs can contain normally issued, live IDs. Even an
// outlying future ACK cannot displace live bits: only expire clears a window.
func ackWindowIndex(expiry int64) int {
	const epoch = int64((dispatchWindow + dispatchClockSkew) / time.Second)
	return int((expiry / epoch) % 2)
}

func (b *dispatchBucket) rememberTimedACK(key [sha256.Size]byte, expiry int64) {
	if len(b.ACKs) == 0 {
		b.ACKs = make([]dispatchACKWindow, 2)
	}
	w := &b.ACKs[ackWindowIndex(expiry)]
	if len(w.Bits) == 0 {
		w.Bits = make(dispatchFilter, timedFilterBytes)
	}
	w.Bits.match(key, true)
	w.Until = max(w.Until, expiry)
}

func (b *dispatchBucket) compactACKs() error {
	for fingerprint, expiry := range b.Claims {
		if expiry == 0 {
			continue
		}
		var key [sha256.Size]byte
		if expiry < 0 || len(fingerprint) != hex.EncodedLen(len(key)) {
			return errors.New("invalid acknowledged dispatch claim")
		}
		if _, err := hex.Decode(key[:], []byte(fingerprint)); err != nil {
			return err
		}
		b.rememberTimedACK(key, expiry)
		delete(b.Claims, fingerprint)
	}
	return nil
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
	for i := 1; i <= 11; i += 2 { // six locations independent of the shard byte
		bit := int(binary.BigEndian.Uint16(key[i:i+2])) % (len(*filter) * 8)
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
	disk := dispatchBucketFile{dispatchBucket: *b}
	if len(b.ACKs) != 0 {
		disk.AdmissionClock = &b.Clock
		disk.Clock = max(b.Clock, b.ACKs[0].Until, b.ACKs[1].Until)
	}
	raw, err := json.Marshal(&disk)
	if err != nil {
		return err
	}
	if len(raw) > dispatchBucketMaxBytes {
		return errDispatchCapacity
	}
	return writeOutboxFile(path, raw)
}

func (d *dispatchBucketFile) restoreAdmissionClock() bool {
	if len(d.ACKs) == 0 {
		return d.AdmissionClock == nil
	}
	if len(d.ACKs) != 2 || d.AdmissionClock == nil || *d.AdmissionClock < 0 {
		return false
	}
	for i, w := range d.ACKs {
		if w.Until == 0 && len(w.Bits) == 0 {
			continue
		}
		if w.Until <= *d.AdmissionClock || ackWindowIndex(w.Until) != i || len(w.Bits) != timedFilterBytes {
			return false
		}
	}
	if d.Clock != max(*d.AdmissionClock, d.ACKs[0].Until, d.ACKs[1].Until) {
		return false
	}
	d.Clock = *d.AdmissionClock
	return true
}

func loadDispatchBucket(path string) (*dispatchBucket, error) {
	var disk dispatchBucketFile
	b := &disk.dispatchBucket
	f, err := os.Open(path)
	if err == nil {
		defer f.Close()
		raw, err := io.ReadAll(io.LimitReader(f, dispatchBucketMaxBytes+1))
		if err != nil {
			return nil, err
		}
		if len(raw) > dispatchBucketMaxBytes || json.Unmarshal(raw, &disk) != nil || b.Claims == nil || b.Clock < 0 || !disk.restoreAdmissionClock() ||
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
	return s.changeDispatchBucket(nil, shard, create, change)
}

func (s *Server) changeDispatchBucket(wait context.Context, shard byte, create bool, change func(*dispatchBucket) (bool, error)) error {
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
	if wait == nil {
		var cancel context.CancelFunc
		wait, cancel = context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
	}
	defer lock.Close()
	if held, err := lock.TryLockContext(wait, 10*time.Millisecond); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return errors.Join(errDispatchBusy, err)
		}
		return err
	} else if !held {
		return errDispatchBusy
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
		if deadline > 0 && deadline <= clock {
			delete(b.Claims, key)
		}
	}
	dirty := before != len(b.Claims)
	for i, w := range b.ACKs {
		if w.Until != 0 && w.Until <= clock {
			b.ACKs[i] = dispatchACKWindow{}
			dirty = true
		}
	}
	if len(b.ACKs) != 0 && b.ACKs[0].Until == 0 && b.ACKs[1].Until == 0 {
		b.ACKs = nil
	}
	if dirty {
		b.Clock = max(clock, b.Clock)
	}
	return dirty
}

// One existing shard each 15 seconds bounds idle maintenance to one small read
// per tick. Using wall time spreads cleanup over shards across process restarts.
func (s *Server) pruneDispatchShard(now time.Time) error {
	return s.withDispatchBucket(byte(now.Unix()/15), false, func(b *dispatchBucket) (bool, error) {
		return b.expire(max(now.Unix(), b.Clock)), nil
	})
}

func (s *Server) updateDispatch(id string, now time.Time, ack bool) (bool, error) {
	return s.updateDispatchWithRefusal(nil, id, now, ack, nil)
}

func (s *Server) updateDispatchWithRefusal(wait context.Context, id string, now time.Time, ack bool, recordRefusal func() error) (bool, error) {
	if !safeReceiptID(id) {
		return false, errors.New("invalid executor job ID")
	}
	expiry, err := dispatchExpiry(id)
	if err != nil {
		return false, err
	}
	key := sha256.Sum256([]byte(id))
	var seen bool
	err = s.changeDispatchBucket(wait, key[0], true, func(b *dispatchBucket) (bool, error) {
		if now.IsZero() {
			now = time.Now()
		}
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
			if expiry != 0 && len(b.ACKs) != 0 {
				w := &b.ACKs[ackWindowIndex(expiry)]
				// Even a saturated filter cannot refuse an issuance later than its newest ACK.
				if expiry <= w.Until && w.Bits.match(key, false) {
					return false, errStaleDispatch
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
		case ack:
			if _, exists := b.Claims[fingerprint]; exists || len(b.Claims) < dispatchBucketSlots {
				b.Claims[fingerprint] = expiry
			} else {
				// An outbox predating the journal can ACK without an exact slot.
				b.rememberTimedACK(key, expiry)
			}
		default:
			if len(b.Claims) >= dispatchBucketSlots {
				if err := b.compactACKs(); err != nil {
					return false, err
				}
				if len(b.Claims) >= dispatchBucketSlots {
					// Polling already gave the task a running state at its owner.
					// Publish evidence of refusal under the claims lock, so a
					// concurrent retry cannot start work before the receipt exists.
					if recordRefusal != nil {
						if err := recordRefusal(); err != nil {
							return false, err
						}
					}
					return false, errDispatchCapacity
				}
			}
			b.Claims[fingerprint] = 0
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
	// Only the original polled callback owns an envelope awaiting admission.
	// A direct request must never acknowledge it before the durable claim.
	if pending := s.pendingClaim; pending != nil && pending.id == id {
		if acceptedPoll {
			return true, nil
		}
		return false, errDispatchBusy
	}
	var recordRefusal func() error
	if acceptedPoll && s.hub != nil {
		recordRefusal = func() error {
			return s.persistDispatchRefusal(id)
		}
	}
	seen, err := s.updateDispatchWithRefusal(nil, id, time.Time{}, false, recordRefusal)
	if err != nil || seen {
		return seen, err
	}
	ctx, finish, _ := s.beginCallbackLocked(id, acceptedPoll)
	go func() { defer finish(); run(ctx) }()
	return false, nil
}

type pendingDispatchClaim struct {
	id   string
	stop context.CancelFunc
}

// finishPolledAdmission retains the one envelope already leased by this queue
// loop. The next poll cannot lease more work while this claim is pending.
func (s *Server) finishPolledAdmission(queue context.Context, id string, run func(context.Context)) error {
	s.callbackMu.Lock()
	if s.callbackClosed {
		s.callbackMu.Unlock()
		return context.Canceled
	}
	if s.callbackCancels[id] != nil {
		s.callbackMu.Unlock()
		return nil
	}
	ctx, finish, _ := s.beginCallbackLocked(id, true)
	admission, stop := context.WithCancel(queue)
	s.pendingClaim = &pendingDispatchClaim{id: id, stop: stop}
	s.callbackMu.Unlock()
	defer stop()

	// Keep the cancellation of the command separate from stopping the
	// executor. Only a winning durable claim can certify non-execution.
	duplicate, err := s.updateDispatchWithRefusal(admission, id, time.Time{}, false, func() error {
		return s.persistDispatchRefusal(id)
	})
	if err != nil || duplicate {
		// Clear the callback first. A direct delivery cannot register a new
		// callback for this ID until the transient fence below is gone.
		finish()
		s.callbackMu.Lock()
		s.pendingClaim = nil
		s.callbackMu.Unlock()
		return err
	}
	s.callbackMu.Lock()
	s.pendingClaim = nil
	if admission.Err() != nil {
		s.callbackCancels[id]()
	}
	s.callbackMu.Unlock()
	go func() {
		defer finish()
		if ctx.Err() != nil {
			s.deliverCallbackResult(hub.TaskResult{ID: id, Error: map[string]any{"code": "cancelled", "executed": false}})
			return
		}
		run(ctx)
	}()
	return nil
}
