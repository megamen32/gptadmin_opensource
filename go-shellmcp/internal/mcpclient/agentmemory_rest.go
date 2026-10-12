// SPDX-License-Identifier: Apache-2.0
// HTTP protocol mapping follows AgentMemory0.9.28, Rohit Ghumare and
// contributors: https://github.com/rohitg00/agentmemory . See
// docs/licenses/agentmemory-Apache-2.0.txt for its upstream license.
package mcpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/supervisor"
)

// Matches the installed force-proxy AgentMemory 0.9.28 shim. It retains its
// bounded sessions/export and honest degraded audit behavior, never local KV.
// Only the backend HTTP API is used: no stdio, process, implicit project or
// caller credential is created. Each scoped Client owns this immutable binding.
type agentMemoryRESTSession struct {
	mu            sync.Mutex
	httpClient    *http.Client
	agent         supervisor.Agent
	frozenHeaders bool
	closed        bool
}

func (s *agentMemoryRESTSession) close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func (s *agentMemoryRESTSession) request(ctx context.Context, method, path string, body any) (map[string]any, error) {
	base, err := url.Parse(s.agent.URL)
	if err != nil || base.User != nil || base.RawQuery != "" || base.Fragment != "" ||
		(base.Scheme != "https" && !(base.Scheme == "http" && (base.Hostname() == "127.0.0.1" || base.Hostname() == "localhost" || base.Hostname() == "::1"))) {
		return nil, errors.New("agentmemory requires an authenticated HTTPS or local HTTP base URL")
	}
	var reader io.Reader
	if body != nil {
		encoded, encodeErr := json.Marshal(body)
		if encodeErr != nil {
			return nil, encodeErr
		}
		if len(encoded) > 1<<20 {
			return nil, errors.New("agentmemory request exceeds 1MiB")
		}
		reader = bytes.NewReader(encoded)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(s.agent.URL, "/")+path, reader)
	if err != nil {
		return nil, err
	}
	for key, value := range s.agent.Headers {
		if !s.frozenHeaders {
			value = os.ExpandEnv(value)
		}
		req.Header.Set(key, value)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	client := *s.httpClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(req)
	if err != nil {
		return nil, errors.New("agentmemory backend request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("agentmemory backend HTTP %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil {
		return nil, errors.New("agentmemory backend response incomplete")
	}
	if len(raw) > 1<<20 {
		return nil, errors.New("agentmemory backend response exceeds 1MiB")
	}
	var result map[string]any
	if json.Unmarshal(raw, &result) != nil || result == nil {
		return nil, errors.New("agentmemory backend response is not a JSON object")
	}
	return result, nil
}

func memoryRESTLimit(raw any, fallback int) int {
	var n float64
	switch v := raw.(type) {
	case float64:
		n = v
	case int:
		n = float64(v)
	case json.Number:
		n, _ = v.Float64()
	case string:
		n, _ = strconv.ParseFloat(strings.TrimSpace(v), 64)
	default:
		return fallback
	}
	if math.IsNaN(n) || math.IsInf(n, 0) || n <= 0 {
		return fallback
	}
	if n > 100 {
		return 100
	}
	return int(math.Floor(n))
}

func memoryRESTText(value map[string]any) map[string]any {
	encoded, _ := json.Marshal(value)
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": string(encoded)}}}
}

func memoryRESTList(raw any) []string {
	var list []any
	switch v := raw.(type) {
	case string:
		for _, a := range strings.Split(v, ",") {
			list = append(list, a)
		}
	case []any:
		list = v
	case []string:
		for _, a := range v {
			list = append(list, a)
		}
	}
	out := []string{}
	for _, v := range list {
		if t, ok := v.(string); ok && strings.TrimSpace(t) != "" {
			out = append(out, strings.TrimSpace(t))
		}
	}
	return out
}

func memoryRESTBoundList(value map[string]any, key string, limit int) (int, bool) {
	rows, ok := value[key].([]any)
	if !ok {
		return 0, false
	}
	n := len(rows)
	if n > limit {
		value[key] = rows[:limit]
	}
	return n, n > limit
}

func (s *agentMemoryRESTSession) call(ctx context.Context, method string, params any) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("agentmemory HTTP binding is closed")
	}
	p, _ := params.(map[string]any)
	switch method {
	case "initialize":
		return map[string]any{"protocolVersion": protocolVersion, "capabilities": map[string]any{"tools": map[string]any{"listChanged": false}}, "serverInfo": map[string]any{"name": "agentmemory", "version": "0.9.28-rest-v1"}}, nil
	case "notifications/initialized", "ping":
		return map[string]any{}, nil
	case "tools/list":
		result, err := s.request(ctx, "GET", "/agentmemory/mcp/tools", nil)
		if err != nil {
			return nil, err
		}
		tools, ok := result["tools"].([]any)
		if !ok {
			return nil, errors.New("agentmemory tools array unavailable; no local fallback")
		}
		if len(tools) > 128 {
			return nil, errors.New("agentmemory tool count exceeds 128")
		}
		for _, raw := range tools {
			tool, ok := raw.(map[string]any)
			if !ok {
				return nil, errors.New("agentmemory tool schema unavailable")
			}
			switch tool["name"] {
			case "memory_sessions":
				tool["inputSchema"] = map[string]any{"type": "object", "properties": map[string]any{"limit": map[string]any{"type": "number", "description": "Max sessions to return (default 20, max 100)"}}}
			case "memory_recall", "memory_smart_search":
				schema, ok := tool["inputSchema"].(map[string]any)
				if !ok {
					schema = map[string]any{"type": "object"}
					tool["inputSchema"] = schema
				}
				properties, ok := schema["properties"].(map[string]any)
				if !ok {
					properties = map[string]any{}
					schema["properties"] = properties
				}
				properties["project"] = map[string]any{"type": "string", "description": "Canonical project identifier to scope recall"}
			}
		}
		return map[string]any{"tools": tools}, nil
	case "tools/call":
		name, _ := p["name"].(string)
		args, _ := p["arguments"].(map[string]any)
		if args == nil {
			args = map[string]any{}
		}
		result, err := s.tool(ctx, name, args)
		if err != nil {
			return map[string]any{"content": []any{map[string]any{"type": "text", "text": err.Error()}}, "isError": true}, nil
		}
		return result, nil
	default:
		return nil, fmt.Errorf("agentmemory unsupported MCP method %q", method)
	}
}

func (s *agentMemoryRESTSession) tool(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
	var path, method string
	var body any
	switch name {
	case "memory_recall", "memory_smart_search":
		query, ok := args["query"].(string)
		if !ok || strings.TrimSpace(query) == "" {
			return nil, errors.New("query is required")
		}
		payload := map[string]any{"query": strings.TrimSpace(query), "limit": memoryRESTLimit(args["limit"], 10)}
		if project, ok := args["project"].(string); ok && strings.TrimSpace(project) != "" {
			payload["project"] = strings.TrimSpace(project)
		}
		if format, ok := args["format"].(string); ok && strings.TrimSpace(format) != "" {
			payload["format"] = strings.ToLower(strings.TrimSpace(format))
		}
		if name == "memory_recall" && payload["format"] == nil {
			payload["format"] = "full"
		}
		if budget, ok := args["token_budget"].(float64); ok && budget > 0 && !math.IsInf(budget, 0) && !math.IsNaN(budget) {
			payload["token_budget"] = math.Floor(budget)
		} else if budget, ok := args["token_budget"].(string); ok {
			if n, err := strconv.ParseFloat(strings.TrimSpace(budget), 64); err == nil && n > 0 && !math.IsInf(n, 0) && !math.IsNaN(n) {
				payload["token_budget"] = math.Floor(n)
			}
		}
		path = "/agentmemory/search"
		if name == "memory_smart_search" {
			path = "/agentmemory/smart-search"
		}
		method = "POST"
		body = payload
	case "memory_save":
		rawContent := args["content"]
		if rawContent == nil {
			rawContent = args["query"]
		}
		content, ok := rawContent.(string)
		if !ok || strings.TrimSpace(content) == "" {
			return nil, errors.New("content is required")
		}
		payload := map[string]any{"content": content, "type": "fact", "concepts": memoryRESTList(args["concepts"]), "files": memoryRESTList(args["files"])}
		if value, ok := args["type"].(string); ok && value != "" {
			payload["type"] = value
		}
		appendConcept := func(value string) {
			list := payload["concepts"].([]string)
			for _, existing := range list {
				if existing == value {
					return
				}
			}
			payload["concepts"] = append(list, value)
		}
		if project, ok := args["project"].(string); ok && strings.TrimSpace(project) != "" {
			project = strings.TrimSpace(project)
			payload["project"] = project
			appendConcept("project:" + project)
		}
		if agent := strings.TrimSpace(s.agent.Env["AGENT_ID"]); agent != "" {
			payload["agentId"] = agent
			appendConcept("agent:" + agent)
		}
		path = "/agentmemory/remember"
		method = "POST"
		body = payload
	case "memory_sessions":
		limit := memoryRESTLimit(args["limit"], 20)
		value, err := s.request(ctx, "GET", fmt.Sprintf("/agentmemory/sessions?limit=%d", limit), nil)
		if err != nil {
			return nil, err
		}
		memoryRESTBoundList(value, "sessions", limit)
		return memoryRESTText(value), nil
	case "memory_governance_delete":
		ids := memoryRESTList(args["memoryIds"])
		if len(ids) == 0 {
			return nil, errors.New("memoryIds is required")
		}
		reason := args["reason"]
		if reason == nil || reason == "" {
			reason = "plugin skill request"
		}
		path = "/agentmemory/governance/memories"
		method = "DELETE"
		body = map[string]any{"memoryIds": ids, "reason": reason}
	case "memory_export":
		memories, err := s.request(ctx, "GET", "/agentmemory/memories?limit=100", nil)
		if err != nil {
			return nil, err
		}
		sessions, err := s.request(ctx, "GET", "/agentmemory/sessions?limit=100", nil)
		if err != nil {
			return nil, err
		}
		mc, mt := memoryRESTBoundList(memories, "memories", 100)
		sc, st := memoryRESTBoundList(sessions, "sessions", 100)
		if memories["memories"] == nil {
			memories["memories"] = []any{}
		}
		if sessions["sessions"] == nil {
			sessions["sessions"] = []any{}
		}
		return memoryRESTText(map[string]any{"available": true, "synthesized": true, "memories": memories["memories"], "sessions": sessions["sessions"], "truncated": map[string]any{"memories": mt, "sessions": st}, "counts": map[string]any{"memoriesSeen": mc, "sessionsSeen": sc}, "note": "Synthesized from working memories/sessions endpoints because backend /export is unavailable; bounded to 100 each."}), nil
	case "memory_audit":
		return memoryRESTText(map[string]any{"available": false, "degraded": true, "reason": "backend audit endpoint is disabled in the MCP client because the deployed server lacks api::audit and invoking it can transiently destabilize other endpoints", "note": "No synthetic audit entries are returned because sessions are not an audit log."}), nil
	default:
		if name == "" {
			return nil, errors.New("tool name is required")
		}
		value, err := s.request(ctx, "POST", "/agentmemory/mcp/call", map[string]any{"name": name, "arguments": args})
		if err != nil {
			return nil, err
		}
		if content, ok := value["content"].([]any); ok {
			result := map[string]any{"content": content}
			if failed, specified := value["isError"]; specified {
				flag, valid := failed.(bool)
				if !valid {
					return nil, errors.New("agentmemory backend returned invalid tool error flag")
				}
				result["isError"] = flag
			}
			return result, nil
		}
		return memoryRESTText(value), nil
	}
	value, err := s.request(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	return memoryRESTText(value), nil
}
