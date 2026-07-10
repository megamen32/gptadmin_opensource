// Package ws implements a minimal stdlib-only WebSocket client used by
// go-shellmcp to talk to the hub's websocket transport.
//
// It targets the subset of RFC 6455 needed by the ShellMCP hub protocol:
//
//   - text (opcode 0x1) and binary (opcode 0x2) data frames
//   - ping (0x9), pong (0xA) and close (0x8) control frames
//   - client->server frames are masked (RFC 6455 section 5.3, mandatory)
//   - server->client frames are NOT masked (browser/server-side enforcement)
//   - no fragmentation: a single data message must fit in a single frame;
//     multi-frame messages are rejected with an error
//   - no compression (per-message-deflate, RFC 7692)
//   - no extensions negotiated
//
// The hand-rolled HTTP/1.1 Upgrade handshake, Sec-WebSocket-Key/Accept
// derivation and frame codec live entirely in stdlib (net, net/http,
// crypto/sha1, encoding/binary, encoding/base64) so go.mod stays clean.
//
// This package is intentionally a faithful port of the Python websocket
// transport in /home/roomhacker/gptadmin/client/shellmcp.py around lines
// 914-966 (websocket_loop). The Python hub expects:
//
//	-> {"type":"hello","payload":<Beat>}
//	<- {"ok":true,...}
//	<- {"type":"exec","id":"<tid>","payload":{"cmd":..,"cwd":..,"timeout":..,"env":..}}
//	-> {"type":"result","id":"<tid>","result":<TaskResult>}
//
// heartbeat messages may be sent as either JSON text frames or rely on
// the websocket-level ping/pong protocol.
//
// TODO: confirm against the real hub ws endpoint the exact auth scheme
// (the Python code uses HUB_URL-derived bearer; we expose it as the
// Authorization header on Dial). Confirm hello/heartbeat shapes.
package ws

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// RFC 6455 magic GUID used for Sec-WebSocket-Accept derivation.
const acceptMagic = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// Message type constants (RFC 6455 opcodes).
const (
	OpcodeContinuation = 0x0
	OpcodeText         = 0x1
	OpcodeBinary       = 0x2
	OpcodeClose        = 0x8
	OpcodePing         = 0x9
	OpcodePong         = 0xA
)

// ErrFragmented is returned when a peer sends a message split across
// multiple frames. This client does not reassemble fragments.
var ErrFragmented = errors.New("ws: fragmented messages not supported")

// ErrClosed is returned by ReadMessage/WriteMessage after Close.
var ErrClosed = errors.New("ws: connection closed")

// computeAccept derives the expected Sec-WebSocket-Accept value for the
// supplied client key per RFC 6455 section 4.2.2.
func computeAccept(key string) string {
	h := sha1.New()
	h.Write([]byte(key))
	h.Write([]byte(acceptMagic))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// newClientKey generates a random 16-byte nonce encoded as base64.
func newClientKey() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// Conn is a single websocket connection after a successful handshake.
//
// It is safe to call ReadMessage and WriteMessage from different
// goroutines (a mutex serialises writes; reads are also serialised so
// interleaved control frames don't get lost).
type Conn struct {
	netConn net.Conn
	br      *bufio.Reader

	closeOnce sync.Once
	closeErr  error

	writeMu sync.Mutex
	readMu  sync.Mutex

	closed chan struct{}
}

// Close performs the RFC 6455 closing handshake (sends a Close frame)
// and then tears down the underlying TCP connection.
//
// The close frame is written best-effort on a separate goroutine
// with a short write deadline so a dead peer cannot stall Close():
// if the peer doesn't drain the frame in 50ms we proceed to tear the
// socket down regardless. This matches the behaviour of nhooyr's
// websocket client and avoids blocking callers (including test
// harnesses) on a stuck peer.
func (c *Conn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		if !c.isClosed() {
			closePayload := make([]byte, 2)
			binary.BigEndian.PutUint16(closePayload, uint16(1000)) // normal closure

			done := make(chan struct{})
			go func() {
				_ = c.netConn.SetWriteDeadline(time.Now().Add(50 * time.Millisecond))
				_ = c.writeFrame(OpcodeClose, closePayload, true)
				_ = c.netConn.SetWriteDeadline(time.Time{})
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(50 * time.Millisecond):
			}
		}

		err = c.netConn.Close()
		close(c.closed)
	})
	return err
}

// isClosed returns whether the underlying conn has already been
// closed (we use the closed channel as the source of truth to make
// sure writeFrame skips a doomed socket).
func (c *Conn) isClosed() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

