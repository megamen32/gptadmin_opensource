package hub

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSingleWriterHandoffPreservesReplicatedAuth(t *testing.T) {
	primary := authContinuityNode(t, "writer", "")
	reader := authContinuityNode(t, "reader", "writer-identity")
	token, _, err := primary.issueManagedMCPToken("ordinary", 7, primary.cfg.PublicOrigin, primary.cfg.MCPResource)
	if err != nil {
		t.Fatal(err)
	}
	registration := authContinuityHTTP(primary, "POST", "/register", "", []byte(`{"redirect_uris":["http://127.0.0.1:49173/callback"],"client_name":"handoff-probe"}`))
	if registration.Code != 201 {
		t.Fatalf("register: %d %s", registration.Code, registration.Body.String())
	}
	var client map[string]any
	if err = json.Unmarshal(registration.Body.Bytes(), &client); err != nil {
		t.Fatal(err)
	}
	exported := authContinuityHTTP(primary, "POST", "/admin/api/auth-snapshot/export", "fixture-owner", nil)
	if exported.Code != 200 {
		t.Fatalf("export %d", exported.Code)
	}
	applied := authContinuityHTTP(reader, "POST", "/admin/api/auth-snapshot/apply", "fixture-owner", exported.Body.Bytes())
	if applied.Code != 200 {
		t.Fatalf("apply %d: %s", applied.Code, applied.Body.String())
	}
	cfg := reader.cfg
	if err = reader.Close(); err != nil {
		t.Fatal(err)
	}
	cfg.AuthMode = "writer"
	cfg.AuthSourceID = ""
	promoted := New(cfg)
	defer promoted.Close()
	if err = promoted.RegisterLocalExecutor("reader", map[string]string{"server_id": "reader-identity", "public_key": "reader-public", "fingerprint": "reader-fingerprint"}, strings.Repeat("x", 32)); err != nil {
		t.Fatal(err)
	}
	if err = promoted.InitializeAuthContinuity(); err != nil {
		t.Fatal(err)
	}
	if promoted.authState.ReadOnly || promoted.authState.WriterID != "reader-identity" {
		t.Fatalf("writer state %+v", promoted.authState)
	}
	if got := authContinuityHTTP(promoted, "GET", "/mcp", token, nil); got.Code == 401 {
		t.Fatal("prior managed token rejected")
	}
	id, _ := client["client_id"].(string)
	if _, ok := promoted.oauthClients[id]; !ok {
		t.Fatal("registered client lost during handoff")
	}
	r := authContinuityHTTP(promoted, "POST", "/register", "", []byte(`{"redirect_uris":["http://127.0.0.1:49174/callback"],"client_name":"new-writer-probe"}`))
	if r.Code != 201 {
		t.Fatalf("new writer cannot register: %d %s", r.Code, r.Body.String())
	}
}
