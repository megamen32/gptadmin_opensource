package mcpclient

import (
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
	ID     json.RawMessage `json:"id"`
	Result map[string]any  `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type Client struct {
	HTTP        *http.Client
	mu          sync.Mutex
	stdio       map[string]*stdioClient
	starting    map[string]*startingSession
	generations map[string]uint64
	remote      map[string]remoteSession
	validator   func(supervisor.Agent) bool
}

type startingSession struct {
	cancel     context.CancelFunc
	done       chan struct{}
	generation uint64
}

// RuntimeStatus describes the protocol session owned by Client.
type RuntimeStatus struct {
	Ref       string    `json:"ref"`
	Running   bool      `json:"running"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at,omitempty"`
	ExitedAt  time.Time `json:"exited_at,omitempty"`
	ExitCode  int       `json:"exit_code"`
}

type stdioClient struct {
	mu        sync.Mutex
	stateMu   sync.Mutex
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	enc       *json.Encoder
	dec       *json.Decoder
	stderr    boundedBuffer
	nextID    int64
	startedAt time.Time
	exitedAt  time.Time
	exitCode  int
	done      chan struct{}
}

func New() *Client {
	return &Client{
		HTTP:        &http.Client{Timeout: 30 * time.Second},
		stdio:       make(map[string]*stdioClient),
		starting:    make(map[string]*startingSession),
		generations: make(map[string]uint64),
		remote:      make(map[string]remoteSession),
	}
}

// SetAgentValidator installs a final registry check used before a newly
// initialized process becomes active. Close generation checks and this hook
// together prevent stale request snapshots from reviving removed definitions.
func (c *Client) SetAgentValidator(validator func(supervisor.Agent) bool) {
	c.mu.Lock()
	c.validator = validator
	c.mu.Unlock()
}

// Start initializes the configured MCP server. Stdio processes and negotiated
// remote protocol sessions remain active until Close or a transport failure.
func (c *Client) Start(ctx context.Context, agent supervisor.Agent) error {
	if !agent.Enabled {
		return fmt.Errorf("mcp child %q is disabled", agent.Ref)
	}
	if agent.Transport == "stdio" {
		_, err := c.getStdio(ctx, agent)
		return err
	}
	_, err := c.ListTools(ctx, agent)
	return err
}

// Restart replaces the active stdio protocol session and initializes its
// replacement before returning.
func (c *Client) Restart(ctx context.Context, agent supervisor.Agent) error {
	if agent.Transport != "stdio" {
		return fmt.Errorf("mcp child %q restart only applies to stdio transport", agent.Ref)
	}
	if err := c.Close(agent.Ref); err != nil {
		return err
	}
	return c.Start(ctx, agent)
}

// Status returns the actual stdio protocol session state. Remote transports
// do not retain a child process between calls and therefore report stopped.
func (c *Client) Status(ref string) RuntimeStatus {
	c.mu.Lock()
	session := c.stdio[ref]
	c.mu.Unlock()
	if session == nil {
		return RuntimeStatus{Ref: ref}
	}
	return session.status(ref)
}

func (c *Client) Close(ref string) error {
	c.mu.Lock()
	c.generations[ref]++
	session := c.stdio[ref]
	starting := c.starting[ref]
	remote := c.remote[ref]
	delete(c.stdio, ref)
	delete(c.remote, ref)
	c.mu.Unlock()
	if starting != nil {
		starting.cancel()
		<-starting.done
	}
	if session != nil {
		if err := session.close(); err != nil {
			return err
		}
	}
	if remote != nil {
		return remote.close()
	}
	return nil
}

func (s *stdioClient) close() error {
	_ = s.stdin.Close()
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	<-s.done
	return nil
}

func (s *stdioClient) status(ref string) RuntimeStatus {
	status := RuntimeStatus{Ref: ref, StartedAt: s.startedAt}
	select {
	case <-s.done:
		s.stateMu.Lock()
		status.ExitedAt = s.exitedAt
		status.ExitCode = s.exitCode
		s.stateMu.Unlock()
	default:
		status.Running = true
		if s.cmd.Process != nil {
			status.PID = s.cmd.Process.Pid
		}
	}
	return status
}

func (c *Client) CloseAll() {
	c.mu.Lock()
	sessions := make([]*stdioClient, 0, len(c.stdio))
	for _, session := range c.stdio {
		sessions = append(sessions, session)
	}
	remotes := make([]remoteSession, 0, len(c.remote))
	for _, session := range c.remote {
		remotes = append(remotes, session)
	}
	starting := make([]*startingSession, 0, len(c.starting))
	for ref, session := range c.starting {
		c.generations[ref]++
		starting = append(starting, session)
	}
	c.stdio = make(map[string]*stdioClient)
	c.remote = make(map[string]remoteSession)
	c.mu.Unlock()
	for _, session := range starting {
		session.cancel()
	}
	for _, session := range starting {
		<-session.done
	}
	for _, session := range sessions {
		_ = session.close()
	}
	for _, session := range remotes {
		_ = session.close()
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
		return c.remoteSession(ctx, agent, method, params)
	default:
		return nil, fmt.Errorf("mcp child %q has unsupported transport %q", agent.Ref, agent.Transport)
	}
}

