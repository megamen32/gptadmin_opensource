package networkproxy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

func TestServeLocalProxyDynamicTargetPassesConnectAuthorityToTicketSource(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	requested := make(chan string, 1)
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- ServeLocalProxy(ctx, listener, LocalProxyConfig{
			AllowAnyTarget: true,
			MaxFrameBytes:  32 * 1024,
			WriteTimeout:   time.Second,
			TicketSource: func(_ context.Context, target string) (string, string, error) {
				requested <- target
				return "", "", errors.New("test ticket issuer rejection")
			},
		})
	}()

	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := fmt.Fprint(client, "CONNECT selected.example:8443 HTTP/1.1\r\nHost: selected.example:8443\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	response, err := bufio.NewReader(client).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(response, "502") {
		t.Fatalf("CONNECT response = %q, want proxy upstream error", response)
	}
	select {
	case target := <-requested:
		if target != "selected.example:8443" {
			t.Fatalf("ticket source target = %q", target)
		}
	case <-time.After(time.Second):
		t.Fatal("ticket source was not called")
	}

	cancel()
	if err := <-serveDone; err != nil {
		t.Fatalf("ServeLocalProxy() error = %v", err)
	}
}
