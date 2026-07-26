package relay

import (
	"bufio"
	"context"
	"io"
	"net"
	"testing"
	"time"
)

func TestLocalTCPEchoThroughRealWebSocketPair(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}()

	h := newRelayHarness(t, nil)
	client, agent := pairedConnections(t, h, "stream-echo", nil)
	agentDone := make(chan error, 1)
	go func() {
		dialer := net.Dialer{Timeout: time.Second}
		tcpConn, dialErr := dialer.DialContext(context.Background(), "tcp", listener.Addr().String())
		if dialErr != nil {
			agentDone <- dialErr
			return
		}
		defer tcpConn.Close()
		frame := readFrame(t, agent, 32*1024)
		if _, writeErr := tcpConn.Write(frame.Payload); writeErr != nil {
			agentDone <- writeErr
			return
		}
		line, readErr := bufio.NewReader(tcpConn).ReadString('\n')
		if readErr != nil {
			agentDone <- readErr
			return
		}
		writeFrame(t, agent, Frame{Type: FrameData, Payload: []byte(line)})
		agentDone <- nil
	}()

	writeFrame(t, client, Frame{Type: FrameData, Payload: []byte("echo-over-relay\n")})
	got := readFrame(t, client, 32*1024)
	if string(got.Payload) != "echo-over-relay\n" {
		t.Fatalf("echo = %q", got.Payload)
	}
	if err := <-agentDone; err != nil {
		t.Fatal(err)
	}
}
