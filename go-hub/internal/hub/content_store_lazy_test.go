package hub

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestContentLazySaveReadSearchAndRestart(t *testing.T) {
	p := filepath.Join(t.TempDir(), "content.db")
	s, err := OpenContentStore(p, ContentLimits{})
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("noise line\n", 500) + "LAZY_NEEDLE exact final line\n"
	a, err := s.SaveFormatted("owner", "log", "exec:index", "text", body)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.SaveFormatted("owner", "other", "exec:index", "text", "OTHER_NEEDLE")
	if err != nil {
		t.Fatal(err)
	}
	if a.Indexed || b.Indexed {
		t.Fatal("save eagerly indexed")
	}
	var n int
	s.db.QueryRow("SELECT count(*) FROM content_fts").Scan(&n)
	if n != 0 {
		t.Fatal("FTS created on save")
	}
	text, err := s.Read("owner", a.ID, 1, 0, 0, true)
	if err != nil || text == "" {
		t.Fatal(err)
	}
	s.db.QueryRow("SELECT count(*) FROM content_fts").Scan(&n)
	if n != 0 {
		t.Fatal("read eagerly indexed")
	}
	hits, err := s.Search("owner", a.ID, "LAZY_NEEDLE", 5)
	if err != nil || len(hits) != 1 {
		t.Fatalf("search %v %v", hits, err)
	}
	meta, _ := s.Source("owner", a.ID)
	other, _ := s.Source("owner", b.ID)
	if !meta.Indexed || other.Indexed {
		t.Fatal("materialized wrong sources")
	}
	s.db.QueryRow("SELECT count(*) FROM content_fts").Scan(&n)
	if n != a.Chunks {
		t.Fatal("unexpected FTS rows", n)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenContentStore(p, ContentLimits{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	hits, err = s.Search("owner", a.ID, "LAZY_NEEDLE", 5)
	if err != nil || len(hits) != 1 {
		t.Fatal("restart lookup", err)
	}
	s.db.QueryRow("SELECT count(*) FROM content_fts").Scan(&n)
	if n != a.Chunks {
		t.Fatal("duplicate indexing", n)
	}
	if _, err = s.Search("stranger", b.ID, "OTHER_NEEDLE", 5); err == nil {
		t.Fatal("foreign source indexed")
	}
	other, _ = s.Source("owner", b.ID)
	if other.Indexed {
		t.Fatal("foreign request changed source")
	}
}
