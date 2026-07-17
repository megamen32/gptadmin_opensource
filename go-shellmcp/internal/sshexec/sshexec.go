// Package sshexec is a thin Go port of the Python shellmcp_ssh
// (paramiko) backend. It exists so that the go-shellmcp server can
// dispatch shell commands to a remote host over SSH when SSH_HOST is
// set in the environment.
//
// Mirrors env vars from client/shellmcp_ssh.py:
//
//	SSH_HOST               (required; empty -> disabled)
//	SSH_PORT               (default 22)
//	SSH_USER               (default: unset, must be supplied)
//	SSH_KEY_FILE / SSH_KEY (path to a PEM-encoded private key)
//	SSH_PASSWORD           (fallback when no key is present)
//	SSH_KNOWN_HOSTS_FILE   (path to an OpenSSH known_hosts file)
//	SSH_TIMEOUT_S          (per-call timeout in seconds; default 300)
//
// Auth precedence matches Python: key file (or password-locked key)
// wins if configured, else plain password. SSH agent and automatic
// ~/.ssh lookup are NOT consulted (mirrors paramiko's
// allow_agent=False, look_for_keys=False).
//
// Connection model: v1 opens a fresh SSH connection per Run call. This
// keeps the implementation small and avoids surprises with long-lived
// sockets in a tool that already has its own retry layer upstream. A
// small idle-connection pool can be added later if profiling shows
// dialing dominates latency.
package sshexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Result is what Run returns. Stdout / Stderr are raw UTF-8 strings
// decoded from the SSH channel (lossy like the Python reference);
// ReturnCode is the remote process exit code, or 124 if the call
// timed out before the remote side finished.
type Result struct {
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ReturnCode int    `json:"returncode"`
	Error      string `json:"error,omitempty"`
	TimedOut   bool   `json:"timed_out,omitempty"`
	DurationMS int    `json:"duration_ms,omitempty"`
}

// Config is the resolved, validated set of connection parameters.
// Construct it directly (tests) or via ConfigFromEnv (production).
type Config struct {
	Host       string
	Port       int
	User       string
	KeyPath    string
	Password   string
	KnownHosts string
	Timeout    time.Duration
}

// envLookup is factored so ConfigFromEnv can be driven from a
// t.Setenv-friendly fake in tests.
type envLookup func(string) (string, bool)

// ConfigFromEnv reads SSH_* env vars. An empty Host returns the zero
// Config to signal "SSH transport disabled"; callers (wiring agent)
// should check Enabled() before constructing a Client.
func ConfigFromEnv() Config {
	return configFromEnv(osLookEnv)
}

// Enabled reports whether the config targets a real host. Zero-value
// configs (no SSH_HOST) are considered disabled.
func (c Config) Enabled() bool { return c.Host != "" }

// Addr returns host:port using Config.Port (or 22 if unset).
func (c Config) Addr() string {
	port := c.Port
	if port == 0 {
		port = 22
	}
	return net.JoinHostPort(c.Host, strconv.Itoa(port))
}

func configFromEnv(get envLookup) Config {
	host, _ := get("SSH_HOST")
	if host == "" {
		return Config{}
	}

	cfg := Config{Host: host, User: readEnv(get, "SSH_USER")}

	if portStr, ok := get("SSH_PORT"); ok && portStr != "" {
		if p, err := strconv.Atoi(portStr); err == nil && p > 0 {
			cfg.Port = p
		}
	}
	if cfg.Port == 0 {
		cfg.Port = 22
	}

	// Mirror Python: SSH_KEY_FILE and SSH_KEY both resolve to the
	// key path; SSH_KEY wins if both are set.
	cfg.KeyPath = readEnv(get, "SSH_KEY_FILE")
	if k := readEnv(get, "SSH_KEY"); k != "" {
		cfg.KeyPath = k
	}

	cfg.Password = readEnv(get, "SSH_PASSWORD")
	cfg.KnownHosts = readEnv(get, "SSH_KNOWN_HOSTS_FILE")

	if t := readEnv(get, "SSH_TIMEOUT_S"); t != "" {
		if n, err := strconv.Atoi(t); err == nil && n > 0 {
			cfg.Timeout = time.Duration(n) * time.Second
		}
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 300 * time.Second
	}

	return cfg
}

