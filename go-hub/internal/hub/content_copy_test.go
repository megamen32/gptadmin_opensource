package hub

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestStandbyContentDeleteFencesDelayedCopy(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "private.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(copyContentDDL); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Second)
	id, _ := encodeOwnerHandle("content", "physical-a", "unique")
	meta := ContentSource{ID: id, Origin: contentFreeTextOrigin, Format: "text", Bytes: 4, Chunks: 1, CreatedAt: now, ExpiresAt: now.Add(time.Hour), Complete: true}
	put := contentCopy{Generation: "test", Principal: "principal-a", ID: id, Expires: meta.ExpiresAt.Unix(), Source: &meta, Chunks: []copyChunk{{1, 1, 1, "body"}}}
	if !validContentCopy(put, defaultExecContentLimits) {
		t.Fatal("valid source rejected")
	}
	if err = storeContentCopy(db, put, now); err != nil {
		t.Fatal(err)
	}
	meta.Indexed = true
	if err = storeContentCopy(db, put, now); err != nil {
		t.Fatal(err)
	}
	var indexed bool
	if err = db.QueryRow("SELECT json_extract(body,'$.source.indexed') FROM content_standby").Scan(&indexed); err != nil || !indexed {
		t.Fatalf("lazy index did not advance: %v", err)
	}
	del := contentCopy{Generation: put.Generation, Principal: put.Principal, Retire: []contentRetirement{{id, put.Expires}}}
	if err = storeContentCopy(db, del, now); err != nil {
		t.Fatal(err)
	}
	if err = storeContentCopy(db, put, now); err != nil {
		t.Fatal(err)
	}
	var tomb bool
	if err = db.QueryRow("SELECT body IS NULL FROM content_standby WHERE id=?", id).Scan(&tomb); err != nil || !tomb {
		t.Fatalf("standby source revived: %v", err)
	}
	if err = storeContentCopy(db, put, meta.ExpiresAt); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err = db.QueryRow("SELECT count(*) FROM content_standby").Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("expired source/tombstone retained: %d, %v", rows, err)
	}
}

func TestNewContentCannotEvictWithoutStandbyInvalidation(t *testing.T) {
	lim := ContentLimits{MaxSources: 1, MaxSourceBytes: 64, MaxTotalBytes: 128, TTL: time.Hour}
	st, err := OpenContentStore(":memory:", lim)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.ownerID = func() string { return "physical-a" }
	if _, err = st.db.Exec("ALTER TABLE content_sources ADD COLUMN standby_generation TEXT NOT NULL DEFAULT ''"); err != nil {
		t.Fatal(err)
	}
	st.standby = &contentCopyHooks{generation: "gen", owner: "physical-a", put: func(ctx context.Context, c contentCopy) error {
		if c.Source == nil {
			return errors.New("standby unavailable")
		}
		return nil
	}}
	first, err := st.SaveFormatted("p", codeModeResultSourceName, contentFreeTextOrigin, "text", "first")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.SaveFormatted("p", codeModeResultSourceName, contentFreeTextOrigin, "text", "second"); err == nil {
		t.Fatal("uncertain eviction accepted")
	}
	if err = st.Delete("p", first.ID); err == nil {
		t.Fatal("unconfirmed delete accepted")
	}
	if err = st.Delete("foreign", first.ID); err != nil {
		t.Fatal(err)
	}
	text, err := st.Read("p", first.ID, 1, 0, 0, true)
	if err != nil || text != "first" {
		t.Fatalf("previous authoritative content lost: %q, %v", text, err)
	}
}

