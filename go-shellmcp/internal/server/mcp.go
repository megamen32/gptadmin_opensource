package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/shell"
	"github.com/megamen32/gptadmin/go-shellmcp/internal/system"
)

const mcpProtocolVersion = "2025-03-26"

// MCPTransportRequested reports whether the process should run the real MCP
// protocol over stdio instead of the legacy HTTP shellmcp transport.  HTTP mode
// still exposes the same MCP protocol at /mcp; this selector only chooses the
// process transport used by local launchers such as generic stdio MCP relays.
func MCPTransportRequested(args []string, envValue string) bool {
	v := strings.ToLower(strings.TrimSpace(envValue))
	if v == "stdio" || v == "mcp-stdio" || v == "real-mcp-stdio" {
		return true
	}
	for _, arg := range args {
		a := strings.ToLower(strings.TrimSpace(arg))
		if a == "stdio" || a == "mcp-stdio" || a == "--mcp-stdio" || a == "--transport=stdio" || a == "--mcp-transport=stdio" {
			return true
		}
	}
	return false
}

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type toolCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type resourceReadParams struct {
	URI string `json:"uri"`
}

func (s *Server) mcpHTTP(w http.ResponseWriter, r *http.Request) {
	s.setMCPHeaders(w, r)
	switch r.Method {
	case http.MethodPost:
		s.mcpHTTPPost(w, r)
	case http.MethodGet:
		s.mcpHTTPPoll(w, r)
	case http.MethodOptions:
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, MCP-Protocol-Version, Mcp-Session-Id")
		w.WriteHeader(http.StatusNoContent)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

func (s *Server) mcpHTTPPost(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "empty MCP JSON-RPC body"})
		return
	}
	if trimmed[0] == '[' {
		var raw []json.RawMessage
		if err := json.Unmarshal(trimmed, &raw); err != nil {
			writeJSON(w, http.StatusBadRequest, mcpError(nil, -32700, err.Error()))
			return
		}
		responses := make([]any, 0, len(raw))
		for _, item := range raw {
			resp, reply := s.handleMCPJSON(context.Background(), item)
			if reply {
				responses = append(responses, resp)
			}
		}
		if len(responses) == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, http.StatusOK, responses)
		return
	}
	resp, reply := s.handleMCPJSON(context.Background(), trimmed)
	if !reply {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// mcpHTTPPoll is a deliberately thin streamable-http-like polling transport.
// ShellMCP does not need server-initiated capability messages yet, but MCP
// clients that expect a GET side channel can poll this endpoint and receive a
// short SSE transport descriptor instead of a hard 404.
func (s *Server) mcpHTTPPoll(w http.ResponseWriter, r *http.Request) {
	acceptsSSE := strings.Contains(r.Header.Get("Accept"), "text/event-stream") || truthy(r.URL.Query().Get("sse"))
	if !acceptsSSE {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true,
			"transport": map[string]any{
				"protocol":   "mcp",
				"kind":       "streamable-http-poll",
				"mode":       "poll",
				"post_path":  "/mcp",
				"session_id": sessionIDFromRequest(r),
			},
		})
		return
	}
	timeout := parseSmallTimeout(r.URL.Query().Get("timeout"), 0, 60)
	if timeout > 0 {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(time.Duration(timeout) * time.Second):
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	payload := map[string]any{
		"protocol":   "mcp",
		"kind":       "streamable-http-poll",
		"mode":       "poll",
		"post_path":  "/mcp",
		"session_id": sessionIDFromRequest(r),
		"time":       time.Now().Unix(),
	}
	b, _ := json.Marshal(payload)
	_, _ = fmt.Fprintf(w, "event: transport\ndata: %s\n\n", b)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *Server) setMCPHeaders(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("MCP-Protocol-Version", mcpProtocolVersion)
	w.Header().Set("Mcp-Session-Id", sessionIDFromRequest(r))
}

func sessionIDFromRequest(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("Mcp-Session-Id")); v != "" {
		return v
	}
	if v := strings.TrimSpace(r.URL.Query().Get("session_id")); v != "" {
		return v
	}
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "shellmcp-" + hex.EncodeToString(b[:])
}

