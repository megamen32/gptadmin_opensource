package security

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNonceCache_FreshNonceIsAccepted(t *testing.T) {
	c := NewNonceCache(50 * time.Millisecond)
	if !c.CheckAndRemember("n1") {
		t.Fatal("expected fresh nonce to be accepted")
	}
}

func TestNonceCache_DuplicateImmediatelyRejected(t *testing.T) {
	c := NewNonceCache(50 * time.Millisecond)
	if !c.CheckAndRemember("n1") {
		t.Fatal("first use should be accepted")
	}
	if c.CheckAndRemember("n1") {
		t.Fatal("immediate replay should be rejected")
	}
}

func TestNonceCache_DifferentNoncesAreIndependent(t *testing.T) {
	c := NewNonceCache(50 * time.Millisecond)
	if !c.CheckAndRemember("a") {
		t.Fatal("a should be accepted")
	}
	if !c.CheckAndRemember("b") {
		t.Fatal("b should be accepted")
	}
	if !c.CheckAndRemember("c") {
		t.Fatal("c should be accepted")
	}
}

func TestNonceCache_AfterTTLExpiryNonceFreshAgain(t *testing.T) {
	c := NewNonceCache(50 * time.Millisecond)
	if !c.CheckAndRemember("n1") {
		t.Fatal("first use should be accepted")
	}
	time.Sleep(120 * time.Millisecond)
	if !c.CheckAndRemember("n1") {
		t.Fatal("after TTL expiry, nonce should be accepted again")
	}
}

func TestNonceCache_EmptyNonceRejected(t *testing.T) {
	c := NewNonceCache(50 * time.Millisecond)
	if c.CheckAndRemember("") {
		t.Fatal("empty nonce should be rejected")
	}
}

func TestNonceCache_DefaultTTLWhenZeroOrNegative(t *testing.T) {
	if NewNonceCache(0).ttl <= 0 {
		t.Fatal("ttl<=0 should be replaced with a sane positive default")
	}
	if NewNonceCache(-1 * time.Second).ttl <= 0 {
		t.Fatal("negative ttl should be replaced with a sane positive default")
	}
	// Should still function correctly with default TTL.
	c := NewNonceCache(0)
	if !c.CheckAndRemember("x") {
		t.Fatal("first use with default ttl should be accepted")
	}
	if c.CheckAndRemember("x") {
		t.Fatal("immediate replay with default ttl should be rejected")
	}
}

func TestNonceCache_ConcurrencyExactlyOneWinner(t *testing.T) {
	c := NewNonceCache(time.Second)
	const racers = 100
	var winners int64
	var wg sync.WaitGroup
	wg.Add(racers)
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		go func() {
			defer wg.Done()
			<-start
			if c.CheckAndRemember("race") {
				atomic.AddInt64(&winners, 1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if winners != 1 {
		t.Fatalf("expected exactly 1 winner among %d racers, got %d", racers, winners)
	}
}

func TestNonceCache_ConcurrencyDistinctNoncesAllAccepted(t *testing.T) {
	c := NewNonceCache(time.Second)
	const racers = 200
	var accepted int64
	var wg sync.WaitGroup
	wg.Add(racers)
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		i := i
		go func() {
			defer wg.Done()
			<-start
			if c.CheckAndRemember(fmt.Sprintf("nonce-%d", i)) {
				atomic.AddInt64(&accepted, 1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if accepted != racers {
		t.Fatalf("expected all %d distinct nonces to be accepted, got %d", racers, accepted)
	}
}

func TestNonceCache_BoundedMemoryAfterPrune(t *testing.T) {
	c := NewNonceCache(30 * time.Millisecond)
	const n = 5000
	for i := 0; i < n; i++ {
		if !c.CheckAndRemember(fmt.Sprintf("n-%d", i)) {
			t.Fatalf("nonce %d should be fresh", i)
		}
	}
	// Wait for all entries to expire, then force a lazy prune by inserting one more.
	time.Sleep(80 * time.Millisecond)
	if !c.CheckAndRemember("trigger-prune") {
		t.Fatal("trigger nonce should be accepted")
	}
	if got := c.size(); got > 50 {
		t.Fatalf("cache size after prune should be small (got %d)", got)
	}
}

func TestNonceCache_SizeReflectsInserts(t *testing.T) {
	c := NewNonceCache(time.Second)
	c.CheckAndRemember("a")
	c.CheckAndRemember("b")
	c.CheckAndRemember("c")
	if got := c.size(); got != 3 {
		t.Fatalf("expected size 3, got %d", got)
	}
}