package sshexec

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// --- ConfigFromEnv ---------------------------------------------------

func TestConfigFromEnv_DisabledWhenNoHost(t *testing.T) {
	t.Setenv("SSH_HOST", "")
	cfg := ConfigFromEnv()
	if cfg.Enabled() {
		t.Fatalf("expected zero config to be disabled, got %+v", cfg)
	}
	if _, err := New(cfg); err == nil {
		t.Fatalf("expected New to reject empty host")
	}
}

func TestConfigFromEnv_ParsesAllFields(t *testing.T) {
	t.Setenv("SSH_HOST", "example.com")
	t.Setenv("SSH_PORT", "2222")
	t.Setenv("SSH_USER", "alice")
	t.Setenv("SSH_KEY_FILE", "/path/to/key")
	t.Setenv("SSH_KEY", "")
	t.Setenv("SSH_PASSWORD", "s3cret")
	t.Setenv("SSH_KNOWN_HOSTS_FILE", "/etc/ssh/known_hosts")
	t.Setenv("SSH_TIMEOUT_S", "45")

	cfg := ConfigFromEnv()
	if cfg.Host != "example.com" {
		t.Errorf("host: got %q", cfg.Host)
	}
	if cfg.Port != 2222 {
		t.Errorf("port: got %d", cfg.Port)
	}
	if cfg.User != "alice" {
		t.Errorf("user: got %q", cfg.User)
	}
	if cfg.KeyPath != "/path/to/key" {
		t.Errorf("key path: got %q", cfg.KeyPath)
	}
	if cfg.Password != "s3cret" {
		t.Errorf("password: got %q", cfg.Password)
	}
	if cfg.KnownHosts != "/etc/ssh/known_hosts" {
		t.Errorf("known_hosts: got %q", cfg.KnownHosts)
	}
	if cfg.Timeout.Seconds() != 45 {
		t.Errorf("timeout: got %s", cfg.Timeout)
	}
	if got := cfg.Addr(); got != "example.com:2222" {
		t.Errorf("addr: got %q", got)
	}
}

func TestConfigFromEnv_Defaults(t *testing.T) {
	t.Setenv("SSH_HOST", "h")
	t.Setenv("SSH_PORT", "")
	t.Setenv("SSH_USER", "u")
	t.Setenv("SSH_TIMEOUT_S", "")

	cfg := ConfigFromEnv()
	if cfg.Port != 22 {
		t.Errorf("default port: got %d want 22", cfg.Port)
	}
	if cfg.Timeout.Seconds() != 300 {
		t.Errorf("default timeout: got %s want 300s", cfg.Timeout)
	}
}

func TestConfigFromEnv_SSHKeyWinsOverSSHKeyFile(t *testing.T) {
	t.Setenv("SSH_HOST", "h")
	t.Setenv("SSH_USER", "u")
	t.Setenv("SSH_KEY_FILE", "/old/path")
	t.Setenv("SSH_KEY", "/new/path")

	cfg := ConfigFromEnv()
	if cfg.KeyPath != "/new/path" {
		t.Errorf("SSH_KEY should win; got %q", cfg.KeyPath)
	}
}

func TestConfigFromEnv_BadPortFallsBackToDefault(t *testing.T) {
	t.Setenv("SSH_HOST", "h")
	t.Setenv("SSH_USER", "u")
	t.Setenv("SSH_PORT", "not-a-number")
	cfg := ConfigFromEnv()
	if cfg.Port != 22 {
		t.Errorf("bad port should default to 22; got %d", cfg.Port)
	}
}

// --- New validation --------------------------------------------------

func TestNew_RejectsNoUser(t *testing.T) {
	_, err := New(Config{Host: "h"})
	if err == nil || !strings.Contains(err.Error(), "SSH_USER") {
		t.Fatalf("expected SSH_USER error, got %v", err)
	}
}

