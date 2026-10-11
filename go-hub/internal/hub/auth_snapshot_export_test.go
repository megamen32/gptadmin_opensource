package hub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacyAuthSnapshotExportReadOnly(t *testing.T) {
	dir := t.TempDir()
	tokens := managedMCPTokenState{Tokens: map[string]managedMCPToken{
		"revoked": {ID: "revoked", ClientID: "ordinary", TokenKind: "configured_bearer", TokenValue: "ordinary-secret", RevokedAt: 42},
		"control": {ID: "control", TokenKind: "legacy_ctl", TokenValue: "owner-secret"},
	}}
	for name, value := range map[string]any{"mcp_tokens_state.json": tokens, oauthClientsStateFilename: oauthClientsState{}} {
		path := filepath.Join(dir, name)
		if err := withAccessStateLock(path, func() error { return writeAccessStateJSON(path, value, 1<<20) }); err != nil {
			t.Fatal(err)
		}
	}
	before := map[string][]byte{}
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		before[entry.Name()], _ = os.ReadFile(filepath.Join(dir, entry.Name()))
	}
	output := filepath.Join(t.TempDir(), "snapshot.json")
	if err := ExportLegacyAuthSnapshot(dir, "primary-id", output, 256<<10, "owner-secret"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var bundle authSnapshot
	if err = json.Unmarshal(raw, &bundle); err != nil {
		t.Fatal(err)
	}
	if bundle.Generation != 1 || bundle.WriterID != "primary-id" || bundle.Profiles != nil || len(bundle.Tokens.Tokens) != 1 || bundle.Tokens.Tokens["revoked"].RevokedAt != 42 {
		t.Fatal("bootstrap lost authority or revocation")
	}
	info, _ := os.Stat(output)
	if info.Mode().Perm() != 0600 {
		t.Fatal("output not private")
	}
	after, _ := os.ReadDir(dir)
	if len(after) != len(before) {
		t.Fatal("source files created")
	}
	for _, entry := range after {
		raw, _ := os.ReadFile(filepath.Join(dir, entry.Name()))
		if string(raw) != string(before[entry.Name()]) {
			t.Fatal("source mutated")
		}
	}
	if err := ExportLegacyAuthSnapshot(dir, "primary-id", output, 256<<10, "owner-secret"); err == nil {
		t.Fatal("existing output overwritten")
	}
	if err := ExportLegacyAuthSnapshot(dir, "primary-id", filepath.Join(t.TempDir(), "small.json"), 20, "owner-secret"); err == nil {
		t.Fatal("oversize accepted")
	}
	reader := authContinuityNode(t, "reader", "primary-id")
	if got := authContinuityHTTP(reader, "POST", "/admin/api/auth-snapshot/apply", "fixture-owner", raw); got.Code != 200 {
		t.Fatalf("bootstrap import failed: %d %s", got.Code, got.Body.String())
	}
	if got := authContinuityHTTP(reader, "POST", "/admin/api/auth-snapshot/apply", "fixture-owner", raw); got.Code != 409 {
		t.Fatal("bootstrap could overwrite a seeded reader")
	}
	// Future drained migration bootstraps the same unchanged root stores at 1;
	// its first API export must then advance beyond the bootstrap helper's 1.
	writer := New(Config{ConfigDir: dir, AuthMode: "writer", CtlToken: "owner-secret", OAuthClientSecret: "signer"})
	defer writer.Close()
	if err := writer.RegisterLocalExecutor("primary", map[string]string{"server_id": "primary-id", "public_key": "public", "fingerprint": "fingerprint"}, strings.Repeat("x", 32)); err != nil {
		t.Fatal(err)
	}
	if err := writer.InitializeAuthContinuity(); err != nil {
		t.Fatal(err)
	}
	exported := authContinuityHTTP(writer, "POST", "/admin/api/auth-snapshot/export", "owner-secret", nil)
	if exported.Code != 200 {
		t.Fatal(exported.Body.String())
	}
	if err := json.Unmarshal(exported.Body.Bytes(), &bundle); err != nil || bundle.Generation <= 1 {
		t.Fatal("future writer did not advance past bootstrap")
	}
	if got := authContinuityHTTP(reader, "POST", "/admin/api/auth-snapshot/apply", "fixture-owner", exported.Body.Bytes()); got.Code != 200 {
		t.Fatal(got.Body.String())
	}
}
