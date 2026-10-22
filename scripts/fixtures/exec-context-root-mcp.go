// Command exec-context-root-mcp is a real HTTP MCP root plus a GPTAdmin relay
// worker used by the candidate E2E. It serves MCP over HTTP and independently
// polls the candidate relay as one registered root agent.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

type envelope struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      any            `json:"id"`
	Method  string         `json:"method"`
	Params  map[string]any `json:"params"`
}

func main() {
	addr := env("ROOT_FIXTURE_ADDR", "127.0.0.1:0")
	hub := strings.TrimRight(os.Getenv("ROOT_FIXTURE_HUB"), "/")
	agent := env("ROOT_FIXTURE_AGENT", "root:exec-fixture")
	token := os.Getenv("ROOT_FIXTURE_TOKEN")
	if hub == "" || token == "" {
		log.Fatal("ROOT_FIXTURE_HUB and ROOT_FIXTURE_TOKEN are required")
	}
	http.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		var req envelope
		if err := json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		var result any
		var rpcErr any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "exec-context-root-mcp", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "root_marker", "description": "Return the exact zircon root fixture result", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}}}
		case "tools/call":
			if req.Params["name"] != "root_marker" {
				rpcErr = map[string]any{"code": -32601, "message": "unknown tool"}
			} else {
				result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "zircon-root-http-real-result"}}, "structuredContent": map[string]any{"answer": "zircon-root-http-real-result"}}
			}
		default:
			rpcErr = map[string]any{"code": -32601, "message": "method not found"}
		}
		out := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		if rpcErr != nil {
			out["error"] = rpcErr
		} else {
			out["result"] = result
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
	go relayWorker(hub, agent, token)
	log.Printf("root MCP fixture listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
func relayWorker(hub, agent, token string) {
	client := &http.Client{Timeout: 20 * time.Second}
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		req, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/mcp-relay/poll/%s?timeout=10", hub, agent), nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := client.Do(req)
		if err != nil {
			cancel()
			time.Sleep(100 * time.Millisecond)
			continue
		}
		var task map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&task)
		_ = resp.Body.Close()
		cancel()
		id := fmt.Sprint(task["id"])
		method := fmt.Sprint(task["method"])
		if id == "<nil>" {
			continue
		}
		params, _ := json.Marshal(task["params"])
		upstream := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
		var decodedParams any
		_ = json.Unmarshal(params, &decodedParams)
		upstream["params"] = decodedParams
		body, _ := json.Marshal(upstream)
		call, _ := http.NewRequest("POST", "http://"+strings.TrimPrefix(env("ROOT_FIXTURE_ADDR", "127.0.0.1:0"), "http://")+"/mcp", bytes.NewReader(body))
		call.Header.Set("Content-Type", "application/json")
		up, err := client.Do(call)
		var decoded map[string]any
		if err == nil {
			_ = json.NewDecoder(io.LimitReader(up.Body, 4<<20)).Decode(&decoded)
			_ = up.Body.Close()
		}
		ok := err == nil && decoded["error"] == nil
		result := mapValue(decoded["result"])
		postBody, _ := json.Marshal(map[string]any{"id": id, "ok": ok, "result": result, "error": decoded["error"]})
		post, _ := http.NewRequest("POST", fmt.Sprintf("%s/mcp-relay/result/%s", hub, agent), bytes.NewReader(postBody))
		post.Header.Set("Authorization", "Bearer "+token)
		post.Header.Set("Content-Type", "application/json")
		if ack, e := client.Do(post); e == nil {
			_, _ = io.Copy(io.Discard, ack.Body)
			_ = ack.Body.Close()
		}
	}
}
func mapValue(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}
func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
