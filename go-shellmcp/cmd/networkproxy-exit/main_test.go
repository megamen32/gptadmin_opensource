package main

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

func TestSelectedNodeDrivesGrantForLocalHTTPConnect(t *testing.T) {
	issued := make(chan string, 1)
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/proxy-control/v1/issue" || r.Header.Get("Authorization") != "Bearer test-control" {
			http.Error(w, "unexpected request", http.StatusForbidden)
			return
		}
		var request struct {
			CapabilityID string `json:"capability_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		issued <- request.CapabilityID
		_ = json.NewEncoder(w).Encode(map[string]any{"client_grant": map[string]string{"token": "client-ticket"}})
	}))
	defer hub.Close()

	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer connection.Close(websocket.StatusNormalClosure, "done")
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		_, _, _ = connection.Read(ctx)
		ready, _ := json.Marshal(map[string]any{"state": "open", "protocol_version": 1, "stream_id": "test"})
		_ = connection.Write(ctx, websocket.MessageText, ready)
		_, payload, err := connection.Read(ctx)
		if err == nil {
			_ = connection.Write(ctx, websocket.MessageBinary, append([]byte{1}, payload[1:]...))
		}
	}))
	defer relay.Close()

	selected := &selector{nodes: map[string]exitNode{"shell:chosen": {AgentID: "shell:chosen", CapabilityID: "cap-chosen"}}}
	if err := selected.choose("shell:chosen"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- networkproxy.ServeLocalProxy(ctx, listener, networkproxy.LocalProxyConfig{AllowAnyTarget: true, MaxFrameBytes: 32 * 1024, WriteTimeout: time.Second, TicketSource: hubTicketSource(hub.URL, "ws"+strings.TrimPrefix(relay.URL, "http"), "test-control", selected)})
	}()
	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, _ = io.WriteString(client, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\nping\n")
	reader := bufio.NewReader(client)
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("proxy status = %s", response.Status)
	}
	if payload, err := reader.ReadString('\n'); err != nil || payload != "ping\n" {
		t.Fatalf("proxy payload = %q error=%v", payload, err)
	}
	select {
	case capability := <-issued:
		if capability != "cap-chosen" {
			t.Fatalf("capability_id = %q", capability)
		}
	case <-time.After(time.Second):
		t.Fatal("Hub issue endpoint was not called")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
