package networkproxy

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/security"
)

func TestOfferLimitsUseStableSnakeCaseJSONNames(t *testing.T) {
	raw, err := json.Marshal(Limits{DialTimeoutSeconds: 10, MaxBytes: 100, ConnectionLifetimeSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"dial_timeout_seconds":10,"max_bytes":100,"connection_lifetime_seconds":60}` {
		t.Fatalf("limits JSON = %s", raw)
	}
}

func TestPullAndWebhookUseIdenticalVerification(t *testing.T) {
	t.Parallel()

	publicKey, privateKey := testOfferKey(t)
	offer := testOffer(time.Now())
	body := testOfferBody(t, offer)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/proxy-agent/offers" {
			t.Errorf("request = %s %s, want GET /proxy-agent/offers", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		writeSignedOffer(t, w, privateKey, http.MethodGet, r.URL.Path, body, offer.Nonce)
	}))
	defer server.Close()

	pullVerifier := testVerifier(t, publicKey, offer.AgentID)
	pullSource, err := NewPullOfferSource(server.Client(), server.URL+"/proxy-agent/offers", pullVerifier, 4096)
	if err != nil {
		t.Fatalf("NewPullOfferSource() error = %v", err)
	}
	pulled, err := pullSource.Next(context.Background())
	if err != nil {
		t.Fatalf("PullOfferSource.Next() error = %v", err)
	}
	if !reflect.DeepEqual(pulled, offer) {
		t.Fatalf("pulled offer = %#v, want %#v", pulled, offer)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("pull requests = %d, want 1", got)
	}

	webhookVerifier := testVerifier(t, publicKey, offer.AgentID)
	webhook, delivered, err := WebhookOfferHandler(webhookVerifier, 1, 4096)
	if err != nil {
		t.Fatalf("WebhookOfferHandler() error = %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/proxy-agent/offers", strings.NewReader(string(body)))
	writeSignedOfferHeaders(req.Header, privateKey, http.MethodPost, req.URL.Path, body, offer.Nonce)
	recorder := httptest.NewRecorder()
	webhook.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("webhook status = %d, want %d", recorder.Code, http.StatusAccepted)
	}
	select {
	case received := <-delivered:
		if !reflect.DeepEqual(received, offer) {
			t.Fatalf("webhook offer = %#v, want %#v", received, offer)
		}
	default:
		t.Fatal("webhook did not deliver offer")
	}
}

func TestSignedOfferVerifierRejectsWrongAgentReplayAndExpiredOffer(t *testing.T) {
	t.Parallel()

	publicKey, privateKey := testOfferKey(t)
	base := testOffer(time.Now())
	verifier := testVerifier(t, publicKey, base.AgentID)

	wrongAgent := base
	wrongAgent.AgentID = "other-agent"
	wrongAgentBody := testOfferBody(t, wrongAgent)
	_, err := verifier.Verify(http.MethodPost, "/proxy-agent/offers", testSignedOfferHeaders(privateKey, http.MethodPost, "/proxy-agent/offers", wrongAgentBody, wrongAgent.Nonce), wrongAgentBody)
	if !errors.Is(err, ErrOfferAgentMismatch) {
		t.Fatalf("wrong agent error = %v, want ErrOfferAgentMismatch", err)
	}

	badSignature := testOffer(time.Now())
	badSignature.Nonce = "bad-signature-nonce"
	badSignatureBody := testOfferBody(t, badSignature)
	badSignatureHeaders := testSignedOfferHeaders(privateKey, http.MethodPost, "/proxy-agent/offers", badSignatureBody, badSignature.Nonce)
	badSignatureHeaders.Set("X-GPTAdmin-Signature", "not-a-signature")
	_, err = testVerifier(t, publicKey, badSignature.AgentID).Verify(http.MethodPost, "/proxy-agent/offers", badSignatureHeaders, badSignatureBody)
	if !errors.Is(err, ErrOfferSignature) {
		t.Fatalf("bad signature error = %v, want ErrOfferSignature", err)
	}

	missingField := testOffer(time.Now())
	missingField.Nonce = "missing-field-nonce"
	missingField.RelayTicket = ""
	missingFieldBody := testOfferBody(t, missingField)
	_, err = testVerifier(t, publicKey, missingField.AgentID).Verify(http.MethodPost, "/proxy-agent/offers", testSignedOfferHeaders(privateKey, http.MethodPost, "/proxy-agent/offers", missingFieldBody, missingField.Nonce), missingFieldBody)
	if !errors.Is(err, ErrOfferInvalid) {
		t.Fatalf("missing field error = %v, want ErrOfferInvalid", err)
	}

	body := testOfferBody(t, base)
	headers := testSignedOfferHeaders(privateKey, http.MethodPost, "/proxy-agent/offers", body, base.Nonce)
	if _, err := verifier.Verify(http.MethodPost, "/proxy-agent/offers", headers, body); err != nil {
		t.Fatalf("first Verify() error = %v", err)
	}
	if _, err := verifier.Verify(http.MethodPost, "/proxy-agent/offers", headers, body); !errors.Is(err, ErrOfferReplay) {
		t.Fatalf("replayed Verify() error = %v, want ErrOfferReplay", err)
	}

	expired := testOffer(time.Now())
	expired.Nonce = "expired-offer-nonce"
	expired.IssuedAt = time.Now().Add(-time.Minute).Unix()
	expired.ExpiresAt = time.Now().Add(-time.Second).Unix()
	expiredBody := testOfferBody(t, expired)
	_, err = testVerifier(t, publicKey, expired.AgentID).Verify(http.MethodPost, "/proxy-agent/offers", testSignedOfferHeaders(privateKey, http.MethodPost, "/proxy-agent/offers", expiredBody, expired.Nonce), expiredBody)
	if !errors.Is(err, ErrOfferExpired) {
		t.Fatalf("expired offer error = %v, want ErrOfferExpired", err)
	}
}

func TestSignedOfferVerifierRejectsStaleSignature(t *testing.T) {
	t.Parallel()

	publicKey, privateKey := testOfferKey(t)
	offer := testOffer(time.Now())
	body := testOfferBody(t, offer)
	headers := testSignedOfferHeadersAt(privateKey, http.MethodPost, "/proxy-agent/offers", body, offer.Nonce, time.Now().Add(-time.Minute))
	_, err := testVerifier(t, publicKey, offer.AgentID).Verify(http.MethodPost, "/proxy-agent/offers", headers, body)
	if !errors.Is(err, ErrOfferSignature) {
		t.Fatalf("stale signature error = %v, want ErrOfferSignature", err)
	}
}

func TestWebhookOfferHandlerRejectsOversizedAndOverflow(t *testing.T) {
	t.Parallel()

	publicKey, privateKey := testOfferKey(t)
	offer := testOffer(time.Now())
	webhook, delivered, err := WebhookOfferHandler(testVerifier(t, publicKey, offer.AgentID), 1, 512)
	if err != nil {
		t.Fatalf("WebhookOfferHandler() error = %v", err)
	}

	oversized := strings.Repeat("x", 513)
	overflowReq := httptest.NewRequest(http.MethodPost, "/proxy-agent/offers", strings.NewReader(oversized))
	overflowRecorder := httptest.NewRecorder()
	webhook.ServeHTTP(overflowRecorder, overflowReq)
	if overflowRecorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized status = %d, want %d", overflowRecorder.Code, http.StatusRequestEntityTooLarge)
	}

	for _, nonce := range []string{"capacity-one", "capacity-two"} {
		offer.Nonce = nonce
		body := testOfferBody(t, offer)
		req := httptest.NewRequest(http.MethodPost, "/proxy-agent/offers", strings.NewReader(string(body)))
		writeSignedOfferHeaders(req.Header, privateKey, http.MethodPost, req.URL.Path, body, nonce)
		recorder := httptest.NewRecorder()
		webhook.ServeHTTP(recorder, req)
		want := http.StatusAccepted
		if nonce == "capacity-two" {
			want = http.StatusTooManyRequests
		}
		if recorder.Code != want {
			t.Fatalf("nonce %q status = %d, want %d", nonce, recorder.Code, want)
		}
	}
	if got := <-delivered; got.Nonce != "capacity-one" {
		t.Fatalf("delivered nonce = %q, want capacity-one", got.Nonce)
	}
}

func TestOfferDeliveryUsesOnlyDedicatedOffersPath(t *testing.T) {
	publicKey, _ := testOfferKey(t)
	verifier := testVerifier(t, publicKey, "agent-1")
	client := &http.Client{}
	if _, err := NewPullOfferSource(client, "https://hub.example/queue/agent-1", verifier, 4096); !errors.Is(err, ErrOfferInvalid) {
		t.Fatalf("queue endpoint error = %v, want ErrOfferInvalid", err)
	}
	handler, _, err := WebhookOfferHandler(verifier, 1, 4096)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/queue/agent-1", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("webhook control-path status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
}

func TestWebhookOverflowDoesNotConsumeOfferNonce(t *testing.T) {
	publicKey, privateKey := testOfferKey(t)
	offer := testOffer(time.Now())
	handler, delivered, err := WebhookOfferHandler(testVerifier(t, publicKey, offer.AgentID), 1, 4096)
	if err != nil {
		t.Fatal(err)
	}

	blocker := offer
	blocker.Nonce = "blocker"
	blockerBody := testOfferBody(t, blocker)
	blockerRequest := httptest.NewRequest(http.MethodPost, "/proxy-agent/offers", strings.NewReader(string(blockerBody)))
	writeSignedOfferHeaders(blockerRequest.Header, privateKey, http.MethodPost, blockerRequest.URL.Path, blockerBody, blocker.Nonce)
	blockerResponse := httptest.NewRecorder()
	handler.ServeHTTP(blockerResponse, blockerRequest)
	if blockerResponse.Code != http.StatusAccepted {
		t.Fatalf("blocker status = %d, want %d", blockerResponse.Code, http.StatusAccepted)
	}

	retry := offer
	retry.Nonce = "retry-after-overflow"
	retryBody := testOfferBody(t, retry)
	makeRetry := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/proxy-agent/offers", strings.NewReader(string(retryBody)))
		writeSignedOfferHeaders(req.Header, privateKey, http.MethodPost, req.URL.Path, retryBody, retry.Nonce)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		return recorder
	}
	if response := makeRetry(); response.Code != http.StatusTooManyRequests {
		t.Fatalf("overflow status = %d, want %d", response.Code, http.StatusTooManyRequests)
	}
	<-delivered
	if response := makeRetry(); response.Code != http.StatusAccepted {
		t.Fatalf("retry status = %d, want %d", response.Code, http.StatusAccepted)
	}
}

func TestOfferConsumerStopsOnCancellationWithoutRetry(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	source := offerSourceFunc(func(ctx context.Context) (Offer, error) {
		close(started)
		<-ctx.Done()
		return Offer{}, ctx.Err()
	})
	consumer := OfferConsumer{}
	var cancelledHandlerCalls atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- consumer.Run(ctx, source, func(context.Context, Offer) error {
			cancelledHandlerCalls.Add(1)
			return nil
		})
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v, want nil after cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() did not stop after cancellation")
	}
	if got := cancelledHandlerCalls.Load(); got != 0 {
		t.Fatalf("cancellation handler calls = %d, want 0", got)
	}

	var calls atomic.Int32
	offer := testOffer(time.Now())
	err := consumer.Run(context.Background(), offerSourceFunc(func(context.Context) (Offer, error) {
		return offer, nil
	}), func(context.Context, Offer) error {
		calls.Add(1)
		return errors.New("handler failure")
	})
	if err == nil || calls.Load() != 1 {
		t.Fatalf("Run() error = %v, handler calls = %d; want one call and its failure", err, calls.Load())
	}
}

type offerSourceFunc func(context.Context) (Offer, error)

func (f offerSourceFunc) Next(ctx context.Context) (Offer, error) { return f(ctx) }

func testOffer(now time.Time) Offer {
	return Offer{
		CapabilityID: "capability-1",
		StreamID:     "stream-1",
		AgentID:      "agent-1",
		ProfileID:    "profile-1",
		Target:       "origin.example:443",
		Scope:        ScopeInternetEgress,
		AllowedPorts: []uint16{443},
		RelayURL:     "wss://relay.example/stream",
		RelayTicket:  "relay-ticket",
		Limits:       Limits{DialTimeoutSeconds: 5, MaxBytes: 1024, ConnectionLifetimeSeconds: 30},
		IssuedAt:     now.Add(-time.Second).Unix(),
		ExpiresAt:    now.Add(time.Minute).Unix(),
		Nonce:        "offer-nonce",
	}
}

func testOfferBody(t *testing.T, offer Offer) []byte {
	t.Helper()
	body, err := json.Marshal(offer)
	if err != nil {
		t.Fatalf("marshal offer: %v", err)
	}
	return body
}

func testOfferKey(t *testing.T) (string, ed25519.PrivateKey) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return security.B64(publicKey), privateKey
}

func testVerifier(t *testing.T, publicKey, agentID string) *SignedOfferVerifier {
	t.Helper()
	verifier, err := NewSignedOfferVerifier(OfferVerifierConfig{
		HubPublicKey: publicKey,
		AgentID:      agentID,
		MaxSkew:      10 * time.Second,
		NonceTTL:     time.Minute,
	})
	if err != nil {
		t.Fatalf("NewSignedOfferVerifier() error = %v", err)
	}
	return verifier
}

func writeSignedOffer(t *testing.T, writer http.ResponseWriter, privateKey ed25519.PrivateKey, method, path string, body []byte, nonce string) {
	t.Helper()
	writeSignedOfferHeaders(writer.Header(), privateKey, method, path, body, nonce)
	_, _ = writer.Write(body)
}

func writeSignedOfferHeaders(headers http.Header, privateKey ed25519.PrivateKey, method, path string, body []byte, nonce string) {
	writeSignedOfferHeadersAt(headers, privateKey, method, path, body, nonce, time.Now())
}

func writeSignedOfferHeadersAt(headers http.Header, privateKey ed25519.PrivateKey, method, path string, body []byte, nonce string, timestamp time.Time) {
	ts := fmt.Sprintf("%d", timestamp.Unix())
	signature := ed25519.Sign(privateKey, security.Canonical(method, path, ts, nonce, body))
	headers.Set("X-GPTAdmin-Timestamp", ts)
	headers.Set("X-GPTAdmin-Nonce", nonce)
	headers.Set("X-GPTAdmin-Signature", security.B64(signature))
}

func testSignedOfferHeaders(privateKey ed25519.PrivateKey, method, path string, body []byte, nonce string) http.Header {
	return testSignedOfferHeadersAt(privateKey, method, path, body, nonce, time.Now())
}

func testSignedOfferHeadersAt(privateKey ed25519.PrivateKey, method, path string, body []byte, nonce string, timestamp time.Time) http.Header {
	headers := make(http.Header)
	writeSignedOfferHeadersAt(headers, privateKey, method, path, body, nonce, timestamp)
	return headers
}
