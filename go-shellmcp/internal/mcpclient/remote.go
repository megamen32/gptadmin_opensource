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
	"sync/atomic"
	"time"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/supervisor"
)

type remoteSession interface {
	call(context.Context, string, any) (map[string]any, error)
	close() error
}

type streamableSession struct {
	// mu serializes session lifecycle decisions (initialize, reset, close)
	// only. Individual JSON-RPC POSTs run concurrently: streamable HTTP
	// correlates requests by Mcp-Session-Id, and holding a lock across the
	// whole round-trip serialized every caller behind the slowest child call
	// (one long GrepMesh search made the per-host fan-out sequential).
	mu              sync.Mutex
	httpClient      *http.Client
	agent           supervisor.Agent
	sessionID       atomic.Value // string
	protocolVersion atomic.Value // string
	nextID          atomic.Int64
	initialized     atomic.Bool
	frozenHeaders   bool
	generation      uint64 // guarded by mu, including across reused session IDs
	closed          bool   // prevents an admitted waiter from reopening an evicted session
}

type streamableRequestState struct {
	generation      uint64
	sessionID       string
	protocolVersion string
}

type streamableCallError struct {
	generation uint64
	err        error
}

func (e *streamableCallError) Error() string { return e.err.Error() }
func (e *streamableCallError) Unwrap() error { return e.err }

func (s *streamableSession) sessionIDString() string {
	if v := s.sessionID.Load(); v != nil {
		if id, ok := v.(string); ok {
			return id
		}
	}
	return ""
}

func (s *streamableSession) protocolVersionString() string {
	if v := s.protocolVersion.Load(); v != nil {
		if p, ok := v.(string); ok {
			return p
		}
	}
	return ""
}

type legacySSESession struct {
	mu            sync.Mutex
	httpClient    *http.Client
	agent         supervisor.Agent
	cancel        context.CancelFunc
	body          io.ReadCloser
	scanner       *bufio.Scanner
	postURL       string
	nextID        int64
	initialized   bool
	frozenHeaders bool
}

type httpStatusError struct {
	status int
	err    error
}

// A tool execution may legitimately outlive the discovery/handshake timeout.
// Only an explicit caller deadline grants that budget; retain the configured
// HTTP bound otherwise. Copy the client so other calls and sessions keep their
// limits, transport, headers policy and cancellation behavior.
func executionHTTPClient(client *http.Client, ctx context.Context, method string) *http.Client {
	if method == "tools/call" {
		if deadline, ok := ctx.Deadline(); ok {
			if remaining := time.Until(deadline); remaining > 0 {
				execution := *client
				execution.Timeout = remaining
				return &execution
			}
		}
	}
	return client
}

func (e *httpStatusError) Error() string { return e.err.Error() }
func (e *httpStatusError) Unwrap() error { return e.err }

