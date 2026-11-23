package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFRPRelayJoinSpaceWebHelper(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	leader := authorityTestVoters(t, ctx)[0]
	root := t.TempDir()
	helper := filepath.Join(root, "helper")
	creds := filepath.Join(root, "credentials")
	if err := os.WriteFile(creds, []byte("fixture-credential"), 0600); err != nil {
		t.Fatal(err)
	}
	// This executable simulates the provider process contract without DNS/network I/O.
	body := `#!/usr/bin/python3
import json, os, pathlib, sys
root = pathlib.Path(__file__).parent
assert sys.argv[1:] == ["--allow-append-a"]
assert os.environ["GPTADMIN_FRP_DNS_ZONE"] == "relay.test"
assert os.environ["GPTADMIN_FRP_DNS_SPACEWEB_NAME"] == "frp.relay.test"
assert os.environ["GPTADMIN_FRP_DNS_SPACEWEB_CREDENTIALS_FILE"] == str(root / "credentials")
assert "FRP_TEST_UNRELATED_SECRET" not in os.environ
request = json.load(sys.stdin)
assert request["name"] == "frp.relay.test"
with (root / "calls").open("a") as log:
    log.write(request["action"] + "\n")
if request["action"] == "append_a":
    assert request["value"] == "192.0.2.22"
    print("untrusted-fixture-provider-body", file=sys.stderr)
    sys.exit(3)
assert request == {"name": "frp.relay.test", "action": "read_a_pool"}
print(json.dumps({"ok": True, "values": ["192.0.2.22"] if (root / "visible").exists() else []}))
`
	if err := os.WriteFile(helper, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GPTADMIN_FRP_DNS_PROVIDER", "spaceweb")
	t.Setenv("GPTADMIN_FRP_DNS_ZONE", "relay.test")
	t.Setenv("GPTADMIN_FRP_DNS_SPACEWEB_HELPER", helper)
	t.Setenv("GPTADMIN_FRP_DNS_SPACEWEB_CREDENTIALS_FILE", creds)
	t.Setenv("GPTADMIN_FRP_DNS_SPACEWEB_NAME", "frp.relay.test")
	t.Setenv("FRP_TEST_UNRELATED_SECRET", "fixture-only")

	leader.cfg.CtlToken = "fixture-owner"
	grantResponse := membershipHTTP(leader, http.MethodPost, "/admin/api/nodes/grant", "fixture-owner",
		[]byte(`{"name":"shell:spaceweb-relay","frp_relay_ipv4":"192.0.2.22"}`))
	var grant struct{ Grant string }
	if grantResponse.Code != http.StatusOK || json.Unmarshal(grantResponse.Body.Bytes(), &grant) != nil || grant.Grant == "" {
		t.Fatalf("grant HTTP %d", grantResponse.Code)
	}
	join := func(key string, want int) {
		t.Helper()
		nodeID := strings.Repeat("ae", 16)
		payload := []byte(`{"node_id":"` + nodeID + `","node_key":"` + key +
			`","name":"shell:spaceweb-relay","connect_to":"192.0.2.99","frp_relay_ipv4":"192.0.2.99"}`)
		r := membershipHTTP(leader, http.MethodPost, "/admin/api/nodes/join", grant.Grant, payload)
		if r.Code != want || strings.Contains(r.Body.String(), "untrusted-fixture-provider-body") {
			t.Fatalf("join HTTP %d, want %d", r.Code, want)
		}
		if want == http.StatusServiceUnavailable && !strings.Contains(r.Body.String(), "frp_dns_publication_failed") {
			t.Fatal("missing publication failure")
		}
		if want == http.StatusOK && !strings.Contains(r.Body.String(), `"generation":1`) {
			t.Fatal("join did not reuse generation")
		}
	}
	nodeKey := strings.Repeat("cd", 32)
	join(nodeKey, http.StatusServiceUnavailable)
	join(strings.Repeat("ef", 32), http.StatusForbidden)
	join(nodeKey, http.StatusServiceUnavailable)
	if err := os.WriteFile(filepath.Join(root, "visible"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	join(nodeKey, http.StatusOK)
	calls, err := os.ReadFile(filepath.Join(root, "calls"))
	if err != nil || string(calls) != "append_a\nread_a_pool\nread_a_pool\nread_a_pool\n" {
		t.Fatalf("unexpected helper calls %q (%v)", calls, err)
	}
}
