package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAdoptSystemdTCPListenerRejectsPartialAndWrongMetadata(t *testing.T) {
	for _, test := range []struct {
		name, pid, fds, names string
	}{
		{name: "partial", pid: strconv.Itoa(os.Getpid())},
		{name: "wrong-pid", pid: "1", fds: "1"},
		{name: "multiple", pid: strconv.Itoa(os.Getpid()), fds: "2"},
		{name: "multiple-names", pid: strconv.Itoa(os.Getpid()), fds: "1", names: "one:two"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("LISTEN_PID", test.pid)
			t.Setenv("LISTEN_FDS", test.fds)
			t.Setenv("LISTEN_FDNAMES", test.names)
			if _, activated, err := AdoptSystemdTCPListener(); !activated || err == nil {
				t.Fatalf("activated=%v err=%v", activated, err)
			}
		})
	}
}

func TestAdoptSystemdTCPListenerHelper(t *testing.T) {
	mode := os.Getenv("GPTADMIN_SOCKET_HELPER")
	if mode == "" {
		return
	}
	if err := os.Setenv("LISTEN_PID", strconv.Itoa(os.Getpid())); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("LISTEN_FDS", "1"); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("LISTEN_FDNAMES", "http"); err != nil {
		t.Fatal(err)
	}
	listener, activated, err := AdoptSystemdTCPListener()
	if mode == "pipe" {
		if !activated || err == nil {
			t.Fatalf("non-TCP activation accepted: activated=%v err=%v", activated, err)
		}
		return
	}
	if err != nil || !activated {
		t.Fatalf("activation failed: activated=%v err=%v", activated, err)
	}
	defer listener.Close()
	if _, ok := listener.(*net.TCPListener); !ok {
		t.Fatalf("listener type %T", listener)
	}
	for _, name := range []string{"LISTEN_PID", "LISTEN_FDS", "LISTEN_FDNAMES"} {
		if value := os.Getenv(name); value != "" {
			t.Fatalf("%s not cleared: %q", name, value)
		}
	}
}

func TestAdoptSystemdTCPListenerFromFD3AndRejectsPipe(t *testing.T) {
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	file, err := tcp.(*net.TCPListener).File()
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	for _, test := range []struct {
		name string
		file *os.File
		mode string
	}{
		{name: "tcp", file: file, mode: "tcp"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestAdoptSystemdTCPListenerHelper$")
			cmd.Env = append(os.Environ(), "GPTADMIN_SOCKET_HELPER="+test.mode)
			cmd.ExtraFiles = []*os.File{test.file}
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("helper: %v\n%s", err, output)
			}
		})
	}

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestAdoptSystemdTCPListenerHelper$")
	cmd.Env = append(os.Environ(), "GPTADMIN_SOCKET_HELPER=pipe")
	cmd.ExtraFiles = []*os.File{reader}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pipe helper: %v\n%s", err, output)
	}
}

func TestHandoffListenerQueuesConnectionUntilResume(t *testing.T) {
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener := newHandoffListener(base)
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	acceptErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- conn
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := listener.Pause(ctx); err != nil {
		t.Fatal(err)
	}
	client, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	select {
	case conn := <-accepted:
		conn.Close()
		t.Fatal("connection accepted while paused")
	case err := <-acceptErr:
		t.Fatalf("accept failed while paused: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := listener.Resume(); err != nil {
		t.Fatal(err)
	}
	select {
	case conn := <-accepted:
		conn.Close()
	case err := <-acceptErr:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("queued connection was not accepted after resume")
	}
}

type raceListener struct {
	firstEntered chan struct{}
	releaseFirst chan struct{}
	second       chan struct{}
	closed       chan struct{}
	mu           sync.Mutex
	calls        int
	peer         net.Conn
}

func newRaceListener() *raceListener {
	return &raceListener{
		firstEntered: make(chan struct{}),
		releaseFirst: make(chan struct{}),
		second:       make(chan struct{}),
		closed:       make(chan struct{}),
	}
}

func (l *raceListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	l.calls++
	call := l.calls
	l.mu.Unlock()
	if call == 1 {
		close(l.firstEntered)
		<-l.releaseFirst
		left, right := net.Pipe()
		l.peer = right
		return left, nil
	}
	signalChannel(l.second)
	<-l.closed
	return nil, net.ErrClosed
}
func (l *raceListener) Close() error {
	signalChannel(l.closed)
	if l.peer != nil {
		_ = l.peer.Close()
	}
	return nil
}
func (l *raceListener) Addr() net.Addr              { return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1} }
func (l *raceListener) SetDeadline(time.Time) error { return nil }

