package networkproxy

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAuthorizedPullOfferSourceSendsBearerOnlyToOfferEndpoint(t *testing.T) {
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/proxy-agent/offers" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer pull-secret" {
			t.Fatalf("Authorization = %q", got)
		}
		http.Error(w, "no offer", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	verifier, err := NewSignedOfferVerifier(OfferVerifierConfig{HubPublicKey: base64.RawURLEncoding.EncodeToString(publicKey), AgentID: "shell:test", MaxSkew: time.Minute, NonceTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	source, err := NewAuthorizedPullOfferSource(server.Client(), server.URL+"/proxy-agent/offers", verifier, " pull-secret ", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Next(context.Background()); err == nil {
		t.Fatal("Next() accepted a non-200 response")
	}
}
