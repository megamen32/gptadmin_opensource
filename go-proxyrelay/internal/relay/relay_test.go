package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/megamen32/gptadmin/go-proxyrelay/internal/ticket"
)

var relayTestKey = []byte("0123456789abcdef0123456789abcdef")

type relayHarness struct {
	t       *testing.T
	server  *Server
	http    *httptest.Server
	signer  *ticket.Signer
	baseURL string
}

func newRelayHarness(t *testing.T, audit AuditFunc) *relayHarness {
	t.Helper()
	verifier, err := ticket.NewVerifier(relayTestKey, time.Now, 256)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ticket.NewSigner(relayTestKey)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Config{
		Verifier:          verifier,
		HandshakeTimeout:  300 * time.Millisecond,
		PairTimeout:       300 * time.Millisecond,
		WriteTimeout:      200 * time.Millisecond,
		MaxHandshakeBytes: 8 * 1024,
		Audit:             audit,
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	h := &relayHarness{
		t:       t,
		server:  server,
		http:    httpServer,
		signer:  signer,
		baseURL: "ws" + strings.TrimPrefix(httpServer.URL, "http"),
	}
	t.Cleanup(func() {
		server.Close()
		httpServer.Close()
	})
	return h
}

func relayClaims(role, streamID, jti string) ticket.Claims {
	return ticket.Claims{
		Kind:            ticket.KindStream,
		ProtocolVersion: ProtocolVersion,
		CapabilityID:    "cap-1",
		StreamID:        streamID,
		ProfileID:       "profile-1",
		AgentID:         "agent-1",
		Target:          "192.0.2.10:443",
		Role:            role,
		ExpiresAt:       time.Now().Add(time.Minute).Unix(),
		JTI:             jti,
		Limits: ticket.Limits{
			MaxFrameBytes:            32 * 1024,
			MaxPendingFrames:         4,
			DialTimeoutSeconds:       1,
			IdleTimeoutSeconds:       2,
			MaxStreamLifetimeSeconds: 10,
			MaxBytes:                 1 << 20,
			BandwidthBytesPerSecond:  1 << 20,
			MaxStreamsPerAgent:       2,
			MaxStreamsPerProfile:     2,
		},
	}
}

func (h *relayHarness) sign(t *testing.T, claims ticket.Claims) string {
	t.Helper()
	raw, err := h.signer.SignStream(claims)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (h *relayHarness) connect(t *testing.T, role, rawTicket string, version int) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, h.baseURL+"/v1/stream/"+role, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", role, err)
	}
	handshake, err := json.Marshal(Handshake{ProtocolVersion: version, Ticket: rawTicket})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Write(ctx, websocket.MessageText, handshake); err != nil {
		t.Fatalf("write handshake: %v", err)
	}
	t.Cleanup(func() { c.CloseNow() })
	return c
}

func readReady(t *testing.T, c *websocket.Conn) Ready {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	typ, raw, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read ready: %v", err)
	}
	if typ != websocket.MessageText {
		t.Fatalf("ready message type = %v", typ)
	}
	var ready Ready
	if err := json.Unmarshal(raw, &ready); err != nil {
		t.Fatal(err)
	}
	if ready.State != "open" || ready.ProtocolVersion != ProtocolVersion {
		t.Fatalf("unexpected ready: %#v", ready)
	}
	return ready
}

func TestMetricsEndpointExposesBoundedRelayCounters(t *testing.T) {
	h := newRelayHarness(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	h.server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics status=%d body=%s", rec.Code, rec.Body.String())
	}
	var metrics map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &metrics); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"active_sessions", "authenticated_peers_total", "pairs_started_total", "resets_total", "max_observed_queue_depth"} {
		if _, ok := metrics[name]; !ok {
			t.Fatalf("metrics missing %q: %v", name, metrics)
		}
	}
	if strings.Contains(rec.Body.String(), "ticket") || strings.Contains(rec.Body.String(), "target") {
		t.Fatalf("metrics leaked relay authorization data: %s", rec.Body.String())
	}
}

func writeFrame(t *testing.T, c *websocket.Conn, frame Frame) {
	t.Helper()
	raw, err := EncodeFrame(frame, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageBinary, raw); err != nil {
		t.Fatalf("write frame: %v", err)
	}
}

func readFrame(t *testing.T, c *websocket.Conn, max int64) Frame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	typ, raw, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	if typ != websocket.MessageBinary {
		t.Fatalf("frame message type = %v", typ)
	}
	frame, err := DecodeFrame(raw, max)
	if err != nil {
		t.Fatal(err)
	}
	return frame
}

