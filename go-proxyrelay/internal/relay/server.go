package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/megamen32/gptadmin/go-proxyrelay/internal/ticket"
)

const (
	maxConfiguredHandshakeBytes = 64 * 1024
	maxConfiguredTimeout        = 24 * time.Hour
)

var ErrInvalidConfig = errors.New("relay config is invalid")

// Config contains the finite process-local relay settings.
type Config struct {
	Verifier          *ticket.Verifier
	HandshakeTimeout  time.Duration
	PairTimeout       time.Duration
	WriteTimeout      time.Duration
	MaxHandshakeBytes int64
	Audit             AuditFunc
}

// Handshake is the only accepted first WebSocket message.
type Handshake struct {
	ProtocolVersion int    `json:"protocol_version"`
	Ticket          string `json:"ticket"`
}

// Ready confirms that both authorized stream sides are paired.
type Ready struct {
	State           string `json:"state"`
	ProtocolVersion int    `json:"protocol_version"`
	StreamID        string `json:"stream_id"`
}

// Stats reports bounded relay queue observations.
type Stats struct {
	MaxObservedQueueDepth int   `json:"max_observed_queue_depth"`
	ActiveSessions        int   `json:"active_sessions"`
	AuthenticatedPeers    int64 `json:"authenticated_peers_total"`
	PairsStarted          int64 `json:"pairs_started_total"`
	Resets                int64 `json:"resets_total"`
}

// Server owns the isolated in-memory stream pairing registry.
type Server struct {
	config         Config
	handler        http.Handler
	ctx            context.Context
	cancel         context.CancelFunc
	closeOnce      sync.Once
	mu             sync.Mutex
	closed         bool
	sessions       map[string]*relaySession
	agentStreams   map[string]int
	profileStreams map[string]int
	maxQueue       atomic.Int64
	activeSessions atomic.Int64
	authenticated  atomic.Int64
	pairsStarted   atomic.Int64
	resets         atomic.Int64
}

type relaySession struct {
	claims   ticket.Claims
	client   *relayPeer
	agent    *relayPeer
	pair     *streamPair
	paired   chan struct{}
	done     chan struct{}
	pairOnce sync.Once
	doneOnce sync.Once
}

type relayPeer struct {
	role  string
	conn  *websocket.Conn
	frame *websocketFrameConn
}

type websocketFrameConn struct {
	conn          *websocket.Conn
	maxFrameBytes int64
	writeTimeout  time.Duration
	writeMu       sync.Mutex
	closeOnce     sync.Once
}

