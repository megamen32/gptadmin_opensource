package hub

import (
	"fmt"
	"strings"
	"testing"
)

func TestContentStorePreservesLineAtChunkBoundary(t *testing.T) {
	store, err := OpenContentStore(":memory:", ContentLimits{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, pad := range []int{3900, 3970, 3990, 3999, 4000, 4010} {
		text := strings.Repeat("a", pad) + "\nRARE_END marker=END-EXACT request=fixture-030001\n"
		source, err := store.Index("user", "boundary", "exec:index", text)
		if err != nil {
			t.Fatal(err)
		}
		hits, err := store.Search("user", source.ID, "END-EXACT", 2)
		if err != nil || len(hits) != 1 {
			t.Fatalf("padding %d hits %v error %v", pad, hits, err)
		}
		got, err := store.Read("user", source.ID, hits[0].ChunkID, 0, 0, true)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got, "RARE_END marker=END-EXACT request=fixture-030001\n") {
			t.Fatalf("padding %d split marker: %q", pad, got)
		}
		all := strings.Builder{}
		for i := 1; i <= source.Chunks; i++ {
			v, e := store.Read("user", source.ID, i, 0, 0, true)
			if e != nil {
				t.Fatal(e)
			}
			all.WriteString(v)
		}
		if all.String() != text {
			t.Fatalf("padding %d raw bytes changed", pad)
		}
	}
}
func TestContentStoreLogFirstMiddleLastExactLines(t *testing.T) {
	store, err := OpenContentStore(":memory:", ContentLimits{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	b := strings.Builder{}
	want := map[string]string{}
	for i := 0; i < 30001; i++ {
		line := fmt.Sprintf("row=%05d status=OK payload=deterministic-noise-data", i)
		if i == 0 || i == 15000 || i == 30000 {
			key := fmt.Sprintf("marker%05d", i)
			line = "ERROR " + key + " request=exact-start-middle-end"
			want[key] = line
		}
		b.WriteString(line + "\n")
	}
	source, err := store.Index("user", "log", "exec:index", b.String())
	if err != nil {
		t.Fatal(err)
	}
	for key, line := range want {
		hits, err := store.Search("user", source.ID, key, 2)
		if err != nil || len(hits) != 1 {
			t.Fatalf("%s: hits=%v err=%v", key, hits, err)
		}
		got, err := store.Read("user", source.ID, hits[0].ChunkID, 0, 0, true)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, l := range strings.Split(got, "\n") {
			if l == line {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing complete %s line in %q", key, got)
		}
	}
}