func TestNew_RejectsMissingCredentials(t *testing.T) {
	_, err := New(Config{Host: "h", User: "u"})
	if err == nil || !strings.Contains(err.Error(), "SSH_KEY_FILE") {
		t.Fatalf("expected missing-creds error, got %v", err)
	}
}

func TestNew_RejectsBadKeyFile(t *testing.T) {
	_, err := New(Config{Host: "h", User: "u", KeyPath: "/no/such/file"})
	if err == nil || !strings.Contains(err.Error(), "load key") {
		t.Fatalf("expected key-load error, got %v", err)
	}
}

func TestNew_AcceptsValidKeyFile(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "id_ed25519")
	writeEd25519Key(t, keyPath, nil)

	client, err := New(Config{Host: "h", User: "u", KeyPath: keyPath})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if client.Host() != "h" {
		t.Errorf("host() = %q", client.Host())
	}
	if client.Close() != nil {
		t.Errorf("Close should be no-op")
	}
}

func TestNew_AcceptsPassword(t *testing.T) {
	client, err := New(Config{Host: "h", User: "u", Password: "p"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if client == nil {
		t.Fatal("expected client")
	}
}

func TestNew_BadKnownHostsFile(t *testing.T) {
	// ssh/knownhosts.New opens the file lazily, but our buildHostKeyCallback
	// parses it via os.ReadFile semantics. Use an unreadable path: a file
	// that exists but contains garbage.
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad_known_hosts")
	if err := os.WriteFile(bad, []byte("this is not a known_hosts line\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := New(Config{Host: "h", User: "u", Password: "p", KnownHosts: bad})
	if err == nil {
		t.Fatalf("expected error from garbage known_hosts")
	}
}

// --- key parsing helpers --------------------------------------------

func TestLoadSignerFromBytes_Ed25519(t *testing.T) {
	_, pemBytes := generateEd25519Key(t)
	signer, err := LoadSignerFromBytes(pemBytes, "")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if signer == nil {
		t.Fatal("nil signer")
	}
}

func TestLoadSignerFromBytes_BadPEM(t *testing.T) {
	_, err := LoadSignerFromBytes([]byte("not pem"), "")
	if err == nil {
		t.Fatal("expected parse error")
	}
}

// --- helpers --------------------------------------------------------

func generateEd25519Key(t *testing.T) (ed25519.PrivateKey, []byte) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	pemBlock, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("ssh.MarshalPrivateKey: %v", err)
	}
	return priv, pem.EncodeToMemory(pemBlock)
}

func writeEd25519Key(t *testing.T, path string, passphrase []byte) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	pemBlock, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("ssh.MarshalPrivateKey: %v", err)
	}
	// passphrase parameter reserved for future use; ed25519 PEM keys
	// in this library aren't passphrase-encryptable.
	_ = passphrase
	if err := os.WriteFile(path, pem.EncodeToMemory(pemBlock), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
}

// Silence unused warnings for helpers that are referenced for parity
// with future test additions.
var _ = x509.MarshalPKCS8PrivateKey

func init() {
	// Ensure a known context helper compiles against the std lib on
	// older toolchains.
	_ = context.Background
}

// ---------- ComposeCmd unit tests ----------

func TestComposeCmd(t *testing.T) {
	t.Run("empty cmd", func(t *testing.T) {
		if got := ComposeCmd("", "/tmp", nil); got != "" {
			t.Fatalf("want empty, got %q", got)
		}
		if got := ComposeCmd("   ", "/tmp", nil); got != "" {
			t.Fatalf("whitespace-only should be empty, got %q", got)
		}
	})
	t.Run("plain command", func(t *testing.T) {
		got := ComposeCmd("echo hi", "", nil)
		if !strings.HasSuffix(got, "bash -lc 'echo hi'") {
			t.Fatalf("unexpected compose: %q", got)
		}
		if strings.Contains(got, "cd '/") {
			t.Fatalf("no cwd expected, got: %q", got)
		}
	})
	t.Run("with cwd", func(t *testing.T) {
		got := ComposeCmd("ls", "/tmp", nil)
		if !strings.Contains(got, "cd '/tmp'") {
			t.Fatalf("missing cd prefix: %q", got)
		}
		if !strings.Contains(got, "&&") {
			t.Fatalf("missing && separator: %q", got)
		}
		if !strings.HasSuffix(got, "bash -lc 'ls'") {
			t.Fatalf("bad trailing: %q", got)
		}
	})
	t.Run("with env", func(t *testing.T) {
		got := ComposeCmd("env", "", map[string]string{"FOO": "bar", "BAZ": "qux"})
		if !strings.Contains(got, "'FOO'='bar' ") {
			t.Fatalf("missing FOO env: %q", got)
		}
		if !strings.Contains(got, "'BAZ'='qux' ") {
			t.Fatalf("missing BAZ env: %q", got)
		}
		if !strings.HasSuffix(got, "bash -lc 'env'") {
			t.Fatalf("bad trailing: %q", got)
		}
	})
	t.Run("cwd plus env", func(t *testing.T) {
		got := ComposeCmd("env", "/tmp", map[string]string{"K": "V"})
		if !strings.Contains(got, "cd '/tmp'") {
			t.Fatalf("missing cd: %q", got)
		}
		if !strings.Contains(got, "'K'='V' ") {
			t.Fatalf("missing env: %q", got)
		}
		if !strings.HasSuffix(got, "bash -lc 'env'") {
			t.Fatalf("bad trailing: %q", got)
		}
	})
	t.Run("escapes embedded single quote", func(t *testing.T) {
		got := ComposeCmd("echo it's me", "", nil)
		if !strings.Contains(got, "echo it'\\''s me'") {
			t.Fatalf("quote not escaped: %q", got)
		}
	})
}

func TestComposeCmdForUserDropsRootTransportPrivileges(t *testing.T) {
	got := ComposeCmdForUser("touch marker", "/home/roomhacker", nil, "roomhacker", "root")
	if !strings.HasPrefix(got, "sudo -H -u 'roomhacker' -- bash -lc ") {
		t.Fatalf("remote root transport did not downshift user: %q", got)
	}
	if !strings.Contains(got, "touch marker") {
		t.Fatalf("wrapped command missing: %q", got)
	}
}

// ---------- Enabled truth table ----------

func TestConfigEnabledTruthTable(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want bool
	}{
		{"empty", Config{}, false},
		{"host only", Config{Host: "h"}, true},
		{"user only", Config{User: "u"}, false},
		{"both", Config{Host: "h", User: "u"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.Enabled(); got != tc.want {
				t.Fatalf("Enabled()=%v want %v", got, tc.want)
			}
		})
	}
}

