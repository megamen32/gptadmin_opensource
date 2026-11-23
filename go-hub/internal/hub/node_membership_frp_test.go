package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFRPRelayJoinRecoversPowerDNSFailure(t *testing.T) {
	t.Setenv("GPTADMIN_FRP_DNS_PROVIDER", "")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	voters := authorityTestVoters(t, ctx)
	leader := voters[0]

	var patches atomic.Int32
	var rrsets []frpDNSRRset
	var zoneMu sync.Mutex
	dns := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/servers/localhost/zones/relay.test." || r.Header.Get("X-API-Key") != "fixture-key" {
			t.Errorf("unexpected PowerDNS request %s", r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		zoneMu.Lock()
		defer zoneMu.Unlock()
		switch r.Method {
		case http.MethodGet:
			if rrsets == nil {
				json.NewEncoder(w).Encode(frpDNSZone{RRsets: []frpDNSRRset{{Name: "frp-members.relay.test.", Type: "A",
					Records: []frpDNSRecord{{Content: "127.0.0.21"}}}}})
				return
			}
			json.NewEncoder(w).Encode(frpDNSZone{RRsets: rrsets})
		case http.MethodPatch:
			if leader.membershipMu.TryLock() {
				leader.membershipMu.Unlock()
			} else {
				t.Error("membership lock held during PowerDNS request")
			}
			if patches.Add(1) == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			var change frpDNSZone
			if err := json.NewDecoder(r.Body).Decode(&change); err != nil {
				t.Errorf("decode PowerDNS patch: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			rrsets = change.RRsets
			want := []frpDNSRecord{{Content: "127.0.0.21"}, {Content: "127.0.0.22"}}
			if len(rrsets) != 2 || rrsets[0].ChangeType != "REPLACE" || rrsets[1].ChangeType != "REPLACE" ||
				!slices.Equal(rrsets[0].Records, want) || len(rrsets[1].Records) != 1 ||
				!strings.Contains(rrsets[1].Records[0].Content, "stringmatch='gptadmin-go-hub'") {
				t.Error("unexpected PowerDNS patch")
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer dns.Close()
	t.Setenv("GPTADMIN_FRP_DNS_API_URL", dns.URL)
	t.Setenv("GPTADMIN_FRP_DNS_API_KEY", "fixture-key")
	t.Setenv("GPTADMIN_FRP_DNS_ZONE", "relay.test")
	t.Setenv("GPTADMIN_FRP_DNS_HEALTH_URL", "http://frp.relay.test:9080/version")

	leader.cfg.CtlToken = "fixture-owner"
	grantResponse := membershipHTTP(leader, http.MethodPost, "/admin/api/nodes/grant", "fixture-owner",
		[]byte(`{"name":"shell:frp-retry","frp_relay_ipv4":"127.0.0.22"}`))
	if grantResponse.Code != http.StatusOK {
		t.Fatalf("grant HTTP %d: %s", grantResponse.Code, grantResponse.Body)
	}
	var grant struct{ Grant string }
	if json.Unmarshal(grantResponse.Body.Bytes(), &grant) != nil || grant.Grant == "" {
		t.Fatal("missing grant")
	}
	nodeID, nodeKey := strings.Repeat("ab", 16), strings.Repeat("cd", 32)
	join := func(hub *Server, id, key string) *httptest.ResponseRecorder {
		body, err := json.Marshal(map[string]any{
			"node_id": id, "node_key": key, "name": "shell:frp-retry", "connect_to": "127.0.0.99",
			"frp_relay_ipv4": "127.0.0.99", "endpoints": []string{"http://127.0.0.99:9080"},
		})
		if err != nil {
			t.Fatal(err)
		}
		return membershipHTTP(hub, http.MethodPost, "/admin/api/nodes/join", grant.Grant, body)
	}
	if r := join(leader, nodeID, nodeKey); r.Code != http.StatusServiceUnavailable ||
		!strings.Contains(r.Body.String(), "frp_dns_publication_failed") || patches.Load() != 1 {
		t.Fatalf("failed provider was not observable: HTTP %d: %s, patches=%d", r.Code, r.Body, patches.Load())
	}
	if r := join(leader, strings.Repeat("ac", 16), nodeKey); r.Code != http.StatusForbidden {
		t.Fatalf("grant reuse by a different identity: HTTP %d", r.Code)
	}
	if r := join(leader, nodeID, strings.Repeat("ce", 32)); r.Code != http.StatusForbidden {
		t.Fatalf("grant reuse with a different key: HTTP %d", r.Code)
	}
	for _, want := range []int32{2, 2} {
		r := join(leader, nodeID, nodeKey)
		if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"generation":1`) || patches.Load() != want {
			t.Fatalf("join replay: HTTP %d: %s, patches=%d want %d", r.Code, r.Body, patches.Load(), want)
		}
	}
	heartbeat := membershipHTTP(leader, http.MethodPost, "/admin/api/nodes/heartbeat", nodeKey,
		[]byte(`{"state":"joining","storage_ok":true}`))
	if heartbeat.Code != http.StatusOK || patches.Load() != 2 {
		t.Fatalf("heartbeat: HTTP %d: %s, patches=%d", heartbeat.Code, heartbeat.Body, patches.Load())
	}

}

func TestFRPRelayJoinRequiresQuorum(t *testing.T) {
	writer := membershipWriterNode(t, "frp-writer")
	response := membershipHTTP(writer, http.MethodPost, "/admin/api/nodes/grant", "fixture-owner",
		[]byte(`{"name":"shell:frp-member","frp_relay_ipv4":"127.0.0.22"}`))
	if response.Code != http.StatusOK {
		t.Fatalf("grant HTTP %d", response.Code)
	}
	var grant struct{ Grant string }
	if json.Unmarshal(response.Body.Bytes(), &grant) != nil || grant.Grant == "" {
		t.Fatal("missing grant")
	}
	payload := membershipJoin(t, writer, grant.Grant, strings.Repeat("ab", 16), strings.Repeat("cd", 32),
		"shell:frp-member", "http://127.0.0.22")
	if payload["__status"] != http.StatusServiceUnavailable || payload["detail"] != errFRPJoinLeader.Error() {
		t.Fatalf("unexpected non-quorum response: %v", payload)
	}
}

func TestFRPPowerDNSAPITransport(t *testing.T) {
	for _, url := range []string{"http://127.0.0.1:8081", "http://[::1]:8081", "https://dns.test"} {
		if !validFRPDNSAPIURL(url) {
			t.Errorf("rejected %s", url)
		}
	}
	for _, url := range []string{"http://dns.test", "http://192.0.2.1", "https://a:b@dns.test", "https://dns.test/?key=foo"} {
		if validFRPDNSAPIURL(url) {
			t.Errorf("accepted %s", url)
		}
	}
}
