package hub

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// The node↔writer live connection. The member dials OUT to the writer and
// keeps the session open, so neither side needs an inbound route to the
// member's network (NAT, LAN-only origins). The writer prefers this path
// for peer calls; the advertised-origin HTTP dial remains the fallback and
// the receipt contract is identical: the original bearer travels to the
// owner and the owner reauthorizes through its normal admission surface.

const (
	nodeConnPingInterval   = 20 * time.Second
	nodeConnIdleTimeout    = 70 * time.Second
	nodeConnWriteTimeout   = 15 * time.Second
	nodeConnDialBackoffMin = 5 * time.Second
	nodeConnDialBackoffMax = 60 * time.Second
	nodeConnBodyLimit      = 64 << 20
	nodeConnConnectPath    = "/admin/api/nodes/connect"
)

// nodeConnFrame is the single JSON frame format on the wire.
type nodeConnFrame struct {
	Op       string            `json:"op"`
	ID       string            `json:"id,omitempty"`
	Endpoint string            `json:"endpoint,omitempty"`
	Method   string            `json:"method,omitempty"`
	Auth     string            `json:"auth,omitempty"`
	Body     json.RawMessage   `json:"body,omitempty"`
	Status   int               `json:"status,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
}

type nodeConnResult struct {
	status  int
	body    []byte
	headers http.Header
}

// nodeConn is one live member connection on the writer.
type nodeConn struct {
	name        string
	managedHTTP bool
	ws          *websocket.Conn
	writeMu     sync.Mutex
	pendingMu   sync.Mutex
	pending     map[string]chan nodeConnResult
	lastActive  atomic.Int64
	done        chan struct{}
	closeOnce   sync.Once
}

func (c *nodeConn) write(frame nodeConnFrame) error {
	data, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), nodeConnWriteTimeout)
	defer cancel()
	return c.ws.Write(ctx, websocket.MessageText, data)
}

func (c *nodeConn) close() {
	c.closeOnce.Do(func() {
		close(c.done)
		_ = c.ws.Close(websocket.StatusNormalClosure, "")
	})
}

func (c *nodeConn) resolve(id string, result nodeConnResult) {
	c.pendingMu.Lock()
	ch := c.pending[id]
	delete(c.pending, id)
	c.pendingMu.Unlock()
	if ch != nil {
		ch <- result
	}
}

func (c *nodeConn) failAllPending(detail string) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	for id, ch := range c.pending {
		delete(c.pending, id)
		ch <- nodeConnResult{status: http.StatusBadGateway,
			body: json.RawMessage(`{"detail":"node connection closed before a receipt arrived; execution outcome may be unknown"}`)}
	}
}

type nodeConnRegistry struct {
	mu    sync.Mutex
	conns map[string]*nodeConn
}

func newNodeConnRegistry() *nodeConnRegistry {
	return &nodeConnRegistry{conns: map[string]*nodeConn{}}
}

func (r *nodeConnRegistry) put(conn *nodeConn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if previous := r.conns[conn.name]; previous != nil && previous != conn {
		previous.close()
	}
	r.conns[conn.name] = conn
}

func (r *nodeConnRegistry) get(name string) *nodeConn {
	r.mu.Lock()
	conn := r.conns[name]
	r.mu.Unlock()
	if conn == nil {
		return nil
	}
	select {
	case <-conn.done:
		return nil
	default:
		return conn
	}
}

// names lists the member names with a live connection, sorted; the mesh
// summary surfaces them so the operator sees which routes ride the live path.
func (r *nodeConnRegistry) names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	names := make([]string, 0, len(r.conns))
	for name, conn := range r.conns {
		select {
		case <-conn.done:
			continue
		default:
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func (r *nodeConnRegistry) remove(conn *nodeConn) {
	r.mu.Lock()
	if r.conns[conn.name] == conn {
		delete(r.conns, conn.name)
	}
	r.mu.Unlock()
	conn.close()
}

// closeAll tears down every live member connection; used on server shutdown
// so the read loops do not outlive the process's HTTP server.
func (r *nodeConnRegistry) closeAll() {
	r.mu.Lock()
	conns := r.conns
	r.conns = map[string]*nodeConn{}
	r.mu.Unlock()
	for _, conn := range conns {
		conn.close()
	}
}

// nodeConnectHTTP upgrades an authenticated member to the persistent
// outbound connection on the writer side.
func (s *Server) nodeConnectHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "GET required"})
		return
	}
	if s.writerRedirect(w, r) {
		return
	}
	member := s.nodeMemberAuth(r)
	if member == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"detail": "node credential required"})
		return
	}
	if member.State == nodeStateDisabled {
		writeJSON(w, http.StatusForbidden, map[string]any{"detail": "node is disabled"})
		return
	}
	// The endpoint authenticates by bearer, not by browser origin.
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"*"}})
	if err != nil {
		return
	}
	ws.SetReadLimit(nodeConnBodyLimit + (1 << 20))
	conn := &nodeConn{name: member.Name, managedHTTP: r.Header.Get(managedNodeProtocolHeader) == managedNodeProtocol, ws: ws, pending: map[string]chan nodeConnResult{}, done: make(chan struct{})}
	s.nodeConns.put(conn)
	log.Printf("node connection established name=%s node_id=%s", member.Name, member.NodeID)
	s.nodeConnReadLoop(conn, member.Name)
}

// nodeConnReadLoop serves result frames and keepalives for one writer-side
// connection and fails every pending caller when the connection dies.
func (s *Server) nodeConnReadLoop(conn *nodeConn, name string) {
	defer s.nodeConns.remove(conn)
	defer conn.failAllPending("node connection closed before a receipt arrived; execution outcome may be unknown")
	conn.lastActive.Store(time.Now().Unix())
	stopKeepalive := make(chan struct{})
	defer close(stopKeepalive)
	go func() {
		ticker := time.NewTicker(nodeConnPingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stopKeepalive:
				return
			case <-conn.done:
				return
			case <-ticker.C:
				if time.Since(time.Unix(conn.lastActive.Load(), 0)) > nodeConnIdleTimeout {
					log.Printf("node connection idle timeout name=%s", name)
					conn.close()
					return
				}
				if err := conn.write(nodeConnFrame{Op: "ping"}); err != nil {
					conn.close()
					return
				}
			}
		}
	}()
	for {
		_, data, err := conn.ws.Read(context.Background())
		if err != nil {
			return
		}
		conn.lastActive.Store(time.Now().Unix())
		var frame nodeConnFrame
		if json.Unmarshal(data, &frame) != nil {
			continue
		}
		switch frame.Op {
		case "result":
			headers := http.Header{}
			for name, value := range frame.Headers {
				headers.Set(name, value)
			}
			conn.resolve(frame.ID, nodeConnResult{status: frame.Status, body: frame.Body, headers: headers})
		case "pong":
		default:
		}
	}
}

// forwardOverNodeConn delivers the call over the member's live connection.
// The frame write is the point of no return: once it succeeds the call may
// already have executed at the owner, so a missing receipt is reported as
// an unknown outcome and is never retried over the HTTP origin.
func (s *Server) forwardOverNodeConn(w http.ResponseWriter, r *http.Request, target, origin string, body []byte, endpoint string, readingJob bool) bool {
	conn := s.nodeConns.get(target)
	if conn == nil {
		return false
	}
	seed := make([]byte, 12)
	if _, err := rand.Read(seed); err != nil {
		return false
	}
	frame := nodeConnFrame{Op: "call", ID: hex.EncodeToString(seed),
		Endpoint: endpoint, Auth: r.Header.Get("Authorization"), Body: body}
	if endpoint == "/mcp" {
		frame.Headers = map[string]string{}
		for _, name := range []string{"Accept", "MCP-Protocol-Version", "Mcp-Method", "Mcp-Name"} {
			if value := r.Header.Get(name); value != "" {
				frame.Headers[name] = value
			}
		}
	}
	wait := make(chan nodeConnResult, 1)
	conn.pendingMu.Lock()
	conn.pending[frame.ID] = wait
	conn.pendingMu.Unlock()
	if err := conn.write(frame); err != nil {
		conn.pendingMu.Lock()
		delete(conn.pending, frame.ID)
		conn.pendingMu.Unlock()
		return false
	}
	timeout := s.cfg.DefaultTimeout
	if timeout < 20*time.Second {
		timeout = 20 * time.Second
	}
	timeout += 10 * time.Second
	select {
	case result := <-wait:
		for _, name := range []string{"WWW-Authenticate", "MCP-Protocol-Version"} {
			if value := result.headers.Get(name); value != "" {
				w.Header().Set(name, value)
			}
		}
		receipt := finalizeNodePeerReceipt(endpoint, target, origin, result.body)
		if receipt == nil {
			s.writeNodePeerFailure(w, target, origin, endpoint, body, http.StatusBadGateway,
				"node connection returned an invalid receipt; execution outcome may be unknown", !readingJob)
			return true
		}
		// writeJSON marshals []byte as base64; deliver the receipt as the
		// decoded JSON object the HTTP dial path produces.
		var object map[string]json.RawMessage
		if err := json.Unmarshal(receipt, &object); err != nil {
			s.writeNodePeerFailure(w, target, origin, endpoint, body, http.StatusBadGateway,
				"node connection returned an invalid receipt; execution outcome may be unknown", !readingJob)
			return true
		}
		writeJSON(w, result.status, object)
	case <-time.After(timeout):
		s.writeNodePeerFailure(w, target, origin, endpoint, body, http.StatusGatewayTimeout,
			"node connection did not return a receipt before the deadline; execution outcome may be unknown", !readingJob)
	case <-r.Context().Done():
	}
	return true
}

// writeNodePeerFailure mirrors the inline fail closure of
// forwardNodePeerRequest for the live-connection path.
func (s *Server) writeNodePeerFailure(w http.ResponseWriter, target, origin, endpoint string, body []byte, status int, detail string, unknown bool) {
	payload := map[string]any{"detail": detail, "owner_target": target, "owner_endpoint": origin}
	if unknown {
		payload["outcome"] = "unknown"
		payload["retry_policy"] = "inspect owner or retry the same idempotency_key at owner; never replay elsewhere"
	}
	if endpoint == "/mcp" {
		var envelope map[string]json.RawMessage
		_ = json.Unmarshal(body, &envelope)
		writeJSON(w, status, map[string]any{"jsonrpc": "2.0", "id": envelope["id"],
			"error": map[string]any{"code": -32000, "message": detail, "data": payload}})
		return
	}
	writeJSON(w, status, payload)
}

// startNodeConnLoop launches the member's outbound dial loop. Writers and
// standalone nodes have nothing to dial.
func (s *Server) startNodeConnLoop() {
	if s.nodeIdentity == nil || s.membership != nil {
		return
	}
	s.membershipDone.Add(1)
	go func() {
		defer s.membershipDone.Done()
		s.nodeConnDialLoop()
	}()
}

func (s *Server) nodeConnDialLoop() {
	backoff := nodeConnDialBackoffMin
	for {
		select {
		case <-s.membershipStop:
			return
		case <-time.After(backoff):
		}
		writer := strings.TrimSuffix(s.memberWriterURL(), "/")
		key := s.memberNodeKey()
		if writer == "" || key == "" {
			backoff = nodeConnDialBackoffMin
			continue
		}
		dialURL := nodeConnDialURL(writer)
		if dialURL == "" {
			backoff = nodeConnDialBackoffMin
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			select {
			case <-s.membershipStop:
				cancel()
			case <-ctx.Done():
			}
		}()
		// The heartbeat/snapshot client already pins the writer's physical
		// address for split-horizon DNS; the persistent connection must use
		// the same override or it would loop back to this node's own proxy.
		ws, _, err := websocket.Dial(ctx, dialURL, &websocket.DialOptions{
			HTTPHeader: http.Header{"Authorization": {"Bearer " + key}, managedNodeProtocolHeader: {managedNodeProtocol}},
			HTTPClient: newNodeConnDialClient(s.cfg.NodeWriterConnectTo),
		})
		if err != nil {
			cancel()
			log.Printf("node connection dial failed writer=%s err=%v", writer, err)
			backoff *= 2
			if backoff > nodeConnDialBackoffMax {
				backoff = nodeConnDialBackoffMax
			}
			continue
		}
		ws.SetReadLimit(nodeConnBodyLimit + (1 << 20))
		backoff = nodeConnDialBackoffMin
		log.Printf("node connection established writer=%s", writer)
		s.nodeConnServeLoop(ctx, ws)
		cancel()
		log.Printf("node connection closed writer=%s", writer)
	}
}

func nodeConnDialURL(writer string) string {
	switch {
	case strings.HasPrefix(writer, "https://"):
		return "wss://" + strings.TrimPrefix(writer, "https://") + nodeConnConnectPath
	case strings.HasPrefix(writer, "http://"):
		return "ws://" + strings.TrimPrefix(writer, "http://") + nodeConnConnectPath
	default:
		return ""
	}
}

// newNodeConnDialClient is the WebSocket handshake counterpart of the
// membership client: same pinned physical dialing, handshake-sized timeout.
func newNodeConnDialClient(connectTo string) *http.Client {
	if transport := newNodeDialTransport(connectTo); transport != nil {
		return &http.Client{Transport: transport, Timeout: 30 * time.Second}
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// nodeConnServeLoop serves forwarded calls on the member side. Reads stay
// on this goroutine; each call executes under a small concurrency limit so
// a slow owner cannot starve the keepalive path.
func (s *Server) nodeConnServeLoop(ctx context.Context, ws *websocket.Conn) {
	var writeMu sync.Mutex
	write := func(frame nodeConnFrame) error {
		data, err := json.Marshal(frame)
		if err != nil {
			return err
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		wctx, cancel := context.WithTimeout(context.Background(), nodeConnWriteTimeout)
		defer cancel()
		return ws.Write(wctx, websocket.MessageText, data)
	}
	limiter := make(chan struct{}, 8)
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		var frame nodeConnFrame
		if json.Unmarshal(data, &frame) != nil {
			continue
		}
		switch frame.Op {
		case "ping":
			if err := write(nodeConnFrame{Op: "pong"}); err != nil {
				return
			}
		case "call":
			if frame.ID == "" {
				continue
			}
			select {
			case limiter <- struct{}{}:
			case <-ctx.Done():
				return
			}
			wg.Add(1)
			go func(frame nodeConnFrame) {
				defer wg.Done()
				defer func() { <-limiter }()
				status, headers, body := s.nodeConnLocalExecute(frame)
				result := nodeConnFrame{Op: "result", ID: frame.ID, Status: status, Body: body}
				if len(headers) > 0 {
					result.Headers = map[string]string{}
					for _, name := range []string{"WWW-Authenticate", "MCP-Protocol-Version", "Mcp-Session-Id"} {
						if value := headers.Get(name); value != "" {
							result.Headers[name] = value
						}
					}
				}
				_ = write(result)
			}(frame)
		}
	}
}

// nodeConnLocalExecute replays the forwarded call against the node's own
// listener so owner-side admission, approval and queueing is the exact
// production surface. The original bearer is preserved; the owner
// reauthorizes exactly like on the advertised-origin path.
func (s *Server) nodeConnLocalExecute(frame nodeConnFrame) (int, http.Header, []byte) {
	host := s.cfg.Addr
	if host == "" {
		return http.StatusBadGateway, nil, []byte(`{"detail":"node listener address unknown"}`)
	}
	if h, port, err := net.SplitHostPort(host); err == nil && (h == "" || h == "0.0.0.0" || h == "::") {
		host = net.JoinHostPort("127.0.0.1", port)
	}
	method := http.MethodPost
	managed := managedConnectionPath(frame.Endpoint)
	if managed {
		if frame.Method != http.MethodPost && frame.Method != http.MethodDelete {
			return http.StatusBadRequest, nil, []byte(`{"detail":"invalid managed peer method"}`)
		}
		method = frame.Method
	} else if frame.Method != "" && frame.Method != http.MethodPost {
		return http.StatusBadRequest, nil, []byte(`{"detail":"unsupported peer method"}`)
	}
	request, err := http.NewRequestWithContext(context.Background(), method,
		"http://"+host+frame.Endpoint, bytes.NewReader(frame.Body))
	if err != nil {
		return http.StatusBadGateway, nil, []byte(`{"detail":"node loopback request construction failed"}`)
	}
	if frame.Auth != "" {
		request.Header.Set("Authorization", frame.Auth)
	}
	request.Header.Set("Content-Type", "application/json")
	if frame.Endpoint == "/mcp" {
		// Match the HTTP peer route. Never propagate a browser cookie, client
		// session ID or arbitrary frame header into another node's session.
		for _, name := range []string{"Accept", "MCP-Protocol-Version", "Mcp-Method", "Mcp-Name"} {
			if value := frame.Headers[name]; value != "" {
				request.Header.Set(name, value)
			}
		}
	}
	if managed {
		request.Header.Set(nodePeerHopHeader, "1")
		for _, name := range []string{"Accept", "MCP-Protocol-Version", "Mcp-Method", "Mcp-Name", "Mcp-Session-Id"} {
			if value := frame.Headers[name]; value != "" {
				request.Header.Set(name, value)
			}
		}
		request.GetBody = nil
	}
	timeout := s.cfg.DefaultTimeout
	if timeout < 20*time.Second {
		timeout = 20 * time.Second
	}
	timeout += 10 * time.Second
	client := &http.Client{Timeout: timeout}
	if managed {
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	response, err := client.Do(request)
	if err != nil {
		return http.StatusBadGateway, nil, []byte(`{"detail":"node loopback delivery failed; execution outcome may be unknown"}`)
	}
	defer response.Body.Close()
	headers := http.Header{}
	for _, name := range []string{"WWW-Authenticate", "MCP-Protocol-Version", "Mcp-Session-Id"} {
		if value := response.Header.Get(name); value != "" {
			headers.Set(name, value)
		}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, nodeConnBodyLimit+1))
	if err != nil || len(data) > nodeConnBodyLimit {
		return http.StatusBadGateway, headers, []byte(`{"detail":"node loopback receipt could not be read; execution outcome may be unknown"}`)
	}
	return response.StatusCode, headers, data
}
