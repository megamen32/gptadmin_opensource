// Package bridge exposes the owner-scoped Streamable HTTP facade used by the
// external GPTAdmin child host. It owns only transport sessions and stdio
// process lifetime; Hub remains the authority for ACL, jobs and receipts.
package bridge

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/mcpclient"
	"github.com/megamen32/gptadmin/go-shellmcp/internal/supervisor"
)

const defaultIdleTTL = 5 * time.Hour
const maxBodyBytes = 8 << 20

type Child struct {
	Command string
	Args    []string
	Env     map[string]string
	Cwd     string
	User    string
}

type Config struct {
	Children  map[string]Child
	IdleTTL   time.Duration
	BridgeKey string
	Now       func() time.Time
}

type instance struct {
	key        string
	ref        string
	child      Child
	client     *mcpclient.Client
	generation uint64
	lastUsed   time.Time
	active     int
	closed     bool
}

type session struct {
	id         string
	key        string
	ref        string
	generation uint64
	owner      string
	lastUsed   time.Time
}

type Host struct {
	mu          sync.Mutex
	children    map[string]Child
	instances   map[string]*instance
	pending     map[string]*pendingStart
	generations map[string]uint64
	sessions    map[string]session
	idleTTL     time.Duration
	bridgeKey   string
	now         func() time.Time
}

type pendingStart struct {
	done       chan struct{}
	instance   *instance
	err        error
	generation uint64
}

func New(cfg Config) *Host {
	idle := cfg.IdleTTL
	if idle <= 0 {
		idle = defaultIdleTTL
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	children := make(map[string]Child, len(cfg.Children))
	for name, child := range cfg.Children {
		child.Args = append([]string(nil), child.Args...)
		child.Env = cloneStrings(child.Env)
		children[name] = child
	}
	return &Host{
		children: children, instances: map[string]*instance{}, pending: map[string]*pendingStart{},
		generations: map[string]uint64{}, sessions: map[string]session{}, idleTTL: idle,
		bridgeKey: cfg.BridgeKey, now: now,
	}
}

func cloneStrings(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func (h *Host) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/health" && r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
		return
	}
	if h.bridgeKey != "" && !h.validBridgeKey(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "bridge authentication required"})
		return
	}
	childName, ok := parseChildPath(r.URL.Path)
	if !ok || (r.Method != http.MethodPost && r.Method != http.MethodGet && r.Method != http.MethodDelete) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	owner, ok := ownerKey(r, childName)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "owner authentication required"})
		return
	}
	handleErr := h.handleMCP(w, r, childName, owner)
	if handleErr != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": handleErr.Error()})
	}
}

func (h *Host) validBridgeKey(r *http.Request) bool {
	return r.Header.Get("X-Gptadmin-Bridge-Key") == h.bridgeKey || r.Header.Get("X-API-Key") == h.bridgeKey
}

func ownerKey(r *http.Request, child string) (string, bool) {
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	if value == "" {
		return "", false
	}
	// Keep the owner partition stable without retaining the bearer itself in
	// the in-memory instance/session maps. Hub remains responsible for
	// authenticating this credential before the request reaches the bridge.
	digest := sha256.Sum256([]byte(value + "\x00" + child))
	return hex.EncodeToString(digest[:]), true
}

func parseChildPath(path string) (string, bool) {
	if !strings.HasPrefix(path, "/mcp/") {
		return "", false
	}
	name := strings.Trim(strings.TrimPrefix(path, "/mcp/"), "/")
	if name == "" || strings.ContainsAny(name, "/\\%?#") {
		return "", false
	}
	return name, true
}

func (h *Host) handleMCP(w http.ResponseWriter, r *http.Request, childName, owner string) error {
	child, ok := h.children[childName]
	if !ok || strings.TrimSpace(child.Command) == "" {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "unknown child"})
		return nil
	}
	key := owner + "\x00" + childName
	if r.Method == http.MethodGet {
		return h.handleStream(w, r, key, childName, owner)
	}
	if r.Method == http.MethodDelete {
		return h.handleDelete(w, r, key, childName, owner)
	}
	var body map[string]any
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	decoder.UseNumber()
	if err := decoder.Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON-RPC request"})
		return nil
	}
	method, _ := body["method"].(string)
	if method == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "MCP method is required"})
		return nil
	}
	instance, err := h.acquire(r.Context(), key, childName, child)
	if err != nil {
		return err
	}
	finish := h.begin(instance)
	defer finish()
	if method == "initialize" {
		result, callErr := instance.client.Request(r.Context(), h.agent(instance), method, body["params"])
		if callErr != nil {
			writeRPCError(w, body["id"], callErr)
			return nil
		}
		id, err := h.createSession(key, owner, instance)
		if err != nil {
			return err
		}
		writeRPCResult(w, body["id"], id, result)
		return nil
	}
	if sessionID := r.Header.Get("Mcp-Session-Id"); sessionID != "" {
		if !h.validSession(sessionID, key, owner, instance) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "invalid or expired MCP session"})
			return nil
		}
	} else {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Mcp-Session-Id is required"})
		return nil
	}
	if method == "notifications/initialized" {
		// getStdio performs the child handshake, including its one required
		// initialized notification. Consume the downstream handshake locally so
		// a restarted HTTP session cannot send a duplicate upstream frame.
		w.WriteHeader(http.StatusAccepted)
		return nil
	}
	if strings.HasPrefix(method, "notifications/") || body["id"] == nil {
		if err := instance.client.Notify(r.Context(), h.agent(instance), method, body["params"]); err != nil {
			return err
		}
		w.WriteHeader(http.StatusAccepted)
		return nil
	}
	result, callErr := instance.client.Request(r.Context(), h.agent(instance), method, body["params"])
	if callErr != nil {
		writeRPCError(w, body["id"], callErr)
		return nil
	}
	if sessionID := r.Header.Get("Mcp-Session-Id"); sessionID != "" {
		h.touchSession(sessionID)
	}
	writeRPCResult(w, body["id"], r.Header.Get("Mcp-Session-Id"), result)
	return nil
}

