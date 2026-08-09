package hub

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
)

func TestHeartbeatBeatCarriesExistingChildMCPCatalog(t *testing.T) {
	beat := Beat{MCPAgents: []map[string]any{{"ref": "BrowserClaw", "transport": "stdio", "enabled": true}}}
	b, err := json.Marshal(beat)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) == "{}" || !bytes.Contains(b, []byte(`"mcp_agents"`)) || !bytes.Contains(b, []byte(`"BrowserClaw"`)) {
		t.Fatalf("heartbeat omitted child MCP catalog: %s", b)
	}
}

func TestNewUsesConfiguredDNSServer(t *testing.T) {
	dnsConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}

	var queries atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 512)
		for {
			n, addr, err := dnsConn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			queries.Add(1)
			if _, err := dnsConn.WriteToUDP(dnsAResponse(buf[:n]), addr); err != nil {
				return
			}
		}
	}()
	defer func() {
		dnsConn.Close()
		<-done
	}()

	hubServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ready" {
			t.Fatalf("path=%q want /ready", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer hubServer.Close()

	port := hubServer.Listener.Addr().(*net.TCPAddr).Port
	client := New(fmt.Sprintf("http://hub.invalid:%d", port), nil, "", dnsConn.LocalAddr().String(), "")
	resp, _, err := client.do(context.Background(), http.MethodGet, "/ready", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status=%d want %d", resp.StatusCode, http.StatusNoContent)
	}
	if queries.Load() == 0 {
		t.Fatal("configured DNS server received no query")
	}
}

func TestNewUsesPreferredHubIPBeforeDNS(t *testing.T) {
	hubServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "" {
			t.Fatal("missing original Hub host")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer hubServer.Close()

	port := hubServer.Listener.Addr().(*net.TCPAddr).Port
	client := New(fmt.Sprintf("http://hub.invalid:%d", port), nil, "", "", "127.0.0.1")
	resp, _, err := client.do(context.Background(), http.MethodGet, "/ready", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status=%d want %d", resp.StatusCode, http.StatusNoContent)
	}
}

func TestNewUsesConfiguredSSLBundle(t *testing.T) {
	hubServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer hubServer.Close()

	caFile := t.TempDir() + "/roots.pem"
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: hubServer.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSL_CERT_FILE", caFile)
	// Confirm the fixture is a real certificate rather than relying on a
	// malformed bundle accidentally making the test pass.
	if _, err := x509.ParseCertificate(hubServer.Certificate().Raw); err != nil {
		t.Fatal(err)
	}

	port := hubServer.Listener.Addr().(*net.TCPAddr).Port
	client := New(fmt.Sprintf("https://example.com:%d", port), nil, "", "", "127.0.0.1")
	resp, _, err := client.do(context.Background(), http.MethodGet, "/ready", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status=%d want %d", resp.StatusCode, http.StatusNoContent)
	}
}

func dnsAResponse(query []byte) []byte {
	if len(query) < 17 {
		return nil
	}
	questionEnd := 12
	for questionEnd < len(query) {
		labelLength := int(query[questionEnd])
		questionEnd++
		if labelLength == 0 {
			break
		}
		questionEnd += labelLength
	}
	questionEnd += 4 // QTYPE and QCLASS
	if questionEnd > len(query) {
		return nil
	}

	response := make([]byte, 0, questionEnd+16)
	response = append(response, query[:2]...)
	response = append(response, 0x81, 0x80) // standard response, recursion available
	response = append(response, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00)
	response = append(response, query[12:questionEnd]...)
	response = append(response,
		0xc0, 0x0c, // name pointer to the question
		0x00, 0x01, // A
		0x00, 0x01, // IN
		0x00, 0x00, 0x00, 0x1e,
		0x00, 0x04,
		127, 0, 0, 1,
	)
	return response
}
