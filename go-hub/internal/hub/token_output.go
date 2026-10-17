package hub

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const operationTTL = 30 * time.Minute
const operationMaxBytes = 32 << 20
const operationMaxItem = 4 << 20
const operationMaxCount = 128
const compactResultBytes = 1000

// Native clients may opt in to text-only results. The default keeps the
// structured contract (including cached outputSchema) without duplicate text.
func nativeFacadeTools(r *http.Request, tools []map[string]any) []map[string]any {
	if r == nil || r.Header.Get("X-GPTAdmin-Result-Mode") != "text" {
		return tools
	}
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		copy := cloneMap(tool)
		switch firstString(tool, "name") {
		case "ui", "render_gptadmin_dashboard", "renderGptadminDashboard":
		default:
			delete(copy, "outputSchema")
		}
		out = append(out, copy)
	}
	return out
}

func nativeFacadeResult(r *http.Request, raw any) any {
	if r == nil || r.Header.Get("X-GPTAdmin-Result-Mode") == "dual" {
		return raw
	}
	result, ok := raw.(map[string]any)
	if !ok || result["isError"] == true {
		return raw
	}
	structured, exists := result["structuredContent"]
	blocks := outputRows(result["content"])
	if !exists || len(blocks) != 1 {
		return raw
	}
	block := mapValue(blocks[0])
	if len(block) != 2 || firstString(block, "type") != "text" {
		return raw
	}
	decoder := json.NewDecoder(strings.NewReader(firstString(block, "text")))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil || decoder.Decode(new(any)) != io.EOF {
		return raw
	}
	encoded, err := json.Marshal(value)
	expected, expectedErr := json.Marshal(structured)
	if err != nil || expectedErr != nil || !bytes.Equal(encoded, expected) {
		return raw
	}
	out := cloneMap(result)
	if r.Header.Get("X-GPTAdmin-Result-Mode") == "text" {
		delete(out, "structuredContent")
	} else {
		out["content"] = []any{}
	}
	return out
}

type tokenOperation struct {
	InstructionContext                      string
	Principal, Target, Facade, Tool, Origin string
	Created                                 time.Time
	Data                                    []byte
}

func expandToolDefinition(security []map[string]any, meta map[string]any) map[string]any {
	return map[string]any{
		"name": "expand", "title": "Раскрыть операцию",
		"description": "Read saved operation details without executing again. id=operation_id; path is a JSON Pointer. offset/limit page UTF-8 bytes. Expires after 30 minutes, eviction or restart.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"id": map[string]any{"type": "string"}, "path": map[string]any{"type": "string"},
			"offset": map[string]any{"type": "integer", "minimum": 0}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 16000},
		}, "required": []string{"id"}, "additionalProperties": false},
		"outputSchema":    map[string]any{"type": "object", "additionalProperties": true},
		"annotations":     map[string]any{"readOnlyHint": true, "destructiveHint": false, "openWorldHint": false},
		"securitySchemes": security, "_meta": meta,
	}
}

