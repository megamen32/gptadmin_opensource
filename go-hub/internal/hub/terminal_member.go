package hub

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/megamen32/gptadmin/go-shellmcp/receiptredaction"
)

const memberCopyPath = "/internal/owner/member-receipt-grant"

func (s *Server) receiptExecutorBindingLocked(name string) *dispatchVoter {
	target := "shell:" + name
	e, err := s.executorEnrollmentLocked(target)
	if err != nil {
		return nil
	}
	if e != nil {
		if !e.bound() {
			return nil
		}
		b := &dispatchVoter{ID: e.NodeID, Physical: e.LocalID, Fingerprint: e.LocalFingerprint}
		if !e.voter {
			if s.localExecutors[target] != nil || !s.uniqueReceiptCredentialLocked(target, e.NodeID, e.KeyHash) {
				return nil
			}
			b.Kind = "member"
			b.Credential = e.KeyHash
		}
		return b
	}
	a := s.agents[target]
	if a == nil || a.Status == "disabled" || a.Status == "awaiting_approval" || !truthyAny(a.Meta["approved"]) {
		return nil
	}
	physical, fp, credential := firstString(a.Meta, "server_id"), firstString(a.Meta, "fingerprint"), firstString(a.Meta, shellTokenHashMetaKey)
	if physical == "" || fp == "" || !s.uniqueReceiptCredentialLocked(target, "", credential) {
		return nil
	}
	return &dispatchVoter{ID: target, Physical: physical, Fingerprint: fp, Kind: "agent", Credential: credential}
}

// Under the Hub lock, rule out credentials shared across physical executors.
// Re-evaluate at callback admission so credential rotations take effect.
func (s *Server) uniqueReceiptCredentialLocked(target, memberID, hash string) bool {
	if hash == "" || hash == shellTokenHash(s.cfg.ShellToken) || hash == shellTokenHash(s.cfg.CtlToken) {
		return false
	}
	for name, a := range s.agents {
		if a != nil && name != target && firstString(a.Meta, shellTokenHashMetaKey) == hash {
			return false
		}
	}
	s.membershipMu.Lock()
	defer s.membershipMu.Unlock()
	if s.membership != nil {
		for id, m := range s.membership.Nodes {
			if m != nil && id != memberID && m.KeyHash == hash {
				return false
			}
		}
	}
	return true
}

// Verify the nonvoter's existing scoped node credential, or an individually
// enrolled standalone executor token. A fleet-wide shell token carries no
// identity proof for an independent result copy.
func (s *Server) admittedIndirectExecutor(r *http.Request, name string, member bool) *dispatchVoter {
	if s.quorumNode.VerifyLeader(r.Context()) != nil || s.ensureQuorumAuthorityMaterialized(r.Context()) != nil {
		return nil
	}
	var m *nodeMember
	if member {
		m = s.nodeMemberAuth(r)
		if m == nil {
			return nil
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.receiptExecutorBindingLocked(name)
	if b == nil {
		return nil
	}
	if member {
		if b.Kind == "member" && b.ID == m.NodeID && b.Credential == m.KeyHash && m.Name == "shell:"+name {
			return b
		}
	} else if b.Kind == "agent" && s.pendingShellEnrollmentAllowedLocked(name, r) {
		return b
	}
	return nil
}

// An ingress may send identity and the existing scoped executor credential
// to the leader without any output. Only an affirmative reply after the
// backup also confirms its dispatch grant permits a subsequent body transfer.
func (s *Server) memberCopyViaLeader(r *http.Request, id, name string, member bool, body []byte) bool {
	// A capable worker stamps its canonical outbox and advertises the same
	// version in its result URL. Before a body leaves this ingress, compare
	// both so that a stale or malformed raw outbox cannot cross this hop.
	if len(body) > 0 && r.URL.Query().Get("receipt_redaction") == "1" && receiptredaction.PayloadVersion(body) != receiptredaction.Version {
		return false
	}
	status := s.quorumNode.Status()
	if status.Leader {
		return s.copyForAdmittedMember(r, id, name, member, body)
	}
	if status.LeaderID == "" {
		return false
	}
	q := url.Values{"job_id": {id}, "name": {name}}
	if r.URL.Query().Get("receipt_redaction") == "1" {
		q.Set("receipt_redaction", "1")
	}
	if member {
		q.Set("member", "1")
	}
	resp, err := s.receiptPeerRequest(r.Context(), status.LeaderID, memberCopyPath+"?"+q.Encode(), receiptCredentials(r), bytes.NewReader(body))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusNoContent
}

func (s *Server) memberCopyHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	id, name := q.Get("job_id"), q.Get("name")
	h, ok, e := decodeOwnerHandle(id)
	if r.Method != http.MethodPost || s.receiptPeerID(r) == "" || s.receiptCopyGeneration() == "" || e != nil || !ok || h.Kind != "task" || name == "" || name != canonicalShellQueueName(name) || strings.ContainsAny(name, "/\\") {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	member := q.Get("member") == "1"
	if !s.copyForAdmittedMember(r, id, name, member, nil) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if r.ContentLength != 0 {
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxReceiptCopy))
		if err != nil || len(raw) == 0 || !s.copyForAdmittedMember(r, id, name, member, raw) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) copyForAdmittedMember(r *http.Request, id, name string, member bool, body []byte) bool {
	b := s.admittedIndirectExecutor(r, name, member)
	if b == nil {
		return false
	}
	h, ok, err := decodeOwnerHandle(id)
	if err != nil || !ok || h.Kind != "task" {
		return false
	}
	c := forwardedExecutorResult{Generation: s.receiptCopyGeneration(), ID: id, Server: "shell:" + name, Physical: b.Physical, Fingerprint: b.Fingerprint, Body: body, Delegated: b, Authority: s.quorumNode.ExternalAppliedIndex()}
	if r.URL.Query().Get("receipt_redaction") == "1" {
		c.RedactionVersion = receiptredaction.Version
		if len(body) > 0 && receiptredaction.PayloadVersion(body) != c.RedactionVersion {
			return false
		}
	}
	backup := s.receiptBackup(h.OwnerID)
	here := s.quorumNode.Status().NodeID
	if backup == here {
		return s.acceptExecutorResult(here, c) == nil
	}
	if len(body) == 0 {
		return s.postExecutorCopy(r.Context(), backup, c)
	}
	return s.transmitExecutorResult(r.Context(), backup, c)
}