// Dial performs an HTTP/1.1 Upgrade handshake against the given ws:// or
// wss:// URL and returns a ready Conn. The optional headers map is sent
// verbatim on the upgrade request (use it for Authorization etc.).
//
// ctx cancellation aborts the dial before the handshake completes.
func Dial(ctx context.Context, wsURL string, headers map[string]string) (*Conn, error) {
	u, err := url.Parse(wsURL)
	if err != nil {
		return nil, fmt.Errorf("ws: parse url: %w", err)
	}
	if u.Scheme != "ws" && u.Scheme != "wss" {
		return nil, fmt.Errorf("ws: unsupported scheme %q (want ws or wss)", u.Scheme)
	}

	host := u.Host
	if u.Port() == "" {
		if u.Scheme == "wss" {
			host += ":443"
		} else {
			host += ":80"
		}
	}

	d := net.Dialer{Timeout: 10 * time.Second}
	rawConn, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, fmt.Errorf("ws: dial %s: %w", host, err)
	}
	c := &Conn{netConn: rawConn, br: bufio.NewReader(rawConn), closed: make(chan struct{})}

	key, err := newClientKey()
	if err != nil {
		rawConn.Close()
		return nil, fmt.Errorf("ws: generate key: %w", err)
	}

	if err := c.handshake(ctx, u, key, headers); err != nil {
		rawConn.Close()
		return nil, err
	}
	return c, nil
}

