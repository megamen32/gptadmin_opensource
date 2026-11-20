package hub

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
)

// This peer endpoint accepts only exact reads. It never routes another hop.
const ownerReadPath = "/internal/owner/read"

type ownerHint struct {
	OwnerID       string `json:"owner_id,omitempty"`
	OwnerTarget   string `json:"owner_target,omitempty"`
	OwnerEndpoint string `json:"owner_endpoint,omitempty"`
}

func lookupHint(args map[string]any) ownerHint {
	return ownerHint{firstString(args, "owner_id", "owner_node_id"), firstString(args, "owner_target"), firstString(args, "owner_endpoint")}
}

type ownerRead struct {
	Action string         `json:"action"`
	ID     string         `json:"id"`
	Hint   ownerHint      `json:"owner"`
	Query  string         `json:"query,omitempty"`
	Opts   map[string]any `json:"options,omitempty"`
	RO     bool           `json:"read_only,omitempty"`
	// This binds a forwarded read to the same locally authorized profile and
	// caller. It grants no rights: the recipient authenticates the bearer.
	Principal      string `json:"principal,omitempty"`
	AuthorityIndex uint64 `json:"authority_index,omitempty"`
}

func (q ownerRead) kind() string {
	switch q.Action {
	case "task", "job":
		return "task"
	case "status", "read", "readAll", "search", "indexSource":
		return "content"
	}
	return ""
}

type lookupFailure struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

var (
	readMissing = lookupFailure{"not_found", "no accessible record at this owner", false}
	readHistory = lookupFailure{"history_unavailable", "The owner of this legacy ID is unknown. Supply the owner hint from the original receipt. You can send new commands to available executors.", false}
	readOffline = lookupFailure{"owner_unavailable", "The original owner is unavailable. Retry this lookup when the owner returns. You can send new commands to available executors.", true}
	readInvalid = lookupFailure{"invalid_arguments", "invalid or conflicting owner handle or hint", false}
)

type ownerReadReply struct {
	OwnerID string         `json:"owner_id"`
	ID      string         `json:"id"`
	Value   any            `json:"value,omitempty"`
	Failure *lookupFailure `json:"failure,omitempty"`
	Origin  string         `json:"content_origin,omitempty"`
}

func (f lookupFailure) rpc() map[string]any {
	code := -32602
	if f.Retryable {
		code = -32002
	}
	return map[string]any{"code": code, "message": f.Message, "data": f}
}

func (b *codeModeBridge) lookupError(f *lookupFailure, requestedID string) *codeModeError {
	receipt := map[string]any{"retryable": f.Retryable}
	// Only echo the caller's own handle. A missing or inaccessible row has
	// the same response and never adds owner metadata from storage.
	if f.Code != readInvalid.Code && len(requestedID) > 0 && len(requestedID) <= 256 {
		receipt["id"] = requestedID
	}
	return b.fail(f.Code, f.Message, receipt)
}

func (q *ownerRead) validate() *lookupFailure {
	if q.ID == "" || len(q.ID) > 256 || q.kind() == "" {
		return &readInvalid
	}
	h, scoped, err := decodeOwnerHandle(q.ID)
	if err != nil || scoped && (h.Kind != q.kind() || q.Hint.OwnerID != "" && q.Hint.OwnerID != h.OwnerID) {
		return &readInvalid
	}
	if scoped {
		q.Hint.OwnerID = h.OwnerID
	}
	if q.Hint.OwnerID != "" && !validOwnerID(q.Hint.OwnerID) ||
		q.Hint.OwnerTarget != "" && validateNodeMemberName(q.Hint.OwnerTarget) != nil {
		return &readInvalid
	}
	if q.Hint.OwnerEndpoint != "" {
		origin, err := validateNodeEndpointURL(q.Hint.OwnerEndpoint)
		if err != nil {
			return &readInvalid
		}
		q.Hint.OwnerEndpoint = origin
	}
	return nil
}

type lookupPeer struct {
	id, target, origin string
	private            bool
}

func (h ownerHint) matches(p lookupPeer) bool {
	return (h.OwnerID == "" || h.OwnerID == p.id) &&
		(h.OwnerTarget == "" || h.OwnerTarget == p.target) &&
		(h.OwnerEndpoint == "" || h.OwnerEndpoint == strings.TrimRight(p.origin, "/"))
}

func (s *Server) localLookupPeer() lookupPeer {
	name, _, _ := s.memberLocalExecutorIdentity()
	return lookupPeer{id: s.localReadbackOwnerID(), target: name, origin: s.cfg.NodePublicURL}
}

