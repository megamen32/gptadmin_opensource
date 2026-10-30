package hub

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Node mesh membership. One writer owns the authoritative membership state;
// member nodes hold a replicated view and route peer targets from it. Static
// GPTADMIN_NODE_PEERS entries remain a bootstrap fallback, never an authority.

const (
	nodeMembershipSchema    = 1
	nodeIdentitySchema      = "gptadmin.node-identity/v1"
	nodeHeartbeatInterval   = 30 * time.Second
	nodeLeaseTTL            = 90 * time.Second
	nodeMembershipSyncEvery = 5 * time.Minute
	nodeGrantDefaultTTL     = 30 * time.Minute
	nodeGrantMaxTTL         = 24 * time.Hour
	nodeGrantRetention      = time.Hour
	// Control envelopes add instruction sets and routing around the bounded
	// auth bundle; the admission limit grows by exactly this headroom.
	nodeControlEnvelopeHeadroom = 256 << 10
	nodeMembershipStateLimit    = 1 << 20
)

const (
	nodeStateJoining  = "joining"
	nodeStateReady    = "ready"
	nodeStateDraining = "draining"
	nodeStateOffline  = "offline"
	nodeStateDisabled = "disabled"
)

var nodeCapabilities = []string{"ingress", "executor", "replica"}

// nodeIdentity is created once by the lifecycle manager and never regenerated.
// node_key authenticates this node against the writer's membership endpoints.
type nodeIdentity struct {
	Schema    string    `json:"schema"`
	NodeID    string    `json:"node_id"`
	NodeKey   string    `json:"node_key"`
	Created   time.Time `json:"created"`
	WriterURL string    `json:"writer_url,omitempty"`
	WriterID  string    `json:"writer_id,omitempty"`
}

const (
	nodeIdentityTag = "gptadmin.node-identity/v1"
)