// Reuse credential/profile/session identity; an operation handle grants no new authority.
func operationPrincipal(r *http.Request) string {
	if r == nil {
		return "internal"
	}
	profile, _ := AccessProfileFromRequest(r)
	scope, _ := r.Context().Value(connectionContextKey{}).(connectionScope)
	return connectionDigest(connectionPrincipal(r), r.Header.Get("Authorization"), profile, scope)
}
func (s *Server) rememberOperation(r *http.Request, name, target string, raw any, selectedTool ...string) string {
	data, err := json.Marshal(raw)
	if err != nil || len(data) > operationMaxItem {
		return ""
	}
	var nonce [8]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return ""
	}
	id := "op_" + hex.EncodeToString(nonce[:])
	now := time.Now()
	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	if s.operations == nil {
		s.operations = make(map[string]tokenOperation)
	}
	total := 0
	for key, item := range s.operations {
		if now.Sub(item.Created) > operationTTL {
			delete(s.operations, key)
		} else {
			total += len(item.Data)
		}
	}
	for total+len(data) > operationMaxBytes || len(s.operations) >= operationMaxCount {
		oldest := ""
		var at time.Time
		for key, item := range s.operations {
			if oldest == "" || item.Created.Before(at) {
				oldest = key
				at = item.Created
			}
		}
		if oldest == "" {
			return ""
		}
		total -= len(s.operations[oldest].Data)
		delete(s.operations, oldest)
	}
	tool := ""
	if len(selectedTool) > 0 {
		tool = selectedTool[0]
	}
	origin := name
	if len(selectedTool) > 1 {
		origin = selectedTool[1]
	}
	s.operations[id] = tokenOperation{Origin: origin, Tool: tool, Principal: operationPrincipal(r), Target: target, Facade: name, Created: now, Data: data}
	return id
}
func pageInteger(args map[string]any, key string, fallback int) (int, error) {
	if value, ok := args[key]; ok {
		var f float64
		switch v := value.(type) {
		case int:
			f = float64(v)
		case int64:
			f = float64(v)
		case float64:
			f = v
		case json.Number:
			n, e := v.Float64()
			if e != nil {
				return 0, e
			}
			f = n
		default:
			return 0, fmt.Errorf("invalid %s", key)
		}
		if f < 0 || f > float64(1<<30) || f != float64(int(f)) {
			return 0, fmt.Errorf("invalid %s", key)
		}
		return int(f), nil
	}
	return fallback, nil
}
func jsonPointer(value any, pointer string) (any, error) {
	if pointer == "" {
		return value, nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, fmt.Errorf("path must be a JSON Pointer")
	}
	for _, encoded := range strings.Split(pointer[1:], "/") {
		for i := 0; i < len(encoded); i++ {
			if encoded[i] == '~' {
				if i+1 >= len(encoded) || (encoded[i+1] != '0' && encoded[i+1] != '1') {
					return nil, fmt.Errorf("invalid JSON Pointer escape")
				}
				i++
			}
		}
		key := strings.ReplaceAll(strings.ReplaceAll(encoded, "~1", "/"), "~0", "~")
		switch v := value.(type) {
		case map[string]any:
			var ok bool
			value, ok = v[key]
			if !ok {
				return nil, fmt.Errorf("path not found")
			}
		case []any:
			n, e := strconv.Atoi(key)
			if e != nil || n < 0 || n >= len(v) || strconv.Itoa(n) != key {
				return nil, fmt.Errorf("path not found")
			}
			value = v[n]
		default:
			return nil, fmt.Errorf("path not found")
		}
	}
	return value, nil
}
func (s *Server) expandOperation(r *http.Request, args map[string]any) map[string]any {
	fail := func(message string) map[string]any { return map[string]any{"status": "failed", "error": message} }
	id := firstString(args, "id")
	offset, e := pageInteger(args, "offset", 0)
	if e != nil {
		return fail(e.Error())
	}
	limit, e := pageInteger(args, "limit", 4000)
	if e != nil || limit < 1 || limit > 16000 {
		return fail("limit must be 1..16000 bytes")
	}
	s.operationMu.Lock()
	item, ok := s.operations[id]
	s.operationMu.Unlock()
	if !ok || time.Since(item.Created) > operationTTL || item.Principal != operationPrincipal(r) {
		return fail("operation unavailable: expired, evicted, restarted or different caller; never replay a write for details")
	}
	if !profileAllowsTool(r, item.Origin) || (item.Target != "" && !profileAllowsTarget(r, item.Target)) {
		return fail("access profile denies this operation")
	}
	var value any
	dec := json.NewDecoder(bytes.NewReader(item.Data))
	dec.UseNumber()
	if dec.Decode(&value) != nil {
		return fail("stored result unreadable")
	}
	pointer, _ := args["path"].(string)
	value, e = jsonPointer(value, pointer)
	if e != nil {
		return fail(e.Error())
	}
	data, _ := json.Marshal(value)
	if offset > len(data) {
		return fail("offset exceeds result length")
	}
	if offset < len(data) && !utf8.RuneStart(data[offset]) {
		return fail("offset must be a UTF-8 boundary")
	}
	end := offset + limit
	if end > len(data) {
		end = len(data)
	}
	for end < len(data) && end > offset && !utf8.RuneStart(data[end]) {
		end--
	}
	if end == offset && offset < len(data) {
		return fail("limit too small for next UTF-8 character")
	}
	out := map[string]any{"operation_id": id, "bytes": len(data)}
	if offset == 0 && end == len(data) && item.Facade == "schema" && item.Target != "" {
		definition := mapValue(value)
		if definition["inputSchema"] != nil && firstString(definition, "name") != "" {
			if ref := s.rememberOperation(r, "schema", item.Target, value, firstString(definition, "name"), item.Origin); ref != "" {
				out["call_ref"] = ref
			}
		}
	}
	if offset == 0 && end == len(data) {
		out["data"] = value
	} else {
		out["text"] = string(data[offset:end])
		out["offset"] = offset
		if end < len(data) {
			out["next_offset"] = end
		}
	}
	return out
}

