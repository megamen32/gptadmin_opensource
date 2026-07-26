package proxy

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestHandleHTTPConnectRelaysBidirectionally(t *testing.T) {
	proxyConn, clientConn := net.Pipe()
	upstreamConn, upstreamPeer := net.Pipe()
	defer clientConn.Close()
	defer upstreamPeer.Close()

	done := make(chan error, 1)
	go func() {
		done <- Handle(proxyConn, func(network, address string) (net.Conn, error) {
			if network != "tcp" || address != "example.com:443" {
				return nil, fmt.Errorf("unexpected dial %s %s", network, address)
			}
			return upstreamConn, nil
		})
	}()

	_ = clientConn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.WriteString(clientConn, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n"); err != nil {
		t.Fatalf("write CONNECT: %v", err)
	}
	response, err := bufio.NewReader(clientConn).ReadString('\n')
	if err != nil {
		t.Fatalf("read CONNECT response: %v", err)
	}
	if !strings.HasPrefix(response, "HTTP/1.1 200") {
		t.Fatalf("response=%q", response)
	}

	if _, err := clientConn.Write([]byte("from-client")); err != nil {
		t.Fatalf("write client payload: %v", err)
	}
	clientPayload := make([]byte, len("from-client"))
	if _, err := io.ReadFull(upstreamPeer, clientPayload); err != nil {
		t.Fatalf("read upstream payload: %v", err)
	}
	if string(clientPayload) != "from-client" {
		t.Fatalf("upstream payload=%q", clientPayload)
	}

	if _, err := upstreamPeer.Write([]byte("from-upstream")); err != nil {
		t.Fatalf("write upstream payload: %v", err)
	}
	upstreamPayload := make([]byte, len("from-upstream"))
	if _, err := io.ReadFull(clientConn, upstreamPayload); err != nil {
		t.Fatalf("read client payload: %v", err)
	}
	if string(upstreamPayload) != "from-upstream" {
		t.Fatalf("client payload=%q", upstreamPayload)
	}

	_ = clientConn.Close()
	if err := <-done; err != nil {
		t.Fatalf("Handle: %v", err)
	}
}

func TestHandleSOCKS5ConnectRelaysBidirectionally(t *testing.T) {
	proxyConn, clientConn := net.Pipe()
	upstreamConn, upstreamPeer := net.Pipe()
	defer clientConn.Close()
	defer upstreamPeer.Close()

	done := make(chan error, 1)
	go func() {
		done <- Handle(proxyConn, func(network, address string) (net.Conn, error) {
			if network != "tcp" || address != "example.com:443" {
				return nil, fmt.Errorf("unexpected dial %s %s", network, address)
			}
			return upstreamConn, nil
		})
	}()

	_ = clientConn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := clientConn.Write([]byte{5, 1, 0}); err != nil {
		t.Fatalf("write SOCKS greeting: %v", err)
	}
	method := make([]byte, 2)
	if _, err := io.ReadFull(clientConn, method); err != nil {
		t.Fatalf("read SOCKS method: %v", err)
	}
	if string(method) != string([]byte{5, 0}) {
		t.Fatalf("method response=%v", method)
	}
	request := []byte{5, 1, 0, 3, byte(len("example.com"))}
	request = append(request, []byte("example.com")...)
	request = append(request, 1, 187)
	if _, err := clientConn.Write(request); err != nil {
		t.Fatalf("write SOCKS request: %v", err)
	}
	response := make([]byte, 10)
	if _, err := io.ReadFull(clientConn, response); err != nil {
		t.Fatalf("read SOCKS response: %v", err)
	}
	if response[0] != 5 || response[1] != 0 {
		t.Fatalf("SOCKS response=%v", response)
	}

	if _, err := clientConn.Write([]byte("from-client")); err != nil {
		t.Fatalf("write client payload: %v", err)
	}
	clientPayload := make([]byte, len("from-client"))
	if _, err := io.ReadFull(upstreamPeer, clientPayload); err != nil {
		t.Fatalf("read upstream payload: %v", err)
	}
	if string(clientPayload) != "from-client" {
		t.Fatalf("upstream payload=%q", clientPayload)
	}

	if _, err := upstreamPeer.Write([]byte("from-upstream")); err != nil {
		t.Fatalf("write upstream payload: %v", err)
	}
	upstreamPayload := make([]byte, len("from-upstream"))
	if _, err := io.ReadFull(clientConn, upstreamPayload); err != nil {
		t.Fatalf("read client payload: %v", err)
	}
	if string(upstreamPayload) != "from-upstream" {
		t.Fatalf("client payload=%q", upstreamPayload)
	}

	_ = clientConn.Close()
	if err := <-done; err != nil {
		t.Fatalf("Handle: %v", err)
	}
}
