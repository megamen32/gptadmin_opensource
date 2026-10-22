// Command exec-context-mcp is a deterministic stdio MCP fixture for the
// candidate E2E/benchmark harness. It has no network or filesystem access.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	} `json:"params"`
}

var largeLogCalls int

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	for scanner.Scan() {
		var req request
		if json.Unmarshal(scanner.Bytes(), &req) != nil || len(req.ID) == 0 || string(req.ID) == "null" {
			continue
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "exec-context-fixture", "version": "1"}}
		case "ping":
			result = map[string]any{}
		case "tools/list":
			result = map[string]any{"tools": []any{
				map[string]any{"name": "describe_policy", "description": "Return the exact fixture policy marker", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}},
				map[string]any{"name": "large_log", "description": "Return a deterministic 1.4 MB multiline fixture log", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}},
				map[string]any{"name": "fixture_stats", "description": "Return fixture call counters", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}},
			}}
		case "tools/call":
			result = call(req.Params.Name)
		default:
			write(out, req.ID, nil, map[string]any{"code": -32601, "message": "method not found"})
			continue
		}
		write(out, req.ID, result, nil)
	}
}

func call(name string) map[string]any {
	switch name {
	case "describe_policy":
		return toolResult("quartz-child-real-result", map[string]any{"value": "quartz-child-real-result"})
	case "large_log":
		largeLogCalls++
		text := buildLog()
		return toolResult(text, map[string]any{"text": text, "bytes": len([]byte(text)), "lines": 30001})
	case "fixture_stats":
		return toolResult(fmt.Sprintf("large_log_calls=%d", largeLogCalls), map[string]any{"large_log_calls": largeLogCalls})
	default:
		return map[string]any{"content": []any{map[string]any{"type": "text", "text": "unknown fixture tool"}}, "isError": true}
	}
}

func buildLog() string {
	lines := make([]string, 30001)
	lines[0] = "RARE_START marker=START-EXACT request=fixture-000001"
	for i := 1; i < 30000; i++ {
		lines[i] = fmt.Sprintf("INFO seq=%05d state=stable service=fixture request=generated", i)
	}
	lines[15000] = "RARE_MIDDLE marker=MIDDLE-EXACT request=fixture-015001"
	lines[30000] = "RARE_END marker=END-EXACT request=fixture-030001"
	return strings.Join(lines, "\n")
}

func toolResult(text string, structured map[string]any) map[string]any {
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "structuredContent": structured}
}

func write(out *bufio.Writer, id json.RawMessage, result any, rpcErr any) {
	response := map[string]any{"jsonrpc": "2.0", "id": id}
	if rpcErr != nil {
		response["error"] = rpcErr
	} else {
		response["result"] = result
	}
	_ = json.NewEncoder(out).Encode(response)
	_ = out.Flush()
}
