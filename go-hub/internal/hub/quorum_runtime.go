package hub

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/megamen32/gptadmin/go-hub/internal/hub/quorum"
)

const quorumMinimumVoters = 3

func (s *Server) openQuorum() error {
	if s.cfg.QuorumConfigFile == "" {
		return nil
	}
	if err := s.configureQuorum(); err != nil {
		return err
	}
	go s.quorumBootstrapLoop()
	go s.quorumVoterPromotionLoop()
	return nil
}

func (s *Server) configureQuorum() error {
	if !s.authContinuityEnabled() {
		return errors.New("quorum Hub requires auth continuity in writer or reader mode")
	}
	config, err := readHubQuorumConfig(s.cfg.QuorumConfigFile)
	if err != nil {
		return err
	}
	if config.DataDir == "" {
		config.DataDir = filepath.Join(s.cfg.ConfigDir, "quorum")
	}
	if strings.TrimSpace(config.NodeID) == strings.TrimSpace(config.BootstrapperID) && s.membership == nil {
		return errors.New("quorum bootstrapper requires the initialized authoritative membership store")
	}
	localPeerFound := false
	for _, peer := range config.Peers {
		hubURL := strings.TrimSpace(peer.HubURL)
		parsed, parseErr := url.Parse(hubURL)
		if parseErr != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Path != "" && parsed.Path != "/" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("quorum voter %q requires an enrolled public HTTPS hub_url", peer.ID)
		}
		if peer.ID == strings.TrimSpace(config.NodeID) {
			localPeerFound = true
			if s.cfg.NodePublicURL == "" || strings.TrimRight(s.cfg.NodePublicURL, "/") != strings.TrimRight(hubURL, "/") {
				return errors.New("local quorum hub_url must match GPTADMIN_NODE_PUBLIC_URL")
			}
		}
	}
	if !localPeerFound {
		return errors.New("local node is missing from the quorum public hub URL configuration")
	}
	mode, reopen, err := s.resolveQuorumMode(config)
	if err != nil {
		return err
	}
	if mode == quorum.OwnerLocal && (config.ExternalSnapshotMaxBytes == 0 || config.ExternalSnapshotMaxBytes > quorum.ControlSnapshotLimit) {
		config.ExternalSnapshotMaxBytes = quorum.ControlSnapshotLimit
	}
	if mode == quorum.OwnerLocal && config.ExternalSnapshotFreeReserveBytes == 0 {
		config.ExternalSnapshotFreeReserveBytes = 16 << 20
	}
	config.Mode = mode
	if err = config.Validate(); err != nil {
		return err
	}
	privateTLS, err := prepareQuorumPrivateTLS(config)
	if err != nil {
		return fmt.Errorf("configure private quorum HTTPS: %w", err)
	}
	if mode == quorum.OwnerLocal {
		if err = checkControlDatabaseFile(filepath.Join(config.DataDir, "hub-control.sqlite")); err != nil {
			return err
		}
	}
	if !reopen {
		if err = pinQuorumMode(config.DataDir, quorumStorageIdentity{config.ClusterID, mode}); err != nil {
			return err
		}
	}
	s.quorumMode, s.quorumLegacyReopen = mode, reopen
	s.quorumClusterID = config.ClusterID
	s.quorumBootstrapperID = config.BootstrapperID
	s.quorumPeers = append([]quorum.Peer(nil), config.Peers...)
	s.quorumSnapshotMaxBytes = config.ExternalSnapshotMaxBytes
	s.quorumSnapshotFreeReserve = config.ExternalSnapshotFreeReserveBytes
	s.quorumSnapshotDataDir = config.DataDir
	if err := s.withQuorumDatabase(func(*taskTransaction) error { return nil }); err != nil {
		s.quorumClusterID = ""
		s.quorumBootstrapperID = ""
		s.quorumSnapshotMaxBytes = 0
		s.quorumSnapshotFreeReserve = 0
		s.quorumSnapshotDataDir = ""
		return fmt.Errorf("initialize quorum store: %w", err)
	}
	// Replicated clusters retain their pre-Raft local recovery ordering.
	if !s.quorumControlOnly() {
		if err := s.loadTaskState(); err != nil {
			return err
		}
	}
	node, err := quorum.OpenWithExternalState(config, s.applyTaskQuorumMutations, quorum.ExternalSnapshotCallbacks{
		Snapshot: s.snapshotTaskQuorumState,
		Restore:  s.restoreTaskQuorumState,
	})
	if err != nil {
		s.quorumClusterID = ""
		s.quorumBootstrapperID = ""
		s.quorumSnapshotMaxBytes = 0
		s.quorumSnapshotFreeReserve = 0
		s.quorumSnapshotDataDir = ""
		return err
	}
	s.quorumNode = node
	s.quorumPrivateTLS = privateTLS
	return nil
}

