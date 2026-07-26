package blackbox_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/megamen32/gptadmin/go-shellmcp/internal/networkproxy"
)

func TestLocalHTTPConnectReachesRelayStream(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		connection, err := websocket.Accept(writer, request, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer connection.Close(websocket.StatusNormalClosure, "done")
		ctx, cancel := context.WithTimeout(request.Context(), time.Second)
		defer cancel()
		messageType, _, err := connection.Read(ctx)
		if err != nil || messageType != websocket.MessageText {
			t.Errorf("handshake = %v/%v", messageType, err)
			return
		}
		ready, _ := json.Marshal(map[string]any{"state": "open", "protocol_version": 1, "stream_id": "blackbox"})
		if err := connection.Write(ctx, websocket.MessageText, ready); err != nil {
			t.Errorf("ready: %v", err)
			return
		}
		messageType, payload, err := connection.Read(ctx)
		if err != nil || messageType != websocket.MessageBinary || len(payload) < 2 || payload[0] != 1 {
			t.Errorf("data frame = %v/%v/%x", messageType, err, payload)
			return
		}
		if err := connection.Write(ctx, websocket.MessageBinary, append([]byte{1}, payload[1:]...)); err != nil {
			t.Errorf("echo frame: %v", err)
		}
	}))
	defer relay.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- networkproxy.ServeLocalProxy(ctx, listener, networkproxy.LocalProxyConfig{
			Target:        "127.0.0.1:8080",
			MaxFrameBytes: 32 * 1024,
			WriteTimeout:  time.Second,
			TicketSource:  networkproxy.StaticTicketSource("ws"+strings.TrimPrefix(relay.URL, "http"), "blackbox-ticket"),
		})
	}()

	local, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	if _, err := io.WriteString(local, "CONNECT 127.0.0.1:8080 HTTP/1.1\r\nHost: 127.0.0.1:8080\r\n\r\nhello\n"); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(local)
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("proxy status = %s", response.Status)
	}
	got, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello\n" {
		t.Fatalf("echo = %q", got)
	}
	cancel()
	select {
	case <-serveDone:
	case <-time.After(time.Second):
		t.Fatal("local proxy did not stop")
	}
}
