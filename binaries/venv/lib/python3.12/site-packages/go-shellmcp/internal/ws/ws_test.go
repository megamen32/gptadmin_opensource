package ws

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------- helpers (test-only server-side frame codec) -------------------

// serverCodec writes server-to-client frames (unmasked) and reads
// client-to-server frames (masked, per RFC 6455). Used only by tests
// to drive round-trips over a net.Conn pair.
type serverCodec struct {
	br *bufio.Reader
	bw *bufio.Writer
}

func newServerCodec(c net.Conn) *serverCodec {
	return &serverCodec{br: bufio.NewReader(c), bw: bufio.NewWriter(c)}
}

func (s *serverCodec) writeFrame(opcode int, payload []byte, fin bool) error {
	var header [10]byte
	header[0] = byte(opcode & 0x0F)
	if fin {
		header[0] |= 0x80
	}
	n := len(payload)
	switch {
	case n <= 125:
		header[1] = byte(n) // not masked
	case n <= 0xFFFF:
		header[1] = 126
		binary.BigEndian.PutUint16(header[2:4], uint16(n))
	default:
		header[1] = 127
		binary.BigEndian.PutUint64(header[2:10], uint64(n))
	}
	hl := 2
	if header[1]&0x7F == 126 {
		hl = 4
	}
	if header[1]&0x7F == 127 {
		hl = 10
	}
	if _, err := s.bw.Write(header[:hl]); err != nil {
		return err
	}
	if _, err := s.bw.Write(payload); err != nil {
		return err
	}
	return s.bw.Flush()
}

func (s *serverCodec) readClientFrame() (opcode int, payload []byte, err error) {
	var h [2]byte
	if _, err = io.ReadFull(s.br, h[:]); err != nil {
		return 0, nil, err
	}
	opcode = int(h[0] & 0x0F)
	masked := h[1]&0x80 != 0
	length := int(h[1] & 0x7F)
	switch length {
	case 126:
		var ext [2]byte
		if _, err = io.ReadFull(s.br, ext[:]); err != nil {
			return 0, nil, err
		}
		length = int(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err = io.ReadFull(s.br, ext[:]); err != nil {
			return 0, nil, err
		}
		length = int(binary.BigEndian.Uint64(ext[:]))
	}
	var maskKey [4]byte
	if masked {
		if _, err = io.ReadFull(s.br, maskKey[:]); err != nil {
			return 0, nil, err
		}
	}
	payload = make([]byte, length)
	if _, err = io.ReadFull(s.br, payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= maskKey[i&3]
		}
	}
	return opcode, payload, nil
}

// ---------- 1. handshake helper -------------------------------------------

func TestComputeAcceptMatchesRFC6455Spec(t *testing.T) {
	// Spec example from RFC 6455 section 1.3:
	//   key:   dGhlIHNhbXBsZSBub25jZQ==
	//   accept: s3pPLMBiTxaQ9kYGzzhZRbK+xOo=
	key := "dGhlIHNhbXBsZSBub25jZQ=="
	got := computeAccept(key)
	want := "s3pPLMBiTxaQ9kYGzzhZRbK+xOo="
	if got != want {
		t.Fatalf("computeAccept(%q) = %q, want %q", key, got, want)
	}

	// Cross-check using stdlib crypto/sha1 directly so we know our
	// helper isn't just returning a hard-coded string.
	h := sha1.New()
	h.Write([]byte(key))
	h.Write([]byte(acceptMagic))
	if got != base64.StdEncoding.EncodeToString(h.Sum(nil)) {
		t.Fatalf("computeAccept does not match independent sha1+base64")
	}
}

// ---------- 2. httptest.Server handshake ---------------------------------

// handshakeOnlyServer handles the upgrade and then drains frames
// until the client closes, optionally invoking onFrame per received
// frame. This is the test-side counterpart to the client's Dial —
// it computes the correct Sec-WebSocket-Accept so the upgrade
// succeeds, then hands raw net.Conn access to the callback.
func handshakeOnlyServer(t *testing.T, onFrame func(*serverCodec, int, []byte) error) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "no hijacker", http.StatusInternalServerError)
			return
		}
		conn, bw, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer conn.Close()

		// r *http.Request was already fully parsed by http.Server
		// before the handler was called — use it directly. br from
		// bufio.NewReader(conn) is empty here.
		key := r.Header.Get("Sec-WebSocket-Key")
		if key == "" {
			t.Error("missing Sec-WebSocket-Key")
			return
		}
		expected := computeAccept(key)

		resp := "HTTP/1.1 101 Switching Protocols\r\n" +
			"Upgrade: websocket\r\n" +
			"Connection: Upgrade\r\n" +
			"Sec-WebSocket-Accept: " + expected + "\r\n" +
			"\r\n"
		if _, err := bw.WriteString(resp); err != nil {
			t.Errorf("write 101: %v", err)
			return
		}
		if err := bw.Flush(); err != nil {
			t.Errorf("flush 101: %v", err)
			return
		}

		br := bufio.NewReader(conn)
		sc := &serverCodec{br: br, bw: bufio.NewWriter(conn)}
		for {
			op, payload, err := sc.readClientFrame()
			if err != nil {
				return
			}
			if op == OpcodeClose {
				return
			}
			if onFrame != nil {
				if err := onFrame(sc, op, payload); err != nil {
					return
				}
			}
		}
	}))
}