// Only the local routing table is inspected. A configured peer's physical
// route is the destination; user-supplied endpoint strings are comparisons.
func (s *Server) lookupPeer(h ownerHint) (lookupPeer, bool) {
	var match lookupPeer
	found := false
	add := func(p lookupPeer) bool {
		if !h.matches(p) {
			return true
		}
		if found && match != p {
			return false
		}
		found, match = true, p
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	candidates := make(map[string]nodePeerRoute, len(s.nodePeers)+len(s.membershipRoutes))
	for target, p := range s.nodePeers {
		candidates[target] = p
	}
	for target, p := range s.membershipRoutes {
		candidates[target] = p
	}
	for target, p := range candidates {
		if s.quorumPrivateTLS != nil {
			if voter, ok := s.quorumPrivateTLS.peers[p.NodeID]; ok {
				if !add(lookupPeer{voter.ID, target, voter.HubURL, true}) {
					return lookupPeer{}, false
				}
				continue
			}
		}
		if !add(lookupPeer{p.NodeID, target, p.URL, false}) {
			return lookupPeer{}, false
		}
	}
	// Voters have a physical mTLS route even while their executor lease is
	// draining. A handle names the Hub storing the body, independent of that lease.
	if !found && h.OwnerTarget == "" && s.quorumPrivateTLS != nil {
		for _, p := range s.quorumPrivateTLS.peers {
			if !add(lookupPeer{p.ID, "", p.HubURL, true}) {
				return lookupPeer{}, false
			}
		}
	}
	return match, found
}

// Older tools/call job receipts used owner_target both for transport and for
// a child/paired surface. Preserve that path when no physical Hub is identified.
func legacyJobTarget(args map[string]any) bool {
	_, scoped, _ := decodeOwnerHandle(firstString(args, "id", "job_id"))
	h := lookupHint(args)
	return !scoped && h.OwnerTarget != "" && h.OwnerID == "" && h.OwnerEndpoint == ""
}

func (s *Server) lookupJob(r *http.Request, args map[string]any) ownerReadReply {
	return s.lookupOwner(r, ownerRead{Action: "job", ID: firstString(args, "id", "job_id"), Hint: lookupHint(args), Opts: map[string]any{"detail": args["detail"]}})
}

func (s *Server) lookupOwner(r *http.Request, q ownerRead) ownerReadReply {
	if failure := q.validate(); failure != nil {
		return ownerReadReply{Failure: failure}
	}
	here := s.localLookupPeer()
	if q.Hint.matches(here) {
		reply := s.readAtOwner(r, q, here.id)
		if q.Hint == (ownerHint{}) && reply.Failure == &readMissing {
			reply.Failure = &readHistory
		}
		return reply
	}
	peer, ok := s.lookupPeer(q.Hint)
	if !ok || r == nil || r.Header.Get(nodePeerHopHeader) != "" {
		return ownerReadReply{Failure: &readOffline}
	}
	if r.Header.Get("Authorization") == "" {
		return ownerReadReply{Failure: &readMissing}
	}
	if peer.id != "" {
		q.Hint.OwnerID = peer.id
	} else if q.Hint.OwnerTarget == "" {
		q.Hint.OwnerTarget = peer.target
	}
	// Only a caller-provided endpoint is a continuity pin. A route's public
	// alias can differ from the owner's local listener (e.g. behind NAT).
	q.RO = readOnlyExecRequest(r)
	q.Principal = operationPrincipal(r)
	if peer.private {
		if s.quorumNode.VerifyLeader(r.Context()) != nil {
			return ownerReadReply{Failure: &readOffline}
		}
		q.AuthorityIndex = s.quorumNode.ExternalAppliedIndex()
	}
	body, err := json.Marshal(q)
	if err != nil {
		return ownerReadReply{Failure: &readInvalid}
	}
	var data []byte
	var status int
	if peer.private {
		p := s.quorumPrivateTLS.peers[peer.id]
		transport, err := s.quorumPrivateTLS.transport(peer.id)
		if err != nil {
			return ownerReadReply{Failure: &readOffline}
		}
		defer transport.CloseIdleConnections()
		req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, strings.TrimRight(p.InternalHubURL, "/")+ownerReadPath, bytes.NewReader(body))
		if err != nil {
			return ownerReadReply{Failure: &readOffline}
		}
		req.Header.Set("Authorization", r.Header.Get("Authorization"))
		req.Header.Set("Content-Type", "application/json")
		releaseAuthAdmission(r)
		res, err := (&http.Client{Transport: transport, CheckRedirect: refuseNodeRedirect, Timeout: s.nodePeerClient.Timeout}).Do(req)
		if err != nil {
			return ownerReadReply{Failure: &readOffline}
		}
		defer res.Body.Close()
		status = res.StatusCode
		data, err = io.ReadAll(io.LimitReader(res.Body, (64<<20)+1))
		if err != nil || len(data) > 64<<20 {
			return ownerReadReply{Failure: &readOffline}
		}
	} else {
		w := httptest.NewRecorder()
		if !s.forwardNodePeerRequest(w, r, peer.target, body, ownerReadPath) {
			return ownerReadReply{Failure: &readOffline}
		}
		status, data = w.Code, w.Body.Bytes()
	}
	var reply ownerReadReply
	if status != http.StatusOK || decodeOwnerReply(data, &reply) != nil || reply.ID != q.ID ||
		(peer.id != "" && reply.OwnerID != peer.id) || reply.Value == nil && reply.Failure == nil {
		return ownerReadReply{Failure: &readOffline}
	}
	return reply
}

func decodeOwnerReply(data []byte, reply *ownerReadReply) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	return d.Decode(reply)
}

