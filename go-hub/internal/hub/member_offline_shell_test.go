package hub

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestMemberOfflineShellOnlyFallsBackBeforeDelivery(t *testing.T) {
	const bearer = "header.payload.signature"
	const body = `{"jsonrpc":"2.0","method":"initialize"}`
	for _, test := range []struct {
		name        string
		dead        bool
		remote      int
		proof       int
		want        int
		wantForward int32
		wantProof   int32
	}{
		{"online request has no admission preflight", false, http.StatusOK, http.StatusConflict, http.StatusOK, 1, 0},
		{"remote HTTP rejection is preserved", false, http.StatusForbidden, http.StatusConflict, http.StatusForbidden, 1, 0},
		{"response lost after send", false, 0, http.StatusConflict, http.StatusBadGateway, 1, 0},
		{"dial failed before sending", true, 0, http.StatusConflict, http.StatusNoContent, 0, 1},
		{"writer rejects fallback bearer", true, 0, http.StatusForbidden, http.StatusUnauthorized, 0, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			var proofs, forwards atomic.Int32
			writer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != nodeBearerAdmissionPath {
					t.Errorf("unexpected writer path %s", r.URL.Path)
				}
				proofs.Add(1)
				w.WriteHeader(test.proof)
			}))
			defer writer.Close()
			remote := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				forwards.Add(1)
				got, _ := io.ReadAll(r.Body)
				if string(got) != body {
					t.Errorf("unexpected forwarded body %q", got)
				}
				if test.remote != 0 {
					w.WriteHeader(test.remote)
					return
				}
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Errorf("hijack: %v", err)
					return
				}
				_ = conn.Close()
			}))
			defer remote.Close()
			s := New(Config{AuthMode: "reader", ConfigDir: t.TempDir(), AuthSourceID: "writer",
				ReaderOfflineSharedSigner: true, OAuthClientSecret: "fixture", PublicOrigin: "https://issuer.test"})
			defer s.Close()
			s.nodeIdentity = &nodeIdentity{WriterID: "writer", WriterURL: writer.URL}
			s.authState = authSnapshotState{WriterID: "writer", Generation: 1, Digest: "snapshot", ReadOnly: true}
			s.nodeBearerAdmissionClient = writer.Client()
			s.nodePeerClient = remote.Client()
			origin := remote.URL
			if test.dead {
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				origin = "https://" + listener.Addr().String()
				listener.Close()
			}
			s.memberQuorumHubs.Set([]string{origin})
			local := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got, _ := io.ReadAll(r.Body)
				if !offlineShellMCP(r) || string(got) != body {
					t.Error("fallback lost the restricted MCP guard or original body")
				}
				w.WriteHeader(http.StatusNoContent)
			})
			r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer "+bearer)
			w := httptest.NewRecorder()
			s.withQuorumAdmission(local).ServeHTTP(w, r)
			if w.Code != test.want || forwards.Load() != test.wantForward || proofs.Load() != test.wantProof {
				t.Fatalf("HTTP %d, read-only probes=%d, MCP forwards=%d", w.Code, proofs.Load(), forwards.Load())
			}
			if w.Code == http.StatusBadGateway && !strings.Contains(w.Body.String(), `"outcome":"unknown"`) {
				t.Fatalf("ambiguous MCP response lost its outcome: %s", w.Body.String())
			}
		})
	}
}
