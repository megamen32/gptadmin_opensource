package hub

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAuthSnapshotReserveConfiguration(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want int64
	}{{"", 1 << 20}, {"4096", 4096}, {"0", -1}, {"-1", -1}, {"invalid", -1}} {
		t.Setenv("GPTADMIN_AUTH_SNAPSHOT_RESERVE_BYTES", test.raw)
		if got := authSnapshotReserveFromEnv(); got != test.want {
			t.Fatalf("reserve %q: got %d want %d", test.raw, got, test.want)
		}
	}
}

func TestAuthReaderBudgetRefusalBeforePending(t *testing.T) {
	a := authContinuityNode(t, "writer", "")
	b := authContinuityNode(t, "reader", "writer-identity")
	token, _, err := a.issueManagedMCPToken("budget-client", 7, a.cfg.PublicOrigin, a.cfg.MCPResource)
	if err != nil {
		t.Fatal(err)
	}
	export := authContinuityHTTP(a, "POST", "/admin/api/auth-snapshot/export", "fixture-owner", nil)
	if got := authContinuityHTTP(b, "POST", "/admin/api/auth-snapshot/apply", "fixture-owner", export.Body.Bytes()); got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	before := b.authState
	paths := []string{b.authStatePath(), b.managedMCPStatePath()}
	contents := make([][]byte, len(paths))
	for i, p := range paths {
		contents[i], err = os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
	}
	b.cfg.AuthSnapshotReserveBytes = 1 << 60 // Real filesystem cannot meet this configured reserve; logic test only.
	next := authContinuityHTTP(a, "POST", "/admin/api/auth-snapshot/export", "fixture-owner", nil)
	got := authContinuityHTTP(b, "POST", "/admin/api/auth-snapshot/apply", "fixture-owner", next.Body.Bytes())
	if got.Code != 507 {
		t.Fatalf("want507, got%d %s", got.Code, got.Body.String())
	}
	if b.authState != before || b.authState.Pending {
		t.Fatal("refusal changed active state")
	}
	for i, p := range paths {
		raw, _ := os.ReadFile(p)
		if !bytes.Equal(raw, contents[i]) {
			t.Fatal("refusal changed", p)
		}
	}
	if got := authContinuityHTTP(b, "GET", "/mcp", token, nil); got.Code != 200 {
		t.Fatalf("old auth unavailable: %d", got.Code)
	}
	a.cfg.AuthSnapshotReserveBytes = 1 << 60
	if got := authContinuityHTTP(a, "POST", "/admin/api/auth-snapshot/export", "fixture-owner", nil); got.Code != 200 {
		t.Fatal("reader budget affected writer")
	}
}

func TestAuthBudgetActualEncodingAndCapacity(t *testing.T) {
	values := []any{map[string]any{"nested": map[string]string{"value": "text"}}, map[string]any{"pending": true}}
	need, err := authWriteBudget(values, 4096, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	var expected uint64
	for _, v := range values {
		raw, _ := json.MarshalIndent(v, "", "  ")
		expected += uint64((len(raw)+1+4095)/4096) * 4096
	}
	// One directory plus two lock metadata blocks, without reclaim credit.
	expected += 3 * 4096
	if need.Bytes != expected || need.Inodes != 5 {
		t.Fatalf("wrong estimate: %+v wantbytes%d inodes5", need, expected)
	}
	fs := authFilesystemSpace{Available: need.Bytes + 1024, Block: 4096, Inodes: need.Inodes, HasInodes: true}
	if err := need.admit(fs, 1024); err != nil {
		t.Fatal(err)
	}
	fs.Available--
	if need.admit(fs, 1024) == nil {
		t.Fatal("accepted insufficient bytes")
	}
	fs.Available++
	fs.Inodes--
	if need.admit(fs, 1024) == nil {
		t.Fatal("accepted insufficient inodes")
	}
	if _, err := authFilesystemAvailable(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("unknown filesystem accepted")
	}
}
