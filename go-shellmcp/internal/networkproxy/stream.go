package networkproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	streamProtocolVersion = 1
	streamRoleClient      = "client"
	streamRoleAgent       = "agent"
	streamPathClient      = "/v1/stream/client"
	streamPathAgent       = "/v1/stream/agent"
	streamFrameData       = byte(1)
	streamFrameFIN        = byte(2)
	streamFrameReset      = byte(3)
	streamFrameError      = byte(4)
)

var (
	ErrStreamConfig = errors.New("network tunnel stream configuration is invalid")
	ErrStreamReset  = errors.New("network tunnel stream was reset")
	ErrStreamClosed = errors.New("network tunnel stream is closed")
)

type streamHandshake struct {
	ProtocolVersion int    `json:"protocol_version"`
	Ticket          string `json:"ticket"`
}

type streamReady struct {
	State           string `json:"state"`
	ProtocolVersion int    `json:"protocol_version"`
	StreamID        string `json:"stream_id"`
}

// StreamConfig describes one authorized data-plane WebSocket connection.
type StreamConfig struct {
	RelayURL      string
	Ticket        string
	Role          string
	MaxFrameBytes int64
	WriteTimeout  time.Duration
}

// OpenStream authenticates one relay side and returns a net.Conn-like byte stream.
// The caller owns the one-time ticket and must not reuse it after this call.
func OpenStream(ctx context.Context, config StreamConfig) (net.Conn, error) {
	if ctx == nil || strings.TrimSpace(config.RelayURL) == "" || strings.TrimSpace(config.Ticket) == "" ||
		(config.Role != streamRoleClient && config.Role != streamRoleAgent) || config.MaxFrameBytes <= 0 || config.WriteTimeout <= 0 {
		return nil, ErrStreamConfig
	}
	endpoint, err := streamEndpoint(config.RelayURL, config.Role)
	if err != nil {
		return nil, err
	}
	connection, _, err := websocket.Dial(ctx, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("dial network tunnel relay: %w", err)
	}
	stream := &websocketStream{conn: connection, maxFrameBytes: config.MaxFrameBytes, writeTimeout: config.WriteTimeout}
	if err := stream.authenticate(ctx, config.Ticket); err != nil {
		_ = stream.Close()
		return nil, err
	}
	return stream, nil
}

func streamEndpoint(rawURL, role string) (string, error) {
	endpoint, err := url.Parse(rawURL)
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "ws" && endpoint.Scheme != "wss") {
		return "", fmt.Errorf("%w: relay URL", ErrStreamConfig)
	}
	path := endpoint.Path
	if path == "" || path == "/" {
		if role == streamRoleClient {
			path = streamPathClient
		} else {
			path = streamPathAgent
		}
	}
	expectedPath := streamPathAgent
	if role == streamRoleClient {
		expectedPath = streamPathClient
	}
	if path != expectedPath {
		return "", fmt.Errorf("%w: relay stream path", ErrStreamConfig)
	}
	endpoint.Path = path
	endpoint.RawQuery = ""
	endpoint.Fragment = ""
	return endpoint.String(), nil
}

type websocketStream struct {
	conn          *websocket.Conn
	maxFrameBytes int64
	writeTimeout  time.Duration

	readMu  sync.Mutex
	writeMu sync.Mutex
	mu      sync.Mutex
	readDL  time.Time
	writeDL time.Time
	readBuf []byte
	closed  bool
}

func (s *websocketStream) authenticate(ctx context.Context, ticket string) error {
	handshake, err := json.Marshal(streamHandshake{ProtocolVersion: streamProtocolVersion, Ticket: ticket})
	if err != nil {
		return err
	}
	if err := s.conn.Write(ctx, websocket.MessageText, handshake); err != nil {
		return fmt.Errorf("write network tunnel handshake: %w", err)
	}
	messageType, raw, err := s.conn.Read(ctx)
	if err != nil {
		return fmt.Errorf("read network tunnel handshake: %w", err)
	}
	if messageType != websocket.MessageText {
		return fmt.Errorf("%w: relay ready message type", ErrStreamConfig)
	}
	var ready streamReady
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ready); err != nil {
		return fmt.Errorf("%w: relay ready response", ErrStreamConfig)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) || ready.State != "open" || ready.ProtocolVersion != streamProtocolVersion || ready.StreamID == "" {
		return fmt.Errorf("%w: relay ready response", ErrStreamConfig)
	}
	return nil
}

func (s *websocketStream) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	s.readMu.Lock()
	defer s.readMu.Unlock()
	for len(s.readBuf) == 0 {
		ctx, cancel := s.operationContext(false)
		messageType, raw, err := s.conn.Read(ctx)
		cancel()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return 0, streamDeadlineError{}
			}
			return 0, err
		}
		if messageType != websocket.MessageBinary || len(raw) == 0 {
			return 0, ErrStreamClosed
		}
		switch raw[0] {
		case streamFrameData:
			if int64(len(raw)-1) > s.maxFrameBytes {
				return 0, ErrStreamClosed
			}
			s.readBuf = append(s.readBuf[:0], raw[1:]...)
		case streamFrameFIN:
			return 0, io.EOF
		case streamFrameReset, streamFrameError:
			return 0, ErrStreamReset
		default:
			return 0, ErrStreamClosed
		}
	}
	n := copy(buffer, s.readBuf)
	s.readBuf = s.readBuf[n:]
	return n, nil
}

