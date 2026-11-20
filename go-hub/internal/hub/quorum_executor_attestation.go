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

type attestedVoterKey struct{}

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
	pin := s.localExecutors[target]
	valid := pin["server_id"] != "" && pin["public_key"] != "" && pin["fingerprint"] != "" && token != "" && tokenMatches(r, token)
	if valid && r.Method == http.MethodGet {
		q := r.URL.Query()
		valid = q.Get("server_id") == pin["server_id"] && q.Get("fingerprint") == pin["fingerprint"] && q.Get("public_key") == pin["public_key"]
	}
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
	return s.voterOwnsRemoteExecutor(s.receiptPeerID(r), target, r)
}

func (s *Server) voterOwnsRemoteExecutor(peerID, target string, r *http.Request) bool {
	if peerID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.localExecutors[target] != nil {
		return false
	}
	owner, err := s.executorEnrollmentLocked(target)
	if err != nil || owner == nil || !owner.voter || owner.NodeID != peerID {
		return false
	}
	if r.Method == http.MethodGet {
		id, fp := executorPollIdentity(r)
		if !owner.matches(id, fp) {
			return false
		}
		// A freshly enrolled voter can introduce its executor through the
		// ordinary pending poll. Dispatch and receipts still need the pin.
		if !owner.bound() {
			agent := s.agents[target]
			return agent == nil || (agent.Status == "awaiting_approval" && !truthyAny(agent.Meta["approved"]) &&
				!conflictingExecutorPin(agent.Meta["server_id"], id) && !conflictingExecutorPin(agent.Meta["fingerprint"], fp))
		}
	}
	return owner.bound()
}
