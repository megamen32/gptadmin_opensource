package cloudos

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// writeJSON marshals v as JSON and writes it to w with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// parseOperation reads and decodes a ComputerOperation from the request body.
func parseOperation(r *http.Request) (*ComputerOperation, bool) {
	if r.ContentLength == 0 {
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return nil, false
	}
	var op ComputerOperation
	if err := json.Unmarshal(body, &op); err != nil {
		return nil, false
	}
	return &op, true
}

// methodOK returns true if the request method matches want.
func methodOK(r *http.Request, want string) bool {
	return r.Method == want
}

// requirePOST is middleware that rejects non-POST requests.
func requirePOST(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !methodOK(r, http.MethodPost) {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{
				"detail": "method not allowed",
			})
			return
		}
		next(w, r)
	}
}

// requireGET is middleware that rejects non-GET requests.
func requireGET(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !methodOK(r, http.MethodGet) {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{
				"detail": "method not allowed",
			})
			return
		}
		next(w, r)
	}
}

// extractID extracts the last path segment after the given prefix.
func extractID(r *http.Request, prefix string) string {
	return strings.TrimPrefix(r.URL.Path, prefix)
}

// ---------------------------------------------------------------------------
// Computer management
// ---------------------------------------------------------------------------

// HandleComputerList returns all registered computers.
// GET /api/v1/cloud-os/computers
func HandleComputerList(registry *Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		computers := registry.List()
		writeJSON(w, http.StatusOK, map[string]any{
			"computers": computers,
		})
	}
}

// HandleComputerStatus returns a single computer by ID.
// GET /api/v1/cloud-os/computers/{id}
func HandleComputerStatus(registry *Registry, tunnelMgr TunnelManager) http.HandlerFunc {
	const prefix = "/api/v1/cloud-os/computers/"

	return func(w http.ResponseWriter, r *http.Request) {
		id := extractID(r, prefix)
		if id == "" || strings.Contains(id, "/") {
			writeJSON(w, http.StatusNotFound, map[string]any{
				"detail": "computer not found",
			})
			return
		}

		comp, ok := registry.Get(id)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{
				"detail": "computer not found",
			})
			return
		}

		// Check if the computer has an active tunnel; if not, report offline.
		if comp.Status == "online" && !tunnelMgr.IsConnected(id) {
			snapshot := *comp
			snapshot.Status = "offline"
			writeJSON(w, http.StatusOK, snapshot)
			return
		}

		writeJSON(w, http.StatusOK, comp)
	}
}

// HandleComputerDisconnect revokes a computer's session and tears down
// its tunnel. For network streams, it also triggers the existing
// NetworkProxyController revocation (which posts a gpr1 revoke ticket
// to the relay's /v1/control/revoke endpoint).
// POST /api/v1/cloud-os/computers/{id}/disconnect
func HandleComputerDisconnect(registry *Registry, tunnelMgr TunnelManager) http.HandlerFunc {
	const prefix = "/api/v1/cloud-os/computers/"

	return func(w http.ResponseWriter, r *http.Request) {
		id := extractID(r, prefix)
		id = strings.TrimSuffix(id, "/disconnect")
		if id == "" || strings.Contains(id, "/") {
			writeJSON(w, http.StatusNotFound, map[string]any{
				"detail": "computer not found",
			})
			return
		}

		comp, ok := registry.Get(id)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{
				"detail": "computer not found",
			})
			return
		}

		// Close the proxyrelay tunnel (posts revocation ticket if
		// ProxyRelayTunnelManager is used).
		_ = tunnelMgr.CloseTunnel(id)

		// Mark offline and deregister.
		comp.Status = "offline"
		registry.Deregister(id)

		writeJSON(w, http.StatusOK, map[string]any{
			"status":   "disconnected",
			"computer": id,
		})
	}
}

