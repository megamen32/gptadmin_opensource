// Command proxyrelay runs the isolated Network Tunnel stream relay.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/megamen32/gptadmin/go-proxyrelay/internal/relay"
	"github.com/megamen32/gptadmin/go-proxyrelay/internal/ticket"
)

const (
	defaultListen            = "127.0.0.1:8787"
	defaultReplayCapacity    = 4096
	defaultHandshakeTimeout  = 5 * time.Second
	defaultPairTimeout       = 30 * time.Second
	defaultWriteTimeout      = 10 * time.Second
	defaultShutdownTimeout   = 10 * time.Second
	defaultMaxHandshakeBytes = 4096
	minimumRelayKeyBytes     = 32
	maximumReplayCapacity    = 1_000_000
	maximumHandshakeBytes    = 64 * 1024
	maximumTimeout           = 24 * time.Hour
)

const (
	envListen            = "NETWORK_TUNNEL_RELAY_LISTEN"
	envKeyFile           = "NETWORK_TUNNEL_RELAY_KEY_FILE"
	envReplayCapacity    = "NETWORK_TUNNEL_RELAY_REPLAY_CAPACITY"
	envHandshakeTimeout  = "NETWORK_TUNNEL_RELAY_HANDSHAKE_TIMEOUT"
	envPairTimeout       = "NETWORK_TUNNEL_RELAY_PAIR_TIMEOUT"
	envWriteTimeout      = "NETWORK_TUNNEL_RELAY_WRITE_TIMEOUT"
	envShutdownTimeout   = "NETWORK_TUNNEL_RELAY_SHUTDOWN_TIMEOUT"
	envMaxHandshakeBytes = "NETWORK_TUNNEL_RELAY_MAX_HANDSHAKE_BYTES"
)

type config struct {
	Listen            string
	KeyFile           string
	ReplayCapacity    int
	HandshakeTimeout  time.Duration
	PairTimeout       time.Duration
	WriteTimeout      time.Duration
	ShutdownTimeout   time.Duration
	MaxHandshakeBytes int64
}

func parseConfig(args []string, lookupEnv func(string) (string, bool)) (config, error) {
	if lookupEnv == nil {
		return config{}, errors.New("environment lookup is required")
	}

	listen := envValue(lookupEnv, envListen, defaultListen)
	keyFile := envValue(lookupEnv, envKeyFile, "")
	replayCapacity, err := envInt(lookupEnv, envReplayCapacity, defaultReplayCapacity)
	if err != nil {
		return config{}, err
	}
	handshakeTimeout, err := envDuration(lookupEnv, envHandshakeTimeout, defaultHandshakeTimeout)
	if err != nil {
		return config{}, err
	}
	pairTimeout, err := envDuration(lookupEnv, envPairTimeout, defaultPairTimeout)
	if err != nil {
		return config{}, err
	}
	writeTimeout, err := envDuration(lookupEnv, envWriteTimeout, defaultWriteTimeout)
	if err != nil {
		return config{}, err
	}
	shutdownTimeout, err := envDuration(lookupEnv, envShutdownTimeout, defaultShutdownTimeout)
	if err != nil {
		return config{}, err
	}
	maxHandshakeBytes, err := envInt64(lookupEnv, envMaxHandshakeBytes, defaultMaxHandshakeBytes)
	if err != nil {
		return config{}, err
	}

	flags := flag.NewFlagSet("proxyrelay", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&listen, "listen", listen, "TCP listen address")
	flags.StringVar(&keyFile, "key-file", keyFile, "path to the relay signing key")
	flags.IntVar(&replayCapacity, "replay-capacity", replayCapacity, "maximum number of consumed ticket IDs")
	flags.DurationVar(&handshakeTimeout, "handshake-timeout", handshakeTimeout, "maximum WebSocket handshake duration")
	flags.DurationVar(&pairTimeout, "pair-timeout", pairTimeout, "maximum wait for the second stream side")
	flags.DurationVar(&writeTimeout, "write-timeout", writeTimeout, "per-write deadline")
	flags.DurationVar(&shutdownTimeout, "shutdown-timeout", shutdownTimeout, "graceful HTTP shutdown timeout")
	flags.Int64Var(&maxHandshakeBytes, "max-handshake-bytes", maxHandshakeBytes, "maximum handshake message size")
	if err := flags.Parse(args); err != nil {
		return config{}, err
	}

	cfg := config{
		Listen:            strings.TrimSpace(listen),
		KeyFile:           strings.TrimSpace(keyFile),
		ReplayCapacity:    replayCapacity,
		HandshakeTimeout:  handshakeTimeout,
		PairTimeout:       pairTimeout,
		WriteTimeout:      writeTimeout,
		ShutdownTimeout:   shutdownTimeout,
		MaxHandshakeBytes: maxHandshakeBytes,
	}
	if cfg.Listen == "" {
		return config{}, errors.New("listen address must not be empty")
	}
	if cfg.KeyFile == "" {
		return config{}, errors.New("key-file is required")
	}
	if cfg.ReplayCapacity <= 0 || cfg.ReplayCapacity > maximumReplayCapacity {
		return config{}, fmt.Errorf("replay-capacity must be between 1 and %d", maximumReplayCapacity)
	}
	if cfg.HandshakeTimeout <= 0 || cfg.HandshakeTimeout > maximumTimeout {
		return config{}, fmt.Errorf("handshake-timeout must be between 0 and %s", maximumTimeout)
	}
	if cfg.PairTimeout <= 0 || cfg.PairTimeout > maximumTimeout {
		return config{}, fmt.Errorf("pair-timeout must be between 0 and %s", maximumTimeout)
	}
	if cfg.WriteTimeout <= 0 || cfg.WriteTimeout > maximumTimeout {
		return config{}, fmt.Errorf("write-timeout must be between 0 and %s", maximumTimeout)
	}
	if cfg.ShutdownTimeout <= 0 || cfg.ShutdownTimeout > maximumTimeout {
		return config{}, fmt.Errorf("shutdown-timeout must be between 0 and %s", maximumTimeout)
	}
	if cfg.MaxHandshakeBytes <= 0 || cfg.MaxHandshakeBytes > maximumHandshakeBytes {
		return config{}, fmt.Errorf("max-handshake-bytes must be between 1 and %d", maximumHandshakeBytes)
	}
	return cfg, nil
}

