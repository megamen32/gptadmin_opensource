package main

import (
	"context"
	"log"
	"os"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/server"
)

func main() {
	cfg := server.FromEnv()
	srv := server.New(cfg)
	if server.MCPTransportRequested(os.Args[1:], os.Getenv("SHELLMCP_TRANSPORT")) {
		if err := srv.ServeMCPStdio(context.Background(), os.Stdin, os.Stdout); err != nil {
			log.Fatal(err)
		}
		return
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