func waitForActiveSession(t *testing.T, h *relayHarness) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		stats := h.server.Stats()
		if stats.ActiveSessions == 1 && stats.AuthenticatedPeers == 1 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("first relay peer was not registered: %+v", h.server.Stats())
}

func pairedConnections(t *testing.T, h *relayHarness, streamID string, mutate func(*ticket.Claims, *ticket.Claims)) (*websocket.Conn, *websocket.Conn) {
	t.Helper()
	clientClaims := relayClaims(ticket.RoleClient, streamID, streamID+"-client")
	agentClaims := relayClaims(ticket.RoleAgent, streamID, streamID+"-agent")
	if mutate != nil {
		mutate(&clientClaims, &agentClaims)
	}
	client := h.connect(t, ticket.RoleClient, h.sign(t, clientClaims), ProtocolVersion)
	agent := h.connect(t, ticket.RoleAgent, h.sign(t, agentClaims), ProtocolVersion)
	if got := readReady(t, client).StreamID; got != streamID {
		t.Fatalf("client stream = %q", got)
	}
	if got := readReady(t, agent).StreamID; got != streamID {
		t.Fatalf("agent stream = %q", got)
	}
	return client, agent
}

func TestRelayPairsExactlyOneClientAndAgent(t *testing.T) {
	h := newRelayHarness(t, nil)
	client, agent := pairedConnections(t, h, "stream-pair", nil)
	writeFrame(t, client, Frame{Type: FrameData, Payload: []byte("hello")})
	got := readFrame(t, agent, 32*1024)
	if got.Type != FrameData || string(got.Payload) != "hello" {
		t.Fatalf("agent frame = %#v", got)
	}

	extraClaims := relayClaims(ticket.RoleClient, "stream-pair", "stream-pair-extra-client")
	extra := h.connect(t, ticket.RoleClient, h.sign(t, extraClaims), ProtocolVersion)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, _, err := extra.Read(ctx); err == nil {
		t.Fatal("second client unexpectedly joined an active pair")
	}
	stats := h.server.Stats()
	if stats.AuthenticatedPeers < 2 || stats.PairsStarted < 1 || stats.ActiveSessions < 1 {
		t.Fatalf("relay metrics did not observe the active pair: %+v", stats)
	}
}

