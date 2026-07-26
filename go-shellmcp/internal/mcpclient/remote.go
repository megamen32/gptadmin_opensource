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
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/supervisor"
)

type remoteSession interface {
	call(context.Context, string, any) (map[string]any, error)
	close() error
}

type streamableSession struct {
	mu              sync.Mutex
	httpClient      *http.Client
	agent           supervisor.Agent
	sessionID       string
	protocolVersion string
	nextID          int64
	initialized     bool
}

type legacySSESession struct {
	mu          sync.Mutex
	httpClient  *http.Client
	agent       supervisor.Agent
	cancel      context.CancelFunc
	body        io.ReadCloser
	scanner     *bufio.Scanner
	postURL     string
	nextID      int64
	initialized bool
}

type httpStatusError struct {
	status int
	err    error
}

func (e *httpStatusError) Error() string { return e.err.Error() }
func (e *httpStatusError) Unwrap() error { return e.err }

func (c *Client) remoteSession(ctx context.Context, agent supervisor.Agent, method string, params any) (map[string]any, error) {
	c.mu.Lock()
	session := c.remote[agent.Ref]
	if session == nil {
		switch agent.Transport {
		case "streamable-http":
			session = &streamableSession{httpClient: c.HTTP, agent: agent, protocolVersion: protocolVersion}
		case "sse":
			session = &legacySSESession{httpClient: c.HTTP, agent: agent}
		default:
			c.mu.Unlock()
			return nil, fmt.Errorf("mcp child %q has unsupported remote transport %q", agent.Ref, agent.Transport)
		}
		c.remote[agent.Ref] = session
	}
	c.mu.Unlock()
	result, err := session.call(ctx, method, params)
	if err != nil {
		c.mu.Lock()
		if c.remote[agent.Ref] == session {
			delete(c.remote, agent.Ref)
		}
		c.mu.Unlock()
		_ = session.close()
	}
	return result, err
}

func (s *streamableSession) call(ctx context.Context, method string, params any) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for attempt := 0; attempt < 2; attempt++ {
		if !s.initialized {
			if err := s.initialize(ctx); err != nil {
				return nil, err
			}
		}
		s.nextID++
		result, _, err := s.rpc(ctx, rpcRequest{JSONRPC: "2.0", ID: s.nextID, Method: method, Params: params})
		var statusErr *httpStatusError
		if errors.As(err, &statusErr) && statusErr.status == http.StatusNotFound && s.sessionID != "" && attempt == 0 {
			s.reset()
			continue
		}
		return result, err
	}
	return nil, fmt.Errorf("mcp child %q session recovery failed", s.agent.Ref)
}

func (s *streamableSession) initialize(ctx context.Context) error {
	s.nextID++
	result, sessionID, err := s.rpc(ctx, rpcRequest{JSONRPC: "2.0", ID: s.nextID, Method: "initialize", Params: initializeParams()})
	if err != nil {
		return err
	}
	if negotiated, ok := result["protocolVersion"].(string); ok && strings.TrimSpace(negotiated) != "" {
		s.protocolVersion = negotiated
	}
	s.sessionID = sessionID
	if _, _, err := s.rpc(ctx, rpcRequest{JSONRPC: "2.0", Method: "notifications/initialized", Params: map[string]any{}}); err != nil {
		return err
	}
	s.initialized = true
	return nil
}

func (s *streamableSession) rpc(ctx context.Context, payload rpcRequest) (map[string]any, string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, s.sessionID, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.agent.URL, bytes.NewReader(body))
	if err != nil {
		return nil, s.sessionID, err
	}
	setRemoteHeaders(req, s.agent, s.protocolVersion)
	if s.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", s.sessionID)
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, s.sessionID, fmt.Errorf("mcp child %q HTTP: %w", s.agent.Ref, err)
	}
	defer resp.Body.Close()
	sessionID := s.sessionID
	if header := resp.Header.Get("Mcp-Session-Id"); header != "" {
		sessionID = header
		s.sessionID = header
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		err := fmt.Errorf("mcp child %q HTTP %d: %s", s.agent.Ref, resp.StatusCode, strings.TrimSpace(string(data)))
		return nil, sessionID, &httpStatusError{status: resp.StatusCode, err: err}
	}
	if payload.ID == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return map[string]any{}, sessionID, nil
	}
	decoded, err := readMatchingRPCResponse(resp, payload.ID)
	if err != nil {
		return nil, sessionID, fmt.Errorf("mcp child %q decode: %w", s.agent.Ref, err)
	}
	result, err := responseResult(s.agent.Ref, decoded)
	return result, sessionID, err
}

func (s *streamableSession) reset() {
	s.sessionID = ""
	s.protocolVersion = protocolVersion
	s.initialized = false
}

func (s *streamableSession) close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessionID == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, s.agent.URL, nil)
	if err != nil {
		return err
	}
	setRemoteHeaders(req, s.agent, s.protocolVersion)
	req.Header.Set("Mcp-Session-Id", s.sessionID)
	resp, err := s.httpClient.Do(req)
	s.reset()
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("mcp child %q DELETE HTTP %d", s.agent.Ref, resp.StatusCode)
	}
	return nil
}

