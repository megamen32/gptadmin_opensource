package main

import (
	"log"

	"github.com/megamen32/gptadmin/go-hub/internal/hub"
)

func main() {
	if err := hub.New(hub.FromEnv()).ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
