package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const nodeBearerAdmissionPath = "/admin/api/nodes/bearer-admission"
const nodeBearerAdmissionLimit = 32 << 10

type nodeBearerAdmission struct {
	WriterID        string         `json:"writer_id"`
	Generation      uint64         `json:"generation"`
	AuthorityDigest string         `json:"authority_digest"`
	TokenDigest     string         `json:"token_digest"`
	Claims          map[string]any `json:"claims"`
}

type nodeBearerAdmissionError struct {
	unavailable     bool
	offlineEligible bool
}

func (e *nodeBearerAdmissionError) Error() string {
	if e.unavailable {
		return "enrolled writer bearer admission unavailable"
	}
	return "enrolled writer rejected bearer"
}

func validNodeAdmissionToken(token string) bool {
	return token != "" && len(token) <= 16<<10 && !strings.ContainsAny(token, "\r\n\x00")
}

func newNodeBearerAdmissionClient(pin string) *http.Client {
	transport := newNodeDialTransport(pin)
	if transport == nil {
		transport = http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
	}
	transport.MaxConnsPerHost = 8
	transport.MaxIdleConnsPerHost = 8
	transport.ResponseHeaderTimeout = 5 * time.Second
	return &http.Client{Transport: transport, Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// Existing enrolled nodes may ask their pinned writer to verify an original
// bearer. No signing key, replacement token or execution travels this route.
func (s *Server) nodeBearerAdmissionHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]any{"detail": "POST required"})
		return
	}
	if s.effectiveAuthMode() != "writer" {
		writeJSON(w, 409, map[string]any{"detail": "writer required"})
		return
	}
	member := s.nodeMemberAuth(r)
	if member == nil {
		writeJSON(w, 401, map[string]any{"detail": "node credential required"})
		return
	}
	if member.State != nodeStateReady || !member.LeaseExpires.After(s.now()) {
		writeJSON(w, 403, map[string]any{"detail": "ready enrolled node required"})
		return
	}
	// A slow enrolled caller must not hold an admission slot indefinitely.
	controller := http.NewResponseController(w)
	if err := controller.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "bounded admission read unavailable"})
		return
	}
	defer controller.SetReadDeadline(time.Time{})
	select {
	case s.nodeBearerAdmissionSlots <- struct{}{}:
		defer func() { <-s.nodeBearerAdmissionSlots }()
	default:
		writeJSON(w, 503, map[string]any{"detail": "bearer admission busy"})
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, nodeBearerAdmissionLimit+1))
	if err != nil || len(raw) > nodeBearerAdmissionLimit {
		writeJSON(w, 400, map[string]any{"detail": "bounded admission body required"})
		return
	}
	var input struct {
		Token string `json:"token"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF || !validNodeAdmissionToken(input.Token) {
		writeJSON(w, 400, map[string]any{"detail": "invalid admission token"})
		return
	}
	data, status := s.nodeBearerAdmissionResult(r, input.Token)
	if status != http.StatusOK {
		writeJSON(w, status, map[string]any{"detail": "scoped writer bearer admission rejected or unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, json.RawMessage(data))
}

// Consume ordinary MCP auth responses locally, releasing the generation gate
// before any client response write. No response or claims are cached.
type nodeAdmissionAuthWriter struct {
	header http.Header
	status int
}

func (w *nodeAdmissionAuthWriter) Header() http.Header { return w.header }
func (w *nodeAdmissionAuthWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *nodeAdmissionAuthWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return len(data), nil
}

func (s *Server) nodeBearerAdmissionResult(r *http.Request, token string) ([]byte, int) {
	// Node endpoints bypass the general generation gate for bootstrap. Hold
	// it explicitly here, and use ordinary MCP authentication on the writer.
	s.authMu.RLock()
	defer s.authMu.RUnlock()
	s.mu.Lock()
	state := s.authState
	bundle, readErr := s.readAuthSlot(state)
	initialized := s.authInitialized
	s.mu.Unlock()
	if !initialized || state.Pending || state.Generation == 0 || readErr != nil {
		return nil, http.StatusServiceUnavailable
	}
	// Match the exact control snapshot sent to readers, which intentionally
	// excludes legacy/control credentials even when the writer retains them.
	bundle.WriterID = s.authLocalID
	s.excludeSnapshotControlCredentials(&bundle)
	authorityDigest := authSnapshotDigest(bundle)
	origin := s.cfg.PublicOrigin
	if origin == "" {
		return nil, http.StatusServiceUnavailable
	}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, origin+"/mcp", nil)
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	request.RemoteAddr = r.RemoteAddr
	request.Header.Set("Authorization", "Bearer "+token)
	// mcpAuth attaches authenticated claims and the existing profile. It
	// cannot delegate again because this endpoint exists only on a writer.
	w := &nodeAdmissionAuthWriter{header: make(http.Header)}
	if !s.mcpAuth(w, request) {
		return nil, w.status
	}
	claims, _ := request.Context().Value(authClaimsContextKey{}).(map[string]any)
	if len(claims) == 0 {
		return nil, http.StatusUnauthorized
	}
	s.mu.Lock()
	current, currentErr := s.readAuthSlot(state)
	s.mu.Unlock()
	if currentErr != nil {
		return nil, http.StatusServiceUnavailable
	}
	current.WriterID = s.authLocalID
	s.excludeSnapshotControlCredentials(&current)
	if authSnapshotDigest(current) != authorityDigest {
		return nil, http.StatusServiceUnavailable
	}
	output := nodeBearerAdmission{WriterID: state.WriterID, Generation: state.Generation,
		AuthorityDigest: authorityDigest, TokenDigest: hashNodeSecret(token), Claims: claims}
	data, err := json.Marshal(output)
	if err != nil || len(data) > nodeBearerAdmissionLimit {
		return nil, http.StatusServiceUnavailable
	}
	return data, http.StatusOK
}

// The reader retains its own local profile/target/tool checks. The normal
// path delegates cryptographic verification to the enrolled writer. A trusted
// shared-signer reader may separately verify a JWT during a transport outage;
// it never falls back to unsigned claims or another credential/host.
func (s *Server) verifyNodeWriterBearer(r *http.Request, token string) (map[string]any, error) {
	unavailable := &nodeBearerAdmissionError{unavailable: true}
	if !validNodeAdmissionToken(token) {
		return nil, &nodeBearerAdmissionError{}
	}
	s.mu.Lock()
	var identity nodeIdentity
	if s.nodeIdentity != nil {
		identity = *s.nodeIdentity
	}
	state := s.authState
	s.mu.Unlock()
	if identity.WriterID == "" || identity.WriterID != s.cfg.AuthSourceID || state.WriterID != identity.WriterID || state.Pending || state.Generation == 0 {
		return nil, unavailable
	}
	u, err := url.Parse(identity.WriterURL)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, unavailable
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "::1" || u.Hostname() == "localhost")) {
		return nil, unavailable
	}
	select {
	case s.nodeBearerAdmissionSlots <- struct{}{}:
		defer func() { <-s.nodeBearerAdmissionSlots }()
	default:
		return nil, unavailable
	}
	body, _ := json.Marshal(map[string]string{"token": token})
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(identity.WriterURL, "/")+nodeBearerAdmissionPath, bytes.NewReader(body))
	if err != nil {
		return nil, unavailable
	}
	request.Header.Set("Authorization", "Bearer "+identity.NodeKey)
	request.Header.Set("Content-Type", "application/json")
	request.GetBody = nil
	response, err := s.nodeBearerAdmissionClient.Do(request)
	if err != nil {
		// A failed certificate, TLS protocol error, or malformed peer response is
		// not an outage: never downgrade authentication after such failures.
		var dial *net.OpError
		connectionUnavailable := errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &dial) && dial.Op == "dial")
		if r.Context().Err() != nil || !connectionUnavailable {
			return nil, unavailable
		}
		return nil, &nodeBearerAdmissionError{unavailable: true, offlineEligible: true}
	}
	defer response.Body.Close()
	if response.StatusCode == 401 || response.StatusCode == 403 {
		return nil, &nodeBearerAdmissionError{}
	}
	if response.StatusCode == http.StatusConflict {
		// The public writer address can return to this reader after ingress failover.
		return nil, &nodeBearerAdmissionError{unavailable: true, offlineEligible: true}
	}
	if response.StatusCode != 200 {
		return nil, unavailable
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, nodeBearerAdmissionLimit+1))
	if err != nil || len(data) > nodeBearerAdmissionLimit {
		return nil, unavailable
	}
	var proof nodeBearerAdmission
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&proof) != nil || decoder.Decode(&struct{}{}) != io.EOF || proof.WriterID != identity.WriterID || proof.Generation != state.Generation || proof.AuthorityDigest != state.Digest || proof.TokenDigest != hashNodeSecret(token) || len(proof.Claims) == 0 {
		return nil, unavailable
	}
	return proof.Claims, nil
}

func nodeBearerUnavailable(err error) bool {
	var failure *nodeBearerAdmissionError
	return errors.As(err, &failure) && failure.unavailable
}

// A locally provisioned shared signer may be used only against an intact,
// fully applied snapshot belonging to the node's enrolled writer. The caller
// still verifies signature, expiry, issuer, audience, resource and local policy.
func (s *Server) readerOfflineAuthReady() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.authState
	if !s.authInitialized || !state.ReadOnly || state.Pending || state.Generation == 0 ||
		s.nodeIdentity == nil || s.nodeIdentity.WriterID == "" ||
		s.nodeIdentity.WriterID != s.cfg.AuthSourceID || state.WriterID != s.nodeIdentity.WriterID {
		return false
	}
	bundle, err := s.readAuthSlot(state)
	return err == nil && authSnapshotDigest(bundle) == state.Digest
}
