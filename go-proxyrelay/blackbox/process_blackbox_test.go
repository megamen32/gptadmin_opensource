package blackbox_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/megamen32/gptadmin/go-proxyrelay/internal/relay"
	"github.com/megamen32/gptadmin/go-proxyrelay/internal/ticket"
)

func TestProxyRelayProcessMetricsAndStreamRoundTrip(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "relay.key")
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		t.Fatal(err)
	}
	binaryPath := filepath.Join(tmpDir, "proxyrelay")
	build := exec.Command("go", "build", "-o", binaryPath, "../cmd/proxyrelay")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build proxyrelay: %v (%s)", err, strings.TrimSpace(string(output)))
	}

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listenAddress := probe.Addr().String()
	_ = probe.Close()

	process := exec.Command(binaryPath,
		"-listen", listenAddress,
		"-key-file", keyPath,
		"-handshake-timeout", "1s",
		"-pair-timeout", "1s",
	)
	process.Stdout = io.Discard
	process.Stderr = io.Discard
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if process.Process == nil {
			return
		}
		_ = process.Process.Signal(os.Interrupt)
		waitDone := make(chan struct{})
		go func() {
			_, _ = process.Process.Wait()
			close(waitDone)
		}()
		select {
		case <-waitDone:
		case <-time.After(3 * time.Second):
			_ = process.Process.Kill()
		}
	})

	baseURL := "http://" + listenAddress
	client := &http.Client{Timeout: 500 * time.Millisecond}
	deadline := time.Now().Add(5 * time.Second)
	for {
		response, requestErr := client.Get(baseURL + "/metrics")
		if requestErr == nil {
			body, readErr := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if readErr == nil && response.StatusCode == http.StatusOK && strings.Contains(string(body), `"active_sessions"`) {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("proxyrelay process did not expose /metrics at %s", baseURL)
		}
		time.Sleep(25 * time.Millisecond)
	}

	signer, err := ticket.NewSigner(key)
	if err != nil {
		t.Fatal(err)
	}
	claims := func(role, jti string) ticket.Claims {
		return ticket.Claims{
			Kind: ticket.KindStream, ProtocolVersion: relay.ProtocolVersion,
			CapabilityID: "process-capability", StreamID: "process-stream",
			ProfileID: "process-profile", AgentID: "process-agent",
			Target: "127.0.0.1:443", Role: role,
			ExpiresAt: time.Now().Add(time.Minute).Unix(), JTI: jti,
			Limits: ticket.Limits{
				MaxFrameBytes: 1024, MaxPendingFrames: 4,
				DialTimeoutSeconds: 1, IdleTimeoutSeconds: 5,
				MaxStreamLifetimeSeconds: 30, MaxBytes: 4096,
				BandwidthBytesPerSecond: 4096, MaxStreamsPerAgent: 2,
				MaxStreamsPerProfile: 2,
			},
		}
	}
	connect := func(role, jti string) *websocket.Conn {
		t.Helper()
		ticketText, signErr := signer.SignStream(claims(role, jti))
		if signErr != nil {
			t.Fatal(signErr)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		connection, _, dialErr := websocket.Dial(ctx, "ws"+strings.TrimPrefix(baseURL, "http")+"/v1/stream/"+role, nil)
		if dialErr != nil {
			t.Fatal(dialErr)
		}
		t.Cleanup(func() { connection.CloseNow() })
		handshake, marshalErr := json.Marshal(relay.Handshake{ProtocolVersion: relay.ProtocolVersion, Ticket: ticketText})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if writeErr := connection.Write(ctx, websocket.MessageText, handshake); writeErr != nil {
			t.Fatal(writeErr)
		}
		return connection
	}

	clientConnection := connect(ticket.RoleClient, "process-client")
	agentConnection := connect(ticket.RoleAgent, "process-agent")
	for _, connection := range []*websocket.Conn{clientConnection, agentConnection} {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		typ, _, readErr := connection.Read(ctx)
		cancel()
		if readErr != nil || typ != websocket.MessageText {
			t.Fatalf("ready frame type=%v err=%v", typ, readErr)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	frame, err := relay.EncodeFrame(relay.Frame{Type: relay.FrameData, Payload: []byte("process-blackbox-payload")}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := clientConnection.Write(ctx, websocket.MessageBinary, frame); err != nil {
		t.Fatal(err)
	}
	typ, raw, err := agentConnection.Read(ctx)
	if err != nil || typ != websocket.MessageBinary {
		t.Fatalf("data frame type=%v err=%v", typ, err)
	}
	decoded, err := relay.DecodeFrame(raw, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded.Payload) != "process-blackbox-payload" {
		t.Fatalf("payload = %q", decoded.Payload)
	}
}
