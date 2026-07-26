package networkproxy

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"encoding/json"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/security"
)

const (
	timestampHeader = "X-GPTAdmin-Timestamp"
	nonceHeader     = "X-GPTAdmin-Nonce"
	signatureHeader = "X-GPTAdmin-Signature"
	offerPath       = "/proxy-agent/offers"
)

var (
	// ErrOfferInvalid identifies a malformed or incomplete offer.
	ErrOfferInvalid = errors.New("network offer is invalid")
	// ErrOfferSignature identifies a request that does not meet the Hub signing contract.
	ErrOfferSignature = errors.New("network offer signature is invalid")
	// ErrOfferAgentMismatch identifies an offer delivered to the wrong proxy agent.
	ErrOfferAgentMismatch = errors.New("network offer agent does not match")
	// ErrOfferReplay identifies a duplicate signed-request nonce.
	ErrOfferReplay = errors.New("network offer nonce was already used")
	// ErrOfferExpired identifies an offer whose delivery window has closed.
	ErrOfferExpired = errors.New("network offer has expired")
	// ErrOfferTooLarge identifies an offer body above its configured delivery limit.
	ErrOfferTooLarge = errors.New("network offer body is too large")
)

// Offer is the signed, short-lived activation contract for one Network Tunnel stream.
// It deliberately contains only relay data-plane credentials, never ShellMCP or OAuth control tokens.
type Offer struct {
	CapabilityID string         `json:"capability_id"`
	StreamID     string         `json:"stream_id"`
	AgentID      string         `json:"agent_id"`
	ProfileID    string         `json:"profile_id"`
	Target       string         `json:"target"`
	Scope        Scope          `json:"scope"`
	AllowedCIDRs []netip.Prefix `json:"allowed_cidrs,omitempty"`
	AllowedPorts []uint16       `json:"allowed_ports"`
	RelayURL     string         `json:"relay_url"`
	RelayTicket  string         `json:"relay_ticket"`
	Limits       Limits         `json:"limits"`
	IssuedAt     int64          `json:"issued_at"`
	ExpiresAt    int64          `json:"expires_at"`
	Nonce        string         `json:"nonce"`
}

// Validate checks the immutable offer fields before a later activation handler uses them.
func (o Offer) Validate(now time.Time) error {
	if o.CapabilityID == "" || o.StreamID == "" || o.AgentID == "" || o.ProfileID == "" || o.Target == "" || o.RelayURL == "" || o.RelayTicket == "" || o.Nonce == "" {
		return ErrOfferInvalid
	}
	if o.IssuedAt <= 0 || o.ExpiresAt <= o.IssuedAt {
		return ErrOfferInvalid
	}
	if !now.Before(time.Unix(o.ExpiresAt, 0)) {
		return ErrOfferExpired
	}
	if _, err := ParseTarget(o.Target); err != nil {
		return fmt.Errorf("%w: target", ErrOfferInvalid)
	}
	if err := (Policy{Scope: o.Scope, ApprovedLANCIDRs: o.AllowedCIDRs, AllowedPorts: o.AllowedPorts}).Validate(); err != nil {
		return fmt.Errorf("%w: policy", ErrOfferInvalid)
	}
	if o.Limits.DialTimeoutSeconds <= 0 || o.Limits.MaxBytes <= 0 || o.Limits.ConnectionLifetimeSeconds <= 0 {
		return fmt.Errorf("%w: limits", ErrOfferInvalid)
	}
	relayURL, err := url.ParseRequestURI(o.RelayURL)
	if err != nil || relayURL.Host == "" || (relayURL.Scheme != "ws" && relayURL.Scheme != "wss") {
		return fmt.Errorf("%w: relay URL", ErrOfferInvalid)
	}
	return nil
}

// OfferVerifierConfig configures verification of Hub-delivered tunnel offers.
type OfferVerifierConfig struct {
	HubPublicKey string
	AgentID      string
	MaxSkew      time.Duration
	NonceTTL     time.Duration
}

// SignedOfferVerifier verifies the existing Hub ed25519 request contract and offer binding.
type SignedOfferVerifier struct {
	hubPublicKey string
	agentID      string
	maxSkew      time.Duration
	nonces       *security.NonceCache
}

// NewSignedOfferVerifier constructs a verifier bound to one proxy agent identity.
func NewSignedOfferVerifier(config OfferVerifierConfig) (*SignedOfferVerifier, error) {
	if config.HubPublicKey == "" || config.AgentID == "" || config.MaxSkew <= 0 || config.NonceTTL <= 0 {
		return nil, ErrOfferInvalid
	}
	publicKey, err := security.B64Decode(config.HubPublicKey)
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: Hub public key", ErrOfferInvalid)
	}
	return &SignedOfferVerifier{
		hubPublicKey: config.HubPublicKey,
		agentID:      config.AgentID,
		maxSkew:      config.MaxSkew,
		nonces:       security.NewNonceCache(config.NonceTTL),
	}, nil
}