func (s *legacySSESession) call(ctx context.Context, method string, params any) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.initialized {
		if err := s.open(ctx); err != nil {
			return nil, err
		}
		s.nextID++
		result, err := s.rpc(ctx, rpcRequest{JSONRPC: "2.0", ID: s.nextID, Method: "initialize", Params: initializeParams()})
		if err != nil {
			return nil, err
		}
		_ = result
		if _, err := s.rpc(ctx, rpcRequest{JSONRPC: "2.0", Method: "notifications/initialized", Params: map[string]any{}}); err != nil {
			return nil, err
		}
		s.initialized = true
	}
	s.nextID++
	return s.rpc(ctx, rpcRequest{JSONRPC: "2.0", ID: s.nextID, Method: method, Params: params})
}

func (s *legacySSESession) open(ctx context.Context) error {
	streamCtx, cancel := context.WithCancel(context.Background())
	stopCancellation := context.AfterFunc(ctx, cancel)
	req, err := http.NewRequestWithContext(streamCtx, http.MethodGet, s.agent.URL, nil)
	if err != nil {
		stopCancellation()
		cancel()
		return err
	}
	setRemoteHeaders(req, s.agent, protocolVersion)
	streamClient := *s.httpClient
	streamClient.Timeout = 0
	resp, err := streamClient.Do(req)
	if err != nil {
		stopCancellation()
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("mcp child %q SSE GET: %w", s.agent.Ref, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		stopCancellation()
		cancel()
		return fmt.Errorf("mcp child %q SSE GET HTTP %d", s.agent.Ref, resp.StatusCode)
	}
	scanner := newSSEScanner(resp.Body)
	endpoint, err := readSSEData(scanner)
	if err != nil {
		stopCancellation()
		resp.Body.Close()
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("mcp child %q SSE endpoint: %w", s.agent.Ref, err)
	}
	if !stopCancellation() && ctx.Err() != nil {
		resp.Body.Close()
		cancel()
		return ctx.Err()
	}
	base, err := url.Parse(s.agent.URL)
	if err != nil {
		resp.Body.Close()
		cancel()
		return err
	}
	relative, err := url.Parse(string(endpoint))
	if err != nil {
		resp.Body.Close()
		cancel()
		return err
	}
	s.cancel = cancel
	s.body = resp.Body
	s.scanner = scanner
	s.postURL = base.ResolveReference(relative).String()
	_ = ctx
	return nil
}

func (s *legacySSESession) rpc(ctx context.Context, payload rpcRequest) (map[string]any, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.postURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	setRemoteHeaders(req, s.agent, protocolVersion)
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mcp child %q SSE POST: %w", s.agent.Ref, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("mcp child %q SSE POST HTTP %d", s.agent.Ref, resp.StatusCode)
	}
	if payload.ID == nil {
		return map[string]any{}, nil
	}
	for {
		data, err := readSSEData(s.scanner)
		if err != nil {
			return nil, err
		}
		var decoded rpcResponse
		if err := json.Unmarshal(data, &decoded); err != nil {
			return nil, err
		}
		if responseIDMatches(decoded.ID, payload.ID) {
			return responseResult(s.agent.Ref, decoded)
		}
	}
}

func (s *legacySSESession) close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
	if s.body != nil {
		return s.body.Close()
	}
	return nil
}

func initializeParams() map[string]any {
	return map[string]any{"protocolVersion": protocolVersion, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "shellmcp-go", "version": "1"}}
}

func setRemoteHeaders(req *http.Request, agent supervisor.Agent, version string) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", version)
	for key, value := range agent.Headers {
		req.Header.Set(key, os.ExpandEnv(value))
	}
}

func readMatchingRPCResponse(resp *http.Response, expectedID any) (rpcResponse, error) {
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		if err != nil {
			return rpcResponse{}, err
		}
		var decoded rpcResponse
		if err := json.Unmarshal(data, &decoded); err != nil {
			return rpcResponse{}, err
		}
		if !responseIDMatches(decoded.ID, expectedID) {
			return rpcResponse{}, errors.New("JSON-RPC response ID does not match request")
		}
		return decoded, nil
	}
	scanner := newSSEScanner(io.LimitReader(resp.Body, 8<<20))
	for {
		data, err := readSSEData(scanner)
		if err != nil {
			return rpcResponse{}, err
		}
		var decoded rpcResponse
		if err := json.Unmarshal(data, &decoded); err != nil {
			return rpcResponse{}, err
		}
		if responseIDMatches(decoded.ID, expectedID) {
			return decoded, nil
		}
	}
}

func newSSEScanner(reader io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 8<<20)
	return scanner
}

func readSSEData(scanner *bufio.Scanner) ([]byte, error) {
	var lines []string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if len(lines) > 0 {
				return []byte(strings.Join(lines, "\n")), nil
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			lines = append(lines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return nil, errors.New("mcp child: SSE stream ended before a data event")
}

func responseIDMatches(raw json.RawMessage, expected any) bool {
	if len(raw) == 0 {
		return false
	}
	want, err := json.Marshal(expected)
	return err == nil && bytes.Equal(bytes.TrimSpace(raw), want)
}

func responseResult(ref string, decoded rpcResponse) (map[string]any, error) {
	if decoded.Error != nil {
		return nil, fmt.Errorf("mcp child %q RPC %d: %s", ref, decoded.Error.Code, decoded.Error.Message)
	}
	return decoded.Result, nil
}
