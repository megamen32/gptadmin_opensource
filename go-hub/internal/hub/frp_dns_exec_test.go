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

func TestFRPDNSExecProtocol(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "adapter")
	// Exercise the real child process and JSON boundary using a file-backed test pool.
	const script = `#!/usr/bin/python3
import json, os, pathlib, sys
root = pathlib.Path(__file__).parent
assert len(sys.argv) == 1
assert os.environ.get("FRP_TEST_UNRELATED_SECRET") is None
assert os.environ["GPTADMIN_FRP_DNS_NAME"] == "frp.relay.test"
assert os.environ["GPTADMIN_FRP_DNS_ZONE"] == "relay.test"
req = json.load(sys.stdin)
assert req["name"] == "frp.relay.test"
with (root / "calls").open("a") as log:
    log.write(req["action"] + "\n")
pool = root / "pool"
if req["action"] == "append_a":
    assert set(req) == {"name", "action", "value"}
    assert "GPTADMIN_FRP_DNS_CREDENTIALS_FILE" not in os.environ
    pool.write_text(req["value"])
    # The provider accepted the write but the process lost its reply.
    sys.exit(2)
assert req == {"name": "frp.relay.test", "action": "read_a_pool"}
if (root / "credentials").exists():
    assert pathlib.Path(os.environ["GPTADMIN_FRP_DNS_CREDENTIALS_FILE"]).read_text() == "fixture"
print(json.dumps({"ok": True, "values": [pool.read_text()]}))
`
	if err := os.WriteFile(helper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GPTADMIN_FRP_DNS_PROVIDER", "exec")
	t.Setenv("GPTADMIN_FRP_DNS_HELPER", helper)
	t.Setenv("GPTADMIN_FRP_DNS_ZONE", "Relay.TEST.")
	t.Setenv("GPTADMIN_FRP_DNS_NAME", "FRP.relay.test.")
	t.Setenv("GPTADMIN_FRP_DNS_CREDENTIALS_FILE", "")
	t.Setenv("FRP_TEST_UNRELATED_SECRET", "fixture")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	leader := authorityTestVoters(t, ctx)[0]
	leader.cfg.CtlToken = "fixture-owner"
	granted := membershipHTTP(leader, http.MethodPost, "/admin/api/nodes/grant", "fixture-owner",
		[]byte(`{"name":"shell:exec-relay","frp_relay_ipv4":"192.0.2.50"}`))
	var grant struct{ Grant string }
	if granted.Code != http.StatusOK || json.Unmarshal(granted.Body.Bytes(), &grant) != nil || grant.Grant == "" {
		t.Fatalf("grant HTTP %d", granted.Code)
	}
	join := func() {
		t.Helper()
		result := membershipJoin(t, leader, grant.Grant, strings.Repeat("ab", 16), strings.Repeat("cd", 32),
			"shell:exec-relay", "http://192.0.2.50")
		if result["__status"] != http.StatusOK {
			t.Fatalf("join failed: %v", result["detail"])
		}
	}
	join()
	credentials := filepath.Join(root, "credentials")
	if err := os.WriteFile(credentials, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GPTADMIN_FRP_DNS_CREDENTIALS_FILE", credentials)
	join()
	adapter, err := frpDNSPublisherFromEnv(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.reconcile(t.Context(), "192.0.2.51"); err == nil {
		t.Fatal("reported an unpublished relay as present")
	}
	calls, err := os.ReadFile(filepath.Join(root, "calls"))
	if err != nil || string(calls) != "append_a\nread_a_pool\nread_a_pool\nread_a_pool\n" {
		t.Fatalf("unexpected helper calls %q (%v)", calls, err)
	}
	if _, err := frpDNSPublisherFromEnv(&frpSpaceWebPool{Zone: "relay.test", Name: "frp.relay.test"}); err == nil {
		t.Fatal("exec claimed a pool explicitly approved for SpaceWeb")
	}
	for _, tt := range []struct{ key, bad string }{
		{"GPTADMIN_FRP_DNS_NAME", "frp.other.test"},
		{"GPTADMIN_FRP_DNS_NAME", "*.relay.test"},
		{"GPTADMIN_FRP_DNS_HELPER", "adapter"},
		{"GPTADMIN_FRP_DNS_CREDENTIALS_FILE", t.TempDir()},
	} {
		t.Run(strings.TrimPrefix(tt.key, "GPTADMIN_FRP_DNS_")+"/"+filepath.Base(tt.bad), func(t *testing.T) {
			t.Setenv(tt.key, tt.bad)
			if _, err := frpDNSPublisherFromEnv(nil); err == nil {
				t.Fatal("accepted invalid configuration")
			}
		})
	}
}