func (s *Server) taskQuorumReady() error {
	node := s.quorumNode
	if node == nil {
		return nil
	}
	if node.Status().VoterCount < quorumMinimumVoters {
		return errors.New("quorum Hub requires three committed voters before accepting requests")
	}
	if err := s.taskQuorumReplicaReady(); err != nil {
		return err
	}
	var authorityRevision uint64
	if err := s.withQuorumReadDatabase(func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT authority_revision FROM task_quorum_meta WHERE id=1`).Scan(&authorityRevision)
	}); err != nil {
		return err
	}
	s.quorumAuthorityLoadMu.Lock()
	materialized := s.quorumAuthorityLoadedRevision == authorityRevision && s.quorumAuthorityLoadedLeader == node.Status().NodeID
	s.quorumAuthorityLoadMu.Unlock()
	if !materialized {
		return errors.New("quorum authority state has not been materialized for the current leader")
	}
	return nil
}

func (s *Server) taskQuorumReplicaReady() error {
	node := s.quorumNode
	if node == nil {
		return nil
	}
	if s.quorumLegacyReopen && !s.legacyMarkerCommitted() {
		return errors.New("waiting for the committed legacy baseline")
	}
	if _, err := node.ExternalApplyError(); err != nil {
		return fmt.Errorf("quorum replica has an unapplied durable-state batch: %w", err)
	}
	var clusterID string
	var applied, authorityRevision uint64
	var baselineReady, authorityReady int
	err := s.withQuorumReadDatabase(func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT cluster_id,applied_index,authority_revision,baseline_ready,authority_ready FROM task_quorum_meta WHERE id=1`).Scan(&clusterID, &applied, &authorityRevision, &baselineReady, &authorityReady)
	})
	if err != nil {
		return err
	}
	if clusterID != node.Status().ClusterID || applied != node.ExternalAppliedIndex() {
		return errors.New("quorum replica has not applied the current task-state index")
	}
	if baselineReady != 1 || authorityReady != 1 {
		return errors.New("quorum replica is missing an approved task or authority baseline")
	}
	return nil
}