// ---------- 3. net.Pipe round-trip (the real shape) -----------------------

func pipeConn(t *testing.T) (*Conn, net.Conn) {
	t.Helper()
	a, b := net.Pipe()
	return &Conn{netConn: a, br: bufio.NewReader(a), closed: make(chan struct{})}, b
}

func TestWriteThenReadMaskedTextFrame(t *testing.T) {
	client, serverSide := pipeConn(t)
	defer client.Close()
	defer serverSide.Close()

	sc := newServerCodec(serverSide)

	// Read the client frame on a goroutine since net.Pipe is
	// synchronous — the WriteMessage call would otherwise block.
	type frameResult struct {
		op      int
		payload []byte
		err     error
	}
	resCh := make(chan frameResult, 1)
	go func() {
		op, payload, err := sc.readClientFrame()
		resCh <- frameResult{op: op, payload: payload, err: err}
	}()

	if err := client.WriteMessage(OpcodeText, []byte("hello ws")); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}

	var got frameResult
	select {
	case got = <-resCh:
	case <-time.After(2 * time.Second):
		t.Fatal("server read timed out")
	}
	if got.err != nil {
		t.Fatalf("server read frame: %v", got.err)
	}
	if got.op != OpcodeText {
		t.Fatalf("want opcode 0x1, got 0x%x", got.op)
	}
	if string(got.payload) != "hello ws" {
		t.Fatalf("payload round-trip mismatch: got %q", got.payload)
	}
}

func TestWriteAppliesMaskToClientFrames(t *testing.T) {
	// Direct inspection: encode a known payload and read the raw
	// header bytes off the wire to confirm MASK bit is set.
	client, serverSide := pipeConn(t)
	defer client.Close()
	defer serverSide.Close()

	// Read in a goroutine because net.Pipe is synchronous: we have
	// to drain the full frame (header + mask + payload) so the
	// WriteMessage call returns.
	type hdrResult struct {
		hdr []byte
		err error
	}
	resCh := make(chan hdrResult, 1)
	go func() {
		// 2 byte header + 4 byte mask + N byte payload. We don't
		// care about the payload here, only the header, but we must
		// consume everything off the wire or the writer blocks.
		all := make([]byte, 2+4+len("ping"))
		_, err := io.ReadFull(serverSide, all)
		resCh <- hdrResult{hdr: all[:2], err: err}
	}()

	if err := client.WriteMessage(OpcodeText, []byte("ping")); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}

	var got hdrResult
	select {
	case got = <-resCh:
	case <-time.After(2 * time.Second):
		t.Fatal("read header timed out")
	}
	if got.err != nil {
		t.Fatalf("read header: %v", got.err)
	}
	hdr := got.hdr
	if hdr[1]&0x80 == 0 {
		t.Fatalf("client frame must have MASK bit set, got 0x%02x", hdr[1])
	}
	if hdr[1]&0x7F != 4 {
		t.Fatalf("length byte mismatch: 0x%02x", hdr[1])
	}
	if hdr[0]&0x0F != OpcodeText {
		t.Fatalf("opcode mismatch: 0x%02x", hdr[0])
	}
	if hdr[0]&0x80 == 0 {
		t.Fatalf("FIN should be set on a single-frame message")
	}
}

