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
	"os"
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
	if !s.authContinuityEnabled() {
		return errors.New("quorum Hub requires auth continuity in writer or reader mode")
	}
	info, err := os.Lstat(s.cfg.QuorumConfigFile)
	if err != nil {
		return fmt.Errorf("quorum config unavailable: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("quorum config must be a regular file readable only by its owner")
	}
	data, err := os.ReadFile(s.cfg.QuorumConfigFile)
	if err != nil {
		return fmt.Errorf("read quorum config: %w", err)
	}
	var config quorum.Config
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return fmt.Errorf("decode quorum config: %w", err)
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
	s.quorumClusterID = config.ClusterID
	s.quorumBootstrapperID = config.BootstrapperID
	s.quorumPeers = append([]quorum.Peer(nil), config.Peers...)
	s.quorumSnapshotMaxBytes = config.ExternalSnapshotMaxBytes
	s.quorumSnapshotFreeReserve = config.ExternalSnapshotFreeReserveBytes
	s.quorumSnapshotDataDir = config.DataDir
	if err := s.withTaskDatabase(func(*taskTransaction) error { return nil }); err != nil {
		s.quorumClusterID = ""
		s.quorumBootstrapperID = ""
		s.quorumSnapshotMaxBytes = 0
		s.quorumSnapshotFreeReserve = 0
		s.quorumSnapshotDataDir = ""
		return fmt.Errorf("initialize task quorum store: %w", err)
	}
	privateTLS, err := prepareQuorumPrivateTLS(config)
	if err != nil {
		return fmt.Errorf("configure private quorum HTTPS: %w", err)
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
	go s.quorumBootstrapLoop()
	go s.quorumVoterPromotionLoop()
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
	if err := s.withTaskReadDatabase(func(tx *sql.Tx) error {
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
	if _, err := node.ExternalApplyError(); err != nil {
		return fmt.Errorf("quorum replica has an unapplied durable-state batch: %w", err)
	}
	var clusterID string
	var applied, authorityRevision uint64
	var baselineReady, authorityReady int
	err := s.withTaskReadDatabase(func(tx *sql.Tx) error {
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
		NodeID       string `json:"node_id"`
		ClusterID    string `json:"cluster_id"`
		ReplicaReady bool   `json:"replica_ready"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 4<<10)).Decode(&status) != nil {
		return false
	}
	return status.NodeID == peer.ID && status.ClusterID == s.quorumClusterID && status.ReplicaReady
}

func (s *Server) withQuorumAdmission(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.quorumNode == nil {
			next.ServeHTTP(w, r)
			return
		}
		switch r.URL.Path {
		case "/healthz", "/version", "/metrics":
			next.ServeHTTP(w, r)
			return
		}
		if err := s.quorumNode.VerifyLeader(r.Context()); err != nil {
			s.quorumAuthorityActiveWriter.Store(false)
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
					outbound.Header.Del(quorumExecutorHeader)
					if forwardedExecutor != "" {
						outbound.Header.Set(quorumExecutorHeader, forwardedExecutor)
					}
					outbound.Header.Set("X-GPTAdmin-Quorum-Forwarded", leaderID)
				}
				proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
					writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "current quorum leader is unreachable"})
				}
				proxy.ServeHTTP(w, r)
				return
			}
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "this GPTAdmin node has no quorum-confirmed Hub leadership", "leader_id": leaderID})
			return
		}
		if err := s.ensureQuorumAuthorityMaterialized(r.Context()); err != nil {
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
		next.ServeHTTP(w, r)
	})
}
