// LHC:COVERS go-shellmcp/internal/server/server.go
package server

import (
	"sync"
	"testing"
	"time"
)

// Heartbeat and queue responses can publish settings concurrently. Exercise
// their shared update path together with the retry/backoff consumers, without
// networking, filesystem cleanup or self-update side effects.
func TestRuntimeSettingsConcurrentApplyAndRead(t *testing.T) {
	s := New(Config{QueueTimeout: 55, QueueRetry: time.Second,
		SpoolRetention: time.Hour, OutboxBackoffBase: time.Second, OutboxBackoffCap: time.Second})
	defer s.Close()
	settings := func(n int) map[string]any {
		return map[string]any{
			"queue_long_poll_seconds":     n + 5,
			"queue_retry_seconds":         n,
			"spool_retention_hours":       n,
			"outbox_backoff_base_seconds": n,
			"outbox_backoff_cap_seconds":  n,
			"storage_max_mb":              -1, // keep this unit regression free of I/O
			"self_repair_enabled":         false,
		}
	}
	start := make(chan struct{})
	var workers sync.WaitGroup
	for _, n := range []int{1, 7} {
		v := settings(n)
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			for i := 0; i < 2000; i++ {
				s.applyHubRuntimeSettings(v)
			}
		}()
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		<-start
		for i := 0; i < 2000; i++ {
			if got := s.queueRetry(); got != time.Second && got != 7*time.Second {
				t.Errorf("queue retry=%s, want one published value", got)
				return
			}
			// Each publication has base==cap, so any other result reveals
			// a torn pair even if its individual fields are race-free.
			if got := s.computeOutboxBackoff(2); got != time.Second && got != 7*time.Second {
				t.Errorf("outbox backoff=%s, want one coherent published pair", got)
				return
			}
		}
	}()
	close(start)
	workers.Wait()
}
