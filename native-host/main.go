// gptadmin-cloudos-nativehost is a Chrome native messaging host binary for
// the GPTAdmin Cloud OS feature. It reads JSON requests from stdin using
// Chrome's native messaging protocol (4-byte LE length-prefixed), dispatches
// them to handlers, and writes JSON responses back to stdout.
//
// Usage:
//
//	go build -o gptadmin-cloudos-nativehost .
//	# Chrome launches this automatically via the native messaging manifest.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	// Redirect log output to stderr so it doesn't corrupt the native-messaging
	// stdout stream.
	log.SetOutput(os.Stderr)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	// Set up graceful shutdown on SIGTERM and SIGINT.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		sig := <-sigCh
		log.Printf("received signal %v, shutting down", sig)
		cancel()
		os.Exit(0)
	}()

	log.Println("native host started")

	// Main read-dispatch-write loop.
	for {
		select {
		case <-ctx.Done():
			log.Println("context cancelled, exiting")
			return
		default:
		}

		// Read the next message from Chrome.
		msg, err := ReadMessage(os.Stdin)
		if err != nil {
			// EOF is expected when Chrome closes the pipe — exit cleanly.
			if err.Error() == "EOF" || isEOFError(err) {
				log.Println("stdin closed, exiting")
				return
			}
			log.Printf("error reading message: %v", err)
			return
		}

		log.Printf("request: id=%q method=%q", msg.ID, msg.Method)

		// Dispatch and build the response.
		resp := DispatchMessage(ctx, msg)

		// Write the response back to Chrome.
		if err := WriteMessage(os.Stdout, resp); err != nil {
			log.Printf("error writing response: %v", err)
			return
		}
	}
}

// isEOFError checks whether the underlying error is an EOF.
func isEOFError(err error) bool {
	return err.Error() == "reading message length: EOF" ||
		err.Error() == "reading message body: EOF"
}