// ---------- in-process SSH server ----------

// fakeServer stands up an SSH server on 127.0.0.1 that accepts a
// single hard-coded password and echoes commands through a tiny shim.
// It exists so the integration test covers the full Dial -> Run path
// without hitting the network.
type fakeServer struct {
	listener net.Listener
	addr     string
	hostKey  ssh.Signer
	password string
}

func newFakeServer(t *testing.T, password string) *fakeServer {
	t.Helper()
	hostKey, err := generateHostKey()
	if err != nil {
		t.Fatalf("host key: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &fakeServer{
		listener: ln,
		addr:     ln.Addr().String(),
		hostKey:  hostKey,
		password: password,
	}
	go s.serve()
	return s
}

func (s *fakeServer) Close() { _ = s.listener.Close() }

func (s *fakeServer) serve() {
	for {
		nconn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handle(nconn)
	}
}

func (s *fakeServer) handle(nconn net.Conn) {
	defer nconn.Close()
	config := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if string(pass) == s.password {
				return nil, nil
			}
			return nil, fmt.Errorf("bad password")
		},
	}
	config.AddHostKey(s.hostKey)
	sshConn, chans, reqs, err := ssh.NewServerConn(nconn, config)
	if err != nil {
		return
	}
	defer sshConn.Close()
	go ssh.DiscardRequests(reqs)
	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			_ = newCh.Reject(ssh.UnknownChannelType, "")
			continue
		}
		ch, requests, err := newCh.Accept()
		if err != nil {
			continue
		}
		go s.handleSession(ch, requests)
	}
}