func (s *Server) appsSDKCallForRequest(r *http.Request, name string, args map[string]any) any {
	if name == "expand" {
		return s.expandOperation(r, args)
	}
	if name == "execute" || name == "call_mcp_tool" || name == "callMcpTool" {
		var err error
		args, err = s.resolveExecutionReference(r, args)
		if err != nil {
			return map[string]any{"status": "failed", "error": err.Error()}
		}
	}
	r = requestWithInstructionContext(r, args)
	if failure := s.requireFacadeInstructionContext(r, name, args); failure != nil {
		return failure
	}
	args = withoutInstructionContext(args)
	execution := name == "execute" || name == "call_mcp_tool" || name == "callMcpTool"
	rawArgs := args
	if execution || nodePeerJobTool(name) {
		if e := validateToolOutputDetail(args["detail"]); e != nil {
			return map[string]any{"status": "failed", "error": e.Error()}
		}
		rawArgs = cloneMap(args)
		rawArgs["detail"] = "full"
	}
	raw := s.appsSDKCallRawForRequest(r, name, rawArgs)
	return s.presentTokenOutput(r, name, args, raw)
}

func (s *Server) presentTokenOutput(r *http.Request, name string, args map[string]any, raw any) (presented any) {
	r = requestWithInstructionContext(r, args)
	defer func() { presented = s.presentInstructionContext(r, name, args, presented) }()
	// Scope a selected schema even in full mode; full means detail, not breadth.
	if (name == "schema" || name == "list_mcp_tools" || name == "listMcpTools") && firstString(args, "tool") != "" {
		if value, ok := raw.(map[string]any); ok {
			raw = compactSchema(value, args)
		}
	}
	if (name == "discover" || name == "list_mcp_servers" || name == "listMcpServers") && (firstString(args, "query") != "" || firstString(args, "status") != "") {
		if value, ok := raw.(map[string]any); ok {
			filtered := []any{}
			q := strings.ToLower(firstString(args, "query"))
			st := firstString(args, "status")
			for _, entry := range outputRows(value["servers"]) {
				row := mapValue(entry)
				if st != "" && firstString(row, "status") != st {
					continue
				}
				if q != "" && !strings.Contains(strings.ToLower(firstString(row, "server_id")+" "+firstString(row, "name")), q) {
					continue
				}
				filtered = append(filtered, entry)
			}
			value = cloneMap(value)
			value["servers"] = filtered
			raw = value
		}
	}
	full := args["detail"] == "full"
	if args["detail"] == nil || args["detail"] == "" {
		s.mu.Lock()
		full = s.hubSettingBoolLocked("tool_output_verbose")
		s.mu.Unlock()
	}
	execution := name == "execute" || name == "call_mcp_tool" || name == "callMcpTool"
	if full {
		return raw
	}
	payload, ok := raw.(map[string]any)
	if !ok {
		return raw
	}
	out := payload
	readableGrepMesh := false
	originFacade := name
	schemaResult := name == "schema" || name == "list_mcp_tools" || name == "listMcpTools" || ((name == "job" || name == "get_mcp_job" || name == "getMcpJob") && mapValue(payload["response"])["tools"] != nil)
	if schemaResult && name != "schema" {
		name = "schema"
		args = cloneMap(args)
		args["target"] = firstString(payload, "server_id")
	}
	switch name {
	case "discover", "list_mcp_servers", "listMcpServers":
		out = compactDiscovery(payload, args)
	case "schema", "list_mcp_tools", "listMcpTools":
		out = compactSchema(payload, args)
	default:
		if execution {
			out = compactToolOutput(payload, firstString(args, "tool", "tool_name", "name"))
			if firstString(args, "target", "server_id", "agent_id") == "hub" {
				out = compactHubOutput(out)
			} else if target := firstString(args, "target", "server_id", "agent_id"); strings.HasPrefix(target, "mcp:") {
				out = compactChildOutput(out, target, firstString(args, "tool", "tool_name", "name"))
				out, readableGrepMesh = compactGrepMeshOutput(out, target, firstString(args, "tool", "tool_name", "name"))
			}
		} else if nodePeerJobTool(name) {
			target, tool := s.tokenJobRoute(firstString(args, "id", "job_id"))
			out = compactToolOutput(payload, tool)
			if strings.HasPrefix(target, "mcp:") {
				out = compactChildOutput(out, target, tool)
				out, readableGrepMesh = compactGrepMeshOutput(out, target, tool)
			}
		}
	}
	if execution {
		out = leanExecutionOutput(payload, out, firstString(args, "target", "server_id", "agent_id"), firstString(args, "tool", "tool_name", "name"))
	}
	encoded, _ := json.Marshal(out)
	disclosure := execution && firstString(args, "target", "server_id", "agent_id") == "hub" && (firstString(args, "tool", "tool_name", "name") == "expand" || firstString(args, "tool", "tool_name", "name") == "schema")
	preview := !disclosure && !readableGrepMesh && len(encoded) > compactResultBytes && name != "discover" && name != "list_mcp_servers" && name != "listMcpServers" && name != "schema" && name != "list_mcp_tools" && name != "listMcpTools" && name != "ui" && name != "render_gptadmin_dashboard" && name != "renderGptadminDashboard" && !hasMediaOrFailure(out)
	changed := !reflect.DeepEqual(payload, out)
	if (execution || nodePeerJobTool(name)) && !preview && !readableGrepMesh && firstString(out, "job_id") != "" {
		return out
	}
	if !preview && !changed {
		return out
	}
	id := s.rememberOperation(r, name, firstString(args, "target", "server_id", "agent_id"), payload, firstString(args, "tool"), originFacade)
	if id == "" {
		return payload
	}
	if preview {
		out = map[string]any{"preview": utf8Prefix(encoded, compactResultBytes), "truncated": true, "bytes": len(encoded)}
		for _, key := range []string{"status", "job_id", "owner_target", "resources", "error", "isError", "approval_id", "approval_required"} {
			if v, exists := payload[key]; exists {
				out[key] = v
			}
		}
	} else {
		out = cloneMap(out)
	}
	out["operation_id"] = id
	if (name == "schema" || name == "list_mcp_tools" || name == "listMcpTools") && firstString(args, "tool") != "" {
		out["call_ref"] = id
	}
	return out
}

