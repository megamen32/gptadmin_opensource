package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseConfigReadsEnvironmentAndFlags(t *testing.T) {
	env := map[string]string{
		"NETWORK_TUNNEL_RELAY_LISTEN":              "0.0.0.0:9797",
		"NETWORK_TUNNEL_RELAY_KEY_FILE":            "/run/network-relay/key",
		"NETWORK_TUNNEL_RELAY_REPLAY_CAPACITY":     "2048",
		"NETWORK_TUNNEL_RELAY_HANDSHAKE_TIMEOUT":   "7s",
		"NETWORK_TUNNEL_RELAY_PAIR_TIMEOUT":        "45s",
		"NETWORK_TUNNEL_RELAY_WRITE_TIMEOUT":       "11s",
		"NETWORK_TUNNEL_RELAY_SHUTDOWN_TIMEOUT":    "13s",
		"NETWORK_TUNNEL_RELAY_MAX_HANDSHAKE_BYTES": "8192",
	}
	lookup := func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	}

	cfg, err := parseConfig([]string{"-listen", "127.0.0.1:8787", "-replay-capacity", "64"}, lookup)
	if err != nil {
		t.Fatalf("parseConfig() error = %v", err)
	}
	if cfg.Listen != "127.0.0.1:8787" {
		t.Fatalf("Listen = %q", cfg.Listen)
	}
	if cfg.KeyFile != env["NETWORK_TUNNEL_RELAY_KEY_FILE"] {
		t.Fatalf("KeyFile = %q", cfg.KeyFile)
	}
	if cfg.ReplayCapacity != 64 {
		t.Fatalf("ReplayCapacity = %d", cfg.ReplayCapacity)
	}
	if cfg.HandshakeTimeout != 7*time.Second || cfg.PairTimeout != 45*time.Second || cfg.WriteTimeout != 11*time.Second || cfg.ShutdownTimeout != 13*time.Second {
		t.Fatalf("timeouts = %#v", cfg)
	}
	if cfg.MaxHandshakeBytes != 8192 {
		t.Fatalf("MaxHandshakeBytes = %d", cfg.MaxHandshakeBytes)
	}
}

func TestParseConfigRequiresKeyFile(t *testing.T) {
	_, err := parseConfig(nil, func(string) (string, bool) { return "", false })
	if err == nil || !strings.Contains(err.Error(), "key-file") {
		t.Fatalf("parseConfig() error = %v, want required key-file error", err)
	}
}

func TestLoadKeyTrimsTrailingWhitespaceAndRejectsShortKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "relay.key")
	key := strings.Repeat("k", 32)
	if err := os.WriteFile(path, []byte(key+" \t\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadKey(path)
	if err != nil {
		t.Fatalf("loadKey() error = %v", err)
	}
	if string(got) != key {
		t.Fatalf("loadKey() = %q, want %q", got, key)
	}

	shortPath := filepath.Join(dir, "short.key")
	if err := os.WriteFile(shortPath, []byte("too-short\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadKey(shortPath); err == nil {
		t.Fatal("loadKey() accepted a short key")
	}
}

func TestRunServesRelaySurfaceAndStopsWithContext(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "relay.key")
	if err := os.WriteFile(keyPath, []byte(strings.Repeat("k", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := reserved.Addr().String()
	if err := reserved.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logOutput := make(chan string, 2)
	logger := log.New(channelWriter{output: logOutput}, "", 0)
	runResult := make(chan error, 1)
	go func() {
		runResult <- run(ctx, config{
			Listen:            address,
			KeyFile:           keyPath,
			ReplayCapacity:    16,
			HandshakeTimeout:  time.Second,
			PairTimeout:       time.Second,
			WriteTimeout:      time.Second,
			ShutdownTimeout:   time.Second,
			MaxHandshakeBytes: 4096,
		}, logger)
	}()

	select {
	case line := <-logOutput:
		if !strings.Contains(line, "relay listening on "+address) {
			t.Fatalf("startup log = %q", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("relay did not report listening")
	}
	response, err := http.Get(fmt.Sprintf("http://%s/v1/stream/client", address))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusUpgradeRequired {
		response.Body.Close()
		t.Fatalf("GET stream endpoint status = %d", response.StatusCode)
	}
	response.Body.Close()

	cancel()
	select {
	case err := <-runResult:
		if err != nil {
			t.Fatalf("run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("relay did not stop after context cancellation")
	}
}

type channelWriter struct {
	output chan<- string
}

func (writer channelWriter) Write(data []byte) (int, error) {
	writer.output <- string(data)
	return len(data), nil
}