func (s *fakeServer) handleSession(ch ssh.Channel, requests <-chan *ssh.Request) {
	for req := range requests {
		if req.Type != "exec" {
			_ = req.Reply(false, nil)
			ch.Close()
			return
		}
		payload := req.Payload
		if len(payload) < 4 {
			_ = req.Reply(false, nil)
			ch.Close()
			return
		}
		cmdLen := binary.BigEndian.Uint32(payload[:4])
		cmd := string(payload[4 : 4+cmdLen])
		_ = req.Reply(true, nil)
		s.runCommand(ch, cmd)
		ch.Close()
		return
	}
}

// runCommand replays cmd through a minimal shim. The shim understands
// the commands the tests use (echo / pwd / false / exit N) and
// returns them on the channel.
func (s *fakeServer) runCommand(ch ssh.Channel, cmd string) {
	out, errOut, rc := shimRun(stripShellWrapper(cmd))
	if out != "" {
		_, _ = io.WriteString(ch, out)
	}
	if errOut != "" {
		_, _ = ch.Stderr().Write([]byte(errOut))
	}
	_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct {
		Status uint32
	}{Status: uint32(rc)}))
}

// stripShellWrapper unwraps a `bash -lc '...'` or `cd '...' && bash -lc '...'`
// style remote command so the shim sees the inner command.
func stripShellWrapper(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	const marker = "bash -lc "
	idx := strings.LastIndex(cmd, marker)
	if idx == -1 {
		return cmd
	}
	body := cmd[idx+len(marker):]
	if !strings.HasSuffix(body, "'") || len(body) < 2 {
		return cmd
	}
	body = body[1 : len(body)-1]
	body = strings.ReplaceAll(body, `'\\''`, `'`)
	return body
}

func shimRun(cmd string) (string, string, int) {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return "", "", 0
	}
	if cmd == "false" {
		return "", "", 1
	}
	if strings.HasPrefix(cmd, "exit ") {
		var n int
		_, _ = fmt.Sscanf(cmd, "exit %d", &n)
		return "", "", n
	}
	if cmd == "pwd" {
		return "/tmp\n", "", 0
	}
	if strings.HasPrefix(cmd, "echo ") {
		arg := strings.TrimPrefix(cmd, "echo ")
		// honour trailing \n as the remote shell would
		return arg + "\n", "", 0
	}
	if cmd == "env" {
		return "", "", 0
	}
	return "", "shim: unknown: " + cmd, 127
}

func generateHostKey() (ssh.Signer, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return ssh.NewSignerFromKey(priv)
}

