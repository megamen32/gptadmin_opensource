package hub

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type offlinePeerRefusal struct {
	status int
	detail map[string]any
}

func (e offlinePeerRefusal) Error() string { return "peer denied the request before execution" }

func (s *Server) offlineRemoteRoute(r *http.Request, target string) (nodePeerRoute, bool) {
	if r == nil || r.Context().Value(offlineDurableBearerKey{}) != true || r.Header.Get(nodePeerHopHeader) != "" || !offlineShellProfileAllows(r, target) {
		return nodePeerRoute{}, false
	}
	s.mu.Lock()
	route, ok := s.nodePeers[target]
	local := s.localExecutors[target] != nil
	s.mu.Unlock()
	if local {
		return nodePeerRoute{}, false
	}
	if authority, applied := r.Context().Value(offlineShellAuthorityKey{}).(offlineShellAuthority); applied && s.quorumPrivateTLS != nil {
		var selected nodePeerRoute
		for id, member := range authority.members {
			if member == nil || member.Name != target {
				continue
			}
			if member.State == nodeStateDisabled {
				continue
			}
			if member.NodeID != id {
				return nodePeerRoute{}, false
			}
			peer, trusted := s.quorumPrivateTLS.peers[id]
			agent, registered := authority.registry[target]
			if !trusted || id == s.localReadbackOwnerID() || (member.State != nodeStateReady && member.State != nodeStateOffline) || !containsString(member.Capabilities, "executor") || member.LocalID == "" || member.LocalFingerprint == "" || !registered || !truthyAny(agent.Meta["approved"]) || agent.Status == "disabled" || agent.Status == "awaiting_approval" || firstString(agent.Meta, "server_id") != member.LocalID || firstString(agent.Meta, "fingerprint") != member.LocalFingerprint || (member.KeyHash != "" && !strings.EqualFold(peer.NodeKeyHash, member.KeyHash)) {
				return nodePeerRoute{}, false
			}
			if selected.NodeID != "" {
				return nodePeerRoute{}, false
			}
			selected = nodePeerRoute{NodeID: id, URL: strings.TrimRight(peer.InternalHubURL, "/"), private: true}
		}
		if selected.NodeID != "" {
			return selected, true
		}
		return nodePeerRoute{}, false
	}
	u, err := url.Parse(route.URL)
	if !ok || err != nil || u.Scheme != "https" || u.Hostname() == "" || !validOwnerID(route.NodeID) {
		return nodePeerRoute{}, false
	}
	s.membershipMu.Lock()
	defer s.membershipMu.Unlock()
	if s.membershipView == nil {
		return nodePeerRoute{}, false
	}
	var active *nodeMember
	for _, member := range s.membershipView.Nodes {
		if member == nil || member.Name != target || member.State == nodeStateDisabled {
			continue
		}
		if active != nil || member.NodeID != route.NodeID || member.State != nodeStateReady || !containsString(member.Capabilities, "executor") {
			return nodePeerRoute{}, false
		}
		active = member
	}
	return route, active != nil
}

func (s *Server) offlineRemoteAllowed(r *http.Request, target string) bool {
	_, ok := s.offlineRemoteRoute(r, target)
	return ok
}

// Construct a fixed script from JSON values, never execute the caller's script
// on the peer. The peer independently authenticates and authorizes its local target.
func (s *Server) offlineRemoteRPC(r *http.Request, route nodePeerRoute, target, code string, readOnly bool) (map[string]any, error) {
	name := "exec"
	if readOnly {
		name = "ro"
	}
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": map[string]any{"code": code}}})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, route.URL+"/mcp", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", r.Header.Get("Authorization"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set(nodePeerHopHeader, "1")
	req.Header.Set("X-GPTAdmin-Offline-Owner", route.NodeID)
	req.Header.Set("X-GPTAdmin-Offline-Target", target)
	if token := instructionToken(r); token != "" {
		req.Header.Set(instructionContextHeader, token)
	}
	req.GetBody = nil
	client := s.nodePeerClient
	if route.private {
		transport, err := s.quorumPrivateTLS.transport(route.NodeID)
		if err != nil {
			return nil, err
		}
		defer transport.CloseIdleConnections()
		client = &http.Client{Transport: transport, Timeout: s.nodePeerClient.Timeout, CheckRedirect: refuseNodeRedirect}
	} else if route.ConnectTo != "" {
		client = newPinnedNodePeerClient(s.cfg.DefaultTimeout+10e9, route)
		defer client.CloseIdleConnections()
	}
	releaseAuthAdmission(r)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (64<<20)+1))
	if err == nil && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusPreconditionRequired) {
		return nil, offlinePeerRefusal{status: resp.StatusCode, detail: map[string]any{"status": "failed", "error": "peer denied request", "executed": false}}
	}
	if err != nil || len(data) > 64<<20 || resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("peer response unavailable")
	}
	var envelope map[string]any
	if json.Unmarshal(data, &envelope) != nil || envelope["error"] != nil {
		return nil, fmt.Errorf("peer MCP request failed")
	}
	payload := mapValue(mapValue(envelope["result"])["structuredContent"])
	if firstString(payload, "status") != "completed" {
		if payload["executed"] == false && firstString(mapValue(payload["error"]), "code") == "instructions_required" {
			payload["owner_target"] = target
			return nil, offlinePeerRefusal{status: http.StatusPreconditionRequired, detail: payload}
		}
		receipt := mapValue(mapValue(payload["error"])["receipt"])
		if firstString(mapValue(payload["error"]), "code") == "forbidden" && intFromAny(receipt["http_status"]) == http.StatusForbidden {
			receipt["executed"] = false
			return nil, offlinePeerRefusal{status: http.StatusForbidden, detail: receipt}
		}
		if receipt["executed"] == false {
			status := intFromAny(receipt["http_status"])
			if status < 400 || status > 499 {
				status = http.StatusForbidden
			}
			return nil, offlinePeerRefusal{status: status, detail: receipt}
		}
		return nil, fmt.Errorf("peer operation failed: %s", firstString(mapValue(payload["error"]), "code"))
	}
	return mapValue(payload["result"]), nil
}