// New validates finite settings and creates an isolated relay server.
func New(config Config) (*Server, error) {
	if config.Verifier == nil || config.HandshakeTimeout <= 0 || config.HandshakeTimeout > maxConfiguredTimeout ||
		config.PairTimeout <= 0 || config.PairTimeout > maxConfiguredTimeout ||
		config.WriteTimeout <= 0 || config.WriteTimeout > maxConfiguredTimeout ||
		config.MaxHandshakeBytes <= 0 || config.MaxHandshakeBytes > maxConfiguredHandshakeBytes {
		return nil, ErrInvalidConfig
	}
	ctx, cancel := context.WithCancel(context.Background())
	server := &Server{
		config:         config,
		ctx:            ctx,
		cancel:         cancel,
		sessions:       make(map[string]*relaySession),
		agentStreams:   make(map[string]int),
		profileStreams: make(map[string]int),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/stream/client", server.handleStream(ticket.RoleClient))
	mux.HandleFunc("/v1/stream/agent", server.handleStream(ticket.RoleAgent))
	mux.HandleFunc("/v1/control/revoke", server.handleRevoke)
	mux.HandleFunc("/metrics", server.metrics)
	server.handler = mux
	return server, nil
}

// Handler returns the relay-only HTTP surface.
func (s *Server) Handler() http.Handler {
	return s.handler
}

// Close resets active streams and releases all process-local state.
func (s *Server) Close() {
	s.closeOnce.Do(func() {
		s.cancel()
		s.mu.Lock()
		s.closed = true
		sessions := make([]*relaySession, 0, len(s.sessions))
		for _, session := range s.sessions {
			sessions = append(sessions, session)
		}
		s.mu.Unlock()
		for _, session := range sessions {
			s.abortSession(session, "server_closed")
		}
	})
}

// Stats returns queue high-water marks without payload data.
func (s *Server) Stats() Stats {
	maximum := s.maxQueue.Load()
	s.mu.Lock()
	for _, session := range s.sessions {
		if session.pair != nil && int64(session.pair.maxObservedQueueDepth()) > maximum {
			maximum = int64(session.pair.maxObservedQueueDepth())
		}
	}
	s.mu.Unlock()
	return Stats{
		MaxObservedQueueDepth: int(maximum),
		ActiveSessions:        int(s.activeSessions.Load()),
		AuthenticatedPeers:    s.authenticated.Load(),
		PairsStarted:          s.pairsStarted.Load(),
		Resets:                s.resets.Load(),
	}
}

func (s *Server) metrics(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(s.Stats())
}

func (s *Server) handleStream(role string) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		connection, err := websocket.Accept(writer, request, nil)
		if err != nil {
			return
		}
		connection.SetReadLimit(s.config.MaxHandshakeBytes)
		peer, claims, err := s.authenticate(connection, role)
		if err != nil {
			connection.CloseNow()
			return
		}
		s.authenticated.Add(1)
		s.emit(claims, "peer_authenticated", "", role)

		session, startsPair, rejected := s.join(peer, claims)
		if rejected {
			connection.CloseNow()
			return
		}
		if startsPair {
			s.startPair(session)
			<-session.done
			return
		}

		timer := time.NewTimer(s.config.PairTimeout)
		defer timer.Stop()
		select {
		case <-session.paired:
			<-session.done
		case <-session.done:
		case <-timer.C:
			s.expireWaiting(session)
		case <-s.ctx.Done():
			s.abortSession(session, "server_closed")
		}
	}
}

func (s *Server) authenticate(connection *websocket.Conn, role string) (*relayPeer, ticket.Claims, error) {
	ctx, cancel := context.WithTimeout(s.ctx, s.config.HandshakeTimeout)
	defer cancel()
	messageType, raw, err := connection.Read(ctx)
	if err != nil || messageType != websocket.MessageText || int64(len(raw)) > s.config.MaxHandshakeBytes {
		return nil, ticket.Claims{}, ErrInvalidFrame
	}
	var handshake Handshake
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&handshake); err != nil || decoderHasTrailingValue(decoder) ||
		handshake.ProtocolVersion != ProtocolVersion || strings.TrimSpace(handshake.Ticket) == "" {
		return nil, ticket.Claims{}, ErrInvalidFrame
	}
	claims, err := s.config.Verifier.VerifyAndConsumeStream(ctx, handshake.Ticket, role)
	if err != nil || claims.ProtocolVersion != handshake.ProtocolVersion {
		return nil, ticket.Claims{}, fmt.Errorf("verify relay ticket: %w", err)
	}
	frameConnection := &websocketFrameConn{
		conn:          connection,
		maxFrameBytes: claims.Limits.MaxFrameBytes,
		writeTimeout:  s.config.WriteTimeout,
	}
	// The handshake uses the process-wide limit; after authentication narrow
	// the transport reader to the signed per-stream frame budget.
	connection.SetReadLimit(claims.Limits.MaxFrameBytes + 1)
	return &relayPeer{role: role, conn: connection, frame: frameConnection}, claims, nil
}