func TestPingFrameGetsPongResponse(t *testing.T) {
	client, serverSide := pipeConn(t)
	defer client.Close()
	defer serverSide.Close()

	sc := newServerCodec(serverSide)

	// Start a client ReadMessage in a goroutine — it should auto-pong
	// the server's ping, then time out waiting for the next data
	// frame (we use a short read deadline).
	readDone := make(chan error, 1)
	go func() {
		_, _, err := client.ReadMessage()
		readDone <- err
	}()

	// Server sends a ping with a payload. Client should auto-respond
	// with a pong carrying the same payload (echo).
	if err := sc.writeFrame(OpcodePing, []byte("ping-payload"), true); err != nil {
		t.Fatalf("server write ping: %v", err)
	}
	// Drain the pong the client sends back.
	op, payload, err := sc.readClientFrame()
	if err != nil {
		t.Fatalf("read pong: %v", err)
	}
	if op != OpcodePong {
		t.Fatalf("want pong 0x%x, got 0x%x", OpcodePong, op)
	}
	if string(payload) != "ping-payload" {
		t.Fatalf("pong payload mismatch: got %q", payload)
	}

	// The next data read on the client side should block / time out
	// (no more frames). Force a deadline so the goroutine exits.
	_ = client.netConn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	select {
	case <-readDone:
	case <-time.After(2 * time.Second):
		t.Fatal("client ReadMessage never returned")
	}
}