func TestRelayRejectsGrantReplayWrongRoleCapabilityAndProtocol(t *testing.T) {
	t.Run("replay", func(t *testing.T) {
		h := newRelayHarness(t, nil)
		claims := relayClaims(ticket.RoleClient, "stream-replay", "jti-replay")
		raw := h.sign(t, claims)
		first := h.connect(t, ticket.RoleClient, raw, ProtocolVersion)
		waitForActiveSession(t, h)
		second := h.connect(t, ticket.RoleClient, raw, ProtocolVersion)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if _, _, err := second.Read(ctx); err == nil {
			t.Fatal("replayed ticket was accepted")
		}
		if got := h.server.Stats().AuthenticatedPeers; got != 1 {
			t.Fatalf("authenticated peers = %d, want 1 after replay rejection", got)
		}
		first.CloseNow()
	})

	t.Run("wrong role", func(t *testing.T) {
		h := newRelayHarness(t, nil)
		claims := relayClaims(ticket.RoleAgent, "stream-role", "jti-role")
		c := h.connect(t, ticket.RoleClient, h.sign(t, claims), ProtocolVersion)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if _, _, err := c.Read(ctx); err == nil {
			t.Fatal("agent role ticket accepted on client endpoint")
		}
	})

	t.Run("wrong capability", func(t *testing.T) {
		h := newRelayHarness(t, nil)
		clientClaims := relayClaims(ticket.RoleClient, "stream-cap", "jti-cap-client")
		agentClaims := relayClaims(ticket.RoleAgent, "stream-cap", "jti-cap-agent")
		agentClaims.CapabilityID = "cap-other"
		client := h.connect(t, ticket.RoleClient, h.sign(t, clientClaims), ProtocolVersion)
		waitForActiveSession(t, h)
		agent := h.connect(t, ticket.RoleAgent, h.sign(t, agentClaims), ProtocolVersion)
		got := readFrame(t, client, 32*1024)
		if got.Type != FrameReset {
			t.Fatalf("client frame = %v, want RESET", got.Type)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if _, _, err := agent.Read(ctx); err == nil {
			t.Fatal("mismatched capability agent was accepted")
		}
	})

	t.Run("invalid protocol", func(t *testing.T) {
		h := newRelayHarness(t, nil)
		claims := relayClaims(ticket.RoleClient, "stream-v2", "jti-v2")
		c := h.connect(t, ticket.RoleClient, h.sign(t, claims), 2)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if _, _, err := c.Read(ctx); err == nil {
			t.Fatal("protocol version 2 was accepted")
		}
	})
}

func TestFINHalfCloseLetsOppositeDirectionDrain(t *testing.T) {
	h := newRelayHarness(t, nil)
	client, agent := pairedConnections(t, h, "stream-fin", nil)
	writeFrame(t, client, Frame{Type: FrameFIN})
	if got := readFrame(t, agent, 32*1024); got.Type != FrameFIN {
		t.Fatalf("agent frame = %v, want FIN", got.Type)
	}
	writeFrame(t, agent, Frame{Type: FrameData, Payload: []byte("drained")})
	if got := readFrame(t, client, 32*1024); got.Type != FrameData || string(got.Payload) != "drained" {
		t.Fatalf("client drain frame = %#v", got)
	}
	writeFrame(t, agent, Frame{Type: FrameFIN})
	if got := readFrame(t, client, 32*1024); got.Type != FrameFIN {
		t.Fatalf("client frame = %v, want FIN", got.Type)
	}
}

func TestRESETPropagatesImmediately(t *testing.T) {
	h := newRelayHarness(t, nil)
	client, agent := pairedConnections(t, h, "stream-reset", nil)
	writeFrame(t, client, Frame{Type: FrameReset, Payload: []byte("client_abort")})
	if got := readFrame(t, agent, 32*1024); got.Type != FrameReset {
		t.Fatalf("agent frame = %v, want RESET", got.Type)
	}
}

func TestSignedRevocationClosesActivePair(t *testing.T) {
	h := newRelayHarness(t, nil)
	client, agent := pairedConnections(t, h, "stream-revoke", nil)
	revoke, err := h.signer.SignRevocation("cap-1", "revoke-stream", time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	body := strings.NewReader(fmt.Sprintf(`{"ticket":%q}`, revoke))
	req, err := http.NewRequest(http.MethodPost, h.http.URL+"/v1/control/revoke", body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("revoke status = %d body=%s", resp.StatusCode, raw)
	}
	for name, c := range map[string]*websocket.Conn{"client": client, "agent": agent} {
		if got := readFrame(t, c, 32*1024); got.Type != FrameReset {
			t.Fatalf("%s frame = %v, want RESET", name, got.Type)
		}
	}
}

func TestFrameByteStreamAndTimeLimitsResetWithoutGrowingQueues(t *testing.T) {
	t.Run("max frame", func(t *testing.T) {
		h := newRelayHarness(t, nil)
		client, agent := pairedConnections(t, h, "stream-frame", func(c, a *ticket.Claims) {
			c.Limits.MaxFrameBytes = 8
			a.Limits.MaxFrameBytes = 8
		})
		writeFrame(t, client, Frame{Type: FrameData, Payload: make([]byte, 9)})
		if got := readFrame(t, agent, 64); got.Type != FrameReset {
			t.Fatalf("agent frame = %v, want RESET", got.Type)
		}
	})

	t.Run("max bytes", func(t *testing.T) {
		h := newRelayHarness(t, nil)
		client, agent := pairedConnections(t, h, "stream-bytes", func(c, a *ticket.Claims) {
			c.Limits.MaxBytes = 5
			a.Limits.MaxBytes = 5
		})
		writeFrame(t, client, Frame{Type: FrameData, Payload: []byte("123456")})
		if got := readFrame(t, agent, 64); got.Type != FrameReset {
			t.Fatalf("agent frame = %v, want RESET", got.Type)
		}
	})

	t.Run("idle timeout", func(t *testing.T) {
		h := newRelayHarness(t, nil)
		client, agent := pairedConnections(t, h, "stream-idle", func(c, a *ticket.Claims) {
			c.Limits.IdleTimeoutSeconds = 1
			a.Limits.IdleTimeoutSeconds = 1
		})
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		defer cancel()
		typ, raw, err := agent.Read(ctx)
		if err != nil {
			t.Fatalf("read idle reset: %v", err)
		}
		if typ != websocket.MessageBinary {
			t.Fatalf("message type = %v", typ)
		}
		got, err := DecodeFrame(raw, 64)
		if err != nil || got.Type != FrameReset {
			t.Fatalf("idle frame = %#v err=%v", got, err)
		}
		client.CloseNow()
	})

	t.Run("stream lifetime", func(t *testing.T) {
		h := newRelayHarness(t, nil)
		_, agent := pairedConnections(t, h, "stream-life", func(c, a *ticket.Claims) {
			c.Limits.MaxStreamLifetimeSeconds = 1
			a.Limits.MaxStreamLifetimeSeconds = 1
			c.Limits.IdleTimeoutSeconds = 5
			a.Limits.IdleTimeoutSeconds = 5
		})
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		defer cancel()
		typ, raw, err := agent.Read(ctx)
		if err != nil {
			t.Fatalf("read lifetime reset: %v", err)
		}
		if typ != websocket.MessageBinary {
			t.Fatalf("message type = %v", typ)
		}
		got, err := DecodeFrame(raw, 64)
		if err != nil || got.Type != FrameReset {
			t.Fatalf("lifetime frame = %#v err=%v", got, err)
		}
	})

	t.Run("bounded stats", func(t *testing.T) {
		h := newRelayHarness(t, nil)
		client, _ := pairedConnections(t, h, "stream-stats", func(c, a *ticket.Claims) {
			c.Limits.MaxPendingFrames = 1
			a.Limits.MaxPendingFrames = 1
		})
		writeFrame(t, client, Frame{Type: FrameData, Payload: []byte("x")})
		stats := h.server.Stats()
		if stats.MaxObservedQueueDepth > 1 {
			t.Fatalf("observed queue depth = %d, want <= 1", stats.MaxObservedQueueDepth)
		}
	})
}

func TestPerAgentAndProfileStreamLimits(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ticket.Claims)
	}{
		{"agent", func(c *ticket.Claims) { c.ProfileID = "profile-other" }},
		{"profile", func(c *ticket.Claims) { c.AgentID = "agent-other" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newRelayHarness(t, nil)
			client, _ := pairedConnections(t, h, "stream-limit-1", func(c, a *ticket.Claims) {
				c.Limits.MaxStreamsPerAgent = 1
				a.Limits.MaxStreamsPerAgent = 1
				c.Limits.MaxStreamsPerProfile = 1
				a.Limits.MaxStreamsPerProfile = 1
			})
			claims := relayClaims(ticket.RoleClient, "stream-limit-2", "stream-limit-2-client")
			claims.Limits.MaxStreamsPerAgent = 1
			claims.Limits.MaxStreamsPerProfile = 1
			tc.mutate(&claims)
			second := h.connect(t, ticket.RoleClient, h.sign(t, claims), ProtocolVersion)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if _, _, err := second.Read(ctx); err == nil {
				t.Fatalf("second stream exceeded %s limit", tc.name)
			}
			client.CloseNow()
		})
	}
}