func (s *Server) join(peer *relayPeer, claims ticket.Claims) (*relaySession, bool, bool) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, false, true
	}
	if existing := s.sessions[claims.StreamID]; existing != nil {
		if !pairingClaimsMatch(existing.claims, claims) {
			s.removeSessionLocked(existing)
			s.mu.Unlock()
			s.resetAndClose(existing, "claim_mismatch")
			return nil, false, true
		}
		if (peer.role == ticket.RoleClient && existing.client != nil) || (peer.role == ticket.RoleAgent && existing.agent != nil) {
			s.mu.Unlock()
			return nil, false, true
		}
		if peer.role == ticket.RoleClient {
			existing.client = peer
		} else {
			existing.agent = peer
		}
		startsPair := existing.client != nil && existing.agent != nil
		s.mu.Unlock()
		return existing, startsPair, false
	}
	if s.agentStreams[claims.AgentID] >= claims.Limits.MaxStreamsPerAgent ||
		s.profileStreams[claims.ProfileID] >= claims.Limits.MaxStreamsPerProfile {
		s.mu.Unlock()
		return nil, false, true
	}
	session := &relaySession{
		claims: claims,
		paired: make(chan struct{}),
		done:   make(chan struct{}),
	}
	if peer.role == ticket.RoleClient {
		session.client = peer
	} else {
		session.agent = peer
	}
	s.sessions[claims.StreamID] = session
	s.activeSessions.Add(1)
	s.agentStreams[claims.AgentID]++
	s.profileStreams[claims.ProfileID]++
	s.mu.Unlock()
	return session, false, false
}

func (s *Server) startPair(session *relaySession) {
	s.pairsStarted.Add(1)
	var pair *streamPair
	pair = newStreamPair(session.claims, session.client.frame, session.agent.frame, s.config.Audit, func() {
		s.observePair(pair)
		s.finishSession(session)
	})
	pair.resetTimeout = s.config.WriteTimeout

	ready := Ready{State: "open", ProtocolVersion: ProtocolVersion, StreamID: session.claims.StreamID}
	if err := s.writeJSON(session.client.conn, ready); err != nil {
		s.abortSession(session, "ready_write_failed")
		return
	}
	if err := s.writeJSON(session.agent.conn, ready); err != nil {
		s.abortSession(session, "ready_write_failed")
		return
	}

	s.mu.Lock()
	session.pair = pair
	s.mu.Unlock()
	session.pairOnce.Do(func() { close(session.paired) })
	pair.run(s.ctx)
}

func (s *Server) writeJSON(connection *websocket.Conn, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(s.ctx, s.config.WriteTimeout)
	defer cancel()
	return connection.Write(ctx, websocket.MessageText, raw)
}

func (s *Server) expireWaiting(session *relaySession) {
	s.mu.Lock()
	if s.sessions[session.claims.StreamID] != session || session.pair != nil {
		s.mu.Unlock()
		return
	}
	s.removeSessionLocked(session)
	s.mu.Unlock()
	s.resetAndClose(session, "pair_timeout")
}

func (s *Server) abortSession(session *relaySession, reason string) {
	s.mu.Lock()
	if session.pair != nil {
		pair := session.pair
		s.mu.Unlock()
		pair.requestReset(reason)
		return
	}
	if s.sessions[session.claims.StreamID] == session {
		s.removeSessionLocked(session)
	}
	s.mu.Unlock()
	s.resetAndClose(session, reason)
}

func (s *Server) resetAndClose(session *relaySession, reason string) {
	frame := Frame{Type: FrameReset}
	for _, peer := range []*relayPeer{session.client, session.agent} {
		if peer == nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), s.config.WriteTimeout)
		_ = peer.frame.Write(ctx, frame)
		cancel()
		peer.frame.Close()
	}
	s.emit(session.claims, "pair_reset", reason, "")
	session.doneOnce.Do(func() { close(session.done) })
}

func (s *Server) finishSession(session *relaySession) {
	s.mu.Lock()
	if s.sessions[session.claims.StreamID] == session {
		s.removeSessionLocked(session)
	}
	s.mu.Unlock()
	session.doneOnce.Do(func() { close(session.done) })
}

