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

// The handle names the Hub that authorized and issued the job. Its receipt
// remains deliverable to that Hub after it loses leadership.
func receiptUnavailable(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "task owner or receipt authorization unavailable", "retryable": true})
}

// Identify an enrolled voter over mutually authenticated private HTTPS. Public
// headers cannot select a physical executor, even when a Hub is behind a proxy.
func (s *Server) receiptPeerID(r *http.Request) string {
	if s.quorumPrivateTLS == nil || r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
		return ""
	}
	found := ""
	for id, peer := range s.quorumPrivateTLS.peers {
		if r.TLS.PeerCertificates[0].VerifyHostname(peer.ServerName) == nil {
			if found != "" {
				return ""
			}
			found = id
		}
	}
	return found
}

func resultQueue(path string) (name string, member bool) {
	rest, ok := strings.CutPrefix(path, "/queue/")
	if !ok {
		rest, ok = strings.CutPrefix(path, memberQueuePrefix)
		if !ok {
			return "", false
		}
		member = true
	}
	name, ok = strings.CutSuffix(rest, "/result")
	if !ok || name == "" || name != canonicalShellQueueName(name) || strings.ContainsAny(name, "/\\") {
		return "", false
	}
	return
}

// Old ShellMCP starts its JSON envelope with id. Sniff only its small prefix,
// preserve bytes (including the original numeric representation) for the owner,
// and leave unscoped jobs to the existing handler.
func receiptRoutingID(r *http.Request) string {
	if q := r.URL.Query(); q.Has("job_id") {
		return q.Get("job_id")
	}
	if r.Body == nil {
		return ""
	}
	var prefix bytes.Buffer
	d := json.NewDecoder(io.LimitReader(io.TeeReader(r.Body, &prefix), 2048))
	t, err := d.Token()
	var id string
	if err == nil && t == json.Delim('{') {
		for d.More() {
			k, err := d.Token()
			if err != nil {
				break
			}
			if k == "id" {
				_ = d.Decode(&id)
				break
			}
			var skip json.RawMessage
			if d.Decode(&skip) != nil {
				break
			}
		}
	}
	// Preserve the original Close so client disconnects still cancel forwarding.
	r.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(&prefix, r.Body), r.Body}
	return id
}

func (s *Server) withOwnerReceiptRouting(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, member := resultQueue(r.URL.Path)
		if s.quorumNode == nil || r.Method != http.MethodPost || name == "" {
			next.ServeHTTP(w, r)
			return
		}
		id := receiptRoutingID(r)
		h, scoped, err := decodeOwnerHandle(id)
		if err != nil || scoped && h.Kind != "task" {
			writeJSON(w, 400, map[string]any{"detail": "invalid task receipt handle"})
			return
		}
		if !scoped {
			next.ServeHTTP(w, r)
			return
		}
		if r.ContentLength > 64<<20 {
			writeJSON(w, 413, map[string]any{"detail": "receipt too large"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<20)
		localID := s.quorumNode.Status().NodeID
		if h.OwnerID != localID {
			if s.receiptPeerID(r) != "" {
				receiptUnavailable(w)
				return
			}
			s.forwardOwnerReceipt(w, r, h.OwnerID, id)
			return
		}
		q := r.URL.Query()
		q.Set("job_id", id)
		r.URL.RawQuery = q.Encode()
		if member {
			s.memberExecutorQueue(w, r)
		} else {
			s.requireShell(s.queue)(w, r)
		}
	})
}

// Preserve only executor credential headers across the pinned private hop.
// Route hints, executor attestations and forwarded identity are re-created.
func receiptCredentials(r *http.Request) http.Header {
	h := http.Header{}
	for _, name := range []string{"Authorization", "X-CTL-Token", "X-GPTAdmin-Token", "X-MCP-Relay-Token"} {
		if v := r.Header.Get(name); v != "" {
			h.Set(name, v)
		}
	}
	if h.Get("Authorization") == "" {
		if token := r.URL.Query().Get("token"); token != "" {
			h.Set("Authorization", "Bearer "+token)
		}
	}
	h.Set("Content-Type", "application/json")
	return h
}

