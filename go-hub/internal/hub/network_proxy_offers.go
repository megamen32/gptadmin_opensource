package hub

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

const networkProxyOfferPath = "/proxy-agent/offers"

// networkProxyOfferBroker keeps short-lived, unconsumed agent offers only in
// memory. Restarting the Hub drops them; it never restores a stream credential.
type networkProxyOfferBroker struct {
	mu       sync.Mutex
	private  ed25519.PrivateKey
	relayURL string
	queues   map[string][]networkProxyAgentOffer
}

type networkProxyAgentOffer struct {
	CapabilityID string         `json:"capability_id"`
	StreamID     string         `json:"stream_id"`
	AgentID      string         `json:"agent_id"`
	ProfileID    string         `json:"profile_id"`
	Target       string         `json:"target"`
	Scope        string         `json:"scope"`
	AllowedCIDRs []netip.Prefix `json:"allowed_cidrs,omitempty"`
	AllowedPorts []uint16       `json:"allowed_ports"`
	RelayURL     string         `json:"relay_url"`
	RelayTicket  string         `json:"relay_ticket"`
	Limits       map[string]any `json:"limits"`
	IssuedAt     int64          `json:"issued_at"`
	ExpiresAt    int64          `json:"expires_at"`
	Nonce        string         `json:"nonce"`
}

func newNetworkProxyOfferBroker(relayKey []byte, relayURL string) *networkProxyOfferBroker {
	seed := sha256.Sum256(relayKey)
	return &networkProxyOfferBroker{private: ed25519.NewKeyFromSeed(seed[:]), relayURL: relayURL, queues: map[string][]networkProxyAgentOffer{}}
}

func (b *networkProxyOfferBroker) enqueue(capability NetworkProxyCapability, grant ProxyStreamGrant) error {
	if b == nil || grant.Role != "agent" || strings.TrimSpace(b.relayURL) == "" {
		return ErrNetworkProxyUnavailable
	}
	prefixes := make([]netip.Prefix, 0, len(capability.Policy.TargetCIDRs))
	for _, raw := range capability.Policy.TargetCIDRs {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			return err
		}
		prefixes = append(prefixes, prefix)
	}
	ports := make([]uint16, 0, len(capability.Policy.TargetPorts))
	for _, port := range capability.Policy.TargetPorts {
		ports = append(ports, uint16(port))
	}
	nonceBytes := make([]byte, 18)
	if _, err := rand.Read(nonceBytes); err != nil {
		return err
	}
	now := time.Now().UTC()
	offer := networkProxyAgentOffer{CapabilityID: grant.CapabilityID, StreamID: grant.StreamID, AgentID: grant.AgentID, ProfileID: capability.ProfileID, Target: grant.Target, Scope: capability.Policy.Scope, AllowedCIDRs: prefixes, AllowedPorts: ports, RelayURL: b.relayURL, RelayTicket: grant.Token, Limits: map[string]any{"dial_timeout_seconds": 10, "idle_timeout_seconds": 60, "connection_lifetime_seconds": int(capability.Policy.Lease.Seconds()), "max_bytes": capability.Policy.MaxBytes, "max_frame_bytes": 32768, "max_pending_frames": 32, "max_streams_per_agent": capability.Policy.MaxStreams, "max_streams_per_profile": capability.Policy.MaxStreams}, IssuedAt: now.Unix(), ExpiresAt: grant.ExpiresAt.Unix(), Nonce: base64.RawURLEncoding.EncodeToString(nonceBytes)}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.queues[offer.AgentID] = append(b.queues[offer.AgentID], offer)
	return nil
}

func (b *networkProxyOfferBroker) next(agentID string) (networkProxyAgentOffer, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	queue := b.queues[agentID]
	for len(queue) > 0 {
		offer := queue[0]
		queue = queue[1:]
		if time.Now().Before(time.Unix(offer.ExpiresAt, 0)) {
			b.queues[agentID] = queue
			return offer, true
		}
	}
	b.queues[agentID] = queue
	return networkProxyAgentOffer{}, false
}

func (b *networkProxyOfferBroker) signedBody(offer networkProxyAgentOffer) ([]byte, http.Header, error) {
	body, err := json.Marshal(offer)
	if err != nil {
		return nil, nil, err
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	nonceBytes := make([]byte, 18)
	if _, err := rand.Read(nonceBytes); err != nil {
		return nil, nil, err
	}
	nonce := base64.RawURLEncoding.EncodeToString(nonceBytes)
	hash := sha256.Sum256(body)
	canonical := strings.Join([]string{http.MethodGet, networkProxyOfferPath, ts, nonce, hex.EncodeToString(hash[:])}, "\n")
	signature := ed25519.Sign(b.private, []byte(canonical))
	headers := http.Header{"Content-Type": []string{"application/json"}, "X-GPTAdmin-Timestamp": []string{ts}, "X-GPTAdmin-Nonce": []string{nonce}, "X-GPTAdmin-Signature": []string{base64.RawURLEncoding.EncodeToString(signature)}}
	return body, headers, nil
}

func (b *networkProxyOfferBroker) publicKey() string {
	if b == nil || len(b.private) != ed25519.PrivateKeySize {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(b.private.Public().(ed25519.PublicKey))
}

func (s *Server) networkProxyOfferPublicKeyHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.cfg.RelayAgentToken == "" || r.Header.Get("Authorization") != "Bearer "+s.cfg.RelayAgentToken || s.networkProxyOffers == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"public_key": s.networkProxyOffers.publicKey()})
}

func (s *Server) networkProxyOfferPullHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.cfg.RelayAgentToken == "" || r.Header.Get("Authorization") != "Bearer "+s.cfg.RelayAgentToken {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	agentID := strings.TrimSpace(r.URL.Query().Get("agent_id"))
	if agentID == "" || s.networkProxyOffers == nil {
		http.Error(w, "offer delivery unavailable", http.StatusServiceUnavailable)
		return
	}
	offer, ok := s.networkProxyOffers.next(agentID)
	if !ok {
		http.Error(w, "no offer", http.StatusNoContent)
		return
	}
	body, headers, err := s.networkProxyOffers.signedBody(offer)
	if err != nil {
		http.Error(w, "offer signing failed", http.StatusInternalServerError)
		return
	}
	for key, values := range headers {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	_, _ = w.Write(body)
}
