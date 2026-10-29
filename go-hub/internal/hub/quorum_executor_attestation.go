package hub

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// A node may attest only its own in-process executor. The attestation header
// is stripped from all client requests and recreated only after that local
// executor credential is checked. The leader additionally verifies mTLS and
// the authoritative membership mapping before trusting it.
const quorumExecutorHeader = "X-GPTAdmin-Quorum-Executor"
const quorumHeartbeatMaxBytes = 64 << 10

func quorumExecutorTarget(r *http.Request) (string, error) {
	var name string
	switch {
	case strings.HasPrefix(r.URL.Path, "/queue/"):
		parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/queue/"), "/"), "/")
		if len(parts) == 0 || len(parts) > 2 {
			return "", nil
		}
		if !(len(parts) == 1 && r.Method == http.MethodGet || len(parts) == 2 && r.Method == http.MethodPost && (parts[1] == "result" || parts[1] == "acl-check")) {
			return "", nil
		}
		var err error
		name, err = url.PathUnescape(parts[0])
		if err != nil {
			return "", errors.New("invalid executor queue name")
		}
	case r.URL.Path == "/heartbeat" && r.Method == http.MethodPost:
		if r.Body == nil || r.Body == http.NoBody {
			return "", errors.New("missing executor heartbeat")
		}
		data, err := io.ReadAll(io.LimitReader(r.Body, quorumHeartbeatMaxBytes+1))
		if err != nil || len(data) > quorumHeartbeatMaxBytes {
			return "", errors.New("invalid executor heartbeat")
		}
		_ = r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(data))
		var beat map[string]any
		if json.Unmarshal(data, &beat) != nil {
			return "", errors.New("invalid executor heartbeat")
		}
		name = firstString(beat, "name", "server_name", "host", "hostname")
	default:
		return "", nil
	}
	name = canonicalShellQueueName(name)
	if name == "" || strings.ContainsAny(name, "/\\") {
		return "", errors.New("invalid executor name")
	}
	return "shell:" + name, nil
}

func (s *Server) localQuorumExecutorForForward(r *http.Request) (string, error) {
	if s.quorumPrivateTLS == nil {
		return "", nil
	}
	target, err := quorumExecutorTarget(r)
	if err != nil || target == "" {
		return "", err
	}
	s.mu.Lock()
	token := s.localExecutorTokens[target]
	valid := token != "" && tokenMatches(r, token)
	s.mu.Unlock()
	if !valid {
		return "", nil
	}
	return target, nil
}

func (s *Server) authenticatedQuorumExecutor(r *http.Request) bool {
	if s.quorumNode == nil || s.quorumPrivateTLS == nil || r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
		return false
	}
	if r.Header.Get("X-GPTAdmin-Quorum-Forwarded") != s.quorumNode.Status().NodeID {
		return false
	}
	target, err := quorumExecutorTarget(r)
	if err != nil || target == "" || r.Header.Get(quorumExecutorHeader) != target {
		return false
	}
	peerID := ""
	for id, peer := range s.quorumPrivateTLS.peers {
		if r.TLS.PeerCertificates[0].VerifyHostname(peer.ServerName) == nil {
			if peerID != "" {
				return false
			}
			peerID = id
		}
	}
	if peerID == "" {
		return false
	}
	s.mu.Lock()
	local := s.localExecutors[target] != nil
	s.mu.Unlock()
	if local {
		return false
	}
	s.membershipMu.Lock()
	defer s.membershipMu.Unlock()
	if s.membership == nil {
		return false
	}
	member := s.membership.Nodes[peerID]
	if member == nil || member.NodeID != peerID || member.Name != target || !containsString(member.Capabilities, "executor") {
		return false
	}
	return member.State == nodeStateReady || member.State == nodeStateDraining || member.State == nodeStateOffline
}