func (s *Server) tokenJobRoute(id string) (string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if job := s.shellJobs[id]; job != nil {
		if job.ToolName == "mcp_call" && firstString(job.Arguments, "ref") != "" && firstString(job.Arguments, "name") != "" {
			return "mcp:shell:" + job.Server + ":" + firstString(job.Arguments, "ref"), firstString(job.Arguments, "name")
		}
		return "shell:" + job.Server, job.ToolName
	}
	return "", ""
}

// GrepMesh text-only JSON is protocol data, not another business text block.
// Decode only this known child, keeping the original receipt for disclosure.
func compactGrepMeshOutput(raw map[string]any, target, tool string) (map[string]any, bool) {
	if !strings.Contains(strings.ToLower(target), "grepmesh") {
		return raw, false
	}
	upstream := mapValue(raw["result"])
	blocks := outputRows(upstream["content"])
	if len(blocks) != 1 || len(mapValue(blocks[0])) != 2 || firstString(mapValue(blocks[0]), "type") != "text" || truthyAny(upstream["isError"]) {
		return raw, false
	}
	for key := range upstream {
		if key != "content" && key != "isError" {
			return raw, false
		}
	}
	decoder := json.NewDecoder(strings.NewReader(firstString(mapValue(blocks[0]), "text")))
	decoder.UseNumber()
	var decoded map[string]any
	if decoder.Decode(&decoded) != nil || decoder.Decode(new(any)) != io.EOF || decoded == nil {
		return raw, false
	}
	out := cloneMap(raw)
	out["result"] = decoded
	if hasMediaOrFailure(decoded) {
		return out, false
	}
	// Unknown semantic fields are retained rather than silently summarized.
	for key := range decoded {
		switch key {
		case "chunks", "results", "paths", "path", "start_line", "end_line", "host_id", "target_host_id", "origin_host", "host_status", "partial", "truncated", "hop_count", "request_id", "artifact_id":
		default:
			return out, false
		}
	}
	var lines []string
	switch tool {
	case "read_text":
		if decoded["chunks"] == nil || firstString(decoded, "path") == "" {
			return out, false
		}
		for _, chunk := range outputRows(decoded["chunks"]) {
			row := mapValue(chunk)
			if len(row) != 3 || row["start_line"] == nil || row["end_line"] == nil || row["lines"] == nil {
				return out, false
			}
			for _, line := range outputRows(row["lines"]) {
				item := mapValue(line)
				text, ok := item["text"].(string)
				if !ok || len(item) != 2 || item["line_number"] == nil {
					return out, false
				}
				lines = append(lines, fmt.Sprint(item["line_number"])+": "+text)
			}
		}
	case "search":
		if decoded["results"] == nil {
			return out, false
		}
		for _, item := range outputRows(decoded["results"]) {
			row := mapValue(item)
			if len(row) != 3 || firstString(row, "path") == "" || firstString(row, "loc") == "" {
				return out, false
			}
			text, ok := row["data"].(string)
			if !ok {
				return out, false
			}
			lines = append(lines, firstString(row, "path")+":"+firstString(row, "loc")+"\n"+text)
		}
	default:
		return out, false
	}
	text := strings.Join(lines, "\n")
	view := map[string]any{"host": firstString(decoded, "target_host_id", "host_id"), "text": text}
	if path := firstString(decoded, "path"); path != "" {
		view["path"] = path
	}
	for _, key := range []string{"partial", "truncated"} {
		if flag, ok := decoded[key]; ok {
			view[key] = flag
		}
	}
	for _, key := range []string{"host_status", "origin_host", "artifact_id"} {
		if value, ok := decoded[key]; ok {
			view[key] = value
		}
	}
	if len(text) > compactResultBytes {
		prefix := utf8Prefix([]byte(text), compactResultBytes)
		if index := strings.LastIndex(prefix, "\n"); index > 0 {
			prefix = prefix[:index]
		}
		view["text"] = prefix
		view["truncated"] = true
		view["text_bytes"] = len(text)
	}
	out["result"] = view
	return out, true
}
func utf8Prefix(data []byte, n int) string {
	if len(data) <= n {
		return string(data)
	}
	for n > 0 && !utf8.RuneStart(data[n]) {
		n--
	}
	return string(data[:n])
}
func hasMediaOrFailure(value any) bool {
	switch v := value.(type) {
	case map[string]any:
		if truthyAny(v["isError"]) || v["error"] != nil || v["status"] == "failed" || v["status"] == "approval_required" || v["status"] == "running" || v["status"] == "queued" || v["status"] == "input_required" || v["status"] == "cancelled" || (v["returncode"] != nil && intFromAny(v["returncode"]) != 0) {
			return true
		}
		if kind := firstString(v, "type"); kind == "image" || kind == "audio" || kind == "resource" || kind == "resource_link" {
			return true
		}
		for _, item := range v {
			if hasMediaOrFailure(item) {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if hasMediaOrFailure(item) {
				return true
			}
		}
	case []map[string]any:
		for _, item := range v {
			if hasMediaOrFailure(item) {
				return true
			}
		}
	}
	return false
}
func compactDiscovery(raw, args map[string]any) map[string]any {
	servers := make([]map[string]any, 0)
	query := strings.ToLower(firstString(args, "query"))
	status := firstString(args, "status")
	for _, row := range outputRows(raw["servers"]) {
		server := mapValue(row)
		if status != "" && firstString(server, "status") != status {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(firstString(server, "server_id")+" "+firstString(server, "name")), query) {
			continue
		}
		entry := map[string]any{"server_id": server["server_id"], "status": server["status"]}
		name := firstString(server, "name")
		if name != "" && !strings.HasPrefix(name, "Shell: ") && !strings.HasPrefix(name, "Files: ") {
			entry["name"] = name
		}
		servers = append(servers, entry)
	}
	return map[string]any{"servers": servers}
}
func compactSchema(raw, args map[string]any) map[string]any {
	response := mapValue(raw["response"])
	if response["tools"] == nil {
		return raw
	}
	tool := firstString(args, "tool")
	out := cloneMap(raw)
	view := cloneMap(response)
	tools := make([]map[string]any, 0)
	for _, row := range outputRows(response["tools"]) {
		definition := mapValue(row)
		if tool != "" {
			if firstString(definition, "name") == tool {
				tools = append(tools, definition)
			}
			continue
		}
		summary := map[string]any{"name": definition["name"]}
		if description := firstString(definition, "description"); description != "" {
			if i := strings.Index(description, ". "); i >= 0 {
				description = description[:i+1]
			}
			summary["description"] = description
		}
		schema := mapValue(definition["inputSchema"])
		if required, ok := schema["required"]; ok {
			summary["required"] = required
		}
		if a, ok := definition["annotations"]; ok {
			summary["annotations"] = a
		}
		tools = append(tools, summary)
	}
	if tool != "" && len(tools) == 0 {
		return map[string]any{"status": "failed", "error": "unknown tool: " + tool}
	}
	view["tools"] = tools

	out["response"] = view
	if tool == "" {
		out["hint"] = "schema(target,tool) returns exact input schema; expand(operation_id) reads saved full result"
	}
	return out
}

func outputRows(value any) []any {
	switch rows := value.(type) {
	case []any:
		return rows
	case []string:
		out := make([]any, len(rows))
		for i, row := range rows {
			out[i] = row
		}
		return out
	case []map[string]any:
		out := make([]any, len(rows))
		for i, row := range rows {
			out[i] = row
		}
		return out
	}
	return nil
}

// Daily native sessions load only the routing primitives. Administrative and
// specialized tools remain callable, with exact schemas under schema(target=hub).
func compactFacadeTools(r *http.Request, full bool) []map[string]any {
	tools := appsSDKToolsForRequest(r)
	if full || requestAccessMode(r) == accessModeReadonly {
		return tools
	}
	if profile, ok := AccessProfileFromRequest(r); ok && !containsString(profile.AllowedTools, "*") {
		return tools
	}
	selected := make([]map[string]any, 0, 6)
	for _, tool := range tools {
		switch firstString(tool, "name") {
		case "ui", "discover", "schema", "execute", "job", "expand":
			selected = append(selected, tool)
		}
	}
	return selected
}

func (s *Server) writeTokenSchema(w http.ResponseWriter, r *http.Request, args map[string]any, raw any) {
	writeJSON(w, http.StatusOK, s.presentTokenOutput(r, "schema", args, raw))
}

func (s *Server) mcpRelayExpand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "use POST"})
		return
	}
	var args map[string]any
	if err := readJSON(r, &args); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if err := authorizeFacadeCall(r, "expand", args); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s.expandOperation(r, args))
}