func envValue(lookupEnv func(string) (string, bool), key, fallback string) string {
	if value, ok := lookupEnv(key); ok {
		return value
	}
	return fallback
}

func envInt(lookupEnv func(string) (string, bool), key string, fallback int) (int, error) {
	value := envValue(lookupEnv, key, "")
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0, fmt.Errorf("%s is invalid: %w", key, err)
	}
	return parsed, nil
}

func envInt64(lookupEnv func(string) (string, bool), key string, fallback int64) (int64, error) {
	value := envValue(lookupEnv, key, "")
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s is invalid: %w", key, err)
	}
	return parsed, nil
}

func envDuration(lookupEnv func(string) (string, bool), key string, fallback time.Duration) (time.Duration, error) {
	value := envValue(lookupEnv, key, "")
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		return 0, fmt.Errorf("%s is invalid: %w", key, err)
	}
	return parsed, nil
}

func loadKey(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read relay key file %q: %w", path, err)
	}
	data = bytes.TrimRightFunc(data, unicode.IsSpace)
	if len(data) < minimumRelayKeyBytes {
		return nil, fmt.Errorf("relay key file %q must contain at least %d bytes", path, minimumRelayKeyBytes)
	}
	return append([]byte(nil), data...), nil
}

func run(ctx context.Context, cfg config, logger *log.Logger) error {
	if ctx == nil {
		return errors.New("run context is required")
	}
	if logger == nil {
		return errors.New("run logger is required")
	}
	key, err := loadKey(cfg.KeyFile)
	if err != nil {
		return err
	}
	verifier, err := ticket.NewVerifier(key, time.Now, cfg.ReplayCapacity)
	if err != nil {
		return fmt.Errorf("create relay ticket verifier: %w", err)
	}
	relayServer, err := relay.New(relay.Config{
		Verifier:          verifier,
		HandshakeTimeout:  cfg.HandshakeTimeout,
		PairTimeout:       cfg.PairTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		MaxHandshakeBytes: cfg.MaxHandshakeBytes,
	})
	if err != nil {
		return fmt.Errorf("create relay server: %w", err)
	}

	httpServer := &http.Server{Handler: relayServer.Handler()}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		relayServer.Close()
		return fmt.Errorf("listen on %s: %w", cfg.Listen, err)
	}

	serveContext, cancel := context.WithCancel(ctx)
	defer cancel()
	shutdownDone := make(chan struct{})
	shutdownErrors := make(chan error, 1)
	go func() {
		<-serveContext.Done()
		shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer shutdownCancel()
		shutdownErrors <- httpServer.Shutdown(shutdownContext)
		relayServer.Close()
		close(shutdownDone)
	}()

	logger.Printf("relay listening on %s", listener.Addr().String())
	serveErr := httpServer.Serve(listener)
	cancel()
	<-shutdownDone
	shutdownErr := <-shutdownErrors
	if shutdownErr != nil {
		logger.Printf("relay shutdown error: %v", shutdownErr)
	}
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) && ctx.Err() == nil {
		return serveErr
	}
	logger.Printf("relay stopped")
	return shutdownErr
}

func main() {
	cfg, err := parseConfig(os.Args[1:], os.LookupEnv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "proxyrelay: %v\n", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logger := log.New(os.Stderr, "proxyrelay: ", log.LstdFlags)
	if err := run(ctx, cfg, logger); err != nil {
		logger.Printf("fatal: %v", err)
		os.Exit(1)
	}
}