// taskQuorumBaselineReady is the pre-voter admission fence. A node may be
// promoted only after its local SQLite baseline and authority rows are
// approved for this cluster; it need not already be a Raft voter (that is
// the promotion operation itself).
func (s *Server) taskQuorumBaselineReady() bool {
	if s.quorumNode == nil {
		return false
	}
	if s.quorumLegacyReopen && !s.legacyMarkerCommitted() {
		return false
	}
	var clusterID string
	var baselineReady, authorityReady int
	err := s.withQuorumReadDatabase(func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT cluster_id,baseline_ready,authority_ready FROM task_quorum_meta WHERE id=1`).Scan(&clusterID, &baselineReady, &authorityReady)
	})
	return err == nil && clusterID == s.quorumClusterID && baselineReady == 1 && authorityReady == 1
}

// quorumVoterPromotionLoop completes interrupted promotions only for enrolled
// ready members whose public TLS health reports the expected cluster identity.
// The Raft transport independently verifies each peer's enrolled node-key hash.
// Raft stages candidates and commits promotion only after log catch-up; its
// committed configuration survives restart.
func (s *Server) quorumVoterPromotionLoop() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	lastAuthorityError := ""
	for {
		select {
		case <-s.taskRuntimeStop:
			return
		case <-ticker.C:
		}
		node := s.quorumNode
		if node == nil {
			return
		}
		status := node.Status()
		if !status.Leader {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		if err := s.ensureQuorumAuthorityMaterialized(ctx); err != nil {
			if err.Error() != lastAuthorityError {
				log.Printf("quorum authority materialization failed: %v", err)
				lastAuthorityError = err.Error()
			}
			cancel()
			continue
		}
		lastAuthorityError = ""
		if err := s.taskQuorumReplicaReady(); err != nil {
			cancel()
			continue
		}
		cancel()

		s.membershipMu.Lock()
		if s.membership == nil {
			s.membershipMu.Unlock()
			continue
		}
		members := make(map[string]nodeMember, len(s.membership.Nodes))
		for id, member := range s.membership.Nodes {
			if member == nil {
				continue
			}
			copyMember := *member
			copyMember.Endpoints = append([]string(nil), member.Endpoints...)
			members[id] = copyMember
		}
		s.membershipMu.Unlock()

		for _, peer := range s.quorumPeers {
			if peer.ID == status.NodeID || node.IsVoter(peer.ID) {
				continue
			}
			member, ok := members[peer.ID]
			if !ok || !quorumMemberPromotionReady(member, peer, s.now()) {
				continue
			}
			if !s.quorumPeerReplicaReady(peer) {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			err := node.PromoteVoter(ctx, peer.ID)
			cancel()
			if err != nil && node.Status().Leader {
				log.Printf("quorum voter promotion pending node=%s err=%v", peer.ID, err)
			}
		}
	}
}

func quorumMemberPromotionReady(member nodeMember, peer quorum.Peer, now time.Time) bool {
	return member.NodeID == peer.ID && member.State == nodeStateReady && member.LeaseExpires.After(now)
}

func (s *Server) quorumPeerReplicaReady(peer quorum.Peer) bool {
	endpoint := peer.HubURL
	if s.quorumPrivateTLS != nil {
		endpoint = peer.InternalHubURL
	}
	target, err := url.Parse(strings.TrimRight(endpoint, "/") + "/healthz")
	if err != nil || target.Scheme != "https" || target.Host == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return false
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	if s.quorumPrivateTLS != nil {
		transport, err = s.quorumPrivateTLS.transport(peer.ID)
		if err != nil {
			return false
		}
	}
	defer transport.CloseIdleConnections()
	response, err := (&http.Client{Transport: transport, Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	var status struct {
		NodeID        string `json:"node_id"`
		ClusterID     string `json:"cluster_id"`
		ReplicaReady  bool   `json:"replica_ready"`
		BaselineReady bool   `json:"baseline_ready"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 4<<10)).Decode(&status) != nil {
		return false
	}
	return status.NodeID == peer.ID && status.ClusterID == s.quorumClusterID && (status.ReplicaReady || status.BaselineReady)
}