func (c *Client) remoteSession(ctx context.Context, agent supervisor.Agent, method string, params any) (map[string]any, error) {
	c.mu.Lock()
	if c.remoteClosing[agent.Ref] {
		c.mu.Unlock()
		return nil, errors.New("mcp remote session is closing")
	}
	session := c.remote[agent.Ref]
	if session == nil {
		if agent.ProtocolAdapter == "agentmemory-rest-v1" {
			session = &agentMemoryRESTSession{httpClient: c.HTTP, agent: agent, frozenHeaders: c.frozenHeaders}
		} else {
			switch agent.Transport {
			case "streamable-http":
				streamable := &streamableSession{httpClient: c.HTTP, agent: agent, frozenHeaders: c.frozenHeaders}
				streamable.protocolVersion.Store(protocolVersion)
				session = streamable
			case "sse":
				session = &legacySSESession{httpClient: c.HTTP, agent: agent, frozenHeaders: c.frozenHeaders}
			default:
				c.mu.Unlock()
				return nil, fmt.Errorf("mcp child %q has unsupported remote transport %q", agent.Ref, agent.Transport)
			}
		}
		c.remote[agent.Ref] = session
	}
	c.mu.Unlock()
	result, err := session.call(ctx, method, params)
	if err != nil {
		closeSession := session.close
		var failed *streamableCallError
		if streamable, ok := session.(*streamableSession); ok && errors.As(err, &failed) {
			// Keep the generation check and eviction atomic with recovery. An
			// error from an old request must not close its replacement session.
			streamable.mu.Lock()
			defer streamable.mu.Unlock()
			if streamable.generation != failed.generation {
				return result, err
			}
			closeSession = streamable.closeLocked
		}
		c.mu.Lock()
		if c.remote[agent.Ref] != session || c.remoteClosing[agent.Ref] {
			c.mu.Unlock()
			return result, err
		}
		if c.remoteClosing == nil {
			c.remoteClosing = make(map[string]bool)
		}
		c.remoteClosing[agent.Ref] = true
		c.mu.Unlock()
		cleanupErr := closeSession()
		c.mu.Lock()
		if c.remote[agent.Ref] == session {
			if cleanupErr == nil {
				delete(c.remote, agent.Ref)
			}
			delete(c.remoteClosing, agent.Ref)
		}
		c.mu.Unlock()
		err = errors.Join(err, cleanupErr)
	}
	return result, err
}

func (s *streamableSession) call(ctx context.Context, method string, params any) (map[string]any, error) {
	for attempt := 0; attempt < 2; attempt++ {
		state, err := s.ensureInitialized(ctx)
		if err != nil {
			return nil, &streamableCallError{generation: state.generation, err: err}
		}
		id := s.nextID.Add(1)
		result, _, err := s.rpc(ctx, rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}, state)
		var statusErr *httpStatusError
		if errors.As(err, &statusErr) && statusErr.status == http.StatusNotFound && state.sessionID != "" && attempt == 0 && (!s.frozenHeaders || s.agent.ConnectionMode == "stateless") {
			s.beginReinitialize(state)
			// A sibling may already have recovered; retry through its generation.
			continue
		}
		if err != nil {
			err = &streamableCallError{generation: state.generation, err: err}
		}
		return result, err
	}
	return nil, fmt.Errorf("mcp child %q session recovery failed", s.agent.Ref)
}

// ensureInitialized returns one coherent request snapshot. Only the handshake
// holds mu across network IO; ordinary round-trips use the snapshot concurrently.
func (s *streamableSession) ensureInitialized(ctx context.Context) (streamableRequestState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.requestStateLocked(), errors.New("mcp remote session is closed")
	}
	if !s.initialized.Load() {
		if err := s.initialize(ctx); err != nil {
			return s.requestStateLocked(), err
		}
	}
	return s.requestStateLocked(), nil
}

func (s *streamableSession) requestStateLocked() streamableRequestState {
	return streamableRequestState{s.generation, s.sessionIDString(), s.protocolVersionString()}
}

// Invalidate only the generation that actually returned 404. Concurrent callers
// reuse an in-progress or completed replacement instead of resetting it again.
func (s *streamableSession) beginReinitialize(state streamableRequestState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed && s.generation == state.generation && s.sessionIDString() == state.sessionID {
		s.reset()
	}
}

func (s *streamableSession) initialize(ctx context.Context) error {
	s.reset()
	id := s.nextID.Add(1)
	result, sessionID, err := s.rpc(ctx, rpcRequest{JSONRPC: "2.0", ID: id, Method: "initialize", Params: initializeParams()}, s.requestStateLocked())
	if err != nil {
		return err
	}
	if negotiated, ok := result["protocolVersion"].(string); ok && strings.TrimSpace(negotiated) != "" {
		s.protocolVersion.Store(negotiated)
	}
	s.sessionID.Store(sessionID)
	if _, _, err := s.rpc(ctx, rpcRequest{JSONRPC: "2.0", Method: "notifications/initialized", Params: map[string]any{}}, s.requestStateLocked()); err != nil {
		return err
	}
	s.initialized.Store(true)
	return nil
}