func TestCloseFrameClosesCleanly(t *testing.T) {
	client, serverSide := pipeConn(t)
	defer client.Close()
	defer serverSide.Close()

	sc := newServerCodec(serverSide)

	readDone := make(chan error, 1)
	go func() {
		_, _, err := client.ReadMessage()
		readDone <- err
	}()

	// Server sends a close frame. Client should respond with a close
	// frame and then ReadMessage should return ErrClosed.
	if err := sc.writeFrame(OpcodeClose, []byte{0x03, 0xE8}, true); err != nil { // 1000 normal
		t.Fatalf("server write close: %v", err)
	}
	op, _, err := sc.readClientFrame()
	if err != nil {
		t.Fatalf("read echoed close: %v", err)
	}
	if op != OpcodeClose {
		t.Fatalf("want close, got 0x%x", op)
	}

	select {
	case err := <-readDone:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("want ErrClosed, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("client ReadMessage never returned")
	}
}

func TestClientCloseSendsCloseFrame(t *testing.T) {
	client, serverSide := pipeConn(t)
	defer serverSide.Close()

	sc := newServerCodec(serverSide)

	// Server-side reader runs BEFORE Close so the close frame
	// can actually be written across the synchronous pipe.
	readFrameCh := make(chan struct {
		op  int
		err error
	}, 1)
	go func() {
		op, _, err := sc.readClientFrame()
		readFrameCh <- struct {
			op  int
			err error
		}{op: op, err: err}
	}()

	// Client-side reader for sanity: it should see ErrClosed once the
	// underlying conn is torn down.
	clientReadDone := make(chan struct{}, 1)
	go func() {
		_, _, _ = client.ReadMessage()
		clientReadDone <- struct{}{}
	}()

	if err := client.Close(); err != nil {
		t.Fatalf("client.Close: %v", err)
	}

	var got struct {
		op  int
		err error
	}
	select {
	case got = <-readFrameCh:
	case <-time.After(2 * time.Second):
		t.Fatal("server never received close frame")
	}
	if got.err != nil {
		t.Fatalf("server read close: %v", got.err)
	}
	if got.op != OpcodeClose {
		t.Fatalf("want close, got 0x%x", got.op)
	}

	// Subsequent WriteMessage should fail with ErrClosed.
	if err := client.WriteMessage(OpcodeText, []byte("x")); !errors.Is(err, ErrClosed) {
		t.Fatalf("want ErrClosed after Close, got %v", err)
	}

	select {
	case <-clientReadDone:
	case <-time.After(2 * time.Second):
		t.Fatal("client ReadMessage did not return")
	}
}

// ---------- 4. fragment rejection -----------------------------------------

func TestFragmentedMessageRejected(t *testing.T) {
	client, serverSide := pipeConn(t)
	defer client.Close()
	defer serverSide.Close()

	readDone := make(chan error, 1)
	go func() {
		_, _, err := client.ReadMessage()
		readDone <- err
	}()

	// Write a non-FIN continuation frame by hand on the server side.
	sc := newServerCodec(serverSide)
	var hdr [2]byte
	hdr[0] = byte(OpcodeText) // FIN=0
	hdr[1] = byte(len("part1"))
	if _, err := sc.bw.Write(hdr[:]); err != nil {
		t.Fatalf("write first header: %v", err)
	}
	if _, err := sc.bw.Write([]byte("part1")); err != nil {
		t.Fatalf("write first payload: %v", err)
	}
	hdr[0] = byte(OpcodeContinuation) | 0x80 // FIN=1
	hdr[1] = byte(len("part2"))
	if _, err := sc.bw.Write(hdr[:]); err != nil {
		t.Fatalf("write second header: %v", err)
	}
	if _, err := sc.bw.Write([]byte("part2")); err != nil {
		t.Fatalf("write second payload: %v", err)
	}
	if err := sc.bw.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	select {
	case err := <-readDone:
		if !errors.Is(err, ErrFragmented) {
			t.Fatalf("want ErrFragmented, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ReadMessage did not return")
	}
}

// ---------- 5. Runner dispatch --------------------------------------------

func TestRunnerDispatchesExecAndWritesResult(t *testing.T) {
	// We use a manual net.Conn pair so we don't have to mess with the
	// httptest server-side handshake from inside Runner.
	cli, srvSide := net.Pipe()
	defer srvSide.Close()

	// Server side: hand-rolled frame reader/writer.
	srvCodec := newServerCodec(srvSide)

	// Inject a fake Conn into the Runner by giving it a custom Dialer.
	// The fake Dialer hands the existing cli side to the Conn.
	captured := make(chan struct{}, 1)
	dialer := func(ctx context.Context, u string, h map[string]string) (*Conn, error) {
		c := &Conn{netConn: cli, br: bufio.NewReader(cli), closed: make(chan struct{})}
		captured <- struct{}{}
		return c, nil
	}

	execCalled := make(chan struct {
		ID  string
		Raw []byte
	}, 1)

	r := &Runner{
		URL:   "ws://unused",
		Token: "bearer-test",
		Exec: func(ctx context.Context, payload []byte) ([]byte, error) {
			execCalled <- struct {
				ID  string
				Raw []byte
			}{ID: "", Raw: payload}
			return []byte(`{"return_code":0,"stdout":"ok"}`), nil
		},
		HeartbeatInterval: time.Hour, // disable heartbeat for this test
		ReconnectDelay:    50 * time.Millisecond,
		Dialer:            dialer,
	}

	// We deliberately skip the hello exchange by leaving Hello nil.
	// Run Runner in a goroutine.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	doneRun := make(chan error, 1)
	go func() { doneRun <- r.Run(ctx) }()

	select {
	case <-captured:
	case <-time.After(2 * time.Second):
		t.Fatal("dialer never called")
	}

	// Server sends an exec envelope on srvCodec.
	inbound := []byte(`{"type":"exec","id":"abc-123","payload":{"cmd":"echo hi","cwd":"/tmp","timeout":5,"env":{}}}`)
	if err := srvCodec.writeFrame(OpcodeText, inbound, true); err != nil {
		t.Fatalf("server write exec: %v", err)
	}

	// Wait for Exec to be called.
	var ec struct {
		ID  string
		Raw []byte
	}
	select {
	case ec = <-execCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("Exec not invoked")
	}
	if !strings.Contains(string(ec.Raw), "echo hi") {
		t.Fatalf("Exec payload mismatch: %q", ec.Raw)
	}

	// Server reads back the result frame.
	op, payload, err := srvCodec.readClientFrame()
	if err != nil {
		t.Fatalf("server read result: %v", err)
	}
	if op != OpcodeText {
		t.Fatalf("want text result, got opcode 0x%x", op)
	}
	var env resultEnvelope
	if err := json.Unmarshal(payload, &env); err != nil {
		t.Fatalf("parse result envelope: %v body=%q", err, payload)
	}
	if env.Type != "result" {
		t.Fatalf("want type=result, got %q", env.Type)
	}
	if env.ID != "abc-123" {
		t.Fatalf("want id=abc-123, got %q", env.ID)
	}
	// Result is an embedded JSON object; we round-trip via json.RawMessage.
	raw, ok := env.Result.(map[string]any)
	if !ok {
		// json.Unmarshal into `any` from a json.RawMessage may produce
		// map[string]any already. If the type is json.RawMessage it
		// means our jsonRaw wrapper didn't kick in — verify the raw
		// payload contains the expected fields either way.
		body := string(payload)
		if !strings.Contains(body, `"return_code":0`) || !strings.Contains(body, `"stdout":"ok"`) {
			t.Fatalf("result body missing expected fields: %s", body)
		}
	} else {
		if rc, _ := raw["return_code"].(float64); rc != 0 {
			t.Fatalf("want return_code 0, got %v", raw["return_code"])
		}
		if stdout, _ := raw["stdout"].(string); stdout != "ok" {
			t.Fatalf("want stdout=ok, got %v", raw["stdout"])
		}
	}

	cancel()
	select {
	case <-doneRun:
	case <-time.After(2 * time.Second):
		t.Fatal("Runner did not return after cancel")
	}
}

func TestRunnerHelloRejectedClosesSession(t *testing.T) {
	cli, srvSide := net.Pipe()
	defer srvSide.Close()
	srvCodec := newServerCodec(srvSide)

	var dials atomic.Int32
	dialer := func(ctx context.Context, u string, h map[string]string) (*Conn, error) {
		dials.Add(1)
		return &Conn{netConn: cli, br: bufio.NewReader(cli), closed: make(chan struct{})}, nil
	}

	hello := []byte(`{"type":"hello","payload":{}}`)
	r := &Runner{
		URL:              "ws://unused",
		Exec:             func(ctx context.Context, p []byte) ([]byte, error) { return []byte(`{}`), nil },
		HeartbeatInterval: time.Hour,
		ReconnectDelay:    25 * time.Millisecond,
		Hello:            hello,
		Dialer:           dialer,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	doneRun := make(chan error, 1)
	go func() { doneRun <- r.Run(ctx) }()

	// Read the hello.
	op, payload, err := srvCodec.readClientFrame()
	if err != nil {
		t.Fatalf("read hello: %v", err)
	}
	if op != OpcodeText {
		t.Fatalf("want text, got 0x%x", op)
	}
	if !strings.Contains(string(payload), `"hello"`) {
		t.Fatalf("expected hello frame: %q", payload)
	}

	// Reply with ok=false.
	if err := srvCodec.writeFrame(OpcodeText, []byte(`{"ok":false,"reason":"nope"}`), true); err != nil {
		t.Fatalf("server write ack: %v", err)
	}

	// Close the server side so the next dial attempt fails fast.
	srvSide.Close()
	cli.Close()

	// Let the runner loop a couple of reconnect cycles then cancel.
	time.Sleep(150 * time.Millisecond)
	cancel()
	select {
	case <-doneRun:
	case <-time.After(2 * time.Second):
		t.Fatal("Runner did not exit")
	}
}

func TestHUBWSURLConversion(t *testing.T) {
	cases := map[string]string{
		"http://hub.local/x":        "ws://hub.local/x",
		"https://hub.local/x":       "wss://hub.local/x",
		"ws://hub.local/x":          "ws://hub.local/x",
		"wss://hub.local/x":         "wss://hub.local/x",
		"":                          "",
		"   ":                       "",
		"http://hub.local/path?q=1": "ws://hub.local/path?q=1",
	}
	for in, want := range cases {
		if got := HUBWSURL(in); got != want {
			t.Errorf("HUBWSURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// Sanity: the package-level mutexes don't deadlock under concurrent
// writes. (Run with -race.)
func TestConcurrentWriteAndRead(t *testing.T) {
	client, serverSide := pipeConn(t)
	defer client.Close()
	defer serverSide.Close()

	sc := newServerCodec(serverSide)

	const N = 20
	var wg sync.WaitGroup
	wg.Add(2)

	// Writer goroutine.
	go func() {
		defer wg.Done()
		for i := 0; i < N; i++ {
			msg := fmt.Sprintf("msg-%d", i)
			if err := client.WriteMessage(OpcodeText, []byte(msg)); err != nil {
				t.Errorf("WriteMessage: %v", err)
				return
			}
		}
	}()

	// Reader goroutine (server side).
	go func() {
		defer wg.Done()
		for i := 0; i < N; i++ {
			op, _, err := sc.readClientFrame()
			if err != nil {
				t.Errorf("server read: %v", err)
				return
			}
			if op != OpcodeText {
				t.Errorf("want text, got 0x%x", op)
			}
		}
	}()

	wg.Wait()
}

// Sanity that NewClientKey + Dial both play nicely end-to-end against
// a live httptest.Server (mirrors the production path).
func TestEndToEndDialOverHTTPServer(t *testing.T) {
	upgraded := make(chan struct{}, 1)
	srv := handshakeOnlyServer(t, func(sc *serverCodec, op int, payload []byte) error {
		// Echo.
		return sc.writeFrame(OpcodeText, payload, true)
	})
	defer srv.Close()
	_ = upgraded

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, err := Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), map[string]string{
		"Authorization": "Bearer test-token",
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()

	if err := c.WriteMessage(OpcodeText, []byte("ping")); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
	_ = c.netConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	mt, payload, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if mt != OpcodeText {
		t.Fatalf("want text, got 0x%x", mt)
	}
	if string(payload) != "ping" {
		t.Fatalf("payload mismatch: %q", payload)
	}
}