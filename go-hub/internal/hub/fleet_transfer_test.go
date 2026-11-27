package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseFleetTransferAllowlistRequiresCanonicalDistinctPairs(t *testing.T) {
	allowed, err := parseFleetTransferAllowlist(`[ {"source":"file:node-a","destination":"file:node-b"} ]`)
	if err != nil || len(allowed) != 1 {
		t.Fatalf("valid allowlist rejected: %v %#v", err, allowed)
	}
	for _, raw := range []string{
		`[{"source":"shell:node-a","destination":"file:node-b"}]`,
		`[{"source":"file:server01","destination":"file:node-b"}]`,
		`[{"source":"file:node-a","destination":"file:node-a"}]`,
		`[{"source":"file:node-a","destination":"file:node-b","extra":true}]`,
	} {
		if _, err := parseFleetTransferAllowlist(raw); err == nil {
			t.Fatalf("invalid allowlist accepted: %s", raw)
		}
	}
}

func TestFleetTransferRefusesPairOutsideAllowlist(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir(), FleetTransferAllowlistJSON: `[{"source":"file:source-node","destination":"file:destination-node"}]`})
	defer s.Close()

	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{}`))
	result, status := s.fleetTransferForRequest(r, map[string]any{
		"source":           "file:destination-node",
		"destination":      "file:source-node",
		"source_path":      "/srv/source.bin",
		"destination_path": "/srv/destination.bin",
	})
	if status != http.StatusForbidden || firstString(result, "error") != "fleet transfer pair is not allowlisted" {
		t.Fatalf("unallowlisted pair was not refused: status=%d result=%#v", status, result)
	}
}

func TestFleetTransferIsPublishedAndUsesKnownWriteClassification(t *testing.T) {
	found := false
	for _, tool := range hubTools() {
		if firstString(tool, "name") == fleetTransferToolName {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("fleet_transfer missing from Hub tool schema")
	}
	if !knownWriteOperation("hub", fleetTransferToolName, nil) || readOnlyHubToolAllowed(fleetTransferToolName) {
		t.Fatal("fleet_transfer is not classified as a write-only Hub operation")
	}

	s := New(Config{ConfigDir: t.TempDir()})
	defer s.Close()
	result := mapValue(s.appsSDKCallRawForRequest(nil, fleetTransferToolName, map[string]any{
		"source":           "file:source-node",
		"destination":      "file:destination-node",
		"source_path":      "/srv/source.bin",
		"destination_path": "/srv/destination.bin",
	}))
	if firstString(result, "error") == "unsupported hub tool" {
		t.Fatal("fleet_transfer still uses the old unsupported-tool path")
	}
}

func TestFleetTransferUsesConfiguredShellPeerRouteForFileTarget(t *testing.T) {
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["target"] != "file:node-b" || body["tool"] != "file_transfer" || r.Header.Get("X-Gptadmin-Node-Hop") != "1" {
			t.Fatalf("peer received wrong route: %#v headers=%v", body, r.Header)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "completed",
			"response": map[string]any{
				"structuredContent": map[string]any{
					"transfer": map[string]any{"transfer_id": "peer-transfer", "size": int64(3), "sha256": fleetTransferDigest([]byte("abc"))},
				},
			},
		})
	}))
	defer peer.Close()
	s := peerTestHub(t, "peer-transfer-token", map[string]string{"shell:node-b": peer.URL})
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{}`))
	r.Header.Set("Authorization", "Bearer peer-transfer-token")
	payload, status, err := s.fleetTransferFileCall(r, "file:node-b", map[string]any{"action": "download_begin", "path": "/srv/a.bin"})
	if err != nil || status != http.StatusOK || firstString(payload, "transfer_id") != "peer-transfer" {
		t.Fatalf("peer file route failed: status=%d payload=%#v err=%v", status, payload, err)
	}
}
