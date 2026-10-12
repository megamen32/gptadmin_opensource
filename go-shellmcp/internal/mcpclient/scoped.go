package mcpclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/cookiejar"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/supervisor"
)

// Match the Hub's finite 128 downstream namespace slots. This is bookkeeping,
// not 128 running children/requests: HTTP work is separately capped at eight
// and managed stdio remains disabled with its original eight-slot ceiling.
const maxScopedPartitions = 128
const maxScopedHTTPRequests = 8
const maxScopedStdioPartitions = 8
const scopedIdleTTL = 5 * time.Minute
const closedScopeTTL = 5 * time.Minute
const maxClosedScopes = 128

type scopeCloseAttempt struct {
	done chan struct{}
	err  error
}

type scopedPartition struct {
	idle                    chan struct{}
	closeAttempt            *scopeCloseAttempt
	ref, scope, fingerprint string
	stateless               bool
	stdio                   bool
	client                  *Client
	ctx                     context.Context
	cancel                  context.CancelFunc
	active                  int
	lastUsed                time.Time
	closing                 bool
}

// ValidateScope accepts only opaque Hub-created connection partitions.
func ValidateScope(scope string) error {
	if len(scope) != 64 {
		return errors.New("mcp connection scope must be 64 hexadecimal characters")
	}
	if _, err := hex.DecodeString(scope); err != nil {
		return errors.New("mcp connection scope must be 64 hexadecimal characters")
	}
	return nil
}

// effectiveDefinition snapshots secrets without exposing them in the pool key.
// The original definition is retained separately for registry validation.
func effectiveDefinition(agent supervisor.Agent) (supervisor.Agent, []string, string) {
	effective := agent
	effective.Env = make(map[string]string, len(agent.Env))
	effective.Headers = make(map[string]string, len(agent.Headers))
	for k, v := range agent.Env {
		effective.Env[k] = os.ExpandEnv(v)
	}
	for k, v := range agent.Headers {
		effective.Headers[k] = os.ExpandEnv(v)
	}
	var environment []string
	if agent.Transport == "stdio" {
		environment = make([]string, 0)
		merged := make(map[string]string)
		for _, pair := range os.Environ() {
			k, v, ok := strings.Cut(pair, "=")
			if ok && scopedRuntimeEnvironmentKey(k) {
				merged[k] = v
			}
		}
		for k, v := range effective.Env {
			merged[k] = v
		}
		for k, v := range merged {
			environment = append(environment, k+"="+v)
		}
		sort.Strings(environment)
	}
	encoded, _ := json.Marshal(struct {
		Agent       supervisor.Agent
		Environment []string
	}{effective, environment})
	digest := sha256.Sum256(encoded)
	return effective, environment, hex.EncodeToString(digest[:])
}