func (s *streamableSession) rpc(ctx context.Context, payload rpcRequest, state streamableRequestState) (map[string]any, string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, state.sessionID, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.agent.URL, bytes.NewReader(body))
	if err != nil {
		return nil, state.sessionID, err
	}
	setRemoteHeadersResolved(req, s.agent, state.protocolVersion, s.frozenHeaders)
	if state.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", state.sessionID)
	}
	resp, err := executionHTTPClient(s.httpClient, ctx, payload.Method).Do(req)
	if err != nil {
		return nil, state.sessionID, fmt.Errorf("mcp child %q HTTP: %w", s.agent.Ref, err)
	}
	defer resp.Body.Close()
	sessionID := state.sessionID
	if header := resp.Header.Get("Mcp-Session-Id"); header != "" {
		sessionID = header
		// Only initialize publishes the returned ID. A late ordinary response
		// cannot overwrite the current generation with its old session header.
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
	s.generation++
	s.sessionID.Store("")
	s.protocolVersion.Store(protocolVersion)
	s.initialized.Store(false)
}

func (s *streamableSession) close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeLocked()
}

func (s *streamableSession) closeLocked() error {
	s.closed = true
	if s.sessionIDString() == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, s.agent.URL, nil)
	if err != nil {
		return err
	}
	setRemoteHeadersResolved(req, s.agent, s.protocolVersionString(), s.frozenHeaders)
	req.Header.Set("Mcp-Session-Id", s.sessionIDString())
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode == http.StatusNotFound || (resp.StatusCode == http.StatusMethodNotAllowed && !s.frozenHeaders) {
		s.reset()
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("mcp child %q DELETE HTTP %d", s.agent.Ref, resp.StatusCode)
	}
	s.reset()
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
	handshakeCtx, cancelHandshake := context.WithTimeout(ctx, sseHandshakeTimeout(s.httpClient.Timeout))
	defer cancelHandshake()
	stopCancellation := context.AfterFunc(handshakeCtx, cancel)
	req, err := http.NewRequestWithContext(streamCtx, http.MethodGet, s.agent.URL, nil)
	if err != nil {
		stopCancellation()
		cancel()
		return err
	}
	setRemoteHeadersResolved(req, s.agent, protocolVersion, s.frozenHeaders)
	streamClient := *s.httpClient
	streamClient.Timeout = 0
	resp, err := streamClient.Do(req)
	if err != nil {
		stopCancellation()
		cancel()
		if handshakeCtx.Err() != nil {
			return handshakeCtx.Err()
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
		if handshakeCtx.Err() != nil {
			return handshakeCtx.Err()
		}
		return fmt.Errorf("mcp child %q SSE endpoint: %w", s.agent.Ref, err)
	}
	if !stopCancellation() && handshakeCtx.Err() != nil {
		resp.Body.Close()
		cancel()
		return handshakeCtx.Err()
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
	// Interrupt scanner reads during initialize as well as ordinary calls.
	streamCancel := s.cancel
	stop := context.AfterFunc(ctx, func() {
		if streamCancel != nil {
			streamCancel()
		}
	})
	defer stop()
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.postURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	setRemoteHeadersResolved(req, s.agent, protocolVersion, s.frozenHeaders)
	resp, err := executionHTTPClient(s.httpClient, ctx, payload.Method).Do(req)
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
	setRemoteHeadersResolved(req, agent, version, false)
}

func setRemoteHeadersResolved(req *http.Request, agent supervisor.Agent, version string, frozen bool) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", version)
	for key, value := range agent.Headers {
		if !frozen {
			value = os.ExpandEnv(value)
		}
		req.Header.Set(key, value)
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

func sseHandshakeTimeout(configured time.Duration) time.Duration {
	if configured > 0 {
		return configured
	}
	return 30 * time.Second
}