// handshake writes the Upgrade request and verifies the 101 response.
func (c *Conn) handshake(ctx context.Context, u *url.URL, key string, headers map[string]string) error {
	resource := u.RequestURI()
	if resource == "" {
		resource = "/"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "GET %s HTTP/1.1\r\n", resource)
	fmt.Fprintf(&b, "Host: %s\r\n", u.Host)
	fmt.Fprintf(&b, "Upgrade: websocket\r\n")
	fmt.Fprintf(&b, "Connection: Upgrade\r\n")
	fmt.Fprintf(&b, "Sec-WebSocket-Key: %s\r\n", key)
	fmt.Fprintf(&b, "Sec-WebSocket-Version: 13\r\n")
	for k, v := range headers {
		fmt.Fprintf(&b, "%s: %s\r\n", k, v)
	}
	b.WriteString("\r\n")

	if _, err := c.netConn.Write([]byte(b.String())); err != nil {
		return fmt.Errorf("ws: write upgrade: %w", err)
	}

	// Bound the read so ctx cancellation can interrupt the handshake.
	if dl, ok := ctx.Deadline(); ok && !time.Now().After(dl) {
		_ = c.netConn.SetDeadline(dl)
		defer c.netConn.SetDeadline(time.Time{})
	}

	resp, err := http.ReadResponse(c.br, nil)
	if err != nil {
		return fmt.Errorf("ws: read upgrade response: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusSwitchingProtocols {
		return fmt.Errorf("ws: handshake failed: HTTP %d", resp.StatusCode)
	}
	if !strings.EqualFold(resp.Header.Get("Upgrade"), "websocket") {
		return errors.New("ws: handshake missing Upgrade: websocket")
	}
	expected := computeAccept(key)
	if got := resp.Header.Get("Sec-WebSocket-Accept"); got != expected {
		return fmt.Errorf("ws: bad Sec-WebSocket-Accept: got %q want %q", got, expected)
	}
	return nil
}

// ReadMessage returns the next data frame from the peer.
//
// Ping frames are handled transparently: the caller receives a pong
// reply and ReadMessage keeps blocking for the next data frame. Close
// frames cause ReadMessage to return ErrClosed.
//
// Multi-frame (fragmented) data messages return ErrFragmented.
func (c *Conn) ReadMessage() (msgType int, data []byte, err error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()

	for {
		select {
		case <-c.closed:
			return 0, nil, ErrClosed
		default:
		}

		opcode, payload, fin, err := c.readFrame()
		if err != nil {
			return 0, nil, err
		}

		switch opcode {
		case OpcodePing:
			// Mandatory pong reply, masked from us.
			if err := c.writeFrame(OpcodePong, payload, true); err != nil {
				return 0, nil, err
			}
			continue
		case OpcodePong:
			// Ignore unsolicited pongs.
			continue
		case OpcodeClose:
			// Echo close back per RFC 6455 then tear down.
			_ = c.writeFrame(OpcodeClose, payload, true)
			c.Close()
			return 0, nil, ErrClosed
		case OpcodeText, OpcodeBinary:
			if !fin {
				return 0, nil, ErrFragmented
			}
			return opcode, payload, nil
		case OpcodeContinuation:
			return 0, nil, ErrFragmented
		default:
			return 0, nil, fmt.Errorf("ws: unsupported opcode 0x%x", opcode)
		}
	}
}

// WriteMessage sends a single data frame with the given opcode.
func (c *Conn) WriteMessage(msgType int, data []byte) error {
	switch msgType {
	case OpcodeText, OpcodeBinary:
		// ok
	default:
		return fmt.Errorf("ws: WriteMessage opcode 0x%x not allowed (use 0x1 text / 0x2 binary)", msgType)
	}
	return c.writeFrame(msgType, data, true)
}

// writeFrame encodes and writes a single frame. fin=true marks this as
// the final fragment of the message.
func (c *Conn) writeFrame(opcode int, payload []byte, fin bool) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	select {
	case <-c.closed:
		return ErrClosed
	default:
	}

	var header [14]byte // 2 base + 2 mask + 4 mask key + up to 8 len bytes
	hl := 2

	b0 := byte(opcode & 0x0F)
	if fin {
		b0 |= 0x80
	}
	header[0] = b0
	header[1] = 0 // MASK bit cleared for the header byte we'll mutate below

	n := len(payload)
	masked := true // RFC 6455: client->server MUST mask
	switch {
	case n <= 125:
		header[1] = 0x80 | byte(n)
	case n <= 0xFFFF:
		header[1] = 0x80 | 126
		binary.BigEndian.PutUint16(header[2:4], uint16(n))
		hl = 4
	default:
		header[1] = 0x80 | 127
		binary.BigEndian.PutUint64(header[2:10], uint64(n))
		hl = 10
	}

	// Generate the mask up front: it is written into the frame header
	// AND applied to the payload XOR-wise.
	var mask [4]byte
	if masked {
		if _, err := rand.Read(mask[:]); err != nil {
			return err
		}
		header[1] |= 0x80
		header[hl] = mask[0]
		header[hl+1] = mask[1]
		header[hl+2] = mask[2]
		header[hl+3] = mask[3]
		hl += 4
	}

	if _, err := c.netConn.Write(header[:hl]); err != nil {
		return err
	}
	if masked {
		buf := make([]byte, len(payload))
		for i, b := range payload {
			buf[i] = b ^ mask[i&3]
		}
		if _, err := c.netConn.Write(buf); err != nil {
			return err
		}
	} else {
		if _, err := c.netConn.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

// readFrame reads a single frame from the wire and returns its
// opcode, payload, FIN flag. Server->client frames are expected to be
// unmasked.
func (c *Conn) readFrame() (opcode int, payload []byte, fin bool, err error) {
	var h [2]byte
	if _, err = io.ReadFull(c.br, h[:]); err != nil {
		return 0, nil, false, err
	}
	fin = h[0]&0x80 != 0
	opcode = int(h[0] & 0x0F)
	masked := h[1]&0x80 != 0
	length := int(h[1] & 0x7F)

	switch length {
	case 126:
		var ext [2]byte
		if _, err = io.ReadFull(c.br, ext[:]); err != nil {
			return 0, nil, false, err
		}
		length = int(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err = io.ReadFull(c.br, ext[:]); err != nil {
			return 0, nil, false, err
		}
		length = int(binary.BigEndian.Uint64(ext[:]))
	}

	var maskKey [4]byte
	if masked {
		if _, err = io.ReadFull(c.br, maskKey[:]); err != nil {
			return 0, nil, false, err
		}
	}

	payload = make([]byte, length)
	if _, err = io.ReadFull(c.br, payload); err != nil {
		return 0, nil, false, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= maskKey[i&3]
		}
	}

	switch opcode {
	case OpcodeText, OpcodeBinary, OpcodePing, OpcodePong, OpcodeClose, OpcodeContinuation:
		// ok
	default:
		return 0, nil, false, fmt.Errorf("ws: reserved opcode 0x%x", opcode)
	}
	if opcode >= 0x3 && opcode <= 0x7 {
		return 0, nil, false, fmt.Errorf("ws: reserved opcode 0x%x", opcode)
	}
	if opcode >= 0xB && opcode <= 0xF {
		return 0, nil, false, fmt.Errorf("ws: reserved opcode 0x%x", opcode)
	}
	return opcode, payload, fin, nil
}

// Runner drives the websocket transport for a ShellMCP agent: connect
// to the hub, exchange the hello handshake, send heartbeats, and
// dispatch incoming exec messages via the supplied Exec callback.
//
// Exec receives the raw inbound JSON payload (the hub's "payload"
// object: {cmd, cwd, timeout, env}) and must return the JSON-marshalled
// result. Runner wraps it into {"type":"result","id":<id>,"result":...}
// and writes it back on the same connection.
type Runner struct {
	URL              string
	Token            string
	Exec             func(ctx context.Context, payload []byte) ([]byte, error)
	HeartbeatInterval time.Duration

	// Hello is sent immediately after the websocket upgrade as the
	// {"type":"hello","payload":...} frame. May be nil.
	Hello []byte

	// Logger is used for status messages. Defaults to log.Default().
	Logger *log.Logger

	// ReconnectDelay is the back-off between reconnect attempts after
	// a disconnect. Defaults to 5s.
	ReconnectDelay time.Duration

	// Dialer, if non-nil, is used instead of Dial (handy for tests).
	Dialer func(ctx context.Context, url string, headers map[string]string) (*Conn, error)
}

func (r *Runner) logger() *log.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return log.Default()
}

func (r *Runner) dial(ctx context.Context) (*Conn, error) {
	dial := r.Dialer
	if dial == nil {
		dial = Dial
	}
	headers := map[string]string{}
	if r.Token != "" {
		headers["Authorization"] = "Bearer " + r.Token
	}
	return dial(ctx, r.URL, headers)
}

// Run blocks until ctx is cancelled, reconnecting with back-off on
// transient failures.
func (r *Runner) Run(ctx context.Context) error {
	delay := r.ReconnectDelay
	if delay <= 0 {
		delay = 5 * time.Second
	}
	hb := r.HeartbeatInterval
	if hb <= 0 {
		hb = 30 * time.Second
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := r.session(ctx, hb)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			r.logger().Printf("ws: session ended: %v (reconnecting in %s)", err, delay)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
}

// session runs one connect/dispatch loop until the websocket closes
// or ctx is cancelled.
func (r *Runner) session(ctx context.Context, hb time.Duration) error {
	conn, err := r.dial(ctx)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	// Hello frame.
	if len(r.Hello) > 0 {
		if err := conn.WriteMessage(OpcodeText, r.Hello); err != nil {
			return fmt.Errorf("write hello: %w", err)
		}
		// Wait for the first server message as the hello ack.
		mt, ack, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read hello ack: %w", err)
		}
		if mt != OpcodeText {
			return fmt.Errorf("hello ack: unexpected opcode 0x%x", mt)
		}
		// The Python side rejects when ack.ok is falsy. We surface the
		// ack to the caller via a soft check: if it parses and has
		// ok:false we bail.
		var env struct {
			OK bool `json:"ok"`
		}
		// Best-effort parse; if it's not JSON we just continue.
		// (Use a tiny inline decoder to avoid pulling encoding/json
		// into the hot path here; but json.Unmarshal is cheap.)
		if isJSON(ack) {
			if err := decodeAck(ack, &env); err == nil && !env.OK {
				return fmt.Errorf("hello rejected: %s", string(ack))
			}
		}
		r.logger().Printf("ws: connected to %s", r.URL)
	}

	heartbeat := time.NewTicker(hb)
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-heartbeat.C:
			// Use a websocket-level ping (cheaper than a JSON
			// heartbeat text frame) and ignore errors — the read
			// loop will surface a real disconnect.
			_ = conn.writeFrame(OpcodePing, nil, true)
		default:
		}

		// Set a short read deadline so we can also service the
		// heartbeat ticker. Reset it after each successful read.
		_ = conn.netConn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		mt, raw, err := conn.ReadMessage()
		_ = conn.netConn.SetReadDeadline(time.Time{})
		if err != nil {
			if isTimeoutErr(err) {
				continue
			}
			return err
		}
		if mt != OpcodeText && mt != OpcodeBinary {
			continue
		}

		env, err := decodeEnvelope(raw)
		if err != nil {
			r.logger().Printf("ws: bad inbound envelope: %v", err)
			continue
		}
		switch env.Type {
		case "heartbeat", "hello":
			// Already handled on hello; heartbeats are advisory.
			continue
		case "exec":
			go r.handleExec(ctx, conn, env)
		default:
			r.logger().Printf("ws: unknown inbound type %q", env.Type)
		}
	}
}

// handleExec runs the user callback off the read loop and writes the
// result frame back over the same conn.
func (r *Runner) handleExec(ctx context.Context, conn *Conn, env envelope) {
	if r.Exec == nil {
		r.logger().Printf("ws: exec received but no Runner.Exec set (id=%s)", env.ID)
		_ = writeJSON(conn, resultEnvelope{Type: "result", ID: env.ID, Result: map[string]any{"error": "no exec handler"}})
		return
	}
	res, err := r.Exec(ctx, env.Payload)
	if err != nil {
		res = mustJSON(map[string]any{"error": err.Error()})
	}
	if err := writeJSON(conn, resultEnvelope{Type: "result", ID: env.ID, Result: jsonRaw(res)}); err != nil {
		r.logger().Printf("ws: write result (id=%s): %v", env.ID, err)
	}
}

// envelope is the hub's wire envelope for inbound messages.
type envelope struct {
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	Payload json.RawMessage `json:"payload"`
}

type resultEnvelope struct {
	Type   string `json:"type"`
	ID     string `json:"id"`
	Result any    `json:"result"`
}