// HandleComputerPair accepts a pairing request from the browser
// extension, registers the computer, and returns the agent-side
// relay ticket. The extension passes this ticket to the native host
// via native messaging.
//
// POST /api/v1/cloud-os/computers/pair
// Body: {"name": "MacBook Home", "os": "darwin", "capabilities": ["files","processes","network"]}
func HandleComputerPair(registry *Registry, tunnelMgr TunnelManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name         string   `json:"name"`
			OS           string   `json:"os"`
			Capabilities []string `json:"capabilities"`
			SessionID    string   `json:"session_id"`
			Endpoint     string   `json:"endpoint"`
			AgentToken   string   `json:"agent_token"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"detail": "invalid request body",
			})
			return
		}

		if req.Endpoint != "" {
			u, err := url.Parse(req.Endpoint)
			if err != nil || u.Scheme != "http" || u.Host == "" || req.AgentToken == "" {
				writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "invalid direct agent endpoint"})
				return
			}
		}

		comp := &Computer{
			ID:           newComputerID(),
			Name:         req.Name,
			OS:           req.OS,
			Capabilities: req.Capabilities,
			Status:       "online",
			SessionID:    req.SessionID,
			Endpoint:     req.Endpoint,
			AgentToken:   req.AgentToken,
		}
		registry.Register(comp)

		// If the tunnel manager supports issuing agent tickets,
		// issue a control stream ticket so the native host can
		// connect to the relay immediately.
		var controlTicket string
		if ptm, ok := tunnelMgr.(*ProxyRelayTunnelManager); ok {
			controlStreamID := newStreamID()
			ticket, err := ptm.IssueAgentTicket(comp.ID, controlStreamID, StreamTypeControl)
			if err == nil {
				controlTicket = ticket
			}
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"status":         "paired",
			"computer":       comp,
			"control_ticket": controlTicket,
			"relay_url":      relayURL(tunnelMgr),
		})
	}
}

// HandleAgentTicket issues an agent-side relay ticket for a specific
// stream type. The browser extension calls this for each stream the
// native host needs to open.
//
// POST /api/v1/cloud-os/computers/{id}/ticket
// Body: {"stream_type": "filesystem"}
func HandleAgentTicket(tunnelMgr TunnelManager) http.HandlerFunc {
	const prefix = "/api/v1/cloud-os/computers/"

	return func(w http.ResponseWriter, r *http.Request) {
		id := extractID(r, prefix)
		id = strings.TrimSuffix(id, "/ticket")
		if id == "" || strings.Contains(id, "/") {
			writeJSON(w, http.StatusNotFound, map[string]any{
				"detail": "computer not found",
			})
			return
		}

		var req struct {
			StreamType string `json:"stream_type"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"detail": "invalid request body",
			})
			return
		}

		ptm, ok := tunnelMgr.(*ProxyRelayTunnelManager)
		if !ok {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"detail": "relay tunnel not available",
			})
			return
		}

		streamID := newStreamID()
		ticket, err := ptm.IssueAgentTicket(id, streamID, req.StreamType)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"detail": err.Error(),
			})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"status":    "issued",
			"stream_id": streamID,
			"ticket":    ticket,
			"relay_url": ptm.config.RelayURL,
		})
	}
}

// HandleHeartbeat updates a computer's last-seen timestamp.
// POST /api/v1/cloud-os/computers/{id}/heartbeat
func HandleHeartbeat(registry *Registry) http.HandlerFunc {
	const prefix = "/api/v1/cloud-os/computers/"

	return func(w http.ResponseWriter, r *http.Request) {
		id := extractID(r, prefix)
		id = strings.TrimSuffix(id, "/heartbeat")
		if id == "" || strings.Contains(id, "/") {
			writeJSON(w, http.StatusNotFound, map[string]any{
				"detail": "computer not found",
			})
			return
		}

		_, ok := registry.Get(id)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{
				"detail": "computer not found",
			})
			return
		}

		registry.UpdateLastSeen(id)
		updated, _ := registry.Get(id)

		writeJSON(w, http.StatusOK, map[string]any{
			"status":   "ok",
			"computer": updated,
		})
	}
}

// ---------------------------------------------------------------------------
// File operations — relayed through proxyrelay as filesystem streams
// ---------------------------------------------------------------------------

// HandleFilesList lists a directory on a remote computer.
// POST /api/v1/cloud-os/files/list
func HandleFilesList(registry *Registry, tunnelMgr TunnelManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		op, ok := parseOperation(r)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"detail": "invalid request body",
			})
			return
		}

		comp, err := requireOnlineComputer(registry, tunnelMgr, op.Computer, w)
		if err != nil {
			return
		}
		if comp.Endpoint != "" {
			var result struct {
				Path    string      `json:"path"`
				Entries []FileEntry `json:"entries"`
			}
			if err := callDirectAgent(comp, http.MethodGet, "/v1/files?path="+url.QueryEscape(op.Path), nil, &result); err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]any{"detail": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, result)
			return
		}

		writeJSON(w, http.StatusOK, relayResponse(op, StreamTypeFilesystem))
	}
}

// HandleFilesRead reads a file from a remote computer.
// POST /api/v1/cloud-os/files/read
func HandleFilesRead(registry *Registry, tunnelMgr TunnelManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		op, ok := parseOperation(r)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"detail": "invalid request body",
			})
			return
		}

		if _, err := requireOnlineComputer(registry, tunnelMgr, op.Computer, w); err != nil {
			return
		}

		writeJSON(w, http.StatusOK, relayResponse(op, StreamTypeFilesystem))
	}
}

// HandleFilesWrite writes content to a file on a remote computer.
// POST /api/v1/cloud-os/files/write
func HandleFilesWrite(registry *Registry, tunnelMgr TunnelManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		op, ok := parseOperation(r)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"detail": "invalid request body",
			})
			return
		}

		if _, err := requireOnlineComputer(registry, tunnelMgr, op.Computer, w); err != nil {
			return
		}

		writeJSON(w, http.StatusOK, relayResponse(op, StreamTypeFilesystem))
	}
}