func (c *Client) acquireScope(agent supervisor.Agent, scope string, contexts ...context.Context) (*scopedPartition, supervisor.Agent, error) {
	if err := ValidateScope(scope); err != nil {
		return nil, agent, err
	}
	if !agent.Enabled {
		return nil, agent, fmt.Errorf("mcp child %q is disabled", agent.Ref)
	}
	scope = strings.ToLower(scope)
	effective, environment, fingerprint := effectiveDefinition(agent)
	key := agent.Ref + "\x00" + scope + "\x00" + fingerprint
	c.mu.Lock()
	if len(contexts) > 0 {
		if expected, ok := contexts[0].Value(expectedGenerationKey{}).(uint64); ok && c.generations[agent.Ref] != expected {
			c.mu.Unlock()
			return nil, agent, errors.New("mcp child lifecycle changed; initialize a new connection")
		}
	}
	if err := c.scopeAdmissionErrorLocked(agent.Ref, scope, time.Now()); err != nil {
		c.mu.Unlock()
		return nil, agent, err
	}
	validator := c.validator
	generationSnapshot := c.generations[agent.Ref]
	epochSnapshot := c.scopedEpoch
	c.mu.Unlock()
	if validator != nil && !validator(agent) {
		return nil, agent, errors.New("mcp child configuration changed")
	}
	// Lazy cleanup happens outside the pool lock; closing entries retain their
	// capacity reservation until the underlying process/session has stopped.
	c.mu.Lock()
	var expired []*scopedPartition
	now := time.Now()
	for _, p := range c.scoped {
		if p.ref == agent.Ref && p.scope == scope && p.fingerprint != fingerprint && (!p.stateless || p.active > 0) {
			c.mu.Unlock()
			return nil, agent, errors.New("mcp connection configuration changed; initialize a new connection")
		}
	}
	for _, p := range c.scoped {
		stale := p.ref == agent.Ref && p.scope == scope && p.fingerprint != fingerprint
		if !p.closing && p.active == 0 && (stale || p.stateless && now.Sub(p.lastUsed) >= scopedIdleTTL) {
			p.closing = true
			p.closeAttempt = &scopeCloseAttempt{done: make(chan struct{})}
			expired = append(expired, p)
		}
	}
	c.mu.Unlock()
	if err := c.disposePartitions(expired); err != nil {
		return nil, agent, fmt.Errorf("mcp expired connection cleanup: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.scopeAdmissionErrorLocked(agent.Ref, scope, time.Now()); err != nil {
		return nil, agent, err
	}
	if c.generations[agent.Ref] != generationSnapshot || c.scopedEpoch != epochSnapshot {
		return nil, agent, errors.New("mcp connection was closed while acquiring")
	}
	if p := c.scoped[key]; p != nil {
		if p.closing {
			return nil, agent, errors.New("mcp connection is closing")
		}
		if p.active == 0 {
			p.idle = make(chan struct{})
		}
		p.active++
		return p, effective, nil
	}
	if len(c.scoped) >= maxScopedPartitions {
		return nil, agent, errors.New("mcp scoped connection capacity exhausted (maximum 128)")
	}
	if agent.Transport == "stdio" {
		count := 0
		for _, partition := range c.scoped {
			if partition.stdio {
				count++
			}
		}
		if count >= maxScopedStdioPartitions {
			return nil, agent, errors.New("mcp scoped stdio capacity exhausted (maximum 8)")
		}
	}
	generation := c.generations[agent.Ref]
	child := New()
	httpClient := *c.HTTP
	if httpClient.Jar != nil {
		httpClient.Jar, _ = cookiejar.New(nil)
	}
	child.HTTP = &httpClient
	child.frozenEnv = environment
	child.frozenHeaders = true
	child.SetAgentValidator(func(_ supervisor.Agent) bool {
		c.mu.Lock()
		currentGeneration := c.generations[agent.Ref]
		currentValidator := c.validator
		c.mu.Unlock()
		return currentGeneration == generation && (currentValidator == nil || currentValidator(agent))
	})
	ctx, cancel := context.WithCancel(context.Background())
	p := &scopedPartition{ref: agent.Ref, scope: scope, fingerprint: fingerprint, stateless: agent.ConnectionMode == "stateless", stdio: agent.Transport == "stdio", client: child, ctx: ctx, cancel: cancel, active: 1, idle: make(chan struct{}), lastUsed: now}
	if c.scoped == nil {
		c.scoped = make(map[string]*scopedPartition)
	}
	c.scoped[key] = p
	return p, effective, nil
}

func (c *Client) releaseScope(p *scopedPartition) {
	c.mu.Lock()
	p.active--
	if p.active == 0 {
		close(p.idle)
	}
	p.lastUsed = time.Now()
	c.mu.Unlock()
}

// RequestScoped preserves native methods and parameters within one partition.
// An empty scope intentionally retains the legacy ref-only session behavior.
func (c *Client) RequestScoped(ctx context.Context, agent supervisor.Agent, scope, method string, params any) (map[string]any, error) {
	if scope == "" {
		return c.session(ctx, agent, method, params)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if agent.Transport == "streamable-http" || agent.Transport == "sse" {
		c.mu.Lock()
		if c.scopedHTTPAdmission == nil {
			c.scopedHTTPAdmission = make(chan struct{}, maxScopedHTTPRequests)
		}
		admission := c.scopedHTTPAdmission
		c.mu.Unlock()
		select {
		case admission <- struct{}{}:
			defer func() { <-admission }()
		default:
			return nil, errors.New("mcp scoped HTTP concurrency exhausted (maximum 8); no request dispatched")
		}
	}
	p, effective, err := c.acquireScope(agent, scope, ctx)
	if err != nil {
		return nil, err
	}
	defer c.releaseScope(p)
	callCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(p.ctx, cancel)
	defer stop()
	defer cancel()
	if p.ctx.Err() != nil {
		return nil, errors.New("mcp connection is closed")
	}
	result, callErr := p.client.session(callCtx, effective, method, params)
	if callErr != nil {
		// A failed mutable session cannot silently reconnect under the same
		// downstream identity. CloseScope retries any remaining upstream cleanup.
		c.mu.Lock()
		if !p.closing {
			p.closing = true
			p.cancel()
			p.closeAttempt = &scopeCloseAttempt{done: make(chan struct{}), err: callErr}
			close(p.closeAttempt.done)
			callErr = errors.Join(callErr, c.rememberClosedScopeLocked(p.ref, p.scope, time.Now()))
		}
		c.mu.Unlock()
	}
	return result, callErr
}

func (c *Client) ListToolsScoped(ctx context.Context, agent supervisor.Agent, scope string) ([]map[string]any, error) {
	result, err := c.RequestScoped(ctx, agent, scope, "tools/list", map[string]any{})
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

func (c *Client) CallToolScoped(ctx context.Context, agent supervisor.Agent, name string, arguments map[string]any, scope string) (map[string]any, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("mcp child: tool name is required")
	}
	if arguments == nil {
		arguments = map[string]any{}
	}
	return c.RequestScoped(ctx, agent, scope, "tools/call", map[string]any{"name": name, "arguments": arguments})
}

func (c *Client) disposePartitions(parts []*scopedPartition) error {
	var cleanupErr error
	for _, p := range parts {
		p.cancel()
	}
	for _, p := range parts {
		<-p.idle
		err := p.client.Close(p.ref)
		cleanupErr = errors.Join(cleanupErr, err)
		c.mu.Lock()
		key := p.ref + "\x00" + p.scope + "\x00" + p.fingerprint
		if err == nil && c.scoped[key] == p {
			delete(c.scoped, key)
		}
		p.closeAttempt.err = err
		close(p.closeAttempt.done)
		c.mu.Unlock()
	}
	return cleanupErr
}

func (c *Client) closePartitions(ref, scope string) error {
	c.mu.Lock()
	c.scopedEpoch++
	if ref != "" && scope == "" {
		c.generations[ref]++
	}
	var parts []*scopedPartition
	var waiting []*scopeCloseAttempt
	var tombstoneErr error
	if scope != "" {
		tombstoneErr = c.rememberClosedScopeLocked(ref, scope, time.Now())
	}
	for _, p := range c.scoped {
		if (ref == "" || p.ref == ref) && (scope == "" || p.scope == scope) {
			tombstoneErr = errors.Join(tombstoneErr, c.rememberClosedScopeLocked(p.ref, p.scope, time.Now()))
			if p.closing {
				select {
				case <-p.closeAttempt.done:
					// A failed disposition retains its slot and can be retried.
					if p.closeAttempt.err == nil {
						continue
					}
				default:
					waiting = append(waiting, p.closeAttempt)
					continue
				}
			}
			p.closing = true
			p.closeAttempt = &scopeCloseAttempt{done: make(chan struct{})}
			parts = append(parts, p)
			if ref == "" {
				c.generations[p.ref]++
			}
		}
	}
	c.mu.Unlock()
	cleanupErr := errors.Join(tombstoneErr, c.disposePartitions(parts))
	for _, attempt := range waiting {
		<-attempt.done
		cleanupErr = errors.Join(cleanupErr, attempt.err)
	}
	return cleanupErr
}

func (c *Client) pruneClosedScopesLocked(now time.Time) {
	for key, record := range c.closedScopes {
		if !record.expires.After(now) {
			delete(c.closedScopes, key)
		}
	}
}

func (c *Client) rememberClosedScopeLocked(ref, scope string, now time.Time) error {
	c.pruneClosedScopesLocked(now)
	key := ref + "\x00" + scope
	if c.closedScopes == nil {
		c.closedScopes = make(map[string]closedScopeRecord)
	}
	if _, exists := c.closedScopes[key]; !exists && len(c.closedScopes) >= maxClosedScopes {
		// Keep a bounded fail-closed quarantine instead of losing an old close.
		c.scopedBlockedUntil = now.Add(closedScopeTTL)
		return errors.New("mcp closed-scope capacity exhausted; scoped admission quarantined for five minutes")
	}
	record := c.closedScopes[key]
	record.expires = now.Add(closedScopeTTL)
	c.closedScopes[key] = record
	return nil
}

func (c *Client) scopeAdmissionErrorLocked(ref, scope string, now time.Time) error {
	c.pruneClosedScopesLocked(now)
	if c.scopedBlockedUntil.After(now) {
		return errors.New("mcp scoped admission is quarantined after close capacity exhaustion")
	}
	if c.closedScopes[ref+"\x00"+scope].expires.After(now) {
		return errors.New("mcp connection scope is closed")
	}
	return nil
}

func (c *Client) CloseScope(ref, scope string) error {
	if strings.TrimSpace(ref) == "" {
		return errors.New("mcp connection close requires child ref")
	}
	if err := ValidateScope(scope); err != nil {
		return err
	}
	scope = strings.ToLower(scope)
	err := c.closePartitions(ref, scope)
	if err == nil {
		c.mu.Lock()
		key := ref + "\x00" + scope
		record := c.closedScopes[key]
		record.confirmed = true
		c.closedScopes[key] = record
		c.mu.Unlock()
	}
	return err
}

// Scoped children inherit only the declared runtime baseline, never the
// executor's unrelated provider, Hub, OAuth, or transport credentials.
func scopedRuntimeEnvironmentKey(key string) bool {
	switch key {
	case "PATH", "HOME", "USER", "LOGNAME", "SHELL", "LANG", "LANGUAGE", "TZ", "TMPDIR", "TMP", "TEMP", "SSL_CERT_FILE", "SSL_CERT_DIR":
		return true
	}
	return strings.HasPrefix(key, "LC_")
}

type closedScopeRecord struct {
	expires   time.Time
	confirmed bool
}

type expectedGenerationKey struct{}

// WithExpectedGeneration binds executor-side instance validation to pool
// admission. A lifecycle mutation between those operations fails closed.
func WithExpectedGeneration(ctx context.Context, generation uint64) context.Context {
	return context.WithValue(ctx, expectedGenerationKey{}, generation)
}

// CanRetryScopeClose admits an old connector epoch only for retained ownership
// or a separately confirmed successful disposition, never an admission fence.
func (c *Client) CanRetryScopeClose(ref, scope string) bool {
	if ValidateScope(scope) != nil {
		return false
	}
	scope = strings.ToLower(scope)
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, p := range c.scoped {
		if p.ref == ref && p.scope == scope {
			return true
		}
	}
	record := c.closedScopes[ref+"\x00"+scope]
	return record.confirmed && record.expires.After(time.Now())
}
