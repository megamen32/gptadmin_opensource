// Command networkproxy exposes one local HTTP CONNECT/SOCKS5 TCP listener
// through an already-issued Network Tunnel client ticket.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/networkproxy"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:3126", "local proxy listen address")
	target := flag.String("target", "", "authorized target host:port")
	relayURL := flag.String("relay", "", "relay WebSocket base URL or stream endpoint")
	ticketFile := flag.String("ticket-file", "", "file containing the one-time client relay ticket")
	maxFrame := flag.Int64("max-frame-bytes", 32*1024, "maximum relay data frame payload")
	writeTimeout := flag.Duration("write-timeout", 10*time.Second, "relay write timeout")
	flag.Parse()

	if strings.TrimSpace(*target) == "" || strings.TrimSpace(*relayURL) == "" || strings.TrimSpace(*ticketFile) == "" {
		log.Fatal("-target, -relay, and -ticket-file are required")
	}
	ticket, err := os.ReadFile(*ticketFile)
	if err != nil {
		log.Fatalf("read client ticket: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	err = networkproxy.ListenAndServeLocalProxy(ctx, *listen, networkproxy.LocalProxyConfig{
		Target:        *target,
		MaxFrameBytes: *maxFrame,
		WriteTimeout:  *writeTimeout,
		TicketSource:  networkproxy.StaticTicketSource(*relayURL, strings.TrimSpace(string(ticket))),
	})
	if err != nil {
		log.Fatal(fmt.Errorf("local network tunnel proxy: %w", err))
	}
}