func TestContentUnavailableStandbyKeepsOriginalHandle(t *testing.T) {
	st, err := OpenContentStore(":memory:", ContentLimits{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.ownerID = func() string { return "owner" }
	st.standby = &contentCopyHooks{generation: "gen", owner: "owner", put: func(context.Context, contentCopy) error { return errors.New("standby offline") }}
	meta, err := st.SaveFormatted("caller", "stored", contentFreeTextOrigin, "text", "unique existing output")
	if err != nil || meta.ID == "" || meta.Durability != "owner_only" {
		t.Fatalf("original handle lost: %#v, %v", meta, err)
	}
	if v, err := st.Read("caller", meta.ID, 1, 0, 0, true); err != nil || v != "unique existing output" {
		t.Fatalf("retained owner copy: %q, %v", v, err)
	}
}

func TestContentRetirementAllowsConcurrentSQLiteWork(t *testing.T) {
	for _, op := range []string{"delete", "evict-two"} {
		t.Run(op, func(t *testing.T) {
			st, err := OpenContentStore(filepath.Join(t.TempDir(), "content.sqlite"), ContentLimits{MaxSourceBytes: 10, MaxTotalBytes: 10, MaxSources: 8, TTL: time.Hour})
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			st.ownerID = func() string { return "owner" }
			if _, err = st.db.Exec("ALTER TABLE content_sources ADD COLUMN standby_generation TEXT NOT NULL DEFAULT ''"); err != nil {
				t.Fatal(err)
			}
			ready := make(chan int, 2)
			release := make(chan struct{})
			releaseOnce := new(sync.Once)
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			st.standby = &contentCopyHooks{generation: "gen", owner: "owner", put: func(ctx context.Context, c contentCopy) error {
				if len(c.Retire) > 0 {
					ready <- len(c.Retire)
					<-release
				}
				return nil
			}}
			first, err := st.SaveFormatted("A", codeModeResultSourceName, contentFreeTextOrigin, "text", "first")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = st.SaveFormatted("A", codeModeResultSourceName, contentFreeTextOrigin, "text", "again"); err != nil {
				t.Fatal(err)
			}
			b, err := st.SaveFormatted("B", "other", contentFreeTextOrigin, "text", "B")
			if err != nil {
				t.Fatal(err)
			}
			finished := make(chan error, 1)
			go func() {
				if op == "delete" {
					finished <- st.Delete("A", first.ID)
				} else {
					_, e := st.SaveFormatted("A", codeModeResultSourceName, contentFreeTextOrigin, "text", "0123456789")
					finished <- e
				}
			}()
			select {
			case n := <-ready:
				want := 1
				if op == "evict-two" {
					want = 2
				}
				if n != want {
					t.Fatalf("private request retired %d sources, want %d", n, want)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("retirement never reached transport")
			}
			// Hold the private ACK. The same single SQLite connection must
			// still serve an unrelated reader and commit an unrelated writer.
			other := make(chan error, 1)
			go func() {
				result, e := st.Read("B", b.ID, 1, 0, 0, true)
				if e == nil && result != "B" {
					e = errors.New("unrelated read mismatch")
				}
				if e == nil {
					_, e = st.Index("B", "new", contentFreeTextOrigin, "BB")
				}
				other <- e
			}()
			select {
			case e := <-other:
				if e != nil {
					t.Fatal(e)
				}
			case <-time.After(time.Second):
				unblock()
				<-finished
				t.Fatal("peer blocked an unrelated SQLite reader or writer")
			}
			unblock()
			if e := <-finished; e != nil {
				t.Fatal(e)
			}
			if _, e := st.Source("A", first.ID); !errors.Is(e, sql.ErrNoRows) {
				t.Fatal("retired source survived")
			}
			if len(ready) != 0 {
				t.Fatal("retirements sent more than once")
			}
		})
	}
}

func TestContentStandbyDiskReserve(t *testing.T) {
	s := &Server{cfg: Config{ConfigDir: t.TempDir()}}
	const n = 2 << 20
	if e := s.checkCopyDisk(n); e != nil {
		t.Fatal(e)
	}
	s.quorumSnapshotFreeReserve = 1 << 62
	if e := s.checkCopyDisk(n); e == nil {
		t.Fatal("positive content admitted without the configured disk reserve")
	}
}