func (h *Host) handleStream(w http.ResponseWriter, r *http.Request, key, childName, owner string) error {
	instance, err := h.acquire(r.Context(), key, childName, h.children[childName])
	if err != nil {
		return err
	}
	if !h.validSession(r.Header.Get("Mcp-Session-Id"), key, owner, instance) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "invalid or expired MCP session"})
		return nil
	}
	finish := h.begin(instance)
	defer finish()
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	<-r.Context().Done()
	return nil
}

func (h *Host) handleDelete(w http.ResponseWriter, r *http.Request, key, childName, owner string) error {
	id := r.Header.Get("Mcp-Session-Id")
	h.mu.Lock()
	instance := h.instances[key]
	h.mu.Unlock()
	if instance == nil || !h.validSession(id, key, owner, instance) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "invalid or expired MCP session"})
		return nil
	}
	h.mu.Lock()
	delete(h.sessions, id)
	h.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (h *Host) acquire(ctx context.Context, key, childName string, child Child) (*instance, error) {
	h.mu.Lock()
	if current := h.instances[key]; current != nil && !current.closed {
		current.lastUsed = h.now()
		h.mu.Unlock()
		return current, nil
	}
	if wait := h.pending[key]; wait != nil {
		h.mu.Unlock()
		select {
		case <-wait.done:
			return wait.instance, wait.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	h.generations[key]++
	generation := h.generations[key]
	pending := &pendingStart{done: make(chan struct{}), generation: generation}
	h.pending[key] = pending
	h.mu.Unlock()
	client := mcpclient.New()
	instance := &instance{key: key, ref: fmt.Sprintf("bridge-%x", generation), child: child, client: client, generation: generation}
	instance.lastUsed = h.now()
	h.mu.Lock()
	delete(h.pending, key)
	pending.instance = instance
	h.instances[key] = instance
	close(pending.done)
	h.mu.Unlock()
	return instance, nil
}

func (h *Host) agent(instance *instance) supervisor.Agent {
	return supervisor.Agent{Ref: instance.ref, Command: instance.child.Command, Args: instance.child.Args, Env: instance.child.Env, Cwd: instance.child.Cwd, User: instance.child.User, Transport: "stdio", Enabled: true}
}

func (h *Host) begin(instance *instance) func() {
	h.mu.Lock()
	instance.active++
	instance.lastUsed = h.now()
	done := false
	h.mu.Unlock()
	return func() {
		h.mu.Lock()
		if !done {
			done = true
			instance.active--
			instance.lastUsed = h.now()
		}
		h.mu.Unlock()
	}
}

func (h *Host) createSession(key, owner string, instance *instance) (string, error) {
	var random [24]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(random[:])
	h.mu.Lock()
	h.sessions[id] = session{id: id, key: key, ref: instance.ref, generation: instance.generation, owner: owner, lastUsed: h.now()}
	h.mu.Unlock()
	return id, nil
}

func (h *Host) validSession(id, key, owner string, instance *instance) bool {
	if id == "" {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	sess, ok := h.sessions[id]
	if !ok || sess.key != key || sess.owner != owner || sess.ref != instance.ref || sess.generation != instance.generation || instance.closed {
		return false
	}
	sess.lastUsed = h.now()
	h.sessions[id] = sess
	return true
}

func (h *Host) touchSession(id string) {
	h.mu.Lock()
	if sess, ok := h.sessions[id]; ok {
		sess.lastUsed = h.now()
		h.sessions[id] = sess
	}
	h.mu.Unlock()
}

func (h *Host) EvictIdle(ctx context.Context) error {
	_ = ctx
	h.mu.Lock()
	cutoff := h.now().Add(-h.idleTTL)
	var evicted []*instance
	for key, current := range h.instances {
		if current.active == 0 && current.lastUsed.Before(cutoff) {
			current.closed = true
			delete(h.instances, key)
			for id, sess := range h.sessions {
				if sess.key == key && sess.ref == current.ref && sess.generation == current.generation {
					delete(h.sessions, id)
				}
			}
			evicted = append(evicted, current)
		}
	}
	h.mu.Unlock()
	var closeErr error
	for _, current := range evicted {
		if err := current.client.Close(current.ref); err != nil && !errors.Is(err, context.Canceled) {
			closeErr = errors.Join(closeErr, err)
		}
	}
	return closeErr
}

// Close terminates every managed child and releases all transport sessions.
// It is used during planned host shutdown; a new request cannot create an
// instance after this method returns because the Host is no longer served.
func (h *Host) Close(ctx context.Context) error {
	h.mu.Lock()
	instances := make([]*instance, 0, len(h.instances))
	for key, current := range h.instances {
		current.closed = true
		instances = append(instances, current)
		delete(h.instances, key)
	}
	h.sessions = map[string]session{}
	h.mu.Unlock()

	var closeErr error
	for _, current := range instances {
		done := make(chan error, 1)
		go func(current *instance) { done <- current.client.Close(current.ref) }(current)
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				closeErr = errors.Join(closeErr, err)
			}
		case <-ctx.Done():
			return errors.Join(closeErr, ctx.Err())
		}
	}
	return closeErr
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeRPCResult(w http.ResponseWriter, id any, sessionID string, result map[string]any) {
	if sessionID != "" {
		w.Header().Set("Mcp-Session-Id", sessionID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func writeRPCError(w http.ResponseWriter, id any, err error) {
	writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32603, "message": err.Error()}})
}
