package hub

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"

	"github.com/megamen32/gptadmin/go-shellmcp/hubroute"
)

const memberQueuePrefix = "/admin/api/nodes/executor/queue/"
const memberIngressHeader = "X-GPTAdmin-Member-Ingress"

// Only publish the operator-configured public voter origins. Membership
// endpoints advertised by ordinary executors are not trusted Hub addresses.
func (s *Server) quorumExecutorOrigins() []string {
	if s.quorumNode == nil {
		return nil
	}
	var origins []string
	for _, peer := range s.quorumPeers {
		origins = append(origins, peer.HubURL)
	}
	return hubroute.Parse(strings.Join(origins, ","))
}

// Polling, heartbeat and WS use the same authority after an election.
// The ordinary writer's physical IP pin applies only to its original route.
func (s *Server) memberWriterRoute(ordinary *http.Client) (string, *http.Client) {
	if s.quorumNode != nil {
		if s.quorumNode.Status().Leader {
			return "", nil
		}
		origin, id := s.quorumNode.LeaderForwardURL()
		if origin == "" || id == "" {
			return "", nil
		}
		client := newNodeMembershipClient("")
		if s.quorumPrivateTLS != nil {
			transport, err := s.quorumPrivateTLS.transport(id)
			if err != nil {
				return "", nil
			}
			client.Transport = transport
		}
		client.CheckRedirect = refuseNodeRedirect
		return origin, client
	}
	if origin := s.memberQuorumHubs.Current(); origin != "" {
		return origin, s.nodePeerClient
	}
	return s.memberWriterURL(), ordinary
}

func refuseNodeRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func executorPollIdentity(r *http.Request) (string, string) {
	q := r.URL.Query()
	pub, err := base64.RawURLEncoding.DecodeString(q.Get("public_key"))
	fp := sha256.Sum256(pub)
	if err != nil || len(pub) != ed25519.PublicKeySize || "SHA256:"+base64.RawURLEncoding.EncodeToString(fp[:]) != q.Get("fingerprint") {
		return "", ""
	}
	return q.Get("server_id"), q.Get("fingerprint")
}

type memberExecutorKey struct{}

// The caller holds s.mu. A public fleet credential cannot take the place
// of an enrolled Node's credential, even before its first authenticated poll.
func (s *Server) remoteMemberExecutorLocked(target string) bool {
	if s.quorumNode == nil {
		return false
	}
	owner, err := s.executorEnrollmentLocked(target)
	return err != nil || owner != nil && (s.localExecutors[target] == nil || s.localExecutorTokens[target] == "")
}

// Queue authentication runs before result/ACL handling. These POSTs have
// no poll announcement and must not reset the executor's capabilities.
func (s *Server) executorOwnsReceiptLocked(r *http.Request, name string) bool {
	target := "shell:" + name
	if a := s.agents[target]; a != nil && (a.Status == "disabled" || a.Status == "awaiting_approval") {
		return false
	}
	if r.Context().Value(memberExecutorKey{}) == target || r.Context().Value(attestedVoterKey{}) == target {
		return true
	}
	if token := s.localExecutorTokens[target]; token != "" {
		return tokenMatches(r, token)
	}
	if a := s.agents[target]; a != nil && firstString(a.Meta, shellTokenHashMetaKey) != "" {
		return s.pendingShellEnrollmentAllowedLocked(name, r)
	}
	return true
}

// An enrolled nonvoter presents its own node credential, never a voter's
// certificate or a fleet-wide shell token. The endpoint exposes only its
// own existing queue and verifies the physical executor pin on each poll.
func (s *Server) memberExecutorQueue(w http.ResponseWriter, r *http.Request) {
	if s.quorumNode == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "quorum executor queue unavailable"})
		return
	}
	member := s.nodeMemberAuth(r)
	req := r.Clone(r.Context())
	req.Header.Del(selfEnrollTokenHeader)
	req.Header.Del(globalShellAuthHeader)
	req.URL.Path = "/queue/" + strings.TrimPrefix(r.URL.Path, memberQueuePrefix)
	target, err := quorumExecutorTarget(req)
	if err != nil || member == nil || target == "" || member.Name != target || !containsString(member.Capabilities, "executor") ||
		(member.State != nodeStateReady && member.State != nodeStateOffline && member.State != nodeStateDraining) {
		writeJSON(w, http.StatusForbidden, map[string]any{"detail": "member does not own this executor queue"})
		return
	}
	s.mu.Lock()
	owner, ownerErr := s.executorEnrollmentLocked(target)
	if ownerErr != nil || owner == nil || owner.voter || owner.NodeID != member.NodeID || owner.KeyHash != member.KeyHash {
		s.mu.Unlock()
		writeJSON(w, http.StatusForbidden, map[string]any{"detail": "member does not own this executor queue"})
		return
	}
	member = &owner.nodeMember
	local := s.localExecutors[target] != nil
	agent := s.agents[target]
	bound := member.LocalID != "" && member.LocalFingerprint != ""
	conflict := false
	if agent != nil && bound {
		conflict = firstString(agent.Meta, "server_id") != member.LocalID || firstString(agent.Meta, "fingerprint") != member.LocalFingerprint
	}
	disabled := agent != nil && agent.Status == "disabled"
	s.mu.Unlock()
	if local || conflict || disabled {
		writeJSON(w, http.StatusForbidden, map[string]any{"detail": "member executor identity conflict"})
		return
	}
	if req.Method == http.MethodGet {
		id, fp := executorPollIdentity(req)
		if member.State == nodeStateDraining || !owner.matches(id, fp) {
			writeJSON(w, http.StatusForbidden, map[string]any{"detail": "member poll does not match enrolled identity"})
			return
		}
		if !bound {
			// Generic joins still need ordinary owner approval. Advertise the
			// pending identity but never lease a job until membership pins it.
			s.mu.Lock()
			if s.agents[target] == nil {
				s.touchShellPollLocked(strings.TrimPrefix(target, "shell:"), req)
			}
			s.mu.Unlock()
			writeJSON(w, http.StatusOK, map[string]any{"status": "awaiting_approval"})
			return
		}
	}
	if !bound {
		writeJSON(w, http.StatusForbidden, map[string]any{"detail": "member executor identity is not approved"})
		return
	}
	s.queue(w, req.WithContext(context.WithValue(req.Context(), memberExecutorKey{}, target)))
}

