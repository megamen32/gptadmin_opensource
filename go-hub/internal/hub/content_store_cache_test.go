package hub

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestContentStoreAutomaticResultsReclaimOnlyOwnUnindexedResults(t *testing.T) {
	s, err := OpenContentStore(":memory:", ContentLimits{MaxSources: 4, MaxTotalBytes: 1024, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	old, err := s.SaveFormatted("owner", codeModeResultSourceName, "shell:host\x00shell_exec", "text", "old automatic result")
	if err != nil {
		t.Fatal(err)
	}
	recent, err := s.SaveFormatted("owner", codeModeResultSourceName, "shell:host\x00shell_exec", "text", "more recent automatic result")
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := s.IndexFormatted("owner", codeModeResultSourceName, "shell:host\x00shell_exec", "text", "PINNED_NEEDLE")
	if err != nil {
		t.Fatal(err)
	}
	explicit, err := s.SaveFormatted("owner", "explicit-source", "text", "text", "keep explicit source")
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := s.SaveFormatted("other", codeModeResultSourceName, "shell:host\x00shell_exec", "text", "foreign")
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := s.SaveFormatted("owner", codeModeResultSourceName, "shell:host\x00shell_exec", "text", "fresh result")
	if err != nil {
		t.Fatalf("automatic result was blocked by stale automatic cache: %v", err)
	}
	if _, err := s.Source("owner", old.ID); err == nil {
		t.Fatal("old automatic result was not reclaimed")
	}
	for _, keep := range []ContentSource{recent, pinned, explicit, fresh} {
		if _, err := s.Source("owner", keep.ID); err != nil {
			t.Fatalf("source %q was lost: %v", keep.Name, err)
		}
	}
	if _, err := s.Source("other", foreign.ID); err != nil {
		t.Fatal("another principal's source was removed", err)
	}
	hits, err := s.Search("owner", pinned.ID, "PINNED_NEEDLE", 1)
	if err != nil || len(hits) != 1 {
		t.Fatalf("indexed source changed: %v %v", hits, err)
	}
	for _, table := range []string{"content_chunks", "content_fts"} {
		var n int
		if err := s.db.QueryRow("SELECT count(*) FROM "+table+" WHERE source_id=?", old.ID).Scan(&n); err != nil || n != 0 {
			t.Fatalf("reclaimed source left rows in %s: count=%d error=%v", table, n, err)
		}
	}
}

func TestContentStoreAutomaticResultsReclaimForBytePressure(t *testing.T) {
	s, err := OpenContentStore(":memory:", ContentLimits{MaxSources: 10, MaxTotalBytes: 12})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for range 2 {
		if _, err := s.SaveFormatted("owner", codeModeResultSourceName, "text", "text", "123456"); err != nil {
			t.Fatal(err)
		}
	}
	fresh, err := s.SaveFormatted("owner", codeModeResultSourceName, "text", "text", "12345678")
	if err != nil {
		t.Fatalf("automatic byte pressure was not reclaimed: %v", err)
	}
	sources, err := s.Sources("owner")
	if err != nil || len(sources) != 1 || sources[0].ID != fresh.ID {
		t.Fatalf("unexpected retained sources: %v %v", sources, err)
	}
}

func TestContentStoreAutomaticResultsFailedAdmissionRollsBack(t *testing.T) {
	s, err := OpenContentStore(":memory:", ContentLimits{MaxSources: 3, MaxTotalBytes: 12})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	old, err := s.SaveFormatted("owner", codeModeResultSourceName, "text", "text", "1234")
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := s.Index("owner", "explicit", "text", "12345678")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveFormatted("owner", codeModeResultSourceName, "text", "text", "1234567890"); err == nil {
		t.Fatal("unfulfillable admission exceeded the byte limit")
	}
	for _, keep := range []ContentSource{old, pinned} {
		if _, err := s.Read("owner", keep.ID, 1, 0, 0, true); err != nil {
			t.Fatalf("failed admission lost existing data: %v", err)
		}
	}
	if _, err := s.Index("owner", "explicit-new", "text", "1234567890"); err == nil {
		t.Fatal("explicit indexing exceeded its original quota")
	}
}

func TestContentStoreAutomaticResultsGlobalPressureNeverEvictsForeignSources(t *testing.T) {
	for _, own := range []bool{false, true} {
		t.Run(fmt.Sprintf("own-cache-%v", own), func(t *testing.T) {
			s, err := OpenContentStore(":memory:", ContentLimits{})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			first := 0
			if own {
				if _, err := s.SaveFormatted("owner", codeModeResultSourceName, "text", "text", "old"); err != nil {
					t.Fatal(err)
				}
				first = 1
			}
			tx, err := s.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			for i := first; i < 1000; i++ {
				_, err := tx.Exec("INSERT INTO content_sources(id,principal,name,origin,format,bytes,chunks,created,expires,indexed,complete) VALUES(?,?,?,?,?,?,?,?,?,?,?)",
					fmt.Sprintf("foreign-%d", i), "other", codeModeResultSourceName, "text", "text", 1, 0, time.Now().Unix(), time.Now().Add(time.Hour).Unix(), 0, 1)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			_, err = s.SaveFormatted("owner", codeModeResultSourceName, "text", "text", "new")
			if own && err != nil {
				t.Fatalf("own cache could not free a global slot: %v", err)
			}
			if !own && (err == nil || !strings.Contains(err.Error(), "global content storage limit")) {
				t.Fatalf("foreign-only cache should remain full: %v", err)
			}
			var foreign, total int
			if err := s.db.QueryRow("SELECT count(*) FROM content_sources WHERE principal='other'").Scan(&foreign); err != nil {
				t.Fatal(err)
			}
			if err := s.db.QueryRow("SELECT count(*) FROM content_sources").Scan(&total); err != nil {
				t.Fatal(err)
			}
			if foreign != 1000-first || total != 1000 {
				t.Fatalf("global cache changed foreign data or exceeded limit: foreign=%d total=%d", foreign, total)
			}
		})
	}
}