func TestPairingTimeoutResetsWaitingPeer(t *testing.T) {
	h := newRelayHarness(t, nil)
	claims := relayClaims(ticket.RoleClient, "stream-wait", "stream-wait-client")
	client := h.connect(t, ticket.RoleClient, h.sign(t, claims), ProtocolVersion)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	typ, raw, err := client.Read(ctx)
	if err != nil {
		t.Fatalf("read pairing timeout: %v", err)
	}
	if typ != websocket.MessageBinary {
		t.Fatalf("message type = %v", typ)
	}
	got, err := DecodeFrame(raw, 128)
	if err != nil || got.Type != FrameReset {
		t.Fatalf("timeout frame = %#v err=%v", got, err)
	}
}

func TestAuditLoggerNeverWritesTicketTargetOrPayload(t *testing.T) {
	var log bytes.Buffer
	h := newRelayHarness(t, NewJSONAuditLogger(&log))
	clientClaims := relayClaims(ticket.RoleClient, "stream-audit", "jti-audit-client")
	agentClaims := relayClaims(ticket.RoleAgent, "stream-audit", "jti-audit-agent")
	secretTicket := h.sign(t, clientClaims)
	client := h.connect(t, ticket.RoleClient, secretTicket, ProtocolVersion)
	agent := h.connect(t, ticket.RoleAgent, h.sign(t, agentClaims), ProtocolVersion)
	readReady(t, client)
	readReady(t, agent)
	secretPayload := "payload-super-secret"
	writeFrame(t, client, Frame{Type: FrameData, Payload: []byte(secretPayload)})
	_ = readFrame(t, agent, 32*1024)
	writeFrame(t, client, Frame{Type: FrameReset})
	time.Sleep(20 * time.Millisecond)
	got := log.String()
	for _, secret := range []string{secretTicket, secretPayload, clientClaims.Target} {
		if strings.Contains(got, secret) {
			t.Fatalf("audit log leaked %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, `"capability_id":"cap-1"`) || !strings.Contains(got, `"stream_id":"stream-audit"`) {
		t.Fatalf("audit log lacks metadata: %s", got)
	}
}

type fakeFrameConn struct {
	reads      chan Frame
	writes     chan Frame
	blockWrite bool
	closeOnce  sync.Once
}

func newFakeFrameConn(blockWrite bool) *fakeFrameConn {
	return &fakeFrameConn{reads: make(chan Frame, 8), writes: make(chan Frame, 8), blockWrite: blockWrite}
}

func (c *fakeFrameConn) Read(ctx context.Context, _ int64) (Frame, error) {
	select {
	case frame := <-c.reads:
		return frame, nil
	case <-ctx.Done():
		return Frame{}, ctx.Err()
	}
}

func (c *fakeFrameConn) Write(ctx context.Context, frame Frame) error {
	if c.blockWrite {
		<-ctx.Done()
		return ctx.Err()
	}
	select {
	case c.writes <- frame:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *fakeFrameConn) Close() { c.closeOnce.Do(func() {}) }

func TestSlowConsumerQueueOverflowResetsAtConfiguredBound(t *testing.T) {
	client := newFakeFrameConn(false)
	agent := newFakeFrameConn(true)
	claims := relayClaims(ticket.RoleClient, "stream-slow", "jti-slow")
	claims.Limits.MaxPendingFrames = 1
	p := newStreamPair(claims, client, agent, nil, func() {})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		p.run(ctx)
		close(done)
	}()
	client.reads <- Frame{Type: FrameData, Payload: []byte("one")}
	client.reads <- Frame{Type: FrameData, Payload: []byte("two")}
	client.reads <- Frame{Type: FrameData, Payload: []byte("three")}
	select {
	case got := <-client.writes:
		if got.Type != FrameReset {
			t.Fatalf("client frame = %v, want RESET", got.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("slow consumer overflow did not reset producer")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("pair did not stop after queue overflow")
	}
	if got := p.maxObservedQueueDepth(); got > 1 {
		t.Fatalf("queue depth = %d, want <= 1", got)
	}
}

func TestBandwidthLimitPacesWrites(t *testing.T) {
	client := newFakeFrameConn(false)
	agent := newFakeFrameConn(false)
	claims := relayClaims(ticket.RoleClient, "stream-bandwidth", "jti-bandwidth")
	claims.Limits.BandwidthBytesPerSecond = 1000
	claims.Limits.MaxPendingFrames = 4
	p := newStreamPair(claims, client, agent, nil, func() {})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.run(ctx)
	client.reads <- Frame{Type: FrameData, Payload: make([]byte, 20)}
	client.reads <- Frame{Type: FrameData, Payload: make([]byte, 20)}
	start := time.Now()
	for i := 0; i < 2; i++ {
		select {
		case <-agent.writes:
		case <-time.After(time.Second):
			t.Fatal("paced frame was not delivered")
		}
	}
	if elapsed := time.Since(start); elapsed < 15*time.Millisecond {
		t.Fatalf("two frames delivered in %v; bandwidth limit was not applied", elapsed)
	}
	cancel()
}

func TestBandwidthLimitIsSharedAcrossBothDirections(t *testing.T) {
	client := newFakeFrameConn(false)
	agent := newFakeFrameConn(false)
	claims := relayClaims(ticket.RoleClient, "stream-bandwidth-shared", "jti-bandwidth-shared")
	claims.Limits.BandwidthBytesPerSecond = 1000
	claims.Limits.MaxPendingFrames = 4
	p := newStreamPair(claims, client, agent, nil, func() {})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.run(ctx)

	start := time.Now()
	client.reads <- Frame{Type: FrameData, Payload: make([]byte, 100)}
	agent.reads <- Frame{Type: FrameData, Payload: make([]byte, 100)}
	for range 2 {
		select {
		case <-client.writes:
		case <-agent.writes:
		case <-time.After(time.Second):
			t.Fatal("shared bandwidth frames were not delivered")
		}
	}
	if elapsed := time.Since(start); elapsed < 150*time.Millisecond {
		t.Fatalf("bidirectional traffic completed in %v; stream bandwidth was not shared", elapsed)
	}
}

func TestConfigRejectsMissingVerifierAndUnboundedSettings(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("missing verifier accepted")
	}
	verifier, _ := ticket.NewVerifier(relayTestKey, time.Now, 16)
	_, err := New(Config{Verifier: verifier})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("error = %v, want ErrInvalidConfig", err)
	}
}