func (s *Server) offlineRemoteDispatch(r *http.Request, target, tool string, args map[string]any, key string, background bool) (map[string]any, int) {
	route, ok := s.offlineRemoteRoute(r, target)
	if !ok || tool != "shell_exec" || key != "" || !background || args["secret_env"] != nil {
		return map[string]any{"status": "failed", "error": "offline remote operation is not eligible", "executed": false}, http.StatusForbidden
	}
	targetJSON, _ := json.Marshal(target)
	argsJSON, err := json.Marshal(args)
	if err != nil {
		return map[string]any{"status": "failed", "executed": false}, http.StatusBadRequest
	}
	code := fmt.Sprintf("return await mcp.tasks.start(%s,\"shell_exec\",%s);", targetJSON, argsJSON)
	result, err := s.offlineRemoteRPC(r, route, target, code, false)
	if err != nil {
		if denied, ok := err.(offlinePeerRefusal); ok {
			return denied.detail, denied.status
		}
		return map[string]any{"status": "failed", "error": map[string]any{"code": "task_outcome_unknown", "message": "peer delivery was not confirmed: " + err.Error()}}, http.StatusBadGateway
	}
	if background {
		id := firstString(result, "taskId")
		h, scoped, e := decodeOwnerHandle(id)
		if e != nil || !scoped || h.OwnerID != route.NodeID {
			return map[string]any{"status": "failed", "error": map[string]any{"code": "task_outcome_unknown", "message": "peer task owner could not be verified"}}, http.StatusBadGateway
		}
		result["owner_id"], result["owner_target"] = route.NodeID, target
		return result, http.StatusOK
	}
	return result, http.StatusOK
}

func (s *Server) offlineRemoteRead(r *http.Request, q ownerRead) ownerReadReply {
	if q.Action != "task" {
		return ownerReadReply{Failure: &readOffline}
	}
	var target string
	var route nodePeerRoute
	if authority, ok := r.Context().Value(offlineShellAuthorityKey{}).(offlineShellAuthority); ok {
		for _, member := range authority.members {
			if member != nil && member.NodeID == q.Hint.OwnerID && (q.Hint.OwnerTarget == "" || q.Hint.OwnerTarget == member.Name) {
				p, valid := s.offlineRemoteRoute(r, member.Name)
				if valid && p.private && (q.Hint.OwnerEndpoint == "" || q.Hint.OwnerEndpoint == p.URL) {
					if target != "" {
						return ownerReadReply{Failure: &readOffline}
					}
					target, route = member.Name, p
				}
			}
		}
	}
	s.mu.Lock()
	for t, p := range s.nodePeers {
		if p.NodeID == q.Hint.OwnerID && (q.Hint.OwnerTarget == "" || q.Hint.OwnerTarget == t) && (q.Hint.OwnerEndpoint == "" || strings.TrimRight(p.URL, "/") == q.Hint.OwnerEndpoint) {
			if target != "" && (target != t || route.NodeID != p.NodeID) {
				s.mu.Unlock()
				return ownerReadReply{Failure: &readOffline}
			}
			if target == "" {
				target, route = t, p
			}
		}
	}
	s.mu.Unlock()
	if target == "" || !s.offlineRemoteAllowed(r, target) {
		return ownerReadReply{Failure: &readOffline}
	}
	idJSON, _ := json.Marshal(q.ID)
	value, err := s.offlineRemoteRPC(r, route, target, fmt.Sprintf("return mcp.tasks.get(%s);", idJSON), true)
	if err != nil || firstString(value, "taskId") != q.ID {
		return ownerReadReply{Failure: &readOffline}
	}
	return ownerReadReply{OwnerID: route.NodeID, ID: q.ID, Value: value}
}