// Only a TLS-authenticated current leader can deliver a read to a voter
// outside normal leader admission. The owner still checks the original bearer.
func (s *Server) readFromQuorumLeader(r *http.Request) bool {
	if s.quorumPrivateTLS == nil || r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
		return false
	}
	leader := s.quorumNode.Status().LeaderID
	p, ok := s.quorumPrivateTLS.peers[leader]
	return ok && r.TLS.PeerCertificates[0].VerifyHostname(p.ServerName) == nil
}

type ownerReadAuthorityKey struct{}

// A voter may have been a writer before the election. Its ordinary auth
// slots and enrolled writer URL can still describe that term. Verify the
// original bearer against the locally applied policy for this admitted read.
// The request owns the in-memory auth view; it never installs a snapshot.
func (s *Server) authenticateVoterRead(w http.ResponseWriter, r *http.Request, index uint64) (*http.Request, bool) {
	var bundle authSnapshot
	var security securitySettings
	if !s.readFromQuorumLeader(r) || index == 0 || s.quorumNode.ExternalAppliedIndex() < index ||
		s.readQuorumAuthorityState("auth", "bundle", &bundle) != nil || bundle.Generation == 0 || bundle.Tokens == nil || bundle.Clients == nil ||
		s.readQuorumAuthorityState("security", "settings", &security) != nil {
		writeJSON(w, http.StatusServiceUnavailable, readOffline)
		return r, false
	}
	view := &Server{cfg: s.cfg, security: security, authRate: make(map[string]authRateWindow)}
	view.cfg.ConfigDir, view.cfg.AuthMode = "", ""
	view.installAuthMapsLocked(bundle)
	if !view.mcpAuth(w, r) {
		return r, false
	}
	// Apply identity-bound targets against the physical owner's registry.
	if prof, ok := AccessProfileFromRequest(r); ok && bundle.Profiles != nil {
		if stored, ok := bundle.Profiles.Profiles[prof.ID]; ok {
			r = requestWithAccessProfile(r, s.resolveAccessProfileTargets(stored))
		}
	}
	return r.WithContext(context.WithValue(r.Context(), ownerReadAuthorityKey{}, view)), true
}

func (s *Server) ownerReadHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if s.quorumNode == nil && !s.mcpAuth(w, r) {
		return
	}
	var q ownerRead
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&q); err != nil || q.validate() != nil || q.Hint == (ownerHint{}) {
		writeJSON(w, http.StatusBadRequest, readInvalid)
		return
	}
	if s.quorumNode != nil {
		var ok bool
		r, ok = s.authenticateVoterRead(w, r, q.AuthorityIndex)
		if !ok {
			return
		}
	}
	here := s.localLookupPeer()
	if !q.Hint.matches(here) || q.Principal == "" || q.Principal != operationPrincipal(r) {
		writeJSON(w, http.StatusServiceUnavailable, readOffline)
		return
	}
	if q.RO || requestAccessMode(r) == accessModeReadonly {
		r = requestWithReadOnlyExec(r)
	}
	facade := "job"
	if q.kind() == "content" {
		facade = "exec"
		if readOnlyExecRequest(r) {
			facade = "ro"
		}
	}
	if q.Action != "task" && s.authorizePublicFacade(r, facade, nil) != nil {
		writeJSON(w, http.StatusOK, ownerReadReply{OwnerID: here.id, ID: q.ID, Failure: &readMissing})
		return
	}
	writeJSON(w, http.StatusOK, s.readAtOwner(r, q, here.id))
}

func (s *Server) readAtOwner(r *http.Request, q ownerRead, ownerID string) ownerReadReply {
	reply := ownerReadReply{OwnerID: ownerID, ID: q.ID}
	switch q.Action {
	case "task":
		value, rpcErr := s.mcpTaskGetForRequest(r, q.ID, "")
		if rpcErr == nil {
			reply.Value = value
		} else if intFromAny(mapValue(rpcErr)["code"]) == -32602 {
			reply.Failure = &readMissing
		} else {
			reply.Failure = &readOffline
		}
	case "job":
		if err := validateToolOutputDetail(q.Opts["detail"]); err != nil {
			reply.Failure = &readInvalid
			break
		}
		value, ok := s.taskReceiptForRequest(r, q.ID, q.Opts["detail"])
		if !ok {
			reply.Failure = &readMissing
		} else if firstString(mapValue(value["error"]), "code") == "task_persistence_failed" {
			reply.Failure = &readOffline
		} else {
			reply.Value = value
		}
	default:
		store, err := s.contentStoreForExec()
		if err != nil {
			reply.Failure = &readOffline
			break
		}
		b := &codeModeBridge{server: s, request: r, ctx: r.Context(), readOnly: readOnlyExecRequest(r)}
		meta, err := store.Source(operationPrincipal(r), q.ID)
		if errors.Is(err, sql.ErrNoRows) || err == nil && !b.contentSourceAllowed(meta) {
			reply.Failure = &readMissing
		} else if err != nil {
			reply.Failure = &readOffline
		} else {
			reply.Value, reply.Failure = b.readContentSource(store, meta, q)
			reply.Origin = meta.Origin
		}
	}
	return reply
}