func TestHandoffListenerAllowsRaceAcceptedConnectionThenFencesNextAccept(t *testing.T) {
	base := newRaceListener()
	listener := newHandoffListener(base)
	defer listener.Close()
	first := make(chan net.Conn, 1)
	go func() { conn, _ := listener.Accept(); first <- conn }()
	<-base.firstEntered

	pauseDone := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() { pauseDone <- listener.Pause(ctx) }()
	close(base.releaseFirst)
	conn := <-first
	defer conn.Close()
	select {
	case err := <-pauseDone:
		t.Fatalf("pause acknowledged before next Accept: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	secondDone := make(chan error, 1)
	go func() { _, err := listener.Accept(); secondDone <- err }()
	select {
	case err := <-pauseDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("next Accept did not acknowledge pause")
	}
	if err := listener.Resume(); err != nil {
		t.Fatal(err)
	}
	listener.Close()
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("Close did not unblock resumed Accept")
	}
}

func TestHandoffListenerCloseUnblocksPausedAccept(t *testing.T) {
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener := newHandoffListener(base)
	done := make(chan error, 1)
	go func() { _, err := listener.Accept(); done <- err }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := listener.Pause(ctx); err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not unblock paused Accept")
	}
}

func TestNodeHandoffUnavailableBeforeActivation(t *testing.T) {
	server := New(Config{CtlToken: "owner", ConfigDir: t.TempDir()})
	defer server.Close()
	req := httptest.NewRequest(http.MethodGet, "/admin/api/node/handoff", nil)
	req.Header.Set("Authorization", "Bearer owner")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusPreconditionFailed {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

type liveHandoffServer struct {
	hub      *Server
	http     *http.Server
	listener *handoffListener
	origin   string
}

func startLiveHandoffServer(t *testing.T, counters NodeRuntimeCounters) *liveHandoffServer {
	t.Helper()
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hub := New(Config{CtlToken: "owner", ConfigDir: t.TempDir()})
	wrapped := hub.EnableNodeHandoff(base)
	if counters != nil {
		hub.SetNodeRuntimeCounters(counters)
	}
	httpServer := &http.Server{Handler: hub.Handler(), ReadHeaderTimeout: time.Second}
	hub.setNodeHandoffHTTPServer(httpServer)
	go func() { _ = httpServer.Serve(wrapped) }()
	result := &liveHandoffServer{hub: hub, http: httpServer, listener: wrapped.(*handoffListener), origin: "http://" + base.Addr().String()}
	t.Cleanup(func() { _ = httpServer.Close(); _ = hub.Close() })
	return result
}

func handoffRequest(t *testing.T, client *http.Client, origin string, body map[string]any) (int, nodeHandoffResponse, []byte) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, origin+"/admin/api/node/handoff", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer owner")
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var payload bytes.Buffer
	_, _ = payload.ReadFrom(response.Body)
	var parsed nodeHandoffResponse
	if response.StatusCode == http.StatusOK {
		_ = json.Unmarshal(payload.Bytes(), &parsed)
	}
	return response.StatusCode, parsed, payload.Bytes()
}

func TestNodeHandoffReadyIsIdempotentAndLeaseExpiryResumes(t *testing.T) {
	live := startLiveHandoffServer(t, nil)
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 3 * time.Second}
	body := map[string]any{"action": "begin", "operation_id": "deploy-operation-1", "wait_seconds": 2, "lease_seconds": 1}
	status, first, raw := handoffRequest(t, client, live.origin, body)
	if status != http.StatusOK || !first.Ready || first.State != "ready" || first.LeaseID == "" {
		t.Fatalf("status=%d response=%+v raw=%s", status, first, raw)
	}
	// A response-loss retry with the same operation id returns the same receipt.
	retryReq := httptest.NewRequest(http.MethodPost, "/admin/api/node/handoff", bytes.NewReader(handoffMustJSON(t, body)))
	retryReq.Header.Set("Authorization", "Bearer owner")
	retryReq.Header.Set("Content-Type", "application/json")
	retry := httptest.NewRecorder()
	live.hub.Handler().ServeHTTP(retry, retryReq)
	var second nodeHandoffResponse
	if retry.Code != http.StatusOK || json.Unmarshal(retry.Body.Bytes(), &second) != nil || second.LeaseID != first.LeaseID {
		t.Fatalf("retry=%d %s", retry.Code, retry.Body.String())
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && live.hub.handoff.snapshot().state != handoffIdle {
		time.Sleep(10 * time.Millisecond)
	}
	if state := live.hub.handoff.snapshot().state; state != handoffIdle || live.listener.Paused() || live.hub.NodeDrainStatus().Draining {
		t.Fatalf("state=%s paused=%v drain=%+v", state, live.listener.Paused(), live.hub.NodeDrainStatus())
	}
}

// Regression (live canary 2026-11-10): the unified node's executor dials the
// same listener the handoff pauses, so drain counters can never reach zero
// during a handoff. Begin must fence the writer and go ready immediately
// instead of waiting on counters that cannot move. Handler-level requests
// bypass the paused TCP listener, mirroring the controller's own begin call.
func handlerRequest(t *testing.T, hub *Server, body map[string]any) (int, nodeHandoffResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/admin/api/node/handoff", bytes.NewReader(handoffMustJSON(t, body)))
	req.Header.Set("Authorization", "Bearer owner")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	hub.Handler().ServeHTTP(recorder, req)
	var parsed nodeHandoffResponse
	if recorder.Code == http.StatusOK {
		_ = json.Unmarshal(recorder.Body.Bytes(), &parsed)
	}
	return recorder.Code, parsed
}

func TestNodeHandoffReadyDoesNotWaitForDrainCounters(t *testing.T) {
	live := startLiveHandoffServer(t, runtimeCountersStub{callbacks: 1})
	started := time.Now()
	status, first := handlerRequest(t, live.hub, map[string]any{
		"action": "begin", "operation_id": "deploy-nowait-1", "wait_seconds": 90, "lease_seconds": 60,
	})
	if status != http.StatusOK || !first.Ready || first.State != "ready" || first.LeaseID == "" {
		t.Fatalf("status=%d response=%+v", status, first)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("begin waited on undrainable counters for %s", elapsed)
	}
	if !live.listener.Paused() {
		t.Fatal("ready handoff must hold the listener paused for the successor swap")
	}
	status, _ = handlerRequest(t, live.hub, map[string]any{
		"action": "cancel", "operation_id": "deploy-nowait-1", "lease_id": first.LeaseID,
	})
	if status != http.StatusOK || live.listener.Paused() || live.hub.handoff.snapshot().state != handoffIdle {
		t.Fatalf("cancel=%d paused=%v state=%s", status, live.listener.Paused(), live.hub.handoff.snapshot().state)
	}
}

func TestNodeHandoffCancelRequiresLeaseAndReleasesPause(t *testing.T) {
	live := startLiveHandoffServer(t, nil)
	status, first := handlerRequest(t, live.hub, map[string]any{
		"action": "begin", "operation_id": "deploy-cancel-1", "wait_seconds": 30, "lease_seconds": 120,
	})
	if status != http.StatusOK || !first.Ready || first.LeaseID == "" {
		t.Fatalf("status=%d response=%+v", status, first)
	}
	// A cancel without the exact lease must not release the paused writer.
	status, _ = handlerRequest(t, live.hub, map[string]any{
		"action": "cancel", "operation_id": "deploy-cancel-1", "lease_id": "00000000000000000000000000000000",
	})
	if status != http.StatusConflict || !live.listener.Paused() {
		t.Fatalf("wrong-lease cancel status=%d paused=%v", status, live.listener.Paused())
	}
	status, _ = handlerRequest(t, live.hub, map[string]any{
		"action": "cancel", "operation_id": "deploy-cancel-1", "lease_id": first.LeaseID,
	})
	if status != http.StatusOK || live.listener.Paused() || live.hub.handoff.snapshot().state != handoffIdle {
		t.Fatalf("cancel status=%d paused=%v state=%s", status, live.listener.Paused(),
			live.hub.handoff.snapshot().state)
	}
}

func handoffMustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestHandoffRestartPreservesManagedTokenOAuthClientAndAccessProfile(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		CtlToken:                 "ctl",
		OAuthClientSecret:        "oauth-signing-secret",
		ConfigDir:                dir,
		PublicOrigin:             "https://hub.example",
		MCPResource:              "https://hub.example",
		OAuthPermissiveRedirects: true,
		OAuthPermissiveResources: true,
	}
	first := New(cfg)
	if err := first.RegisterLocalExecutor("writer", map[string]string{"server_id": "writer-id", "public_key": "writer-key", "fingerprint": "writer-fp"}, strings.Repeat("x", 32)); err != nil {
		t.Fatal(err)
	}
	if err := first.InitializeAuthContinuity(); err != nil {
		t.Fatal(err)
	}
	token, _, err := first.issueManagedMCPToken("restart-probe", 7, cfg.PublicOrigin, cfg.MCPResource)
	if err != nil {
		t.Fatal(err)
	}
	registered := oauthInventoryRequest(t, first, http.MethodPost, "/register", "", map[string]any{"redirect_uris": []string{"https://client.example/callback"}})
	if registered.Code != http.StatusCreated {
		t.Fatalf("register=%d %s", registered.Code, registered.Body.String())
	}
	var client struct {
		ClientID string `json:"client_id"`
	}
	if err := json.Unmarshal(registered.Body.Bytes(), &client); err != nil {
		t.Fatal(err)
	}
	profile := oauthInventoryRequest(t, first, http.MethodPut, "/admin/api/access-profiles/restart-profile", cfg.CtlToken, map[string]any{
		"id": "restart-profile", "access_mode": "readonly", "allowed_targets": []string{"hub"}, "allowed_tools": []string{"discover"},
	})
	if profile.Code != http.StatusOK {
		t.Fatalf("profile=%d %s", profile.Code, profile.Body.String())
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second := New(cfg)
	defer second.Close()
	if err := second.RegisterLocalExecutor("writer", map[string]string{"server_id": "writer-id", "public_key": "writer-key", "fingerprint": "writer-fp"}, strings.Repeat("x", 32)); err != nil {
		t.Fatal(err)
	}
	if err := second.InitializeAuthContinuity(); err != nil {
		t.Fatal(err)
	}
	if got := authContinuityHTTP(second, "GET", "/mcp", token, nil); got.Code == http.StatusUnauthorized {
		t.Fatal("managed token lost on restart")
	}
	if _, ok := second.oauthClients[client.ClientID]; !ok {
		t.Fatal("OAuth client lost on restart")
	}
	if _, ok := second.accessProfiles["restart-profile"]; !ok {
		t.Fatal("access profile lost on restart")
	}
}