func (s *Server) removeSessionLocked(session *relaySession) {
	if s.sessions[session.claims.StreamID] != session {
		return
	}
	delete(s.sessions, session.claims.StreamID)
	s.activeSessions.Add(-1)
	s.agentStreams[session.claims.AgentID]--
	if s.agentStreams[session.claims.AgentID] == 0 {
		delete(s.agentStreams, session.claims.AgentID)
	}
	s.profileStreams[session.claims.ProfileID]--
	if s.profileStreams[session.claims.ProfileID] == 0 {
		delete(s.profileStreams, session.claims.ProfileID)
	}
}

func (s *Server) handleRevoke(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(request.Body, s.config.MaxHandshakeBytes+1))
	if err != nil || int64(len(raw)) > s.config.MaxHandshakeBytes {
		http.Error(writer, "invalid request", http.StatusBadRequest)
		return
	}
	var body struct {
		Ticket string `json:"ticket"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || decoderHasTrailingValue(decoder) || strings.TrimSpace(body.Ticket) == "" {
		http.Error(writer, "invalid request", http.StatusBadRequest)
		return
	}
	revocation, err := s.config.Verifier.VerifyAndConsumeRevocation(request.Context(), body.Ticket)
	if err != nil {
		http.Error(writer, "invalid request", http.StatusForbidden)
		return
	}
	s.mu.Lock()
	sessions := make([]*relaySession, 0)
	for _, session := range s.sessions {
		if session.claims.CapabilityID == revocation.CapabilityID {
			sessions = append(sessions, session)
		}
	}
	s.mu.Unlock()
	for _, session := range sessions {
		s.abortSession(session, "capability_revoked")
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) observePair(pair *streamPair) {
	depth := int64(pair.maxObservedQueueDepth())
	for {
		current := s.maxQueue.Load()
		if depth <= current || s.maxQueue.CompareAndSwap(current, depth) {
			return
		}
	}
}

func (s *Server) emit(claims ticket.Claims, event, reason, role string) {
	if event == "pair_reset" {
		s.resets.Add(1)
	}
	if s.config.Audit == nil {
		return
	}
	s.config.Audit(AuditEvent{
		Time:         time.Now().UTC(),
		Event:        event,
		CapabilityID: claims.CapabilityID,
		StreamID:     claims.StreamID,
		ProfileID:    claims.ProfileID,
		AgentID:      claims.AgentID,
		Role:         role,
		Reason:       reason,
	})
}

func pairingClaimsMatch(left, right ticket.Claims) bool {
	return left.ProtocolVersion == right.ProtocolVersion && left.CapabilityID == right.CapabilityID &&
		left.StreamID == right.StreamID && left.ProfileID == right.ProfileID && left.AgentID == right.AgentID &&
		left.Target == right.Target && reflect.DeepEqual(left.Limits, right.Limits)
}

func decoderHasTrailingValue(decoder *json.Decoder) bool {
	var extra any
	return decoder.Decode(&extra) != io.EOF
}

func (c *websocketFrameConn) Read(ctx context.Context, maxFrameBytes int64) (Frame, error) {
	messageType, raw, err := c.conn.Read(ctx)
	if err != nil {
		return Frame{}, err
	}
	if messageType != websocket.MessageBinary {
		return Frame{}, ErrInvalidFrame
	}
	return DecodeFrame(raw, minInt64(maxFrameBytes, c.maxFrameBytes))
}

func (c *websocketFrameConn) Write(parent context.Context, frame Frame) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	raw, err := EncodeFrame(frame, c.maxFrameBytes)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, c.writeTimeout)
	defer cancel()
	return c.conn.Write(ctx, websocket.MessageBinary, raw)
}

func (c *websocketFrameConn) Close() {
	c.closeOnce.Do(func() { c.conn.CloseNow() })
}

func minInt64(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}