func parseSmallTimeout(raw string, def, max int) int {
	if strings.TrimSpace(raw) == "" {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

func (s *Server) handleMCPJSON(ctx context.Context, raw json.RawMessage) (any, bool) {
	var req mcpRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return mcpError(nil, -32700, err.Error()), true
	}
	return s.handleMCPRequest(ctx, req)
}

func (s *Server) handleMCPRequest(ctx context.Context, req mcpRequest) (any, bool) {
	id := req.ID
	if strings.TrimSpace(req.Method) == "" {
		return mcpError(id, -32600, "missing JSON-RPC method"), true
	}
	isNotification := req.ID == nil
	switch req.Method {
	case "initialize":
		return mcpResponse(id, map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities": map[string]any{
				"tools":     map[string]any{"listChanged": false},
				"resources": map[string]any{"subscribe": false, "listChanged": false},
			},
			"serverInfo": map[string]any{"name": "shellmcp-go", "version": fmt.Sprintf("build-%d", parseBuildVersion(BuildVersion))},
		}), !isNotification
	case "notifications/initialized", "notifications/cancelled":
		return nil, false
	case "tools/list":
		return mcpResponse(id, map[string]any{"tools": s.mcpTools()}), !isNotification
	case "tools/call":
		var params toolCallParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return mcpError(id, -32602, err.Error()), !isNotification
		}
		result, err := s.callMCPTool(ctx, params.Name, params.Arguments)
		if err != nil {
			return mcpError(id, -32000, err.Error()), !isNotification
		}
		return mcpResponse(id, result), !isNotification
	case "resources/list":
		return mcpResponse(id, map[string]any{"resources": s.mcpResources()}), !isNotification
	case "resources/read":
		var params resourceReadParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return mcpError(id, -32602, err.Error()), !isNotification
		}
		contents, err := s.readMCPResource(params.URI)
		if err != nil {
			return mcpError(id, -32004, err.Error()), !isNotification
		}
		return mcpResponse(id, map[string]any{"contents": contents}), !isNotification
	default:
		return mcpError(id, -32601, "method not found: "+req.Method), !isNotification
	}
}

func mcpResponse(id any, result any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
}

func mcpError(id any, code int, message string) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}}
}

func mcpText(text string, structured any) map[string]any {
	return map[string]any{
		"content":           []map[string]any{{"type": "text", "text": text}},
		"structuredContent": structured,
	}
}

func (s *Server) mcpTools() []map[string]any {
	return []map[string]any{
		{
			"name":        "shell_exec",
			"description": "Execute a shell command on this ShellMCP host; use background=true for local async jobs.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"cmd":         map[string]any{"type": "string"},
					"cwd":         map[string]any{"type": []string{"string", "null"}},
					"timeout":     map[string]any{"type": []string{"integer", "null"}},
					"env":         map[string]any{"type": []string{"object", "null"}, "additionalProperties": true},
					"background":  map[string]any{"type": "boolean", "default": false},
					"run_as_user": map[string]any{"type": []string{"string", "null"}},
				},
				"required":             []string{"cmd"},
				"additionalProperties": false,
			},
		},
		{
			"name":        "tasks",
			"description": "List or read background shell_exec jobs kept by this ShellMCP process.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"task_id": map[string]any{"type": []string{"string", "null"}},
				},
				"additionalProperties": false,
			},
		},
		{
			"name":        "system_info",
			"description": "Return OS, CPU, memory and hostname information for this ShellMCP host.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
		},
		{
			"name":        "capability_registry",
			"description": "Describe ShellMCP as a real MCP server and its separate transport layer.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
		},
	}
}

func (s *Server) callMCPTool(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
	switch name {
	case "shell_exec":
		return s.mcpShellExec(ctx, args)
	case "tasks":
		return s.mcpTasks(args)
	case "system_info":
		info := system.Get()
		return mcpText(fmt.Sprintf("system info for %s", info.Host), info), nil
	case "capability_registry":
		registry := s.mcpCapabilityRegistry()
		return mcpText("ShellMCP real MCP capability registry", registry), nil
	default:
		return nil, fmt.Errorf("unknown tool %s", name)
	}
}

