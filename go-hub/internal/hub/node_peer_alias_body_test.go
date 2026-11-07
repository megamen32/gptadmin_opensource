package hub

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Exercise actual HTTP forwarding: only routing fields may change. In
// particular untyped map decoding must not round opaque caller numbers.
func TestNodePeerAliasPreservesRawArgumentNumbers(t *testing.T) {
	for _, field := range []string{"target", "server_id", "agent_id"} {
		t.Run(field, func(t *testing.T) {
			var received []byte
			peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received, _ = io.ReadAll(r.Body)
				writeJSON(w, 200, map[string]any{"job_id": "alias-body", "status": "running"})
			}))
			defer peer.Close()
			s := peerTestHub(t, "caller-token", map[string]string{"shell:nodeB": peer.URL})
			s.agents["shell:nodeB"] = &Agent{AgentID: "shell:nodeB", Kind: "shell", Status: "online", Meta: map[string]any{"server_id": "persistent-b", "fingerprint": "key-b"}}
			alias := s.shortTarget("shell:nodeB")
			body := fmt.Sprintf(` { %q:%q, "tool":"shell_exec", "arguments": {"cmd":"printf test","number":9007199254740993,"negative":-9007199254740993,"decimal":1.234567890123456789,"nested":[{"id":9007199254740995}],"text":"<literal>&"}, "opaque":9007199254740997, "background":true,"detail":"full" } `, field, alias)
			w := peerTestCall(s, body, "caller-token", "")
			if w.Code != 200 {
				t.Fatalf("forward: %d %s", w.Code, w.Body.String())
			}
			var before, after map[string]json.RawMessage
			if err := json.Unmarshal([]byte(body), &before); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(received, &after); err != nil {
				t.Fatal(err)
			}
			var target string
			json.Unmarshal(after[field], &target)
			if target != "shell:nodeB" {
				t.Fatalf("alias not canonicalized: %s", received)
			}
			if len(before) != len(after) {
				t.Fatal("non-routing fields added or removed")
			}
			for key, original := range before {
				if key == field {
					continue
				}
				var want, got bytes.Buffer
				json.Compact(&want, original)
				json.Compact(&got, after[key])
				if !bytes.Equal(want.Bytes(), got.Bytes()) {
					t.Errorf("field %s changed: %s -> %s", key, want.Bytes(), got.Bytes())
				}
			}
		})
	}
}