// Historical local jobs continue through their original queue and outbox.
// A result for a cluster job goes to the same ledger that issued the ID.
func (s *Server) proxyMemberResult(w http.ResponseWriter, r *http.Request, name, origin string) bool {
	s.mu.Lock()
	local := s.localExecutors["shell:"+name] != nil && tokenMatches(r, s.localExecutorTokens["shell:"+name])
	s.mu.Unlock()
	if !local {
		return false
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<20))
	var receipt struct {
		ID string `json:"id"`
	}
	if err != nil || json.Unmarshal(raw, &receipt) != nil || receipt.ID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "invalid executor result"})
		return true
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	s.mu.Lock()
	err = s.refreshTaskRecordsLocked(receipt.ID)
	owned := s.shellJobs[receipt.ID] != nil
	s.mu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "local receipt lookup unavailable"})
		return true
	}
	if owned {
		return false
	}
	s.proxyMemberRequest(w, r, origin, true)
	return true
}

func (s *Server) proxyMemberRequest(w http.ResponseWriter, r *http.Request, origin string, executor bool) {
	s.proxyMemberRequestWithFallback(w, r, origin, executor, nil)
}

// The proxy owns the outbound request. Retain the original body's ownership
// only when a local destination has been offered and sending can be excluded.
func (s *Server) proxyMemberRequestWithFallback(w http.ResponseWriter, r *http.Request, origin string, executor bool, fallback http.HandlerFunc) {
	target, err := url.Parse(origin)
	if err != nil || target.Scheme != "https" || target.Host == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "quorum address unavailable"})
		return
	}
	var delivery proxyDelivery
	proxy := &httputil.ReverseProxy{Transport: s.nodePeerClient.Transport, Rewrite: func(p *httputil.ProxyRequest) {
		if fallback != nil {
			delivery.track(p.Out)
		}
		p.SetURL(target)
		p.Out.Host = target.Host
		p.Out.GetBody = nil
		p.Out.Header.Del("Idempotency-Key")
		p.Out.Header.Del("X-Idempotency-Key")
		p.Out.Header.Del(quorumExecutorHeader)
		if executor {
			p.Out.URL.Path = memberQueuePrefix + strings.TrimPrefix(r.URL.Path, "/queue/")
			p.Out.URL.RawPath = ""
			p.Out.Header = http.Header{}
			p.Out.Header.Set("Authorization", "Bearer "+s.memberNodeKey())
			p.Out.Header.Set("Content-Type", "application/json")
		} else {
			p.Out.Header.Set(memberIngressHeader, "1")
		}
	}, ModifyResponse: func(resp *http.Response) error {
		if resp.StatusCode >= 500 {
			s.memberQuorumHubs.Failed(origin)
		}
		return nil
	}, ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
		s.memberQuorumHubs.Failed(origin)
		if fallback != nil && delivery.failedBeforeSend(r.Context(), err) {
			fallback(w, r)
			return
		}
		writeJSON(w, http.StatusBadGateway, map[string]any{"detail": "quorum delivery was not confirmed", "outcome": "unknown"})
	}}
	proxy.ServeHTTP(w, r)
}

// A failed dial is safe to handle locally only while the original body is untouched.
type proxyDelivery struct {
	gotConn  atomic.Bool
	readBody atomic.Bool
}

func (d *proxyDelivery) track(out *http.Request) {
	trace := &httptrace.ClientTrace{GotConn: func(httptrace.GotConnInfo) { d.gotConn.Store(true) }}
	*out = *out.WithContext(httptrace.WithClientTrace(out.Context(), trace))
	if out.Body != nil {
		out.Body = io.NopCloser(trackedProxyBody{Reader: out.Body, read: &d.readBody})
	}
}

func (d *proxyDelivery) failedBeforeSend(ctx context.Context, err error) bool {
	var dial *net.OpError
	return !d.gotConn.Load() && !d.readBody.Load() && ctx.Err() == nil && errors.As(err, &dial) && dial.Op == "dial"
}

type trackedProxyBody struct {
	io.Reader
	read *atomic.Bool
}

func (b trackedProxyBody) Read(p []byte) (int, error) {
	b.read.Store(true)
	return b.Reader.Read(p)
}

func memberQuorumClientPath(path string) bool {
	return path == "/mcp" || path == "/bulk/exec" || strings.HasPrefix(path, "/tasks/") ||
		strings.HasPrefix(path, "/server/") || strings.HasPrefix(path, "/agent/") ||
		path == "/mcp-relay/call" || path == "/mcp-relay/call_mcp_tool" || path == "/mcp-relay/shell_exec" ||
		strings.HasPrefix(path, "/mcp-relay/job/") || strings.HasPrefix(path, "/mcp-relay/get_mcp_job/")
}