// TestDialAndRunAgainstFakeServer exercises the full New -> Run path
// against an in-process SSH server so the test is hermetic but still
// covers wire format, session lifecycle, and result shaping.
func TestNewAndRunAgainstFakeServer(t *testing.T) {
	fs := newFakeServer(t, "hunter2")
	defer fs.Close()
	host, portStr, _ := net.SplitHostPort(fs.addr)
	var port int
	_, _ = fmt.Sscanf(portStr, "%d", &port)

	cfg := Config{
		Host:     host,
		Port:     port,
		User:     "tester",
		Password: "hunter2",
		Timeout:  5 * time.Second,
	}
	client, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer client.Close()

	t.Run("echo via Run", func(t *testing.T) {
		res, err := client.Run(context.Background(), "echo hello", 5*time.Second)
		if err != nil {
			t.Fatalf("Run error: %v", err)
		}
		if res.ReturnCode != 0 {
			t.Fatalf("want 0 got %d", res.ReturnCode)
		}
		if !strings.Contains(res.Stdout, "hello") {
			t.Fatalf("stdout missing hello: %+v", res)
		}
	})

	t.Run("non-zero exit code surfaces", func(t *testing.T) {
		res, err := client.Run(context.Background(), "false", 5*time.Second)
		if err != nil {
			t.Fatalf("Run error: %v", err)
		}
		if res.ReturnCode == 0 {
			t.Fatalf("want nonzero return code: %+v", res)
		}
	})

	t.Run("explicit exit code propagates", func(t *testing.T) {
		res, _ := client.Run(context.Background(), "exit 42", 5*time.Second)
		if res.ReturnCode != 42 {
			t.Fatalf("want 42 got %+v", res)
		}
	})

	t.Run("RunStream emits stdout/exit events", func(t *testing.T) {
		var events []map[string]any
		var mu sync.Mutex
		client.RunStream(context.Background(), "echo streamed", 5*time.Second, func(e map[string]any) {
			mu.Lock()
			events = append(events, e)
			mu.Unlock()
		})
		if len(events) == 0 {
			t.Fatalf("no events emitted")
		}
		var gotStdout, gotExit bool
		for _, e := range events {
			switch e["type"] {
			case "stdout":
				if s, _ := e["data"].(string); strings.Contains(s, "streamed") {
					gotStdout = true
				}
			case "exit":
				gotExit = true
			}
		}
		if !gotStdout || !gotExit {
			t.Fatalf("missing events: gotStdout=%v gotExit=%v", gotStdout, gotExit)
		}
	})

	t.Run("RunStream nil callback is safe", func(t *testing.T) {
		client.RunStream(context.Background(), "echo noop", 5*time.Second, nil)
	})

	t.Run("Run empty cmd", func(t *testing.T) {
		_, err := client.Run(context.Background(), "", 5*time.Second)
		if err == nil {
			t.Fatalf("expected empty-cmd error")
		}
	})
}

// TestNewRejectsBadPasswordOnLiveServer makes sure a wrong credential
// surfaces a clear error rather than hanging the dialer. Auth
// rejection happens during the SSH handshake inside Run, since New
// only resolves auth methods (no dial).
func TestNewRejectsBadPasswordOnLiveServer(t *testing.T) {
	fs := newFakeServer(t, "rightpass")
	defer fs.Close()
	host, portStr, _ := net.SplitHostPort(fs.addr)
	var port int
	_, _ = fmt.Sscanf(portStr, "%d", &port)
	client, err := New(Config{Host: host, Port: port, User: "x", Password: "wrong", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer client.Close()
	res, _ := client.Run(context.Background(), "echo hi", 5*time.Second)
	if res.ReturnCode == 0 && res.Error == "" {
		t.Fatalf("expected auth failure, got %+v", res)
	}
}

// TestDialRefusedHost ensures Run errors are actionable when the
// port is closed. New is a no-network call (the connection opens per
// Run in v1) so the error surfaces during Run.
func TestDialRefusedHost(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no loopback listener available: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	host, portStr, _ := net.SplitHostPort(addr)
	var port int
	_, _ = fmt.Sscanf(portStr, "%d", &port)
	client, err := New(Config{Host: host, Port: port, User: "u", Password: "p", Timeout: 500 * time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer client.Close()
	res, runErr := client.Run(context.Background(), "echo hi", 1*time.Second)
	if runErr == nil && res.Error == "" {
		t.Fatalf("expected dial error")
	}
	if res.Error != "" && !strings.Contains(res.Error, "sshexec") {
		t.Fatalf("expected wrapped sshexec error, got %q", res.Error)
	}
}

// unused retention
var _ = bytes.NewBuffer