func osLookEnv(k string) (string, bool) { v, ok := os.LookupEnv(k); return v, ok }

func readEnv(get envLookup, key string) string {
	v, _ := get(key)
	return v
}

// Client is an SSH-backed command runner. Safe for concurrent use;
// each call opens its own session on a freshly dialed connection.
type Client struct {
	cfg          Config
	clientConfig *ssh.ClientConfig
}

// New validates cfg and pre-resolves auth so failed-credential
// detection happens at startup (matches paramiko's eager connect).
// Pass the same Config to Run for every call.
func New(cfg Config) (*Client, error) {
	if !cfg.Enabled() {
		return nil, errors.New("sshexec: empty host (SSH transport disabled)")
	}
	if cfg.User == "" {
		return nil, errors.New("sshexec: SSH_USER must be set")
	}

	auth, err := buildAuthMethods(cfg)
	if err != nil {
		return nil, err
	}

	hostKeyCallback, err := buildHostKeyCallback(cfg)
	if err != nil {
		return nil, err
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	return &Client{
		cfg: cfg,
		clientConfig: &ssh.ClientConfig{
			User:            cfg.User,
			Auth:            auth,
			HostKeyCallback: hostKeyCallback,
			Timeout:         timeout,
		},
	}, nil
}

// Host returns the configured host (used by wiring / health checks).
func (c *Client) Host() string { return c.cfg.Host }

// dial opens a fresh SSH connection. The TCP step is ctx-aware via
// net.Dialer.DialContext; the SSH handshake uses ClientConfig.Timeout.
func (c *Client) dial(ctx context.Context) (*ssh.Client, error) {
	d := net.Dialer{Timeout: c.timeoutOrDefault()}
	tcpConn, err := d.DialContext(ctx, "tcp", c.cfg.Addr())
	if err != nil {
		return nil, fmt.Errorf("sshexec: dial %s: %w", c.cfg.Addr(), err)
	}
	sc, chans, reqs, err := ssh.NewClientConn(tcpConn, c.cfg.Addr(), c.clientConfig)
	if err != nil {
		_ = tcpConn.Close()
		return nil, fmt.Errorf("sshexec: ssh handshake to %s: %w", c.cfg.Addr(), err)
	}
	return ssh.NewClient(sc, chans, reqs), nil
}

// Run executes cmd on the remote host. The provided ctx bounds the
// dial + the remote execution; if ctx fires first, the session is
// closed and a Result with TimedOut=true and ReturnCode=124 is
// returned, mirroring the Python reference. Callers that want a
// per-request cap should apply context.WithTimeout to ctx before
// calling.
func (c *Client) Run(ctx context.Context, cmd string, timeout time.Duration) (Result, error) {
	started := time.Now()
	if cmd == "" {
		return Result{ReturnCode: -1, Error: "empty cmd", DurationMS: int(time.Since(started) / time.Millisecond)}, errors.New("empty cmd")
	}

	client, err := c.dial(ctx)
	if err != nil {
		return Result{ReturnCode: -1, Error: err.Error(), DurationMS: int(time.Since(started) / time.Millisecond)}, err
	}
	defer client.Close()

	// Bound the remote portion by both caller ctx and the supplied
	// per-request timeout (falling back to the configured default).
	effective := timeout
	if effective <= 0 {
		effective = c.timeoutOrDefault()
	}
	runCtx, cancel := context.WithTimeout(ctx, effective)
	defer cancel()

	type sessResult struct {
		sess *ssh.Session
		err  error
	}
	sessCh := make(chan sessResult, 1)
	go func() {
		s, e := client.NewSession()
		sessCh <- sessResult{sess: s, err: e}
	}()

	var sess *ssh.Session
	select {
	case <-runCtx.Done():
		// Wait for the in-flight NewSession so we can close what
		// it returns; otherwise the goroutine leaks.
		go func() {
			r := <-sessCh
			if r.err == nil {
				_ = r.sess.Close()
			}
		}()
		return timeoutResult(runCtx.Err(), time.Since(started)), nil
	case r := <-sessCh:
		if r.err != nil {
			return Result{ReturnCode: -1, Error: r.err.Error(), DurationMS: int(time.Since(started) / time.Millisecond)}, r.err
		}
		sess = r.sess
	}
	defer sess.Close()

	stdoutPipe, err := sess.StdoutPipe()
	if err != nil {
		return Result{ReturnCode: -1, Error: err.Error()}, err
	}
	stderrPipe, err := sess.StderrPipe()
	if err != nil {
		return Result{ReturnCode: -1, Error: err.Error()}, err
	}

	// Start the command on the remote side BEFORE draining pipes,
	// otherwise the remote process blocks once its channel buffers
	// fill.
	if err := sess.Start(cmd); err != nil {
		return Result{ReturnCode: -1, Error: err.Error()}, err
	}

	var (
		stdoutBuf bytes.Buffer
		stderrBuf bytes.Buffer
		wg        sync.WaitGroup
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(&stdoutBuf, stdoutPipe)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(&stderrBuf, stderrPipe)
	}()

	// Wait either for the process to exit OR for ctx to fire.
	waitCh := make(chan error, 1)
	go func() {
		waitCh <- sess.Wait()
	}()

	select {
	case waitErr := <-waitCh:
		// Drain pipe readers; they finish when the remote channel
		// closes (which happens when the remote process exits).
		wg.Wait()
		rc := 0
		if waitErr != nil {
			var exitErr *ssh.ExitError
			if errors.As(waitErr, &exitErr) {
				rc = exitErr.ExitStatus()
			} else {
				// Transport-level error after start: still try to
				// return whatever we captured.
				return Result{
					Stdout:     stdoutBuf.String(),
					Stderr:     stderrBuf.String(),
					ReturnCode: -1,
					Error:      waitErr.Error(),
					DurationMS: int(time.Since(started) / time.Millisecond),
				}, waitErr
			}
		}
		return Result{
			Stdout:     stdoutBuf.String(),
			Stderr:     stderrBuf.String(),
			ReturnCode: rc,
			DurationMS: int(time.Since(started) / time.Millisecond),
		}, nil

	case <-runCtx.Done():
		// Try to be polite: SIGTERM the remote process so a stray
		// `sleep 9999` doesn't linger on the server.
		_ = sess.Signal(ssh.SIGTERM)
		wg.Wait()
		return timeoutResult(runCtx.Err(), time.Since(started)), nil
	}
}

// Close is a no-op for v1 (no persistent connections). It exists so
// callers can defer c.Close() today and stay working when a pool is
// added later.
func (c *Client) Close() error { return nil }

// --- helpers ---------------------------------------------------------

func (c *Client) timeoutOrDefault() time.Duration {
	if c.cfg.Timeout > 0 {
		return c.cfg.Timeout
	}
	return 300 * time.Second
}

func timeoutResult(cause error, elapsed time.Duration) Result {
	return Result{
		ReturnCode: 124,
		Error:      fmt.Sprintf("timeout: %v", cause),
		TimedOut:   true,
		DurationMS: int(elapsed / time.Millisecond),
	}
}

// --- auth & host-key wiring -----------------------------------------

func buildAuthMethods(cfg Config) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod

	if cfg.KeyPath != "" {
		signer, err := loadSigner(cfg.KeyPath, cfg.Password)
		if err != nil {
			return nil, fmt.Errorf("sshexec: load key %q: %w", cfg.KeyPath, err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}

	if cfg.Password != "" {
		methods = append(methods, ssh.Password(cfg.Password))
	} else if cfg.KeyPath == "" {
		// Mirror Python: with no key the password-auth path is the
		// only option. paramiko passes a NULL password to ssh in
		// that case and lets the server decide. We require it
		// explicitly because ssh-go fails hard on "no auth
		// methods" otherwise.
		return nil, errors.New("sshexec: neither SSH_KEY_FILE/SSH_KEY nor SSH_PASSWORD set")
	}

	return methods, nil
}

// loadSigner loads a PEM-encoded private key, trying the supplied
// password as a passphrase when the key is encrypted.
func loadSigner(path, passphrase string) (ssh.Signer, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if passphrase != "" {
		if signer, err := ssh.ParsePrivateKeyWithPassphrase(raw, []byte(passphrase)); err == nil {
			return signer, nil
		}
	}
	return ssh.ParsePrivateKey(raw)
}

// LoadSignerFromBytes is the test-facing primitive so callers that
// generated a key in-memory can exercise the same parse path.
func LoadSignerFromBytes(pemBytes []byte, passphrase string) (ssh.Signer, error) {
	if passphrase != "" {
		if signer, err := ssh.ParsePrivateKeyWithPassphrase(pemBytes, []byte(passphrase)); err == nil {
			return signer, nil
		}
	}
	return ssh.ParsePrivateKey(pemBytes)
}

func buildHostKeyCallback(cfg Config) (ssh.HostKeyCallback, error) {
	if cfg.KnownHosts == "" {
		// Mirror Python's AutoAddPolicy stance: not safe, but the
		// tool is typically deployed in known networks. Log loudly
		// so operators notice.
		log.Printf("sshexec WARNING: no SSH_KNOWN_HOSTS_FILE configured; " +
			"host keys will be accepted without verification. " +
			"Set SSH_KNOWN_HOSTS_FILE to enable safe verification.")
		return ssh.InsecureIgnoreHostKey(), nil
	}

	callback, err := knownhosts.New(cfg.KnownHosts)
	if err != nil {
		return nil, fmt.Errorf("sshexec: parse known_hosts %q: %w", cfg.KnownHosts, err)
	}
	return callback, nil
}

// ComposeCmd builds the shell string handed to a remote Run. The
// caller (server.go) injects cwd + env at execution time so the
// package-level Run signature stays a single string. The helper is
// intentionally unexported-but-package-internal so it can be unit
// tested without an SSH server.
func ComposeCmd(cmd, cwd string, env map[string]string) string {
	return ComposeCmdForUser(cmd, cwd, env, "", "")
}

// ComposeCmdForUser applies the effective ShellMCP user even when the
// transport itself authenticates as root. A missing remote sudo capability
// fails the command instead of writing user workspaces as root.
func ComposeCmdForUser(cmd, cwd string, env map[string]string, runAsUser, transportUser string) string {
	if strings.TrimSpace(cmd) == "" {
		return ""
	}
	var b strings.Builder
	if cwd = strings.TrimSpace(cwd); cwd != "" {
		b.WriteString("cd ")
		quoteShellArg(&b, cwd)
		b.WriteString(" && ")
	}
	first := true
	for k, v := range env {
		if !first {
			b.WriteByte(' ')
		}
		first = false
		quoteShellArg(&b, k)
		b.WriteByte('=')
		quoteShellArg(&b, v)
	}
	if !first {
		b.WriteByte(' ')
	}
	b.WriteString("bash -lc ")
	quoteShellArg(&b, cmd)
	composed := b.String()
	if runAsUser == "" || runAsUser == "root" || runAsUser == transportUser {
		return composed
	}
	b.Reset()
	b.WriteString("sudo -H -u ")
	quoteShellArg(&b, runAsUser)
	b.WriteString(" -- bash -lc ")
	quoteShellArg(&b, composed)
	return b.String()
}

// quoteShellArg writes s to b wrapped in single quotes, escaping any
// embedded single quotes via the standard '\” trick. Kept as a
// package-private helper to keep allocations low in the streaming
// path.
func quoteShellArg(b *strings.Builder, s string) {
	b.WriteByte('\'')
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			b.WriteString("'\\''")
			continue
		}
		b.WriteByte(s[i])
	}
	b.WriteByte('\'')
}

// RunStream executes cmd on the remote host and forwards stdout /
// stderr / exit events to onEvent. The event payload uses the
// {"type":"stdout","data":"..."}, {"type":"stderr",...},
// {"type":"exit","code":N} shape defined by the spec.
func (c *Client) RunStream(ctx context.Context, cmd string, timeout time.Duration, onEvent func(event map[string]any)) {
	if onEvent == nil {
		onEvent = func(map[string]any) {}
	}
	res, _ := c.Run(ctx, cmd, timeout)
	// Split res.Stdout / Stderr into coarse chunks. Each gets its own
	// event so that downstream SSE / NDJSON consumers see roughly the
	// same cadence as the local shell.RunLive path.
	if res.Stdout != "" {
		onEvent(map[string]any{"type": "stdout", "data": res.Stdout})
	}
	if res.Stderr != "" {
		onEvent(map[string]any{"type": "stderr", "data": res.Stderr})
	}
	onEvent(map[string]any{
		"type":      "exit",
		"code":      res.ReturnCode,
		"timed_out": res.TimedOut,
		"error":     res.Error,
	})
}