// Only unwrap the Hub-owned mcpToolResult envelope when its sole text block
// is exactly the same JSON as structuredContent. Meaningful content stays intact.
func compactHubOutput(raw map[string]any) map[string]any {
	response := mapValue(raw["response"])
	structured, exists := response["structuredContent"]
	if !exists || firstString(response, "resultType") != "complete" {
		return raw
	}
	blocks := outputRows(response["content"])
	if len(blocks) != 1 || firstString(mapValue(blocks[0]), "type") != "text" {
		return raw
	}
	text, _ := mapValue(blocks[0])["text"].(string)
	var value any
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil || decoder.Decode(new(any)) != io.EOF || len(mapValue(blocks[0])) != 2 {
		return raw
	}
	encoded, _ := json.Marshal(value)
	expected, _ := json.Marshal(structured)
	if !bytes.Equal(encoded, expected) {
		return raw
	}
	// Unknown response fields can carry semantic content; never discard them.
	for key := range response {
		if key != "resultType" && key != "content" && key != "structuredContent" {
			return raw
		}
	}
	out := cloneMap(raw)
	delete(out, "response")
	out["result"] = structured
	return out
}

// A schema reference replaces repeated target/tool strings in generated calls.
// Resolve before policy/approval/idempotency; all those gates see canonical args.
func (s *Server) resolveExecutionReference(r *http.Request, args map[string]any) (map[string]any, error) {
	ref := firstString(args, "ref")
	if ref == "" {
		return args, nil
	}
	s.operationMu.Lock()
	item, ok := s.operations[ref]
	s.operationMu.Unlock()
	if !ok || time.Since(item.Created) > operationTTL || item.Principal != operationPrincipal(r) || item.Facade != "schema" || item.Tool == "" {
		return nil, fmt.Errorf("schema reference unavailable; fetch the exact schema again, never replay an unknown write")
	}
	for key, expected := range map[string]string{"target": item.Target, "tool": item.Tool} {
		if actual := firstString(args, key); actual != "" && actual != expected {
			return nil, fmt.Errorf("ref conflicts with %s", key)
		}
	}
	out := cloneMap(args)
	delete(out, "ref")
	out["target"] = item.Target
	out["tool"] = item.Tool
	if firstString(out, "context") == "" && item.InstructionContext != "" {
		out["context"] = item.InstructionContext
	}
	return out, nil
}