func (s *websocketStream) Write(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	written := 0
	for written < len(buffer) {
		chunkSize := len(buffer) - written
		if int64(chunkSize) > s.maxFrameBytes {
			chunkSize = int(s.maxFrameBytes)
		}
		raw := make([]byte, chunkSize+1)
		raw[0] = streamFrameData
		copy(raw[1:], buffer[written:written+chunkSize])
		ctx, cancel := s.operationContext(true)
		err := s.conn.Write(ctx, websocket.MessageBinary, raw)
		cancel()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return written, streamDeadlineError{}
			}
			return written, err
		}
		written += chunkSize
	}
	return written, nil
}

func (s *websocketStream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	return s.conn.Close(websocket.StatusNormalClosure, "closed")
}

func (s *websocketStream) LocalAddr() net.Addr  { return streamAddr("local") }
func (s *websocketStream) RemoteAddr() net.Addr { return streamAddr("relay") }
func (s *websocketStream) SetDeadline(deadline time.Time) error {
	s.mu.Lock()
	s.readDL, s.writeDL = deadline, deadline
	s.mu.Unlock()
	return nil
}
func (s *websocketStream) SetReadDeadline(deadline time.Time) error {
	s.mu.Lock()
	s.readDL = deadline
	s.mu.Unlock()
	return nil
}
func (s *websocketStream) SetWriteDeadline(deadline time.Time) error {
	s.mu.Lock()
	s.writeDL = deadline
	s.mu.Unlock()
	return nil
}

func (s *websocketStream) operationContext(write bool) (context.Context, context.CancelFunc) {
	s.mu.Lock()
	deadline := time.Now().Add(s.writeTimeout)
	if write && !s.writeDL.IsZero() && s.writeDL.Before(deadline) {
		deadline = s.writeDL
	}
	if !write && !s.readDL.IsZero() && s.readDL.Before(deadline) {
		deadline = s.readDL
	}
	s.mu.Unlock()
	return context.WithDeadline(context.Background(), deadline)
}

type streamAddr string

func (a streamAddr) Network() string { return "network-tunnel" }
func (a streamAddr) String() string  { return string(a) }

type streamDeadlineError struct{}

func (streamDeadlineError) Error() string   { return "i/o timeout" }
func (streamDeadlineError) Timeout() bool   { return true }
func (streamDeadlineError) Temporary() bool { return true }

// RunOffer activates the LAN or internet-egress target from one verified offer.
// It uses the edge dialer for target enforcement and the relay only for bytes.
func RunOffer(ctx context.Context, offer Offer) error {
	return RunOfferWithDNS(ctx, offer, "")
}

// RunOfferWithDNS activates one offer and optionally resolves targets through
// an explicit UDP DNS endpoint. Android builds use this when libc DNS points
// at an unavailable localhost stub while the cellular network is healthy.
func RunOfferWithDNS(ctx context.Context, offer Offer, dnsServer string) error {
	if err := offer.Validate(time.Now()); err != nil {
		return err
	}
	target, err := ParseTarget(offer.Target)
	if err != nil {
		return err
	}
	resolver := &net.Resolver{PreferGo: true}
	if dnsServer != "" {
		if _, _, splitErr := net.SplitHostPort(dnsServer); splitErr != nil {
			dnsServer = net.JoinHostPort(dnsServer, "53")
		}
		resolver.Dial = func(resolveCtx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(resolveCtx, "udp", dnsServer)
		}
	}
	dialer, err := NewDialer(
		Policy{Scope: offer.Scope, ApprovedLANCIDRs: offer.AllowedCIDRs, AllowedPorts: offer.AllowedPorts},
		offer.Limits,
		resolver,
		&net.Dialer{},
	)
	if err != nil {
		return err
	}
	targetConn, err := dialer.DialContext(ctx, "tcp", target.String())
	if err != nil {
		return err
	}
	defer targetConn.Close()
	streamConn, err := OpenStream(ctx, StreamConfig{RelayURL: offer.RelayURL, Ticket: offer.RelayTicket, Role: streamRoleAgent, MaxFrameBytes: 32 * 1024, WriteTimeout: 10 * time.Second})
	if err != nil {
		return err
	}
	defer streamConn.Close()
	return bridgeStreams(targetConn, streamConn)
}

func bridgeStreams(left, right net.Conn) error {
	errs := make(chan error, 2)
	go func() { _, err := io.Copy(right, left); _ = right.Close(); errs <- err }()
	go func() { _, err := io.Copy(left, right); _ = left.Close(); errs <- err }()
	return <-errs
}
