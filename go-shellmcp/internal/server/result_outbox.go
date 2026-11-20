package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/hub"
)

// Keep scheduling information ahead of the potentially large result. Existing
// JSON outboxes used sorted map keys with the same order relative to payload.
type outboxReceipt struct {
	Attempts      int             `json:"attempts"`
	NextAttemptAt int64           `json:"next_attempt_at"`
	CreatedAt     int64           `json:"created_at"`
	JobID         string          `json:"job_id"`
	Payload       json.RawMessage `json:"payload"`
}

func (s *Server) storeOutbox(payload hub.TaskResult) error {
	if s.cfg.OutboxDir == "" || !safeReceiptID(payload.ID) {
		return errors.New("invalid outbox directory or receipt ID")
	}
	if err := os.MkdirAll(s.cfg.OutboxDir, 0700); err != nil {
		return err
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	entry := outboxReceipt{JobID: payload.ID, CreatedAt: time.Now().Unix(), Payload: data}
	return entry.save(filepath.Join(s.cfg.OutboxDir, payload.ID+".json"))
}

func (e *outboxReceipt) save(path string) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return writeOutboxFile(path, data)
}

func (s *Server) deliverCallbackResult(payload hub.TaskResult) {
	if s.hub == nil {
		return
	}
	if err := s.storeOutbox(payload); err != nil {
		log.Printf("executor could not persist receipt job=%s: %v", payload.ID, err)
		return
	}
	// The result is durable before the first byte of its POST. A fresh job
	// can deliver immediately even while retry workers wait for another Hub.
	ctx, cancel := s.callbackResultContext()
	defer cancel()
	s.sendOutbox(ctx, payload.ID)
}

func (s *Server) outboxLoop(ctx context.Context) {
	if s.hub == nil || s.cfg.OutboxDir == "" {
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	cleanup := time.NewTicker(15 * time.Second)
	defer cleanup.Stop()
	for {
		s.flushOutbox(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case now := <-cleanup.C:
			if err := s.pruneDispatchShard(now); err != nil {
				log.Printf("executor receipt cleanup: %v", err)
			}
		}
	}
}

type outboxRetry struct {
	Attempts      int   `json:"attempts"`
	NextAttemptAt int64 `json:"next_attempt_at"`
}

func retryPath(payloadPath string) string {
	return strings.TrimSuffix(payloadPath, ".json") + ".retry"
}

// New retries update only a bounded sidecar. Old envelopes keep their original
// inline schedule until the first new retry, including across upgrades.
func outboxSchedule(path string) (attempts int, next int64, err error) {
	f, err := os.Open(retryPath(path))
	if err == nil {
		defer f.Close()
		var retry outboxRetry
		err = json.NewDecoder(io.LimitReader(f, 256)).Decode(&retry)
		if err == nil {
			return retry.Attempts, retry.NextAttemptAt, nil
		}
	} else if !os.IsNotExist(err) {
		return 0, 0, err
	}
	f, err = os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 4096))
	if _, err = d.Token(); err != nil {
		return
	}
	for d.More() {
		var k json.Token
		if k, err = d.Token(); err != nil {
			return
		}
		switch k {
		case "attempts":
			err = d.Decode(&attempts)
		case "next_attempt_at":
			err = d.Decode(&next)
			return
		case "payload":
			return
		default:
			var unused json.RawMessage
			err = d.Decode(&unused)
		}
		if err != nil {
			return
		}
	}
	return
}

// Each physical owner has an independent retry lane. A dead owner costs one
// bounded request per pass. Poll and first delivery have no dependency on it.
func (s *Server) flushOutbox(ctx context.Context) {
	if s.hub == nil || s.cfg.OutboxDir == "" {
		return
	}
	entries, err := os.ReadDir(s.cfg.OutboxDir)
	if err != nil {
		return
	}
	groups := map[string][]string{}
	for _, e := range entries {
		if id, ok := strings.CutSuffix(e.Name(), ".retry"); ok && e.Type().IsRegular() && safeReceiptID(id) {
			// A crash after unlinking an acknowledged payload can leave its schedule.
			if _, err := os.Lstat(filepath.Join(s.cfg.OutboxDir, id+".json")); os.IsNotExist(err) {
				_ = os.Remove(filepath.Join(s.cfg.OutboxDir, e.Name()))
			}
			continue
		}
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !e.Type().IsRegular() || !safeReceiptID(id) {
			continue
		}
		group := ""
		if rest, ok := strings.CutPrefix(id, "ot1."); ok {
			group, _, _ = strings.Cut(rest, ".")
		}
		groups[group] = append(groups[group], id)
	}
	var wg sync.WaitGroup
	limit := make(chan struct{}, 4)
	for _, ids := range groups {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case limit <- struct{}{}:
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-limit }()
			for _, id := range ids {
				if ctx.Err() != nil {
					return
				}
				if !s.sendOutbox(ctx, id) {
					return
				}
			}
		}()
	}
	wg.Wait()
}

func (s *Server) sendOutbox(ctx context.Context, id string) bool {
	if _, busy := s.outboxSending.LoadOrStore(id, true); busy {
		return true
	}
	defer s.outboxSending.Delete(id)
	path := filepath.Join(s.cfg.OutboxDir, id+".json")
	attempts, next, _ := outboxSchedule(path)
	if next > time.Now().Unix() {
		return true
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return true
	}
	var entry outboxReceipt
	var identity struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(data, &entry) != nil || entry.JobID != id ||
		json.Unmarshal(entry.Payload, &identity) != nil || identity.ID != id {
		return true
	}
	if entry.NextAttemptAt > time.Now().Unix() {
		return true
	}
	attempts = max(attempts, entry.Attempts)
	// Bound only network time, not the scheduler's ability to poll new work.
	postCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	err = s.hub.PostResultJSON(postCtx, s.cfg.Name, id, entry.Payload)
	cancel()
	if err == nil {
		// Keep the outbox as replay evidence until the terminal marker is
		// durable. Losing an ACK can cause another POST, never another start.
		if err = s.ackDispatch(id); err != nil {
			log.Printf("cannot commit executor acknowledgement job=%s: %v", id, err)
			return false
		}
		if err = os.Remove(path); err == nil {
			_ = os.Remove(retryPath(path))
			s.outboxDelivered.Add(1)
			_ = syncReceiptDirectory(s.cfg.OutboxDir)
		}
		return true
	}
	if ctx.Err() != nil {
		return false
	}
	attempts++
	next = time.Now().Add(s.computeOutboxBackoff(attempts)).Unix()
	s.outboxRetryFailures.Add(1)
	schedule, _ := json.Marshal(outboxRetry{Attempts: attempts, NextAttemptAt: next})
	if err = writeOutboxFile(retryPath(path), schedule); err != nil {
		log.Printf("cannot persist receipt retry job=%s: %v", id, err)
	}
	return false
}