// Go ShellMCP wraps mcp_call in this exact boilerplate envelope. Its ref/name
// repeat already-resolved routing args. Preserve the upstream result untouched,
// except an exact duplicate text+structured JSON representation.
func compactChildOutput(raw map[string]any, target, tool string) map[string]any {
	value := mapValue(raw["result"])
	blocks := outputRows(value["content"])
	if firstString(value, "resultType") != "complete" || len(blocks) != 1 || len(mapValue(blocks[0])) != 2 || firstString(mapValue(blocks[0]), "type") != "text" || firstString(mapValue(blocks[0]), "text") != "Child MCP tool completed" || len(value) != 3 {
		return raw
	}
	structured := mapValue(value["structuredContent"])
	ref := target[strings.LastIndex(target, ":")+1:]
	result, exists := structured["result"]
	if !exists || len(structured) != 3 || firstString(structured, "ref") != ref || firstString(structured, "name") != tool {
		return raw
	}
	if upstream := mapValue(result); len(upstream) == 2 && upstream["structuredContent"] != nil {
		content := outputRows(upstream["content"])
		if len(content) == 1 && len(mapValue(content[0])) == 2 && firstString(mapValue(content[0]), "type") == "text" {
			text, _ := mapValue(content[0])["text"].(string)
			var decoded any
			decoder := json.NewDecoder(strings.NewReader(text))
			decoder.UseNumber()
			if decoder.Decode(&decoded) == nil && decoder.Decode(new(any)) == io.EOF {
				a, _ := json.Marshal(decoded)
				b, _ := json.Marshal(upstream["structuredContent"])
				if bytes.Equal(a, b) {
					result = upstream["structuredContent"]
				}
			}
		}
	}
	out := cloneMap(raw)
	out["result"] = result
	return out
}
