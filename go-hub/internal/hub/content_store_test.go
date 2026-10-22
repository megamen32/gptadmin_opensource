package hub

import (
	"strings"
	"testing"
	"time"
)

func TestContentStoreIndexSearchIsolationAndDelete(t *testing.T) {
	s, err := OpenContentStore(":memory:", ContentLimits{MaxSourceBytes: 1 << 20, MaxTotalBytes: 2 << 20, MaxSources: 4, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	text := strings.Repeat("routine noise line\n", 500) + "RARE-ERROR: 🧭 punctuation?!\n" + strings.Repeat("tail data\n", 500)
	meta, err := s.Index("alice", "server log", "mcp:logs.read", text)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Bytes != int64(len(text)) || meta.Chunks < 2 {
		t.Fatalf("bad metadata: %+v", meta)
	}
	hits, err := s.Search("alice", meta.ID, "RARE-ERROR 🧭 punctuation?!", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("rare phrase not found")
	}
	if _, err = s.Source("bob", meta.ID); err == nil {
		t.Fatal("cross-principal source lookup succeeded")
	}
	if _, err = s.Search("bob", "", "RARE-ERROR", 10); err != nil {
		t.Fatal(err)
	} else {
		other, _ := s.Search("bob", "", "RARE-ERROR", 10)
		if len(other) != 0 {
			t.Fatal("cross-principal search leaked hit")
		}
	}
	if _, err = s.Read("alice", meta.ID, hits[0].ChunkID, 0, 0, false); err == nil {
		t.Fatal("read skipped origin revalidation gate")
	}
	got, err := s.Read("alice", meta.ID, hits[0].ChunkID, 0, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "RARE-ERROR") {
		t.Fatalf("read missing match: %q", got)
	}
	if err = s.Delete("bob", meta.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Source("alice", meta.ID); err != nil {
		t.Fatalf("foreign delete removed source: %v", err)
	}
	if err = s.Delete("alice", meta.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Source("alice", meta.ID); err == nil {
		t.Fatal("deleted source still visible")
	}
}

func TestContentStoreLimitsAndExpiry(t *testing.T) {
	s, err := OpenContentStore(":memory:", ContentLimits{MaxSourceBytes: 32, MaxTotalBytes: 36, MaxSources: 1, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.Index("p", "", "origin", strings.Repeat("x", 33)); err == nil {
		t.Fatal("source byte limit not enforced")
	}
	x, err := s.Index("p", "", "origin", "small body")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Index("p", "", "origin", "next"); err == nil {
		t.Fatal("source count limit not enforced")
	}
	if err = s.Delete("p", x.ID); err != nil {
		t.Fatal(err)
	}
	s.limits.TTL = time.Millisecond
	x, err = s.Index("p", "", "origin", "small body")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, err = s.Search("p", x.ID, "small", 5); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Source("p", x.ID); err == nil {
		t.Fatal("expired source remained visible")
	}
}

func TestContentStorePreservesRawUnicodeAndLineBoundaries(t *testing.T) {
	s, err := OpenContentStore(":memory:", ContentLimits{MaxSourceBytes: 1 << 20, MaxTotalBytes: 1 << 20, MaxSources: 4, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	text := strings.Repeat("a", 3999) + "🌍 мир\nSTART_MARK\n" + strings.Repeat("middle line\n", 900) + "END_MARK"
	x, err := s.IndexFormatted("p", "unicode", "fixture\x00large_log", "content-text", text)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Read("p", x.ID, 1, 0, 10, true)
	if err != nil {
		t.Fatal(err)
	}
	if got != text {
		t.Fatalf("chunk retrieval changed raw bytes: got %d bytes want %d", len(got), len(text))
	}
	for _, query := range []string{"START_MARK", "END_MARK", "мир"} {
		hits, err := s.Search("p", x.ID, query, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) == 0 {
			t.Fatalf("missing hit %q", query)
		}
	}
	if x.Format != "content-text" {
		t.Fatalf("format provenance missing: %+v", x)
	}
}
