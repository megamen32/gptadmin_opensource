package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const nodeSecretResolvePath = "/admin/api/nodes/secret-resolve"
const nodeSecretResolveLimit = 1 << 20

type nodeSecretResolveRequest struct {
	Token  string         `json:"token"`
	Target string         `json:"target"`
	Ref    string         `json:"ref"`
	Args   map[string]any `json:"args"`
}

// A ready secret currently outlives its ingress TTL; preserve SecretStore.Resolve
// semantics here. The ingress plan describes expiry of refs more broadly. Owner
// identity is the existing secretOwnerFingerprint principal (sub before client_id),
// not a new guarantee that OAuth clients sharing a subject are isolated.
func (s *Server) nodeSecretResolveHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	deny := func(status int) { writeJSON(w, status, map[string]any{"detail": "secret resolve rejected"}) }
	if r.Method != http.MethodPost {
		deny(http.StatusMethodNotAllowed)
		return
	}
	if s.effectiveAuthMode() != "writer" {
		deny(http.StatusConflict)
		return
	}
	member := s.nodeMemberAuth(r)
	if member == nil {
		deny(http.StatusUnauthorized)
		return
	}
	if member.State != nodeStateReady || !member.LeaseExpires.After(s.now()) || !containsString(member.Capabilities, "executor") {
		deny(http.StatusForbidden)
		return
	}
	controller := http.NewResponseController(w)
	if err := controller.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		deny(http.StatusServiceUnavailable)
		return
	}
	defer controller.SetReadDeadline(time.Time{})
	select {
	case s.nodeBearerAdmissionSlots <- struct{}{}:
		defer func() { <-s.nodeBearerAdmissionSlots }()
	default:
		deny(http.StatusServiceUnavailable)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, nodeSecretResolveLimit+1))
	if err != nil || len(raw) > nodeSecretResolveLimit {
		deny(http.StatusBadRequest)
		return
	}
	var input nodeSecretResolveRequest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF || !validNodeAdmissionToken(input.Token) || input.Ref == "" || input.Target != member.Name || !strings.HasPrefix(input.Target, "shell:") || input.Args == nil {
		deny(http.StatusForbidden)
		return
	}
	// Node endpoints bypass the normal snapshot gate. Hold the generation gate
	// during authentication, authorization and resolution.
	s.authMu.RLock()
	defer s.authMu.RUnlock()
	s.mu.Lock()
	state := s.authState
	bundle, readErr := s.readAuthSlot(state)
	ready := s.authInitialized && !state.Pending && state.Generation != 0 && readErr == nil
	s.mu.Unlock()
	if !ready || s.secretStore == nil || s.secretStoreErr != nil {
		deny(http.StatusServiceUnavailable)
		return
	}
	caller, err := http.NewRequestWithContext(r.Context(), http.MethodPost, strings.TrimRight(s.cfg.PublicOrigin, "/")+"/mcp", nil)
	if err != nil || s.cfg.PublicOrigin == "" {
		deny(http.StatusServiceUnavailable)
		return
	}
	caller.RemoteAddr = r.RemoteAddr
	caller.Header.Set("Authorization", "Bearer "+input.Token)
	authResponse := &nodeAdmissionAuthWriter{header: make(http.Header)}
	if !s.mcpAuth(authResponse, caller) || authorizeToolAction(caller, input.Target, "shell_exec", input.Args) != nil {
		deny(http.StatusForbidden)
		return
	}
	owner := secretOwnerFingerprint(caller)
	if owner == "" {
		deny(http.StatusForbidden)
		return
	}
	value, err := s.secretStore.Resolve(input.Ref, owner)
	if err != nil {
		deny(http.StatusForbidden)
		return
	}
	bundle.WriterID = s.authLocalID
	s.excludeSnapshotControlCredentials(&bundle)
	writeJSON(w, http.StatusOK, map[string]any{"value": value, "writer_id": state.WriterID, "generation": state.Generation, "authority_digest": authSnapshotDigest(bundle), "token_digest": hashNodeSecret(input.Token)})
}

func (s *Server) resolveNodeWriterSecret(r *http.Request, target, ref string, args map[string]any) (string, error) {
	s.mu.Lock()
	var identity nodeIdentity
	if s.nodeIdentity != nil {
		identity = *s.nodeIdentity
	}
	state := s.authState
	local := s.localExecutors[target] != nil
	initialized := s.authInitialized
	s.mu.Unlock()
	if !local || s.effectiveAuthMode() != "reader" || !initialized || !state.ReadOnly || state.Pending || state.Generation == 0 || identity.NodeKey == "" || identity.WriterID == "" || identity.WriterID != s.cfg.AuthSourceID || identity.WriterID != state.WriterID {
		return "", ErrSecretNotFound
	}
	u, err := url.Parse(identity.WriterURL)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "::1" || u.Hostname() == "localhost"))) {
		return "", ErrSecretNotFound
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		return "", ErrSecretNotFound
	}
	token := strings.TrimSpace(auth[7:])
	if !validNodeAdmissionToken(token) {
		return "", ErrSecretNotFound
	}
	body, err := json.Marshal(nodeSecretResolveRequest{Token: token, Target: target, Ref: ref, Args: args})
	if err != nil || len(body) > nodeSecretResolveLimit {
		return "", ErrSecretNotFound
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(identity.WriterURL, "/")+nodeSecretResolvePath, bytes.NewReader(body))
	if err != nil {
		return "", ErrSecretNotFound
	}
	req.Header.Set("Authorization", "Bearer "+identity.NodeKey)
	req.Header.Set("Content-Type", "application/json")
	req.GetBody = nil
	resp, err := s.nodeBearerAdmissionClient.Do(req)
	if err != nil {
		return "", ErrSecretNotFound
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", ErrSecretNotFound
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, nodeSecretResolveLimit+1))
	if err != nil || len(data) > nodeSecretResolveLimit {
		return "", ErrSecretNotFound
	}
	var output struct {
		Value           *string `json:"value"`
		WriterID        string  `json:"writer_id"`
		Generation      uint64  `json:"generation"`
		AuthorityDigest string  `json:"authority_digest"`
		TokenDigest     string  `json:"token_digest"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(&output) != nil || dec.Decode(&struct{}{}) != io.EOF || output.Value == nil || output.WriterID != identity.WriterID || output.Generation != state.Generation || output.AuthorityDigest != state.Digest || output.TokenDigest != hashNodeSecret(token) {
		return "", ErrSecretNotFound
	}
	return *output.Value, nil
}
