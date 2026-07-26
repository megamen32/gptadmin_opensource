package hub

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNetworkProxyIssuesRelayCompatibleRoleTicketsWhenKeyConfigured(t *testing.T) {
	clock := &proxyTestClock{now: time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)}
	controller, err := NewNetworkProxyController("", clock.Now, nil)
	if err != nil {
		t.Fatal(err)
	}
	controller.SetRelayKey([]byte("0123456789abcdef0123456789abcdef"))
	capability, err := controller.Request("proxy-profile", testNetworkProxyPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Approve(capability.CapabilityID); err != nil {
		t.Fatal(err)
	}
	client, agent, err := controller.IssueStreamGrants(capability.CapabilityID, "10.20.0.8:443")
	if err != nil {
		t.Fatal(err)
	}
	for _, grant := range []ProxyStreamGrant{client, agent} {
		parts := strings.Split(grant.Token, ".")
		if len(parts) != 3 || parts[0] != "gpr1" {
			t.Fatalf("token = %q, want gpr1 ticket", grant.Token)
		}
		payload, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			t.Fatal(err)
		}
		var claims map[string]any
		if err := json.Unmarshal(payload, &claims); err != nil {
			t.Fatal(err)
		}
		if claims["role"] != grant.Role || claims["target"] != grant.Target || claims["stream_id"] != grant.StreamID {
			t.Fatalf("claims = %#v, grant = %#v", claims, grant)
		}
	}
}

func TestNetworkProxyRevokeSignalsConfiguredRelay(t *testing.T) {
	var mu sync.Mutex
	var body []byte
	relay := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/control/revoke" || request.Method != http.MethodPost {
			t.Errorf("relay request = %s %s", request.Method, request.URL.Path)
		}
		data, _ := io.ReadAll(request.Body)
		mu.Lock()
		body = data
		mu.Unlock()
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer relay.Close()

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "relay.key")
	if err := os.WriteFile(keyPath, []byte("0123456789abcdef0123456789abcdef\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	clock := &proxyTestClock{now: time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)}
	s := New(Config{Now: clock.Now, ConfigDir: dir, NetworkProxyStateFile: filepath.Join(dir, "state.json"), NetworkProxyRelayKeyFile: keyPath, NetworkProxyRelayRevokeURL: relay.URL})
	s.mu.Lock()
	s.accessProfiles["proxy-profile"] = AccessProfile{ID: "proxy-profile", AccessMode: accessModeFull, AllowedTargets: []string{"shell:proxy-1"}, AllowedTools: []string{"network_proxy_request", "network_proxy_approve"}}
	s.agents["shell:proxy-1"] = &Agent{AgentID: "shell:proxy-1", Meta: map[string]any{"approved": true}}
	s.mu.Unlock()
	capability, err := s.requestNetworkProxyCapability("proxy-profile", testNetworkProxyPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.approveNetworkProxyCapabilityForProfile("proxy-profile", capability.CapabilityID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.revokeNetworkProxyCapabilityForProfile("proxy-profile", capability.CapabilityID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := append([]byte(nil), body...)
		mu.Unlock()
		if strings.Contains(string(got), "gpr1.") {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("relay did not receive signed revoke: %s", body)
}