func (s *Server) withQuorumAdmission(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(nodePeerHopHeader) != "" && r.Header.Get("X-GPTAdmin-Offline-Owner") != "" {
			owner, target := r.Header.Get("X-GPTAdmin-Offline-Owner"), r.Header.Get("X-GPTAdmin-Offline-Target")
			s.mu.Lock()
			local := s.localReadbackOwnerIDLocked() == owner && s.localExecutors[target] != nil
			s.mu.Unlock()
			if r.Header.Get(nodePeerHopHeader) != "1" || !local {
				writeJSON(w, http.StatusForbidden, map[string]any{"detail": "peer destination does not own target", "executed": false})
				return
			}
		}
		if s.quorumNode == nil {
			if r.Header.Get(nodePeerHopHeader) == "1" && r.Header.Get("X-GPTAdmin-Offline-Owner") != "" {
				if fallback := s.memberOfflineShellFallback(r, next); fallback != nil {
					fallback(w, r)
					return
				}
				writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "offline peer admission unavailable", "executed": false})
				return
			}
			if origin := s.memberQuorumHubs.Current(); origin != "" && memberQuorumClientPath(r.URL.Path) && r.Header.Get(nodePeerHopHeader) == "" {
				if r.Header.Get(memberIngressHeader) != "" {
					s.memberQuorumHubs.Failed(origin)
					writeJSON(w, http.StatusLoopDetected, map[string]any{"detail": "quorum ingress resolved to an ordinary member", "executed": false})
					return
				}
				s.proxyMemberRequestWithFallback(w, r, origin, false, s.memberOfflineShellFallback(r, next))
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		switch r.URL.Path {
		case ownerReadPath:
			// This exact read authenticates against the applied voter policy.
			// Ordinary reader slots may still name a former writer.
			if !s.readFromQuorumLeader(r) {
				writeJSON(w, http.StatusServiceUnavailable, readOffline)
				return
			}
			s.ownerReadHTTP(w, r)
			return
		case "/healthz", "/version", "/metrics":
			next.ServeHTTP(w, r)
			return
		case "/admin/api/node/drain":
			// A native service manager drains the process behind its loopback
			// listener. Public endpoints continue to follow the elected leader.
			if isLoopbackServiceHost(stripPort(r.Host)) && isLoopbackServiceHost(stripPort(r.RemoteAddr)) &&
				r.Header.Get("X-Forwarded-For") == "" && r.Header.Get("X-GPTAdmin-Quorum-Forwarded") == "" &&
				s.cfg.CtlToken != "" && tokenMatches(r, s.cfg.CtlToken) {
				// This credential is configured on the process. Keep its deadline
				// and audit checks independent of the reader's cached policy slot.
				s.requireCtl(s.nodeDrainHTTP)(w, r)
				return
			}
		}
		termBeforeVerification := s.quorumNode.CurrentTerm()
		if err := s.quorumNode.VerifyLeader(r.Context()); err != nil {
			s.quorumAuthorityActiveWriter.Store(false)
			if s.pollFencedLocalShell(w, r) {
				return
			}
			if s.quorumControlOnly() && s.quorumLocalDispatchRequest(r) {
				next.ServeHTTP(w, r)
				return
			}
			if r.Header.Get("X-GPTAdmin-Quorum-Forwarded") != "" {
				writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "quorum leadership changed during request forwarding"})
				return
			}
			forwardedExecutor, executorErr := s.localQuorumExecutorForForward(r)
			if executorErr != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "invalid local executor request"})
				return
			}
			if forwardedExecutor != "" && r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/queue/") {
				release, admitted := s.beginNodeSubmission(forwardedExecutor, "__quorum_queue_poll")
				if !admitted {
					writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": nodeDrainRetryDetail, "retryable": true})
					return
				}
				defer release()
			}
			hubURL, leaderID := s.quorumNode.LeaderForwardURL()
			if leaderID != "" && r.Header.Get("X-GPTAdmin-Offline-Owner") != "" {
				writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "offline peer admission unavailable while writer is known", "executed": false})
				return
			}
			target, parseErr := url.Parse(hubURL)
			if parseErr == nil && (target.Scheme == "https" || target.Scheme == "http") && target.Host != "" && leaderID != "" && target.Host != r.Host {
				proxy := httputil.NewSingleHostReverseProxy(target)
				director := proxy.Director
				transport := http.DefaultTransport.(*http.Transport).Clone()
				transport.Proxy = nil
				if s.quorumPrivateTLS != nil {
					var transportErr error
					transport, transportErr = s.quorumPrivateTLS.transport(leaderID)
					if transportErr != nil {
						writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "quorum leader has no authenticated private route"})
						return
					}
				}
				defer transport.CloseIdleConnections()
				proxy.Transport = transport
				proxy.Director = func(outbound *http.Request) {
					director(outbound)
					outbound.Host = target.Host
					outbound.GetBody = nil
					outbound.Header.Del("Idempotency-Key")
					outbound.Header.Del("X-Idempotency-Key")
					outbound.Header.Del(quorumExecutorHeader)
					if forwardedExecutor != "" {
						outbound.Header.Set(quorumExecutorHeader, forwardedExecutor)
					}
					outbound.Header.Set("X-GPTAdmin-Quorum-Forwarded", leaderID)
				}
				proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
					writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "current quorum leader delivery was not confirmed", "outcome": "unknown"})
				}
				proxy.ServeHTTP(w, r)
				return
			}
			if leaderID == "" && s.offlineShellMCPRoute(r) {
				next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), offlineShellMCPKey{}, true)))
				return
			}
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "this GPTAdmin node has no quorum-confirmed Hub leadership", "leader_id": leaderID})
			return
		}
		if r.Header.Get("X-GPTAdmin-Offline-Owner") != "" {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "offline peer admission unavailable while writer is active", "executed": false})
			return
		}
		if s.quorumControlOnly() && s.quorumLocalDispatchRequest(r) {
			next.ServeHTTP(w, r)
			return
		}
		if err := s.materializeVerifiedQuorumAuthority(r.Context(), termBeforeVerification); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "quorum authority state is not ready on this leader"})
			return
		}
		// Bootstrap must let enrolled voters renew their leases before the
		// ordinary three-voter admission gate can open. The heartbeat handler
		// still authenticates the member and refuses disabled identities.
		if r.Method == http.MethodPost && r.URL.Path == "/admin/api/nodes/heartbeat" && s.quorumNode.Status().VoterCount < quorumMinimumVoters && s.taskQuorumReplicaReady() == nil {
			member := s.nodeMemberAuth(r)
			if member != nil {
				for _, peer := range s.quorumPeers {
					if peer.ID == member.NodeID {
						next.ServeHTTP(w, r)
						return
					}
				}
			}
		}
		if err := s.taskQuorumReady(); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "quorum Hub state is not ready"})
			return
		}
		if s.quorumControlOnly() {
			r = r.WithContext(context.WithValue(r.Context(), ownerAdmissionTermKey{}, s.quorumAuthorityTerm.Load()))
		}
		next.ServeHTTP(w, r)
	})
}