func (s *Server) receiptPeerRequest(ctx context.Context, peerID, path string, h http.Header, body io.Reader) (*http.Response, error) {
	if s.quorumPrivateTLS == nil {
		return nil, errOwnerHandle
	}
	transport, err := s.quorumPrivateTLS.transport(peerID)
	if err != nil {
		return nil, err
	}
	// Per-call TLS is pinned to this physical voter. Never follow a redirect
	// or replay a result POST on another endpoint.
	transport.DisableKeepAlives = true
	peer := s.quorumPrivateTLS.peers[peerID]
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(peer.InternalHubURL, "/")+path, body)
	if err != nil {
		return nil, err
	}
	req.Header = h
	req.GetBody = nil
	return (&http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: refuseNodeRedirect}).Do(req)
}

func (s *Server) forwardOwnerReceipt(w http.ResponseWriter, r *http.Request, ownerID, id string) {
	h := receiptCredentials(r)
	attested, err := s.localQuorumExecutorForForward(r)
	if err != nil {
		writeReceiptDenial(w, 400)
		return
	}
	if attested != "" {
		h = http.Header{"Content-Type": []string{"application/json"}}
		h.Set(quorumExecutorHeader, attested)
		h.Set("X-GPTAdmin-Quorum-Forwarded", ownerID)
	}
	var payload []byte
	var original io.Reader = r.Body
	name, member := resultQueue(r.URL.Path)
	indirect := attested == "" && s.receiptCopyGeneration() != "" && name != "" && receiptCredentials(r).Get("Authorization") != "" && s.memberCopyViaLeader(r, id, name, member, nil)
	if s.receiptCopyGeneration() != "" && (attested != "" || indirect) {
		payload, err = io.ReadAll(r.Body)
		if err != nil {
			writeReceiptDenial(w, http.StatusBadRequest)
			return
		}
		original = bytes.NewReader(payload)
	}
	resp, err := s.receiptPeerRequest(r.Context(), ownerID, r.URL.EscapedPath()+"?job_id="+url.QueryEscape(id), h, original)
	if len(payload) > 0 && (err != nil || resp.StatusCode >= 500) {
		// This only records the already executed outcome. Keep the original
		// executor outbox until its normal owner has durably acknowledged it.
		if indirect {
			s.memberCopyViaLeader(r, id, name, member, payload)
		} else {
			s.preserveExecutorResult(r.Context(), id, attested, payload)
		}
	}
	if err != nil {
		receiptUnavailable(w)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		writeReceiptDenial(w, resp.StatusCode)
		return
	}
	var ack struct {
		OK    bool   `json:"ok"`
		ID    string `json:"id"`
		Owner string `json:"owner_id"`
	}
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if readErr != nil || len(data) > 4096 || json.Unmarshal(data, &ack) != nil || !ack.OK || ack.ID != id || ack.Owner != ownerID {
		receiptUnavailable(w)
		return
	}
	writeJSON(w, 200, ack)
}

func writeReceiptDenial(w http.ResponseWriter, status int) {
	switch status {
	case 400, 401, 403, 409, 413:
		writeJSON(w, status, map[string]any{"detail": "executor receipt rejected"})
	default:
		receiptUnavailable(w)
	}
}

// Legacy replicated ledgers have a bounded projection. A scoped receipt on
// the accepting Hub must survive its ACK in full, including strings larger
// than the historical 64 KiB preview. Keep the existing secret redaction.
func (s *Server) persistedExecutorReceiptLocked(j *shellJob) (any, error) {
	value := redactSecretValues(j.Result, j.SecretValues)
	h, scoped, err := decodeOwnerHandle(j.ID)
	if err == nil && scoped && h.Kind == "task" && h.OwnerID == s.localReadbackOwnerIDLocked() && s.ownerReceiptStoreIsLocal() {
		return value, nil
	}
	return boundPersistedResult(compactPersistedOutput(value))
}

func (s *Server) ownerReceiptStoreIsLocal() bool {
	if s.quorumNode == nil {
		return true
	}
	record, ok := s.quorumNode.LocalRecord("cluster_meta", "task_baseline")
	var baseline struct {
		Mode      string
		ClusterID string `json:"cluster_id"`
	}
	return ok && !record.Deleted && json.Unmarshal(record.Value, &baseline) == nil && baseline.Mode == "owner_local" && baseline.ClusterID == s.quorumClusterID
}
