package hub

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestNetworkProxyOfferPullRequiresCredentialAndSignsExactOffer(t *testing.T) {
	relayKey := []byte("0123456789abcdef0123456789abcdef")
	broker := newNetworkProxyOfferBroker(relayKey, "wss://relay.example/stream")
	offer := networkProxyAgentOffer{
		CapabilityID: "cap-1", StreamID: "stream-1", AgentID: "shell:edge", ProfileID: "profile-1", Target: "example.com:443",
		Scope: "internet_egress", AllowedPorts: []uint16{443}, RelayURL: "wss://relay.example/stream", RelayTicket: "ticket", Limits: map[string]any{"dial_timeout_seconds": 10},
		IssuedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Minute).Unix(), Nonce: "offer-nonce",
	}
	broker.queues[offer.AgentID] = []networkProxyAgentOffer{offer}
	s := &Server{cfg: Config{RelayAgentToken: "agent-token"}, networkProxyOffers: broker}

	unauthorized := httptest.NewRecorder()
	s.networkProxyOfferPullHTTP(unauthorized, httptest.NewRequest(http.MethodGet, networkProxyOfferPath+"?agent_id=shell:edge", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}

	req := httptest.NewRequest(http.MethodGet, networkProxyOfferPath+"?agent_id=shell:edge", nil)
	req.Header.Set("Authorization", "Bearer agent-token")
	rec := httptest.NewRecorder()
	s.networkProxyOfferPullHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("pull status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"agent_id":"shell:edge"`) || strings.Contains(rec.Body.String(), `agent-token`) {
		t.Fatalf("unexpected offer body %s", rec.Body.String())
	}
	seed := sha256.Sum256(relayKey)
	private := ed25519.NewKeyFromSeed(seed[:])
	timestamp, nonce, signature := rec.Header().Get("X-GPTAdmin-Timestamp"), rec.Header().Get("X-GPTAdmin-Nonce"), rec.Header().Get("X-GPTAdmin-Signature")
	if timestamp == "" || nonce == "" || signature == "" {
		t.Fatal("missing signature headers")
	}
	hash := sha256.Sum256(rec.Body.Bytes())
	canonical := strings.Join([]string{http.MethodGet, networkProxyOfferPath, timestamp, nonce, hex.EncodeToString(hash[:])}, "\n")
	signatureBytes, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil || !ed25519.Verify(private.Public().(ed25519.PublicKey), []byte(canonical), signatureBytes) {
		t.Fatal("offer signature did not verify")
	}
	if _, err := strconv.ParseInt(timestamp, 10, 64); err != nil {
		t.Fatalf("timestamp = %q", timestamp)
	}
}
