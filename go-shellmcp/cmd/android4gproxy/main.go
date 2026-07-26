package main

import (
	"context"
	"flag"
	"log"
	"os/signal"
	"syscall"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/proxy"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:3126", "TCP listen address")
	dns := flag.String("dns", "1.1.1.1:53", "DNS server for domain resolution; empty uses system DNS")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Printf("android4gproxy listening on %s (SOCKS5/HTTP CONNECT, TCP only, dns=%s)", *listen, *dns)
	if err := proxy.ServeWithDNS(ctx, *listen, *dns); err != nil {
		log.Fatal(err)
	}
}