// ---------------------------------------------------------------------------
// Process operations — relayed through proxyrelay as process streams
// ---------------------------------------------------------------------------

// HandleProcessesList lists running processes on a remote computer.
// POST /api/v1/cloud-os/processes/list
func HandleProcessesList(registry *Registry, tunnelMgr TunnelManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		op, ok := parseOperation(r)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"detail": "invalid request body",
			})
			return
		}

		if _, err := requireOnlineComputer(registry, tunnelMgr, op.Computer, w); err != nil {
			return
		}

		writeJSON(w, http.StatusOK, relayResponse(op, StreamTypeProcess))
	}
}

// HandleProcessesExec executes a command on a remote computer.
// POST /api/v1/cloud-os/processes/exec
func HandleProcessesExec(registry *Registry, tunnelMgr TunnelManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		op, ok := parseOperation(r)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"detail": "invalid request body",
			})
			return
		}

		comp, err := requireOnlineComputer(registry, tunnelMgr, op.Computer, w)
		if err != nil {
			return
		}
		if comp.Endpoint != "" {
			var result ExecResult
			if err := callDirectAgent(comp, http.MethodPost, "/v1/exec", map[string]string{"command": op.Command}, &result); err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]any{"detail": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, result)
			return
		}

		writeJSON(w, http.StatusOK, relayResponse(op, StreamTypeProcess))
	}
}

// HandleProcessesKill kills a process on a remote computer.
// POST /api/v1/cloud-os/processes/kill
func HandleProcessesKill(registry *Registry, tunnelMgr TunnelManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		op, ok := parseOperation(r)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"detail": "invalid request body",
			})
			return
		}

		if _, err := requireOnlineComputer(registry, tunnelMgr, op.Computer, w); err != nil {
			return
		}

		writeJSON(w, http.StatusOK, relayResponse(op, StreamTypeProcess))
	}
}

// ---------------------------------------------------------------------------
// Network operations — delegates to existing NetworkProxyController
// ---------------------------------------------------------------------------

// HandleNetworkConnect opens a TCP/HTTP/SOCKS5 proxy tunnel to a target
// through a remote computer.
//
// For network streams, Cloud OS delegates to the existing
// NetworkProxyController capability lifecycle:
//
//  1. If no active capability exists for this computer, one is
//     auto-created with permissive defaults (all CIDRs, all ports).
//  2. IssueStreamGrants() creates client+agent gpr1 tickets.
//  3. The client ticket is returned to the caller.
//  4. The agent ticket is delivered to the native host via the
//     extension's ticket endpoint.
//
// POST /api/v1/cloud-os/network/connect
func HandleNetworkConnect(registry *Registry, tunnelMgr TunnelManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		op, ok := parseOperation(r)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"detail": "invalid request body",
			})
			return
		}

		if _, err := requireOnlineComputer(registry, tunnelMgr, op.Computer, w); err != nil {
			return
		}

		// Network streams use the existing proxyrelay infrastructure.
		// The response includes stream_type "network" so the caller
		// knows to use the proxyrelay WebSocket endpoint.
		resp := relayResponse(op, StreamTypeNetwork)
		resp["note"] = "network streams use existing NetworkProxyController + proxyrelay"
		resp["proxy_control_base"] = "/proxy-control/v1"

		writeJSON(w, http.StatusOK, resp)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// requireOnlineComputer is a shared guard that checks the computer exists
// in the registry and has an active tunnel.
func requireOnlineComputer(registry *Registry, tunnelMgr TunnelManager, computerID string, w http.ResponseWriter) (*Computer, error) {
	comp, ok := registry.Get(computerID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"detail": "computer not found",
		})
		return nil, errComputerNotFound{}
	}
	if comp.Status != "online" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"detail": "computer is offline",
			"status": comp.Status,
		})
		return nil, errComputerOffline{}
	}
	if comp.Endpoint != "" {
		return comp, nil
	}
	if !tunnelMgr.IsConnected(computerID) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"detail": "tunnel not connected",
		})
		return nil, ErrTunnelUnavailable
	}
	return comp, nil
}

// relayResponse is the envelope returned when an operation has been
// validated and is forwarded to the native host via the proxyrelay.
func relayResponse(op *ComputerOperation, streamType string) map[string]any {
	return map[string]any{
		"status":      "relayed",
		"operation":   op.Operation,
		"computer":    op.Computer,
		"session":     op.Session,
		"stream_type": streamType,
	}
}

// relayURL extracts the relay URL from a ProxyRelayTunnelManager
// for use in pairing responses.
func relayURL(tunnelMgr TunnelManager) string {
	if ptm, ok := tunnelMgr.(*ProxyRelayTunnelManager); ok {
		return ptm.config.RelayURL
	}
	return ""
}

type errComputerNotFound struct{}

func (errComputerNotFound) Error() string { return "computer not found" }

type errComputerOffline struct{}

func (errComputerOffline) Error() string { return "computer is offline" }
