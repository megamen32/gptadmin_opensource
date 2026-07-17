package mcpclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/supervisor"
)

const protocolVersion = "2025-03-26"

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}
type rpcResponse struct {
	Result map[string]any `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type Client struct {
	HTTP  *http.Client
	mu    sync.Mutex
	stdio map[string]*stdioClient
}

type stdioClient struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	enc    *json.Encoder
	dec    *json.Decoder
	stderr bytes.Buffer
	nextID int64
}

func New() *Client {
	return &Client{HTTP: &http.Client{Timeout: 30 * time.Second}, stdio: make(map[string]*stdioClient)}
}

func (c *Client) Close(ref string) error {
	c.mu.Lock()
	session := c.stdio[ref]
	delete(c.stdio, ref)
	c.mu.Unlock()
	if session == nil {
		return nil
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	_ = session.stdin.Close()
	if session.cmd.Process != nil {
		_ = session.cmd.Process.Kill()
	}
	_ = session.cmd.Wait()
	return nil
}

func (c *Client) CloseAll() {
	c.mu.Lock()
	refs := make([]string, 0, len(c.stdio))
	for ref := range c.stdio {
		refs = append(refs, ref)
	}
	c.mu.Unlock()
	for _, ref := range refs {
		_ = c.Close(ref)
	}
}

func (c *Client) ListTools(ctx context.Context, agent supervisor.Agent) ([]map[string]any, error) {
	result, err := c.session(ctx, agent, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	raw, ok := result["tools"].([]any)
	if !ok {
		return nil, errors.New("mcp child: tools/list result has no tools array")
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if tool, ok := item.(map[string]any); ok {
			out = append(out, tool)
		}
	}
	return out, nil
}

func (c *Client) CallTool(ctx context.Context, agent supervisor.Agent, name string, arguments map[string]any) (map[string]any, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("mcp child: tool name is required")
	}
	if arguments == nil {
		arguments = map[string]any{}
	}
	return c.session(ctx, agent, "tools/call", map[string]any{"name": name, "arguments": arguments})
}

func (c *Client) session(ctx context.Context, agent supervisor.Agent, method string, params any) (map[string]any, error) {
	if !agent.Enabled {
		return nil, fmt.Errorf("mcp child %q is disabled", agent.Ref)
	}
	switch agent.Transport {
	case "stdio":
		return c.stdioSession(ctx, agent, method, params)
	case "streamable-http", "sse":
		return c.httpSession(ctx, agent, method, params)
	default:
		return nil, fmt.Errorf("mcp child %q has unsupported transport %q", agent.Ref, agent.Transport)
	}
}

func (c *Client) httpSession(ctx context.Context, agent supervisor.Agent, method string, params any) (map[string]any, error) {
	sessionID := ""
	init, sid, err := c.httpRPC(ctx, agent, sessionID, rpcRequest{JSONRPC: "2.0", ID: 1, Method: "initialize", Params: map[string]any{"protocolVersion": protocolVersion, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "shellmcp-go", "version": "1"}}})
	if err != nil {
		return nil, err
	}
	_ = init
	sessionID = sid
	_, _, err = c.httpRPC(ctx, agent, sessionID, rpcRequest{JSONRPC: "2.0", Method: "notifications/initialized", Params: map[string]any{}})
	if err != nil {
		return nil, err
	}
	result, _, err := c.httpRPC(ctx, agent, sessionID, rpcRequest{JSONRPC: "2.0", ID: 2, Method: method, Params: params})
	return result, err
}

func (c *Client) httpRPC(ctx context.Context, agent supervisor.Agent, sessionID string, payload rpcRequest) (map[string]any, string, error) {
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, agent.URL, bytes.NewReader(body))
	if err != nil {
		return nil, sessionID, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", protocolVersion)
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	for k, v := range agent.Headers {
		req.Header.Set(k, os.ExpandEnv(v))
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, sessionID, fmt.Errorf("mcp child %q HTTP: %w", agent.Ref, err)
	}
	defer resp.Body.Close()
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		sessionID = sid
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return nil, sessionID, fmt.Errorf("mcp child %q HTTP %d: %s", agent.Ref, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if payload.ID == nil {
		io.Copy(io.Discard, resp.Body)
		return map[string]any{}, sessionID, nil
	}
	data, err := readRPCBody(resp)
	if err != nil {
		return nil, sessionID, err
	}
	var decoded rpcResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, sessionID, fmt.Errorf("mcp child %q decode: %w", agent.Ref, err)
	}
	if decoded.Error != nil {
		return nil, sessionID, fmt.Errorf("mcp child %q RPC %d: %s", agent.Ref, decoded.Error.Code, decoded.Error.Message)
	}
	return decoded.Result, sessionID, nil
}

func readRPCBody(resp *http.Response) ([]byte, error) {
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	}
	scanner := bufio.NewScanner(io.LimitReader(resp.Body, 8<<20))
	scanner.Buffer(make([]byte, 4096), 8<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			return []byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return nil, errors.New("mcp child: SSE response contained no data event")
}

func (c *Client) stdioSession(ctx context.Context, agent supervisor.Agent, method string, params any) (map[string]any, error) {
	session, err := c.getStdio(agent)
	if err != nil {
		return nil, err
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	session.nextID++
	id := session.nextID
	if err := session.enc.Encode(rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}); err != nil {
		_ = c.Close(agent.Ref)
		return nil, err
	}
	var res rpcResponse
	if err := session.dec.Decode(&res); err != nil {
		_ = c.Close(agent.Ref)
		return nil, fmt.Errorf("mcp child %q decode: %w (%s)", agent.Ref, err, strings.TrimSpace(session.stderr.String()))
	}
	if res.Error != nil {
		return nil, fmt.Errorf("mcp child %q RPC %d: %s", agent.Ref, res.Error.Code, res.Error.Message)
	}
	return res.Result, nil
}

func (c *Client) getStdio(agent supervisor.Agent) (*stdioClient, error) {
	c.mu.Lock()
	if existing := c.stdio[agent.Ref]; existing != nil {
		c.mu.Unlock()
		return existing, nil
	}
	c.mu.Unlock()
	cmd := exec.Command(agent.Command, agent.Args...)
	cmd.Dir = agent.Cwd
	cmd.Env = os.Environ()
	for k, v := range agent.Env {
		cmd.Env = append(cmd.Env, k+"="+os.ExpandEnv(v))
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	session := &stdioClient{cmd: cmd, stdin: stdin, enc: json.NewEncoder(stdin), dec: json.NewDecoder(stdout), nextID: 1}
	cmd.Stderr = &session.stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp child %q start: %w", agent.Ref, err)
	}
	if err := session.enc.Encode(rpcRequest{JSONRPC: "2.0", ID: 1, Method: "initialize", Params: map[string]any{"protocolVersion": protocolVersion, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "shellmcp-go", "version": "1"}}}); err != nil {
		_ = cmd.Process.Kill()
		return nil, err
	}
	var initRes rpcResponse
	if err := session.dec.Decode(&initRes); err != nil {
		_ = cmd.Process.Kill()
		return nil, err
	}
	if initRes.Error != nil {
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("mcp child %q initialize: %s", agent.Ref, initRes.Error.Message)
	}
	if err := session.enc.Encode(rpcRequest{JSONRPC: "2.0", Method: "notifications/initialized", Params: map[string]any{}}); err != nil {
		_ = cmd.Process.Kill()
		return nil, err
	}
	c.mu.Lock()
	if existing := c.stdio[agent.Ref]; existing != nil {
		c.mu.Unlock()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return existing, nil
	}
	c.stdio[agent.Ref] = session
	c.mu.Unlock()
	return session, nil
}
