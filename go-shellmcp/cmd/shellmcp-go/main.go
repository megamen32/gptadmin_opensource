package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/server"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg := server.FromEnv()
	srv := server.New(cfg)
	defer srv.Close()
	if server.MCPTransportRequested(os.Args[1:], os.Getenv("SHELLMCP_TRANSPORT")) {
		if err := srv.ServeMCPStdio(ctx, os.Stdin, os.Stdout); err != nil && ctx.Err() == nil {
			log.Fatal(err)
		}
		return
	}
	if err := srv.ListenAndServeContext(ctx); err != nil && ctx.Err() == nil {
		log.Fatal(err)
	}
}