// Verify validates one exact HTTP request body and returns its authenticated offer.
func (v *SignedOfferVerifier) Verify(method, path string, headers http.Header, body []byte) (Offer, error) {
	if v == nil || v.nonces == nil {
		return Offer{}, ErrOfferInvalid
	}
	timestamp := headers.Get(timestampHeader)
	nonce := headers.Get(nonceHeader)
	signature := headers.Get(signatureHeader)
	if err := security.Verify(v.hubPublicKey, method, path, timestamp, nonce, body, signature, v.maxSkew); err != nil {
		return Offer{}, fmt.Errorf("%w: %v", ErrOfferSignature, err)
	}

	var offer Offer
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&offer); err != nil {
		return Offer{}, fmt.Errorf("%w: JSON body", ErrOfferInvalid)
	}
	if err := requireSingleJSONValue(decoder); err != nil {
		return Offer{}, err
	}
	if err := offer.Validate(time.Now()); err != nil {
		return Offer{}, err
	}
	if offer.AgentID != v.agentID {
		return Offer{}, ErrOfferAgentMismatch
	}
	if offer.Nonce != nonce {
		return Offer{}, fmt.Errorf("%w: offer nonce does not match request nonce", ErrOfferInvalid)
	}
	if !v.nonces.CheckAndRemember(nonce) {
		return Offer{}, ErrOfferReplay
	}
	return offer, nil
}

func requireSingleJSONValue(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: multiple JSON values", ErrOfferInvalid)
	}
	return nil
}

// OfferSource supplies already verified offers to an activation consumer.
type OfferSource interface {
	Next(context.Context) (Offer, error)
}

// PullOfferSource fetches offers only from its explicit proxy-agent offers endpoint.
type PullOfferSource struct {
	client       *http.Client
	offersURL    *url.URL
	verifier     *SignedOfferVerifier
	maxBodyBytes int64
}

// NewPullOfferSource creates a long-poll source with no queue or heartbeat dependency.
func NewPullOfferSource(client *http.Client, offersURL string, verifier *SignedOfferVerifier, maxBodyBytes int64) (*PullOfferSource, error) {
	if client == nil || verifier == nil || maxBodyBytes <= 0 {
		return nil, ErrOfferInvalid
	}
	endpoint, err := url.ParseRequestURI(offersURL)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" || endpoint.Path != offerPath {
		return nil, fmt.Errorf("%w: offers URL", ErrOfferInvalid)
	}
	return &PullOfferSource{client: client, offersURL: endpoint, verifier: verifier, maxBodyBytes: maxBodyBytes}, nil
}

// Next performs one cancellable long-poll GET and verifies exactly the returned body.
func (s *PullOfferSource) Next(ctx context.Context) (Offer, error) {
	if s == nil || s.client == nil || s.offersURL == nil || s.verifier == nil {
		return Offer{}, ErrOfferInvalid
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.offersURL.String(), nil)
	if err != nil {
		return Offer{}, err
	}
	response, err := s.client.Do(request)
	if err != nil {
		return Offer{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Offer{}, fmt.Errorf("offer long poll returned HTTP %d", response.StatusCode)
	}
	body, err := readOfferBody(response.Body, s.maxBodyBytes)
	if err != nil {
		return Offer{}, err
	}
	return s.verifier.Verify(request.Method, s.offersURL.Path, response.Header, body)
}

// WebhookOfferHandler returns an offer-only HTTP handler and bounded delivery channel.
func WebhookOfferHandler(verifier *SignedOfferVerifier, capacity int, maxBodyBytes int64) (http.Handler, <-chan Offer, error) {
	if verifier == nil || capacity <= 0 || maxBodyBytes <= 0 {
		return nil, nil, ErrOfferInvalid
	}
	delivered := make(chan Offer, capacity)
	var deliveryMu sync.Mutex
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			writer.Header().Set("Allow", http.MethodPost)
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if request.URL.Path != offerPath {
			http.NotFound(writer, request)
			return
		}
		body, err := readOfferBody(http.MaxBytesReader(writer, request.Body, maxBodyBytes), maxBodyBytes)
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, ErrOfferTooLarge) {
				status = http.StatusRequestEntityTooLarge
			}
			http.Error(writer, "invalid offer", status)
			return
		}
		// Reserve queue capacity before nonce consumption. A full queue must
		// return 429 without burning a valid offer's replay nonce so the sender
		// can retry after the consumer drains it.
		deliveryMu.Lock()
		defer deliveryMu.Unlock()
		if len(delivered) == cap(delivered) {
			http.Error(writer, "offer delivery queue is full", http.StatusTooManyRequests)
			return
		}
		offer, err := verifier.Verify(request.Method, request.URL.Path, request.Header, body)
		if err != nil {
			http.Error(writer, "invalid offer", http.StatusUnauthorized)
			return
		}
		delivered <- offer
		writer.WriteHeader(http.StatusAccepted)
	})
	return handler, delivered, nil
}

func readOfferBody(reader io.Reader, maxBodyBytes int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maxBodyBytes+1))
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			return nil, ErrOfferTooLarge
		}
		return nil, err
	}
	if int64(len(body)) > maxBodyBytes {
		return nil, ErrOfferTooLarge
	}
	return body, nil
}

// OfferHandler activates one verified offer without owning retry policy.
type OfferHandler func(context.Context, Offer) error

// OfferConsumer applies each verified offer at most once and stops on the first handler error.
type OfferConsumer struct{}

// Run receives offers until cancellation, source failure, or handler failure.
func (OfferConsumer) Run(ctx context.Context, source OfferSource, handler OfferHandler) error {
	if ctx == nil || source == nil || handler == nil {
		return ErrOfferInvalid
	}
	for {
		offer, err := source.Next(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if err := handler(ctx, offer); err != nil {
			return err
		}
	}
}