type nodeJoinGrant struct {
	Name       string    `json:"name"`
	SecretHash string    `json:"secret_hash"`
	IssuedAt   time.Time `json:"issued_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Used       bool      `json:"used"`
	UsedBy     string    `json:"used_by,omitempty"`
	UsedAt     time.Time `json:"used_at,omitempty"`
}

type nodeMember struct {
	NodeID       string    `json:"node_id"`
	KeyHash      string    `json:"key_hash"`
	Name         string    `json:"name"`
	Capabilities []string  `json:"capabilities"`
	Endpoints    []string  `json:"endpoints"`
	ConnectTo    string    `json:"connect_to,omitempty"`
	Generation   uint64    `json:"generation"`
	State        string    `json:"state"`
	LastSeen     time.Time `json:"last_seen"`
	LeaseExpires time.Time `json:"lease_expires"`
	Build        string    `json:"build,omitempty"`
	SnapshotRev  uint64    `json:"snapshot_revision,omitempty"`
	LocalID      string    `json:"local_id,omitempty"`
}

type nodeMembershipState struct {
	Schema    uint64                    `json:"schema"`
	Revision  uint64                    `json:"revision"`
	Epoch     uint64                    `json:"epoch"`
	WriterID  string                    `json:"writer_id"`
	WriterURL string                    `json:"writer_url,omitempty"`
	UpdatedAt time.Time                 `json:"updated_at"`
	Grants    map[string]*nodeJoinGrant `json:"grants,omitempty"`
	Nodes     map[string]*nodeMember    `json:"nodes"`
}

// nodeMembershipView is the member-side replica used to build routes.
type nodeMembershipView struct {
	Schema    uint64        `json:"schema"`
	Revision  uint64        `json:"revision"`
	SHA256    string        `json:"sha256"`
	FetchedAt time.Time     `json:"fetched_at"`
	WriterID  string        `json:"writer_id"`
	Nodes     []*nodeMember `json:"nodes"`
}

// controlSnapshot is the minimal bootstrap payload for a member: the bounded
// auth bundle plus the named instruction sets profiles validate against.
type controlSnapshot struct {
	Revision        uint64                `json:"revision"`
	Generation      uint64                `json:"generation"`
	SHA256          string                `json:"sha256"`
	Bundle          authSnapshot          `json:"bundle"`
	InstructionSets *instructionSetsState `json:"instruction_sets,omitempty"`
}

// nodePeerRouteLocked resolves a peer route: membership is authoritative,
// static GPTADMIN_NODE_PEERS entries are only the bootstrap fallback. The
// process's pinned local executor always wins before this is consulted.
func (s *Server) nodePeerRouteLocked(target string) (nodePeerRoute, bool) {
	if s.membershipRoutes != nil {
		if route, ok := s.membershipRoutes[target]; ok {
			return route, true
		}
	}
	route, ok := s.nodePeers[target]
	return route, ok
}

func hashNodeSecret(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (s *Server) membershipDir() string {
	return filepath.Join(s.cfg.ConfigDir, "membership")
}

func (s *Server) membershipStatePath() string {
	return filepath.Join(s.membershipDir(), "membership.json")
}

func (s *Server) memberViewPath() string {
	return filepath.Join(s.membershipDir(), "member-view.json")
}

func (s *Server) nodeIdentityPath() string {
	if s.cfg.NodeIdentityFile != "" {
		return s.cfg.NodeIdentityFile
	}
	if s.cfg.ConfigDir == "" {
		return ""
	}
	return filepath.Join(s.cfg.ConfigDir, "node-identity.json")
}

func loadNodeIdentityFile(path string) *nodeIdentity {
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("node identity unreadable path=%s err=%v", path, err)
		}
		return nil
	}
	var identity nodeIdentity
	if err := json.Unmarshal(raw, &identity); err != nil {
		log.Printf("node identity malformed path=%s err=%v", path, err)
		return nil
	}
	if identity.NodeID == "" || identity.NodeKey == "" {
		log.Printf("node identity incomplete path=%s", path)
		return nil
	}
	return &identity
}

func saveNodeIdentityFile(path string, identity nodeIdentity) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(identity, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func loadNodeMembershipState(path string) (*nodeMembershipState, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &nodeMembershipState{Schema: nodeMembershipSchema, Revision: 1,
				Grants: map[string]*nodeJoinGrant{}, Nodes: map[string]*nodeMember{}}, nil
		}
		return nil, err
	}
	var state nodeMembershipState
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, err
	}
	if state.Schema != nodeMembershipSchema || state.Nodes == nil {
		return nil, errors.New("invalid membership schema")
	}
	if state.Grants == nil {
		state.Grants = map[string]*nodeJoinGrant{}
	}
	for id, member := range state.Nodes {
		if member == nil || member.NodeID != id {
			return nil, fmt.Errorf("invalid membership node %q", id)
		}
	}
	return &state, nil
}

func loadNodeMembershipView(path string) *nodeMembershipView {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var view nodeMembershipView
	if json.Unmarshal(raw, &view) != nil || view.Schema != nodeMembershipSchema || view.Nodes == nil {
		log.Printf("membership view malformed path=%s", path)
		return nil
	}
	return &view
}

func (s *Server) persistMembershipStateLocked(state *nodeMembershipState) error {
	state.UpdatedAt = s.now()
	if err := s.commitQuorumAuthorityState("membership", "writer", membershipQuorumAuthorityState(state)); err != nil {
		return fmt.Errorf("commit quorum membership state: %w", err)
	}
	if err := os.MkdirAll(s.membershipDir(), 0o750); err != nil {
		return err
	}
	return writeAccessStateJSON(s.membershipStatePath(), state, nodeMembershipStateLimit)
}

func membershipQuorumAuthorityState(state *nodeMembershipState) *nodeMembershipState {
	if state == nil {
		return nil
	}
	copyState := *state
	copyState.UpdatedAt = time.Time{}
	copyState.Grants = make(map[string]*nodeJoinGrant, len(state.Grants))
	for name, grant := range state.Grants {
		if grant == nil {
			copyState.Grants[name] = nil
			continue
		}
		copyGrant := *grant
		copyState.Grants[name] = &copyGrant
	}
	copyState.Nodes = make(map[string]*nodeMember, len(state.Nodes))
	for id, member := range state.Nodes {
		if member == nil {
			copyState.Nodes[id] = nil
			continue
		}
		copyMember := *member
		copyMember.Endpoints = append([]string(nil), member.Endpoints...)
		copyMember.Capabilities = append([]string(nil), member.Capabilities...)
		copyMember.LastSeen = time.Time{}
		copyMember.LeaseExpires = time.Time{}
		copyMember.Build = ""
		copyMember.SnapshotRev = 0
		copyState.Nodes[id] = &copyMember
	}
	return &copyState
}

// membershipSnapshotJSON is the wire form members replicate; grants never leave
// the writer.
func membershipSnapshotJSON(state *nodeMembershipState) ([]byte, string) {
	ids := make([]string, 0, len(state.Nodes))
	for id := range state.Nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	nodes := make([]*nodeMember, 0, len(ids))
	for _, id := range ids {
		clone := *state.Nodes[id]
		nodes = append(nodes, &clone)
	}
	payload := map[string]any{
		"schema":     state.Schema,
		"revision":   state.Revision,
		"epoch":      state.Epoch,
		"writer_id":  state.WriterID,
		"writer_url": state.WriterURL,
		"nodes":      nodes,
	}
	raw, _ := json.Marshal(payload)
	return raw, hashNodeSecret(string(raw))
}

func (s *Server) initNodeMembership() {
	s.nodeIdentity = loadNodeIdentityFile(s.nodeIdentityPath())
	if s.cfg.AuthMode == "writer" {
		state, err := loadNodeMembershipState(s.membershipStatePath())
		if err != nil {
			log.Printf("membership state unavailable path=%s err=%v; join/grant disabled", s.membershipStatePath(), err)
			s.membershipLoadErr = err
		} else {
			s.membership = state
		}
	} else {
		s.membershipView = loadNodeMembershipView(s.memberViewPath())
	}
	s.rebuildMembershipRoutes()
}

// ensureWriterSelfEntry registers the writer itself as a member so every view
// describes the whole mesh. Called once the local executor identity is known.
func (s *Server) ensureWriterSelfEntry() {
	if s.membership == nil {
		return
	}
	s.mu.Lock()
	localNames := make([]string, 0, len(s.localExecutors))
	for name := range s.localExecutors {
		localNames = append(localNames, name)
	}
	s.mu.Unlock()
	if len(localNames) != 1 {
		return
	}
	now := s.now()
	s.membershipMu.Lock()
	if s.membership == nil {
		s.membershipMu.Unlock()
		return
	}
	if s.membership.WriterID == "" {
		// The writer identity is the local executor server_id once auth
		// continuity is initialized; it never changes for this state file.
		s.membership.WriterID = s.authLocalID
		if s.membership.WriterID == "" {
			s.membershipMu.Unlock()
			return
		}
	}
	self, ok := s.membership.Nodes[s.membership.WriterID]
	if !ok {
		self = &nodeMember{NodeID: s.membership.WriterID, Name: "shell:" + localNames[0],
			Capabilities: append([]string(nil), nodeCapabilities...), State: nodeStateReady}
		if s.cfg.NodePublicURL != "" {
			self.Endpoints = []string{s.cfg.NodePublicURL}
		}
		s.membership.Nodes[self.NodeID] = self
	}
	self.Name = localNames[0]
	if s.cfg.NodePublicURL != "" {
		self.Endpoints = []string{s.cfg.NodePublicURL}
	}
	self.LastSeen = now
	self.LeaseExpires = now.Add(nodeLeaseTTL)
	self.Build = GitCommit
	err := s.persistMembershipStateLocked(s.membership)
	s.membershipMu.Unlock()
	if err != nil {
		log.Printf("membership self entry persist failed: %v", err)
	}
	s.rebuildMembershipRoutes()
}

// rebuildMembershipRoutes derives peer routes from the authoritative state
// (writer) or the replicated view (member). Leases gate dispatch without
// deleting identity or membership. On a member, the heartbeat-supplied writer
// URL is the authoritative route to the writer's own shell target even when
// the view's writer entry lacks endpoints, because membership never requires
// the writer to advertise its origin (it does so via heartbeat).
func (s *Server) rebuildMembershipRoutes() {
	now := s.now()
	var nodes []*nodeMember
	s.membershipMu.Lock()
	if s.membership != nil {
		for _, member := range s.membership.Nodes {
			nodes = append(nodes, member)
		}
	} else if s.membershipView != nil {
		nodes = s.membershipView.Nodes
	}
	heartbeatWriterID, heartbeatWriterURL := s.writerHeartbeatContextLocked()
	s.membershipMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	routes := map[string]nodePeerRoute{}
	for _, member := range nodes {
		if member == nil || member.NodeID == s.authLocalID || member.NodeID == s.selfNodeIDLocked() {
			continue
		}
		if member.State != nodeStateReady || !member.LeaseExpires.After(now) {
			continue
		}
		if s.localExecutors[member.Name] != nil {
			continue
		}
		if len(member.Endpoints) == 0 && !(s.nodeIdentity != nil && s.nodeIdentity.WriterID == member.NodeID && heartbeatWriterURL != "") {
			continue
		}
		endpoint := heartbeatWriterURL
		if len(member.Endpoints) > 0 {
			endpoint = member.Endpoints[0]
		}
		routes[member.Name] = nodePeerRoute{URL: endpoint, ConnectTo: member.ConnectTo}
	}
	_ = heartbeatWriterID
	s.membershipRoutes = routes
}

// writerHeartbeatContextLocked returns the writer identity and URL the local
// node most recently learned from membership heartbeat responses, under
// membershipMu.
func (s *Server) writerHeartbeatContextLocked() (string, string) {
	if s.nodeIdentity == nil {
		return "", ""
	}
	return s.nodeIdentity.WriterID, s.nodeIdentity.WriterURL
}

func (s *Server) selfNodeIDLocked() string {
	if s.nodeIdentity != nil {
		return s.nodeIdentity.NodeID
	}
	return ""
}

func validateNodeMemberName(name string) error {
	alias := strings.TrimPrefix(name, "shell:")
	if !strings.HasPrefix(name, "shell:") || alias == "" || alias != canonicalShellQueueName(alias) || strings.ContainsAny(alias, "/\\") {
		return fmt.Errorf("node name must be a canonical shell:<name> target")
	}
	return nil
}

func validateNodeEndpointURL(origin string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(origin))
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		(u.Path != "" && u.Path != "/") || u.Opaque != "" {
		return "", errors.New("endpoint must be an http(s) origin without credentials, path, query or fragment")
	}
	return strings.TrimSuffix(strings.TrimSpace(origin), "/"), nil
}

func normalizeNodeCapabilities(raw []string) []string {
	allowed := map[string]bool{}
	for _, capability := range nodeCapabilities {
		allowed[capability] = true
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(nodeCapabilities))
	for _, capability := range raw {
		capability = strings.TrimSpace(strings.ToLower(capability))
		if allowed[capability] && !seen[capability] {
			seen[capability] = true
			out = append(out, capability)
		}
	}
	if len(out) == 0 {
		return append([]string(nil), nodeCapabilities...)
	}
	sort.Strings(out)
	return out
}

// nodeMemberAuth resolves the bearer against stored node key hashes. It never
// accepts owner credentials: member endpoints are a separate authority class.
func (s *Server) nodeMemberAuth(r *http.Request) *nodeMember {
	bearer := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if bearer == "" || len(s.membership.Nodes) == 0 {
		return nil
	}
	digest := hashNodeSecret(bearer)
	s.membershipMu.Lock()
	defer s.membershipMu.Unlock()
	for _, member := range s.membership.Nodes {
		if member.KeyHash != "" && member.KeyHash == digest {
			clone := *member
			return &clone
		}
	}
	return nil
}

// meshSummaryLocked renders the mesh view for read-only tool output. mode
// names the topology this process runs: "writer" (central membership hub),
// "member" (replicated view from the writer), or "standalone" (static peers
// only, no central hub). Static peers and live member connections are always
// visible so the operator can see which topology is active and where every
// route comes from. Targets keep their owner node_id, state and dispatch
// readiness visible.
func (s *Server) meshSummaryLocked() map[string]any {
	now := s.now()
	mode := "standalone"
	if s.membership != nil {
		mode = "writer"
	} else if s.nodeIdentity != nil {
		mode = "member"
	}
	summary := map[string]any{
		"mode":             mode,
		"enabled":          mode != "standalone",
		"static_peers":     staticNodePeersSummary(s.nodePeers),
		"live_connections": s.nodeConns.names(),
	}
	var nodes []*nodeMemberListEntry
	var revision uint64
	var writerID string
	s.membershipMu.Lock()
	if s.membership != nil {
		revision, writerID = s.membership.Revision, s.membership.WriterID
		for _, member := range s.membership.Nodes {
			clone := *member
			clone.KeyHash = ""
			nodes = append(nodes, &nodeMemberListEntry{nodeMember: clone,
				RoutingReady: member.State == nodeStateReady && member.LeaseExpires.After(now) && len(member.Endpoints) > 0})
		}
	} else if s.membershipView != nil {
		revision, writerID = s.membershipView.Revision, s.membershipView.WriterID
		for _, member := range s.membershipView.Nodes {
			clone := *member
			clone.KeyHash = ""
			nodes = append(nodes, &nodeMemberListEntry{nodeMember: clone,
				RoutingReady: member.State == nodeStateReady && member.LeaseExpires.After(now) && len(member.Endpoints) > 0})
		}
	}
	s.membershipMu.Unlock()
	if nodes == nil {
		return summary
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].NodeID < nodes[j].NodeID })
	summary["revision"] = revision
	summary["writer_id"] = writerID
	summary["nodes"] = nodes
	return summary
}

// staticNodePeersSummary exposes the operator-configured bootstrap routes;
// routes carry no secrets, only origins and the optional physical IP pin.
func staticNodePeersSummary(peers map[string]nodePeerRoute) map[string]nodePeerRoute {
	out := make(map[string]nodePeerRoute, len(peers))
	for target, route := range peers {
		out[target] = route
	}
	return out
}

func (s *Server) nodeMembershipHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch strings.TrimPrefix(r.URL.Path, "/admin/api/nodes") {
	case "", "/":
		s.requireCtl(s.nodeMembershipList)(w, r)
	case "/grant":
		s.requireCtl(s.nodeMembershipGrant)(w, r)
	case "/join":
		s.nodeMembershipJoin(w, r)
	case "/heartbeat":
		s.nodeMembershipHeartbeat(w, r)
	case "/connect":
		s.nodeConnectHTTP(w, r)
	case "/membership":
		s.nodeMembershipGet(w, r)
	case "/control-snapshot":
		s.nodeMembershipControlSnapshot(w, r)
	case "/bearer-admission":
		s.nodeBearerAdmissionHTTP(w, r)
	case "/disable":
		s.requireCtl(func(w http.ResponseWriter, r *http.Request) { s.nodeMembershipSetState(w, r, nodeStateDisabled) })(w, r)
	case "/enable":
		s.requireCtl(func(w http.ResponseWriter, r *http.Request) { s.nodeMembershipSetState(w, r, nodeStateReady) })(w, r)
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "unknown nodes endpoint"})
	}
}

type nodeMemberListEntry struct {
	nodeMember
	RoutingReady bool `json:"routing_ready"`
}

func (s *Server) nodeMembershipList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "GET required"})
		return
	}
	now := s.now()
	s.membershipMu.Lock()
	var revision uint64
	var writerID, writerURL string
	entries := []*nodeMemberListEntry{}
	if s.membership != nil {
		revision, writerID, writerURL = s.membership.Revision, s.membership.WriterID, s.membership.WriterURL
		for _, member := range s.membership.Nodes {
			clone := *member
			clone.KeyHash = ""
			entries = append(entries, &nodeMemberListEntry{nodeMember: clone,
				RoutingReady: member.State == nodeStateReady && member.LeaseExpires.After(now) && len(member.Endpoints) > 0})
		}
	} else if s.membershipView != nil {
		revision, writerID = s.membershipView.Revision, s.membershipView.WriterID
		for _, member := range s.membershipView.Nodes {
			clone := *member
			clone.KeyHash = ""
			entries = append(entries, &nodeMemberListEntry{nodeMember: clone,
				RoutingReady: member.State == nodeStateReady && member.LeaseExpires.After(now) && len(member.Endpoints) > 0})
		}
	}
	s.membershipMu.Unlock()
	sort.Slice(entries, func(i, j int) bool { return entries[i].NodeID < entries[j].NodeID })
	writeJSON(w, http.StatusOK, map[string]any{
		"revision": revision, "writer_id": writerID, "writer_url": writerURL, "nodes": entries,
	})
}

func (s *Server) nodeMembershipGrant(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "POST required"})
		return
	}
	// requireCtl already enforced the owner credential class; grant issuing
	// stays behind the same admin gate as every other owner mutation.
	if s.membershipLoadErr != nil || s.membership == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "membership store unavailable"})
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<10))
	if err != nil || len(raw) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "grant request body required"})
		return
	}
	var request struct {
		Name       string `json:"name"`
		TTLMinutes int    `json:"ttl_minutes"`
	}
	if json.Unmarshal(raw, &request) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "grant request must be JSON"})
		return
	}
	if err := validateNodeMemberName(request.Name); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	ttl := nodeGrantDefaultTTL
	if request.TTLMinutes > 0 {
		ttl = time.Duration(request.TTLMinutes) * time.Minute
	}
	if ttl > nodeGrantMaxTTL {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "grant ttl exceeds the maximum"})
		return
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "grant generation failed"})
		return
	}
	now := s.now()
	grant := &nodeJoinGrant{Name: request.Name, SecretHash: hashNodeSecret(hex.EncodeToString(secret)),
		IssuedAt: now, ExpiresAt: now.Add(ttl)}
	s.membershipMu.Lock()
	s.membership.Grants[grant.SecretHash] = grant
	for hash, existing := range s.membership.Grants {
		if existing.ExpiresAt.Add(nodeGrantRetention).Before(now) {
			delete(s.membership.Grants, hash)
		}
	}
	if err := s.persistMembershipStateLocked(s.membership); err != nil {
		s.membershipMu.Unlock()
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "grant persistence failed"})
		return
	}
	s.membershipMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"grant": hex.EncodeToString(secret), "name": grant.Name,
		"expires_at": grant.ExpiresAt.UTC().Format(time.RFC3339)})
}

// nodeJoinBuild mirrors the package manifest summary a joining node carries.
type nodeJoinBuild struct {
	GitCommit    string `json:"git_commit"`
	BuildVersion any    `json:"build_version"`
}

type nodeJoinRequest struct {
	NodeID       string        `json:"node_id"`
	NodeKey      string        `json:"node_key"`
	Name         string        `json:"name"`
	Endpoints    []string      `json:"endpoints"`
	ConnectTo    string        `json:"connect_to"`
	Capabilities []string      `json:"capabilities"`
	Build        nodeJoinBuild `json:"build"`
	LocalID      string        `json:"local_id"`
}

func (s *Server) writerRedirect(w http.ResponseWriter, r *http.Request) bool {
	if s.membership != nil {
		return false
	}
	payload := map[string]any{"detail": "join and membership writes require the membership writer"}
	if s.nodeIdentity != nil && s.nodeIdentity.WriterURL != "" {
		payload["writer_url"] = s.nodeIdentity.WriterURL
	} else if s.membershipView != nil && s.membershipView.WriterID != "" {
		payload["writer_id"] = s.membershipView.WriterID
	}
	writeJSON(w, http.StatusConflict, payload)
	return true
}

func (s *Server) requestOrigin(r *http.Request) string {
	if s.cfg.NodePublicURL != "" {
		return s.cfg.NodePublicURL
	}
	scheme := "https"
	if r.TLS == nil {
		scheme = "http"
	}
	return scheme + "://" + r.Host
}

func (s *Server) nodeMembershipJoin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "POST required"})
		return
	}
	if s.writerRedirect(w, r) {
		return
	}
	if s.membershipLoadErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "membership store unavailable"})
		return
	}
	bearer := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if bearer == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"detail": "join grant required"})
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<12))
	if err != nil || len(raw) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "join body required"})
		return
	}
	var request nodeJoinRequest
	if json.Unmarshal(raw, &request) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "join body must be JSON"})
		return
	}
	now := s.now()
	if err := validateNodeMemberName(request.Name); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	if len(request.NodeID) < 16 || len(request.NodeID) > 64 || strings.TrimLeft(request.NodeID, "0123456789abcdef") != "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "node_id must be 16-64 hex characters"})
		return
	}
	if len(request.NodeKey) < 32 || len(request.NodeKey)%2 != 0 || strings.TrimLeft(request.NodeKey, "0123456789abcdef") != "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "node_key must be an even hex string of at least 32 characters"})
		return
	}
	endpoints := make([]string, 0, len(request.Endpoints))
	for _, origin := range request.Endpoints {
		normalized, err := validateNodeEndpointURL(origin)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
			return
		}
		endpoints = append(endpoints, normalized)
	}
	connectTo := strings.TrimSpace(request.ConnectTo)
	if connectTo != "" && net.ParseIP(connectTo) == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "connect_to must be a literal IP"})
		return
	}
	s.membershipMu.Lock()
	grant, ok := s.membership.Grants[hashNodeSecret(bearer)]
	if !ok || grant.Used || !now.Before(grant.ExpiresAt) {
		s.membershipMu.Unlock()
		writeJSON(w, http.StatusForbidden, map[string]any{"detail": "join grant is invalid, expired or already used"})
		return
	}
	if grant.Name != request.Name {
		s.membershipMu.Unlock()
		writeJSON(w, http.StatusForbidden, map[string]any{"detail": "join grant was issued for a different node name"})
		return
	}
	for _, member := range s.membership.Nodes {
		if member.NodeID == request.NodeID {
			continue
		}
		if member.State == nodeStateDisabled {
			continue
		}
		if member.Name == request.Name {
			s.membershipMu.Unlock()
			writeJSON(w, http.StatusConflict, map[string]any{"detail": "node name already belongs to another member"})
			return
		}
	}
	if s.authLocalID != "" && request.NodeID == s.authLocalID {
		s.membershipMu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]any{"detail": "node_id collides with the writer identity"})
		return
	}
	keyHash := hashNodeSecret(request.NodeKey)
	previous := s.membership.Nodes[request.NodeID]
	if previous != nil && previous.KeyHash != keyHash {
		s.membershipMu.Unlock()
		writeJSON(w, http.StatusForbidden, map[string]any{"detail": "node identity differs from the enrolled member"})
		return
	}
	member := &nodeMember{NodeID: request.NodeID, KeyHash: keyHash, Name: request.Name,
		Capabilities: normalizeNodeCapabilities(request.Capabilities), Endpoints: endpoints,
		ConnectTo: connectTo, State: nodeStateJoining, LastSeen: now, LeaseExpires: now.Add(nodeLeaseTTL),
		Build: strings.TrimSpace(request.Build.GitCommit), LocalID: strings.TrimSpace(request.LocalID)}
	if previous != nil {
		member.Generation = previous.Generation + 1
	} else {
		member.Generation = 1
	}
	s.membership.Nodes[request.NodeID] = member
	grant.Used = true
	grant.UsedBy = request.NodeID
	grant.UsedAt = now
	s.membership.Revision++
	if s.membership.WriterURL == "" {
		s.membership.WriterURL = s.requestOrigin(r)
	}
	writerID, writerURL := s.membership.WriterID, s.membership.WriterURL
	revision := s.membership.Revision
	var persist error
	if err := s.persistMembershipStateLocked(s.membership); err != nil {
		persist = err
	}
	s.membershipMu.Unlock()
	if persist != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "membership persistence failed"})
		return
	}
	s.rebuildMembershipRoutes()
	writeJSON(w, http.StatusOK, map[string]any{"node_id": member.NodeID, "name": member.Name,
		"state": member.State, "generation": member.Generation, "membership_revision": revision,
		"writer_id": writerID, "writer_url": writerURL,
		"auth_generation": s.writerAuthGeneration()})
}

func (s *Server) writerAuthGeneration() uint64 {
	s.authMu.RLock()
	defer s.authMu.RUnlock()
	if !s.authInitialized {
		return 0
	}
	return s.authState.Generation
}

func (s *Server) nodeMembershipHeartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "POST required"})
		return
	}
	if s.writerRedirect(w, r) {
		return
	}
	member := s.nodeMemberAuth(r)
	if member == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"detail": "node credential required"})
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<10))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "heartbeat body required"})
		return
	}
	var request struct {
		Revision    uint64 `json:"revision"`
		State       string `json:"state"`
		Build       string `json:"build"`
		StorageOK   bool   `json:"storage_ok"`
		SnapshotRev uint64 `json:"snapshot_revision"`
	}
	if len(raw) > 0 && json.Unmarshal(raw, &request) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "heartbeat body must be JSON"})
		return
	}
	reported := strings.TrimSpace(request.State)
	if reported != nodeStateJoining && reported != nodeStateReady && reported != nodeStateDraining {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "heartbeat state must be joining, ready or draining"})
		return
	}
	writerGeneration := s.writerAuthGeneration()
	now := s.now()
	s.membershipMu.Lock()
	current := s.membership.Nodes[member.NodeID]
	if current == nil || current.State == nodeStateDisabled {
		s.membershipMu.Unlock()
		writeJSON(w, http.StatusForbidden, map[string]any{"detail": "node is disabled"})
		return
	}
	next := reported
	if current.State == nodeStateJoining && reported == nodeStateReady {
		// The member declares it has the control snapshot applied locally;
		// only accept the transition when the writer can verify it.
		if writerGeneration == 0 || request.SnapshotRev < writerGeneration {
			s.membershipMu.Unlock()
			writeJSON(w, http.StatusConflict, map[string]any{"detail": "control snapshot not applied locally yet"})
			return
		}
	} else if reported == nodeStateJoining && request.SnapshotRev >= writerGeneration && writerGeneration > 0 {
		// Same transition from the member's own joining heartbeat.
		next = nodeStateReady
	}
	changed := false
	if current.State != next {
		current.State = next
		changed = true
	}
	current.LastSeen = now
	current.LeaseExpires = now.Add(nodeLeaseTTL)
	if request.Build != "" {
		current.Build = strings.TrimSpace(request.Build)
	}
	current.SnapshotRev = request.SnapshotRev
	if changed {
		s.membership.Revision++
	}
	if err := s.persistMembershipStateLocked(s.membership); err != nil {
		s.membershipMu.Unlock()
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "membership persistence failed"})
		return
	}
	revision := s.membership.Revision
	writerID, writerURL := s.membership.WriterID, s.membership.WriterURL
	s.membershipMu.Unlock()
	if changed {
		s.rebuildMembershipRoutes()
	}
	writeJSON(w, http.StatusOK, map[string]any{"revision": revision, "changed": revision != request.Revision,
		"writer_id": writerID, "writer_url": writerURL, "auth_generation": s.writerAuthGeneration()})
}

func (s *Server) nodeMembershipGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "GET required"})
		return
	}
	if s.writerRedirect(w, r) {
		return
	}
	if member := s.nodeMemberAuth(r); member == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"detail": "node credential required"})
		return
	}
	s.membershipMu.Lock()
	raw, digest := membershipSnapshotJSON(s.membership)
	revision := s.membership.Revision
	s.membershipMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"revision": revision, "sha256": digest, "size": len(raw),
		"membership": json.RawMessage(raw)})
}

func (s *Server) nodeMembershipControlSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "GET required"})
		return
	}
	if s.writerRedirect(w, r) {
		return
	}
	if member := s.nodeMemberAuth(r); member == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"detail": "node credential required"})
		return
	}
	if !s.authContinuityEnabled() {
		writeJSON(w, http.StatusConflict, map[string]any{"detail": "control snapshots require auth continuity"})
		return
	}
	bundle, err := s.currentWriterControlBundle()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "auth store unavailable"})
		return
	}
	snapshot := controlSnapshot{Revision: bundle.Generation, Generation: bundle.Generation, Bundle: bundle}
	s.instructionSetsMu.RLock()
	if len(s.instructionSets) > 0 {
		snapshot.InstructionSets = &instructionSetsState{Sets: cloneInstructionSets(s.instructionSets)}
	}
	s.instructionSetsMu.RUnlock()
	inner, _ := json.Marshal(map[string]any{"bundle": snapshot.Bundle, "instruction_sets": snapshot.InstructionSets})
	snapshot.SHA256 = hashNodeSecret(string(inner))
	writeJSON(w, http.StatusOK, snapshot)
}

func (s *Server) nodeMembershipSetState(w http.ResponseWriter, r *http.Request, state string) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "POST required"})
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<10))
	if err != nil || len(raw) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "node_id body required"})
		return
	}
	var request struct {
		NodeID string `json:"node_id"`
	}
	if json.Unmarshal(raw, &request) != nil || request.NodeID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "node_id required"})
		return
	}
	s.membershipMu.Lock()
	member := s.membership.Nodes[request.NodeID]
	if member == nil {
		s.membershipMu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "unknown node"})
		return
	}
	if member.NodeID == s.membership.WriterID {
		s.membershipMu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]any{"detail": "the writer manages its own state"})
		return
	}
	member.State = state
	if state == nodeStateReady {
		member.LeaseExpires = s.now().Add(nodeLeaseTTL)
	}
	s.membership.Revision++
	if err := s.persistMembershipStateLocked(s.membership); err != nil {
		s.membershipMu.Unlock()
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "membership persistence failed"})
		return
	}
	s.membershipMu.Unlock()
	s.rebuildMembershipRoutes()
	writeJSON(w, http.StatusOK, map[string]any{"node_id": member.NodeID, "state": member.State})
}

// startMembershipLoops launches the writer lease loop and/or the member sync
// loop. A node without identity and writer state stays a standalone node.
func (s *Server) startMembershipLoops() {
	s.membershipStop = make(chan struct{})
	s.startNodeConnLoop()
	if s.quorumNode != nil || s.membership != nil {
		s.membershipDone.Add(1)
		go s.membershipWriterLoop()
	}
	if s.nodeIdentity != nil && (s.quorumNode != nil || s.membership == nil) {
		s.membershipDone.Add(1)
		go s.membershipMemberLoop()
	}
}

func (s *Server) stopMembershipLoops() {
	s.membershipStopOnce.Do(func() {
		if s.membershipStop != nil {
			close(s.membershipStop)
		}
	})
	s.nodeConns.closeAll()
	s.membershipDone.Wait()
}

func (s *Server) membershipWriterLoop() {
	defer s.membershipDone.Done()
	if s.quorumNode == nil {
		s.membershipMu.Lock()
		if s.membership != nil && s.membership.WriterID == "" {
			s.membership.WriterID = s.authLocalID
			if s.cfg.NodePublicURL != "" && s.membership.WriterURL == "" {
				s.membership.WriterURL = s.cfg.NodePublicURL
			}
		}
		s.membershipMu.Unlock()
		s.ensureWriterSelfEntry()
	}
	ticker := time.NewTicker(nodeHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.membershipStop:
			return
		case <-ticker.C:
			if s.quorumNode != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				if err := s.ensureQuorumAuthorityMaterialized(ctx); err != nil {
					cancel()
					continue
				}
				cancel()
			}
			s.membershipWriterTick()
		}
	}
}

func (s *Server) membershipWriterTick() {
	if s.quorumNode != nil && s.effectiveAuthMode() != "writer" {
		return
	}
	now := s.now()
	dirty := false
	s.membershipMu.Lock()
	if s.membership == nil {
		s.membershipMu.Unlock()
		return
	}
	if self := s.membership.Nodes[s.membership.WriterID]; self != nil {
		if self.LeaseExpires.Sub(now) < nodeLeaseTTL/2 {
			self.LastSeen = now
			self.LeaseExpires = now.Add(nodeLeaseTTL)
			dirty = true
		}
	}
	for _, member := range s.membership.Nodes {
		if member.NodeID == s.membership.WriterID {
			continue
		}
		if (member.State == nodeStateReady || member.State == nodeStateDraining) && now.After(member.LeaseExpires) {
			// The lease expires dispatch without deleting identity.
			member.State = nodeStateOffline
			dirty = true
		}
	}
	if dirty {
		if err := s.persistMembershipStateLocked(s.membership); err != nil {
			log.Printf("membership lease persist failed: %v", err)
		}
		s.membershipMu.Unlock()
		s.rebuildMembershipRoutes()
		return
	}
	s.membershipMu.Unlock()
}

type nodeHeartbeatResponse struct {
	Revision       uint64 `json:"revision"`
	Changed        bool   `json:"changed"`
	WriterID       string `json:"writer_id"`
	WriterURL      string `json:"writer_url"`
	AuthGeneration uint64 `json:"auth_generation"`
}

func (s *Server) memberWriterURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nodeIdentity == nil {
		return ""
	}
	if s.nodeIdentity.WriterURL != "" {
		return s.nodeIdentity.WriterURL
	}
	return s.cfg.NodeSeedURL
}

func (s *Server) membershipMemberLoop() {
	defer s.membershipDone.Done()
	client := newNodeMembershipClient(s.cfg.NodeWriterConnectTo)
	heartbeat := time.NewTicker(nodeHeartbeatInterval)
	sync := time.NewTicker(nodeMembershipSyncEvery)
	defer heartbeat.Stop()
	defer sync.Stop()
	s.membershipMemberTick(client, true)
	for {
		select {
		case <-s.membershipStop:
			return
		case <-heartbeat.C:
			s.membershipMemberTick(client, false)
		case <-sync.C:
			s.membershipMemberTick(client, true)
		}
	}
}

// membershipMemberTick heartbeats the writer, refreshes the replicated view
// when the revision moves, and applies a new control generation when offered.
func (s *Server) memberNodeKey() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nodeIdentity == nil {
		return ""
	}
	return s.nodeIdentity.NodeKey
}

func (s *Server) membershipMemberTick(client *http.Client, forceSync bool) {
	writer := strings.TrimSuffix(s.memberWriterURL(), "/")
	if s.quorumNode != nil {
		if s.quorumNode.Status().Leader {
			return
		}
		var leaderID string
		writer, leaderID = s.quorumNode.LeaderForwardURL()
		if writer == "" || leaderID == "" {
			return
		}
		if s.quorumPrivateTLS != nil {
			transport, err := s.quorumPrivateTLS.transport(leaderID)
			if err != nil {
				return
			}
			defer transport.CloseIdleConnections()
			client = &http.Client{Transport: transport, Timeout: 15 * time.Second}
		} else {
			// A previous writer's IP pin must not redirect a newly elected leader.
			client = newNodeMembershipClient("")
		}
	}
	if writer == "" {
		return
	}
	s.membershipMu.Lock()
	viewRevision := uint64(0)
	if s.membershipView != nil {
		viewRevision = s.membershipView.Revision
	}
	s.membershipMu.Unlock()
	body, _ := json.Marshal(map[string]any{
		"revision": viewRevision, "state": s.membershipReportedState(), "build": GitCommit,
		"storage_ok": true, "snapshot_revision": s.writerAuthGeneration(),
	})
	response, err := postNodeJSON(client, writer+"/admin/api/nodes/heartbeat",
		"Bearer "+s.memberNodeKey(), body, nodeMembershipStateLimit)
	if err != nil {
		log.Printf("membership heartbeat failed writer=%s err=%v", writer, err)
		return
	}
	var heartbeatResponse nodeHeartbeatResponse
	if json.Unmarshal(response, &heartbeatResponse) != nil {
		log.Printf("membership heartbeat response malformed writer=%s", writer)
		return
	}
	if heartbeatResponse.WriterURL != "" {
		s.mu.Lock()
		changed := heartbeatResponse.WriterURL != s.nodeIdentity.WriterURL || heartbeatResponse.WriterID != s.nodeIdentity.WriterID
		if changed {
			s.nodeIdentity.WriterURL = heartbeatResponse.WriterURL
			s.nodeIdentity.WriterID = heartbeatResponse.WriterID
		}
		identity := *s.nodeIdentity
		s.mu.Unlock()
		if changed {
			if err := saveNodeIdentityFile(s.nodeIdentityPath(), identity); err != nil {
				log.Printf("node identity writer update failed: %v", err)
			}
		}
	}
	if heartbeatResponse.Changed || forceSync || s.membershipView == nil {
		s.membershipPullView(client, writer)
	}
	// Named instructions may change independently of the auth generation.
	// Reconcile the authenticated control snapshot even when the generation is equal.
	if heartbeatResponse.AuthGeneration > 0 {
		s.membershipApplyControlSnapshot(client, writer)
	}
}

func (s *Server) membershipReportedState() string {
	if s.NodeDrainStatus().Draining {
		return nodeStateDraining
	}
	if s.writerAuthGeneration() == 0 {
		return nodeStateJoining
	}
	return nodeStateReady
}

func (s *Server) membershipPullView(client *http.Client, writer string) {
	response, err := getNodeJSON(client, writer+"/admin/api/nodes/membership",
		"Bearer "+s.memberNodeKey(), s.cfg.AuthSnapshotBudget+nodeControlEnvelopeHeadroom)
	if err != nil {
		log.Printf("membership pull failed writer=%s err=%v", writer, err)
		return
	}
	var envelope struct {
		Revision   uint64          `json:"revision"`
		SHA256     string          `json:"sha256"`
		Size       int             `json:"size"`
		Membership json.RawMessage `json:"membership"`
	}
	if json.Unmarshal(response, &envelope) != nil || len(envelope.Membership) == 0 {
		log.Printf("membership pull response malformed writer=%s", writer)
		return
	}
	if envelope.Size > 0 && len(envelope.Membership) != envelope.Size {
		log.Printf("membership pull size mismatch writer=%s", writer)
		return
	}
	var view struct {
		Schema   uint64        `json:"schema"`
		Revision uint64        `json:"revision"`
		WriterID string        `json:"writer_id"`
		Nodes    []*nodeMember `json:"nodes"`
	}
	if json.Unmarshal(envelope.Membership, &view) != nil || view.Schema != nodeMembershipSchema || view.Nodes == nil {
		log.Printf("membership view rejected writer=%s", writer)
		return
	}
	if envelope.SHA256 != hashNodeSecret(string(envelope.Membership)) {
		log.Printf("membership view checksum mismatch writer=%s", writer)
		return
	}
	for _, member := range view.Nodes {
		if member == nil || member.NodeID == "" {
			log.Printf("membership view contains an invalid node writer=%s", writer)
			return
		}
		member.KeyHash = ""
	}
	accepted := &nodeMembershipView{Schema: nodeMembershipSchema, Revision: view.Revision,
		SHA256: envelope.SHA256, FetchedAt: s.now(), WriterID: view.WriterID, Nodes: view.Nodes}
	s.membershipMu.Lock()
	s.membershipView = accepted
	if err := os.MkdirAll(s.membershipDir(), 0o750); err == nil {
		if err := writeAccessStateJSON(s.memberViewPath(), accepted, nodeMembershipStateLimit); err != nil {
			log.Printf("membership view persist failed: %v", err)
		}
	}
	s.membershipMu.Unlock()
	s.rebuildMembershipRoutes()
}

// membershipApplyControlSnapshot pulls and atomically applies the bounded
// control bundle. A rejected or oversized snapshot leaves the last accepted
// generation serving, matching the stale-reader contract.
func (s *Server) membershipApplyControlSnapshot(client *http.Client, writer string) {
	limit := s.cfg.AuthSnapshotBudget + nodeControlEnvelopeHeadroom
	response, err := getNodeJSON(client, writer+"/admin/api/nodes/control-snapshot",
		"Bearer "+s.memberNodeKey(), limit)
	if err != nil {
		log.Printf("control snapshot pull failed writer=%s err=%v", writer, err)
		return
	}
	if len(response) > limit {
		log.Printf("control snapshot exceeds admission budget writer=%s", writer)
		return
	}
	var snapshot controlSnapshot
	if json.Unmarshal(response, &snapshot) != nil {
		log.Printf("control snapshot malformed writer=%s", writer)
		return
	}
	// A snapshot is authoritative only from the pinned writer identity.
	s.mu.Lock()
	knownWriter := s.nodeIdentity.WriterID
	s.mu.Unlock()
	if knownWriter != "" && snapshot.Bundle.WriterID != "" && snapshot.Bundle.WriterID != knownWriter {
		log.Printf("control snapshot writer mismatch got=%s want=%s", snapshot.Bundle.WriterID, knownWriter)
		return
	}
	inner, _ := json.Marshal(map[string]any{"bundle": snapshot.Bundle, "instruction_sets": snapshot.InstructionSets})
	if snapshot.SHA256 == "" || snapshot.SHA256 != hashNodeSecret(string(inner)) {
		log.Printf("control snapshot checksum mismatch writer=%s", writer)
		return
	}
	if snapshot.Revision != snapshot.Bundle.Generation || snapshot.Generation != snapshot.Revision {
		log.Printf("control snapshot generation mismatch writer=%s", writer)
		return
	}
	if snapshot.Revision < s.writerAuthGeneration() {
		return
	}
	sets := map[string]InstructionSet{}
	if snapshot.InstructionSets != nil {
		sets = snapshot.InstructionSets.Sets
	}
	for id, set := range sets {
		if err := validateNamedInstructionSetID(id); err != nil || set.ID != id || validateInstructionContent(set.Content) != nil || set.Version != instructionSetVersion(set.Content) {
			log.Printf("control snapshot instruction set invalid id=%s writer=%s", id, writer)
			return
		}
	}
	if err := validateAuthSnapshotValues(snapshot.Bundle, s.cfg.AuthSnapshotBudget, s.cfg.CtlToken, func(id string) bool {
		if id == "" || id == defaultInstructionSetID {
			return true
		}
		_, ok := sets[id]
		return ok
	}); err != nil {
		log.Printf("control snapshot validation rejected writer=%s err=%v", writer, err)
		return
	}
	s.authMu.Lock()
	s.mu.Lock()
	if snapshot.Revision < s.authState.Generation || (snapshot.Revision == s.authState.Generation && s.authState.Generation != 0 && authSnapshotDigest(snapshot.Bundle) != s.authState.Digest) {
		s.mu.Unlock()
		s.authMu.Unlock()
		log.Printf("control snapshot stale or inconsistent writer=%s", writer)
		return
	}
	s.instructionSetsMu.RLock()
	current, _ := json.Marshal(s.instructionSets)
	incoming, _ := json.Marshal(sets)
	s.instructionSetsMu.RUnlock()
	if snapshot.Revision == s.authState.Generation && !s.authState.Pending && bytes.Equal(current, incoming) {
		s.mu.Unlock()
		s.authMu.Unlock()
		return
	}
	// Persist the fail-closed marker before changing either store. A crash or
	// write failure leaves admission closed until the writer snapshot retries.
	s.authState.Pending = true
	err = writeAccessStateJSON(s.authStatePath(), s.authState, 4096)
	if err == nil {
		err = writeInstructionSetsStateAtomic(s.instructionSetsStatePath(), sets)
	}
	if err == nil {
		s.instructionSetsMu.Lock()
		s.instructionSets = cloneInstructionSets(sets)
		s.instructionSetsMu.Unlock()
		err = s.commitAuthSnapshotLocked(snapshot.Bundle, true)
	}
	s.mu.Unlock()
	s.authMu.Unlock()
	if err != nil {
		log.Printf("control snapshot apply rejected writer=%s err=%v", writer, err)
	}

}

// newNodeDialTransport dials through the pinned physical address when
// split-horizon DNS would loop the hostname back to this node's own proxy.
// TLS keeps verifying the URL hostname. It returns nil when no literal IP
// is pinned, so callers fall back to the default dialing path.
func newNodeDialTransport(connectTo string) *http.Transport {
	if net.ParseIP(connectTo) == nil {
		return nil
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		_, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
		return dialer.DialContext(ctx, network, net.JoinHostPort(connectTo, port))
	}
	return transport
}

func newNodeMembershipClient(connectTo string) *http.Client {
	if transport := newNodeDialTransport(connectTo); transport != nil {
		return &http.Client{Transport: transport, Timeout: 15 * time.Second}
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func postNodeJSON(client *http.Client, url, bearer string, body []byte, limit int) ([]byte, error) {
	return doNodeJSON(client, http.MethodPost, url, bearer, body, limit)
}

func getNodeJSON(client *http.Client, url, bearer string, limit int) ([]byte, error) {
	return doNodeJSON(client, http.MethodGet, url, bearer, nil, limit)
}

func doNodeJSON(client *http.Client, method, url, bearer string, body []byte, limit int) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(context.Background(), method, url, reader)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", bearer)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 1<<10))
		return nil, fmt.Errorf("status %d: %s", response.StatusCode, strings.TrimSpace(string(detail)))
	}
	return io.ReadAll(io.LimitReader(response.Body, int64(limit)+1))
}
