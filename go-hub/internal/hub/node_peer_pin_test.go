package hub

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// Real TLS connections test the transport only; actual node execution and
// owner receipt routing are proven by tests/e2e/node/peer_run.py.
func TestNodePeerPinnedPoolsSeparateSameOriginPhysicalIPs(t *testing.T) {
	var calls [2]atomic.Int32
	var connections [2]atomic.Int32
	closed := make(chan struct{}, 4)
	servers := make([]*httptest.Server, 2)
	first, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := first.Addr().(*net.TCPAddr).Port
	second, err := net.Listen("tcp", fmt.Sprintf("127.0.0.2:%d", port))
	if err != nil {
		first.Close()
		if errors.Is(err, syscall.EADDRNOTAVAIL) {
			// Linux resolves any 127.0.0.N address; macOS needs an explicit
			// lo0 alias that hosted CI runners do not configure.
			t.Skipf("loopback alias 127.0.0.2 unavailable on %s: %v", runtime.GOOS, err)
		}
		t.Fatal(err)
	}
	for i, listener := range []net.Listener{first, second} {
		i := i
		server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls[i].Add(1)
			writeJSON(w, 200, map[string]any{"physical_backend": i})
		}))
		server.Listener.Close()
		server.Listener = listener
		server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
			if state == http.StateNew {
				connections[i].Add(1)
			}
			if state == http.StateClosed {
				closed <- struct{}{}
			}
		}
		server.StartTLS()
		servers[i] = server
		defer server.Close()
	}
	origin := fmt.Sprintf("https://127.0.0.1:%d", port)
	raw := fmt.Sprintf(`{"shell:nodeB":{"url":%q,"connect_to":"127.0.0.1"},"shell:nodeC":{"url":%q,"connect_to":"127.0.0.2"}}`, origin, origin)
	s := New(Config{ConfigDir: t.TempDir(), CtlToken: "token", NodePeersJSON: raw})
	defer s.Close()
	if s.nodePeersErr != nil || len(s.nodePeerPinnedClients) != 2 {
		t.Fatalf("separate pools missing: %v, %d", s.nodePeersErr, len(s.nodePeerPinnedClients))
	}
	roots := x509.NewCertPool()
	roots.AddCert(servers[0].Certificate())
	for _, client := range s.nodePeerPinnedClients {
		client.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: roots}
	}
	for round := 0; round < 2; round++ {
		for i, target := range []string{"shell:nodeB", "shell:nodeC"} {
			body := fmt.Sprintf(`{"target":%q,"tool":"shell_exec","arguments":{"cmd":"fixture-only"}}`, target)
			w := peerTestCall(s, body, "token", "")
			var result map[string]any
			_ = json.Unmarshal(w.Body.Bytes(), &result)
			if w.Code != 200 || result["physical_backend"] != float64(i) {
				t.Fatalf("wrong physical pool: %d %s", w.Code, w.Body.String())
			}
		}
	}
	for i := range calls {
		if calls[i].Load() != 2 || connections[i].Load() != 1 {
			t.Fatalf("backend %d: calls=%d connections=%d", i, calls[i].Load(), connections[i].Load())
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-closed:
		case <-time.After(2 * time.Second):
			t.Fatal("pinned idle connection survived Server.Close")
		}
	}
	// Trusting the certificate still must not disable logical-host verification.
	client := newPinnedNodePeerClient(time.Second, nodePeerRoute{URL: "https://wrong.example", ConnectTo: "127.0.0.1"})
	client.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: roots}
	defer client.CloseIdleConnections()
	response, err := client.Post(fmt.Sprintf("https://wrong.example:%d/", port), "application/json", strings.NewReader(`{}`))
	if response != nil {
		response.Body.Close()
	}
	var hostnameError x509.HostnameError
	if !errors.As(err, &hostnameError) {
		t.Fatalf("expected TLS hostname verification failure, got %T: %v", err, err)
	}
	if response != nil || calls[0].Load() != 2 {
		t.Fatal("wrong-host request reached the HTTP handler")
	}
}