func (c *Client) stdioSession(ctx context.Context, agent supervisor.Agent, method string, params any) (map[string]any, error) {
	session, err := c.getStdio(ctx, agent)
	if err != nil {
		return nil, err
	}
	session.mu.Lock()
	select {
	case <-ctx.Done():
		session.mu.Unlock()
		return nil, ctx.Err()
	default:
	}
	session.nextID++
	id := session.nextID
	requestDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			if session.cmd.Process != nil {
				_ = session.cmd.Process.Kill()
			}
		case <-requestDone:
		}
	}()
	if err := session.enc.Encode(rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}); err != nil {
		close(requestDone)
		session.mu.Unlock()
		c.closeIfCurrent(agent.Ref, session)
		return nil, err
	}
	var res rpcResponse
	if err := session.dec.Decode(&res); err != nil {
		close(requestDone)
		session.mu.Unlock()
		c.closeIfCurrent(agent.Ref, session)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("mcp child %q decode: %w (%s)", agent.Ref, err, strings.TrimSpace(session.stderr.String()))
	}
	close(requestDone)
	session.mu.Unlock()
	if res.Error != nil {
		return nil, fmt.Errorf("mcp child %q RPC %d: %s", agent.Ref, res.Error.Code, res.Error.Message)
	}
	return res.Result, nil
}

func (c *Client) closeIfCurrent(ref string, session *stdioClient) {
	c.mu.Lock()
	if c.stdio[ref] == session {
		delete(c.stdio, ref)
	}
	c.mu.Unlock()
	_ = session.close()
}

func (c *Client) getStdio(ctx context.Context, agent supervisor.Agent) (*stdioClient, error) {
	c.mu.Lock()
	if existing := c.stdio[agent.Ref]; existing != nil {
		if existing.status(agent.Ref).Running {
			c.mu.Unlock()
			return existing, nil
		}
		delete(c.stdio, agent.Ref)
	}
	if starting := c.starting[agent.Ref]; starting != nil {
		done := starting.done
		generation := starting.generation
		c.mu.Unlock()
		select {
		case <-done:
			c.mu.Lock()
			invalidated := c.generations[agent.Ref] != generation
			c.mu.Unlock()
			if invalidated {
				return nil, fmt.Errorf("mcp child %q configuration changed while waiting", agent.Ref)
			}
			return c.getStdio(ctx, agent)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	generation := c.generations[agent.Ref]
	startCtx, cancel := context.WithCancel(ctx)
	starting := &startingSession{cancel: cancel, done: make(chan struct{}), generation: generation}
	c.starting[agent.Ref] = starting
	c.mu.Unlock()

	session, err := startStdio(startCtx, agent)
	valid := err == nil
	c.mu.Lock()
	validator := c.validator
	c.mu.Unlock()
	if valid && validator != nil {
		valid = validator(agent)
	}

	c.mu.Lock()
	publish := valid && c.generations[agent.Ref] == generation
	if publish {
		c.stdio[agent.Ref] = session
	} else if err == nil {
		err = fmt.Errorf("mcp child %q configuration changed while starting", agent.Ref)
	}
	c.mu.Unlock()
	cancel()
	if err != nil {
		if session != nil {
			_ = session.close()
		}
	}
	c.mu.Lock()
	if current := c.starting[agent.Ref]; current == starting {
		delete(c.starting, agent.Ref)
	}
	close(starting.done)
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return session, nil
}

func startStdio(ctx context.Context, agent supervisor.Agent) (*stdioClient, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
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
	session := &stdioClient{cmd: cmd, stdin: stdin, enc: json.NewEncoder(stdin), dec: json.NewDecoder(stdout), nextID: 1, startedAt: time.Now(), done: make(chan struct{})}
	cmd.Stderr = &session.stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp child %q start: %w", agent.Ref, err)
	}
	go func() {
		_ = cmd.Wait()
		session.stateMu.Lock()
		session.exitedAt = time.Now()
		if cmd.ProcessState != nil {
			session.exitCode = cmd.ProcessState.ExitCode()
		}
		session.stateMu.Unlock()
		close(session.done)
	}()
	initDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = cmd.Process.Kill()
		case <-initDone:
		}
	}()
	defer close(initDone)
	if err := session.enc.Encode(rpcRequest{JSONRPC: "2.0", ID: 1, Method: "initialize", Params: map[string]any{"protocolVersion": protocolVersion, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "shellmcp-go", "version": "1"}}}); err != nil {
		_ = cmd.Process.Kill()
		<-session.done
		return nil, err
	}
	var initRes rpcResponse
	if err := session.dec.Decode(&initRes); err != nil {
		_ = cmd.Process.Kill()
		<-session.done
		return nil, err
	}
	if initRes.Error != nil {
		_ = cmd.Process.Kill()
		<-session.done
		return nil, fmt.Errorf("mcp child %q initialize: %s", agent.Ref, initRes.Error.Message)
	}
	if err := session.enc.Encode(rpcRequest{JSONRPC: "2.0", Method: "notifications/initialized", Params: map[string]any{}}); err != nil {
		_ = cmd.Process.Kill()
		<-session.done
		return nil, err
	}
	return session, nil
}
