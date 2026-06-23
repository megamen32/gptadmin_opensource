package main

import (
	"log"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/server"
)

func main() {
	if err := server.New(server.FromEnv()).ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