// Addressed shell work is admitted by the executor owner. The outer MCP
// request may be code mode, so the nested call performs the exact target and
// capability checks later in executeMCPTool. Other targets still encounter
// their normal authority gate there.
func (s *Server) quorumLocalDispatchRequest(r *http.Request) bool {
	if r == nil || r.Method != http.MethodPost || r.Header.Get(nodePeerHopHeader) != "" || r.Body == nil {
		return false
	}
	if r.URL.Path != "/mcp" && r.URL.Path != "/mcp-relay/call" && r.URL.Path != "/mcp-relay/call_mcp_tool" {
		return false
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil || len(raw) == 0 {
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	var envelope map[string]any
	if json.Unmarshal(raw, &envelope) != nil {
		return false
	}
	if r.URL.Path == "/mcp" {
		if firstString(envelope, "method") != "tools/call" {
			return false
		}
		params := mapValue(envelope["params"])
		return firstString(params, "name") == "exec"
	}
	target := firstString(envelope, "target", "server_id", "agent_id")
	tool := firstString(envelope, "tool", "tool_name", "name")
	if tool != "shell_exec" || !strings.HasPrefix(target, "shell:") {
		return false
	}
	if r.Header.Get(memberIngressHeader) == "1" {
		return s.quorumMemberIngressAuthorized(r, target)
	}
	s.mu.Lock()
	local := s.localExecutors[target] != nil
	s.mu.Unlock()
	return local
}

func (s *Server) addressedMemberIngress(r *http.Request, target string) bool {
	return r != nil && s.quorumControlOnly() &&
		r.Context().Value(localShellAdmissionKey{}) == target &&
		!s.quorumAuthorityActiveWriter.Load()
}

func (s *Server) quorumMemberIngressAuthorized(r *http.Request, target string) bool {
	if r == nil || r.Header.Get(nodePeerHopHeader) != "1" || r.Header.Get(memberIngressHeader) != "1" ||
		s.quorumNode == nil || !s.quorumControlOnly() || !strings.HasPrefix(target, "shell:") {
		return false
	}
	peerID := s.receiptPeerID(r)
	if peerID == "" || peerID == s.quorumNode.Status().NodeID || !s.quorumNode.IsVoter(peerID) {
		return false
	}
	s.mu.Lock()
	local := s.localExecutors[target] != nil
	owner, err := s.executorEnrollmentLocked(target)
	s.mu.Unlock()
	if !local || err != nil || owner == nil || !owner.voter || !owner.bound() ||
		owner.NodeID != s.quorumNode.Status().NodeID || owner.State == nodeStateDraining {
		return false
	}
	return true
}
