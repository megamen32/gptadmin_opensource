package hub

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Bound how long the preferred voter may withhold headers. A reachable voter
// retains its ordinary response-body budget and its authoritative rejection.
func TestExactOwnerReadTransport(t *testing.T) {
	headersSent := make(chan struct{})
	releaseBody := make(chan struct{})
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/silent":
			<-r.Context().Done()
		case "/denied":
			w.WriteHeader(http.StatusForbidden)
		case "/slow-body":
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			close(headersSent)
			<-releaseBody
			_, _ = io.WriteString(w, "complete")
		}
	}))
	defer peer.Close()
	transport := peer.Client().Transport.(*http.Transport).Clone()
	// httptest's dedicated TLS transport does not set a dialer.
	transport.DialContext = http.DefaultTransport.(*http.Transport).DialContext
	boundOwnerReadTransport(transport)
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 4 * time.Second}
	start := time.Now()
	res, e := client.Get(peer.URL + "/silent")
	if e == nil {
		res.Body.Close()
		t.Fatal("accepted a silent owner")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("silent owner consumed the client deadline")
	}
	res, e = client.Get(peer.URL + "/denied")
	if e != nil {
		t.Fatal(e)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("authoritative status = %d", res.StatusCode)
	}
	res, e = client.Get(peer.URL + "/slow-body")
	if e != nil {
		t.Fatal(e)
	}
	<-headersSent
	time.Sleep(2 * ownerReadStageTimeout)
	close(releaseBody)
	b, e := io.ReadAll(res.Body)
	res.Body.Close()
	if e != nil || string(b) != "complete" {
		t.Fatalf("healthy owner's slow body = %q, %v", b, e)
	}
}
