// Command gptadmin-mcp-bridge exposes configured local stdio MCP children over
// owner-scoped Streamable HTTP. It is deliberately a small lifecycle host:
// Hub remains responsible for authentication, ACL, tasks and receipts.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/bridge"
	"github.com/megamen32/gptadmin/go-shellmcp/internal/supervisor"
)

const (
	defaultAddr = "127.0.0.1:25991"
	defaultIdle = 5 * time.Hour
)

func main() {
	addr := flag.String("addr", envOr("GPTADMIN_MCP_BRIDGE_ADDR", defaultAddr), "HTTP listen address")
	configPath := flag.String("config", bridgeConfigPath(), "MCP child registry JSON")
	idleTTL := flag.Duration("idle-ttl", idleTTLFromEnv(), "idle duration before a child is closed")
	bridgeKey := flag.String("bridge-key", os.Getenv("GPTADMIN_MCP_BRIDGE_KEY"), "optional local bridge key")
	flag.Parse()
	if *idleTTL <= 0 {
		log.Fatalf("idle-ttl must be positive")
	}

	children, err := loadChildren(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	host := bridge.New(bridge.Config{Children: children, IdleTTL: *idleTTL, BridgeKey: *bridgeKey})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go reapLoop(ctx, host, evictionInterval(*idleTTL))

	server := &http.Server{
		Addr:              *addr,
		Handler:           host,
		ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe() }()
	log.Printf("gptadmin-mcp-bridge listening addr=%s children=%d idle_ttl=%s", *addr, len(children), idleTTL.String())

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		if err := host.Close(shutdownCtx); err != nil {
			log.Printf("bridge child shutdown: %v", err)
		}
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			_ = host.Close(context.Background())
			log.Fatal(err)
		}
	}
}

func bridgeConfigPath() string {
	if value := strings.TrimSpace(os.Getenv("GPTADMIN_MCP_BRIDGE_CONFIG")); value != "" {
		return value
	}
	return strings.TrimSpace(os.Getenv("SHELLMCP_MCP_CONFIG"))
}

func loadChildren(path string) (map[string]bridge.Child, error) {
	agents, err := supervisor.LoadAgents(path)
	if err != nil {
		return nil, err
	}
	children := make(map[string]bridge.Child, len(agents))
	for _, agent := range agents {
		if !agent.Enabled {
			continue
		}
		transport := strings.TrimSpace(agent.Transport)
		if transport != "" && transport != "stdio" {
			// The shared ShellMCP registry may also contain remote MCPs. They
			// remain owned by ShellMCP; this process exposes local stdio entries
			// only and must not make a valid mixed registry unloadable.
			continue
		}
		if strings.TrimSpace(agent.Command) == "" {
			return nil, fmt.Errorf("bridge child %q has no command", agent.Ref)
		}
		children[agent.Ref] = bridge.Child{
			Command: agent.Command,
			Args:    append([]string(nil), agent.Args...),
			Env:     cloneStrings(agent.Env),
			Cwd:     agent.Cwd,
			User:    agent.User,
		}
	}
	return children, nil
}

func cloneStrings(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func idleTTLFromEnv() time.Duration {
	if raw := strings.TrimSpace(os.Getenv("GPTADMIN_MCP_BRIDGE_IDLE_TTL")); raw != "" {
		if value, err := time.ParseDuration(raw); err == nil {
			return value
		}
		log.Printf("invalid GPTADMIN_MCP_BRIDGE_IDLE_TTL=%q; using %s", raw, defaultIdle)
	}
	if raw := strings.TrimSpace(os.Getenv("MCP_WRAPPER_IDLE_MINUTES")); raw != "" {
		if minutes, err := strconv.Atoi(raw); err == nil && minutes > 0 {
			return time.Duration(minutes) * time.Minute
		}
		log.Printf("invalid MCP_WRAPPER_IDLE_MINUTES=%q; using %s", raw, defaultIdle)
	}
	return defaultIdle
}

func evictionInterval(idleTTL time.Duration) time.Duration {
	interval := idleTTL / 10
	if interval < time.Second {
		return time.Second
	}
	return interval
}

func reapLoop(ctx context.Context, host *bridge.Host, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := host.EvictIdle(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("bridge idle eviction: %v", err)
			}
		}
	}
}