func (s *Server) mcpShellExec(ctx context.Context, args map[string]any) (map[string]any, error) {
	var req shell.Request
	b, _ := json.Marshal(args)
	if err := json.Unmarshal(b, &req); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Cmd) == "" {
		return nil, errors.New("shell_exec requires cmd")
	}
	s.applyDefaults(&req)
	if req.Background {
		j := s.jobs.Start(req)
		payload := map[string]any{"status": "running", "job_id": j.ID, "server": s.cfg.Name, "started_at": j.StartedAt}
		return mcpText("Shell command continues in background.", payload), nil
	}
	res := shell.Run(ctx, req, s.cfg.LogLimit)
	payload := map[string]any{"server": s.cfg.Name, "result": res}
	return mcpText("shell_exec completed", payload), nil
}

func (s *Server) mcpTasks(args map[string]any) (map[string]any, error) {
	tid, _ := args["task_id"].(string)
	if tid != "" {
		j, ok := s.jobs.Get(tid)
		if !ok {
			return mcpText("Task not found: "+tid, map[string]any{"task_id": tid, "status": "not_found"}), nil
		}
		return mcpText(fmt.Sprintf("Task %s: %s", tid, j.State), j), nil
	}
	jobs := s.jobs.List()
	return mcpText(fmt.Sprintf("%d task(s)", len(jobs)), map[string]any{"count": len(jobs), "tasks": jobs}), nil
}

func (s *Server) mcpCapabilityRegistry() map[string]any {
	mode := s.cfg.Mode
	if mode == "" {
		mode = "webhook"
	}
	return map[string]any{
		"ok":              true,
		"schema_version":  2,
		"host":            s.cfg.Name,
		"capability_role": "real_mcp_server",
		"protocol":        map[string]any{"name": "mcp", "version": mcpProtocolVersion},
		"transport_layer": map[string]any{
			"name":                       "shellmcp",
			"mode":                       mode,
			"http_path":                  "/mcp",
			"stdio":                      true,
			"poll_mode":                  strings.Contains(strings.ToLower(mode), "poll") || s.cfg.QueueEnabled,
			"streamable_http_compatible": true,
		},
		"tools": s.mcpTools(),
	}
}

func (s *Server) mcpResources() []map[string]any {
	return []map[string]any{
		{"uri": "shellmcp://system/info", "name": "System info", "mimeType": "application/json"},
		{"uri": "shellmcp://system/health", "name": "ShellMCP health", "mimeType": "application/json"},
		{"uri": "shellmcp://jobs", "name": "ShellMCP jobs", "mimeType": "application/json"},
		{"uri": "shellmcp://capabilities", "name": "ShellMCP MCP capability registry", "mimeType": "application/json"},
	}
}

func (s *Server) readMCPResource(uri string) ([]map[string]any, error) {
	var payload any
	switch uri {
	case "shellmcp://system/info":
		payload = system.Get()
	case "shellmcp://system/health":
		payload = map[string]any{"ok": true, "time": time.Now().Unix(), "jobs": len(s.jobs.List()), "name": s.cfg.Name, "heartbeat": s.cfg.HeartbeatEnabled, "queue": s.cfg.QueueEnabled, "mode": s.cfg.Mode, "default_user": s.cfg.DefaultUser, "default_home": s.cfg.DefaultHome, "default_cwd": s.cfg.DefaultCwd}
	case "shellmcp://jobs":
		payload = map[string]any{"count": len(s.jobs.List()), "jobs": s.jobs.List()}
	case "shellmcp://capabilities":
		payload = s.mcpCapabilityRegistry()
	default:
		return nil, fmt.Errorf("unknown resource URI %s", uri)
	}
	b, _ := json.MarshalIndent(payload, "", "  ")
	return []map[string]any{{"uri": uri, "mimeType": "application/json", "text": string(b)}}, nil
}

// ServeMCPStdio runs the real MCP protocol over newline-delimited JSON stdio.
// The generic GPTAdmin relay can select stdio_format=ndjson for this transport;
// HTTP users get the same protocol through POST /mcp plus GET /mcp polling.
func (s *Server) ServeMCPStdio(ctx context.Context, in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	enc := json.NewEncoder(out)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		resp, reply := s.handleMCPJSON(ctx, append([]byte(nil), line...))
		if !reply {
			continue
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return scanner.Err()
}
