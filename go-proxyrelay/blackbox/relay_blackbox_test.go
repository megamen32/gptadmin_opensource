package blackbox_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/megamen32/gptadmin/go-proxyrelay/internal/relay"
	"github.com/megamen32/gptadmin/go-proxyrelay/internal/ticket"
)

func TestPublicRelayRoundTripAcrossClientAndAgentEndpoints(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	verifier, err := ticket.NewVerifier(key, time.Now, 16)
	if err != nil {
		t.Fatal(err)
	}
	server, err := relay.New(relay.Config{
		Verifier:          verifier,
		HandshakeTimeout:  time.Second,
		PairTimeout:       time.Second,
		WriteTimeout:      time.Second,
		MaxHandshakeBytes: 4096,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	signer, err := ticket.NewSigner(key)
	if err != nil {
		t.Fatal(err)
	}
	claims := func(role, jti string) ticket.Claims {
		return ticket.Claims{
			Kind: ticket.KindStream, ProtocolVersion: relay.ProtocolVersion,
			CapabilityID: "cap-blackbox", StreamID: "stream-blackbox",
			ProfileID: "profile-blackbox", AgentID: "agent-blackbox",
			Target: "192.168.2.50:443", Role: role,
			ExpiresAt: time.Now().Add(time.Minute).Unix(), JTI: jti,
			Limits: ticket.Limits{MaxFrameBytes: 1024, MaxPendingFrames: 4, DialTimeoutSeconds: 1, IdleTimeoutSeconds: 5, MaxStreamLifetimeSeconds: 30, MaxBytes: 4096, MaxStreamsPerAgent: 2, MaxStreamsPerProfile: 2},
		}
	}

	connect := func(role, jti string) *websocket.Conn {
		t.Helper()
		ticketText, signErr := signer.SignStream(claims(role, jti))
		if signErr != nil {
			t.Fatal(signErr)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		baseURL := "ws" + strings.TrimPrefix(httpServer.URL, "http")
		conn, _, dialErr := websocket.Dial(ctx, baseURL+"/v1/stream/"+role, nil)
		if dialErr != nil {
			t.Fatal(dialErr)
		}
		handshake, marshalErr := json.Marshal(relay.Handshake{ProtocolVersion: relay.ProtocolVersion, Ticket: ticketText})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if writeErr := conn.Write(ctx, websocket.MessageText, handshake); writeErr != nil {
			t.Fatal(writeErr)
		}
		t.Cleanup(func() { conn.CloseNow() })
		return conn
	}

	client := connect(ticket.RoleClient, "blackbox-client")
	agent := connect(ticket.RoleAgent, "blackbox-agent")
	for _, conn := range []*websocket.Conn{client, agent} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		typ, _, readErr := conn.Read(ctx)
		cancel()
		if readErr != nil || typ != websocket.MessageText {
			t.Fatalf("ready read type=%v err=%v", typ, readErr)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	frame, err := relay.EncodeFrame(relay.Frame{Type: relay.FrameData, Payload: []byte("blackbox-payload")}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Write(ctx, websocket.MessageBinary, frame); err != nil {
		t.Fatal(err)
	}
	typ, raw, err := agent.Read(ctx)
	if err != nil || typ != websocket.MessageBinary {
		t.Fatalf("agent data read type=%v err=%v", typ, err)
	}
	got, err := relay.DecodeFrame(raw, 1024)
	if err != nil || string(got.Payload) != "blackbox-payload" {
		t.Fatalf("agent frame=%#v err=%v", got, err)
	}
}
