package hub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

// AdoptSystemdTCPListener adopts systemd's exact single TCP descriptor. Any
// partial or malformed activation metadata is fail-closed: falling back to a
// second bind while PID 1 owns the public socket would make update behavior
// ambiguous and can create two writers.
func AdoptSystemdTCPListener() (net.Listener, bool, error) {
	pidText := os.Getenv("LISTEN_PID")
	fdsText := os.Getenv("LISTEN_FDS")
	namesText := os.Getenv("LISTEN_FDNAMES")
	if pidText == "" && fdsText == "" && namesText == "" {
		return nil, false, nil
	}
	pid, err := strconv.Atoi(pidText)
	if err != nil || pid != os.Getpid() {
		return nil, true, fmt.Errorf("invalid LISTEN_PID")
	}
	fds, err := strconv.Atoi(fdsText)
	if err != nil || fds != 1 {
		return nil, true, fmt.Errorf("expected exactly one LISTEN_FDS, got %q", fdsText)
	}
	if len(namesText) > 128 {
		return nil, true, fmt.Errorf("LISTEN_FDNAMES exceeds byte limit")
	}
	for _, r := range namesText {
		if r == ':' || r == '\n' || r == '\r' || r == 0 {
			return nil, true, fmt.Errorf("expected one bounded LISTEN_FDNAMES entry")
		}
	}
	file := os.NewFile(uintptr(3), "systemd-listener")
	if file == nil {
		return nil, true, errors.New("systemd listener fd 3 unavailable")
	}
	listener, err := net.FileListener(file)
	_ = file.Close()
	if err != nil {
		return nil, true, fmt.Errorf("adopt systemd listener: %w", err)
	}
	if _, ok := listener.(*net.TCPListener); !ok {
		_ = listener.Close()
		return nil, true, fmt.Errorf("systemd listener is not TCP")
	}
	for _, name := range []string{"LISTEN_PID", "LISTEN_FDS", "LISTEN_FDNAMES"} {
		_ = os.Unsetenv(name)
	}
	return listener, true, nil
}

type deadlineListener interface {
	SetDeadline(time.Time) error
}

// handoffListener keeps the inherited descriptor open while temporarily
// stopping this process from accepting new connections. PID 1 still owns the
// master socket, so new TCP connections wait in its backlog for the successor.
type handoffListener struct {
	net.Listener
	mu       sync.Mutex
	paused   bool
	closed   bool
	pauseAck chan struct{}
	resumeCh chan struct{}
	closeCh  chan struct{}
}

func newHandoffListener(listener net.Listener) *handoffListener {
	return &handoffListener{
		Listener: listener,
		resumeCh: make(chan struct{}),
		closeCh:  make(chan struct{}),
	}
}

func signalChannel(ch chan struct{}) {
	if ch == nil {
		return
	}
	select {
	case <-ch:
	default:
		close(ch)
	}
}

func (l *handoffListener) Accept() (net.Conn, error) {
	for {
		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()
			return nil, net.ErrClosed
		}
		if l.paused {
			ack, resume, closed := l.pauseAck, l.resumeCh, l.closeCh
			signalChannel(ack)
			l.mu.Unlock()
			select {
			case <-resume:
				continue
			case <-closed:
				return nil, net.ErrClosed
			}
		}
		l.mu.Unlock()

		conn, err := l.Listener.Accept()
		if err != nil {
			l.mu.Lock()
			paused, closed := l.paused, l.closed
			l.mu.Unlock()
			if closed {
				return nil, net.ErrClosed
			}
			if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
				// The pause deadline wakes a blocked Accept. Loop into the
				// paused gate; after Resume the deadline has been cleared.
				continue
			}
			if paused {
				continue
			}
			return nil, err
		}
		// One connection may win the pause/deadline race. It was accepted
		// before the accept loop acknowledged the fence and is allowed to
		// complete on the old process; the next Accept closes pauseAck.
		return conn, nil
	}
}

func (l *handoffListener) Pause(ctx context.Context) error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return net.ErrClosed
	}
	if l.paused {
		ack, closed := l.pauseAck, l.closeCh
		l.mu.Unlock()
		select {
		case <-ack:
			return nil
		case <-closed:
			return net.ErrClosed
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	setter, ok := l.Listener.(deadlineListener)
	if !ok {
		l.mu.Unlock()
		return errors.New("activated listener does not support deadlines")
	}
	l.paused = true
	l.pauseAck = make(chan struct{})
	l.resumeCh = make(chan struct{})
	ack, closed := l.pauseAck, l.closeCh
	l.mu.Unlock()

	if err := setter.SetDeadline(time.Now()); err != nil {
		_ = l.Resume()
		return fmt.Errorf("wake listener accept: %w", err)
	}
	select {
	case <-ack:
		return nil
	case <-closed:
		return net.ErrClosed
	case <-ctx.Done():
		_ = l.Resume()
		return ctx.Err()
	}
}

func (l *handoffListener) Resume() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return net.ErrClosed
	}
	if !l.paused {
		l.mu.Unlock()
		return nil
	}
	l.paused = false
	l.pauseAck = nil
	resume := l.resumeCh
	l.mu.Unlock()

	var clearErr error
	if setter, ok := l.Listener.(deadlineListener); ok {
		clearErr = setter.SetDeadline(time.Time{})
	}
	signalChannel(resume)
	if clearErr != nil {
		return fmt.Errorf("clear listener deadline: %w", clearErr)
	}
	return nil
}

func (l *handoffListener) Close() error {
	l.mu.Lock()
	if !l.closed {
		l.closed = true
		signalChannel(l.closeCh)
		signalChannel(l.resumeCh)
		signalChannel(l.pauseAck)
	}
	l.mu.Unlock()
	if setter, ok := l.Listener.(deadlineListener); ok {
		_ = setter.SetDeadline(time.Now())
	}
	return l.Listener.Close()
}

func (l *handoffListener) Paused() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.paused
}

type handoffState uint8

const (
	handoffIdle handoffState = iota
	handoffPreparing
	handoffReady
)

func (state handoffState) String() string {
	switch state {
	case handoffPreparing:
		return "preparing"
	case handoffReady:
		return "ready"
	default:
		return "idle"
	}
}

type nodeHandoff struct {
	mu            sync.Mutex
	listener      *handoffListener
	state         handoffState
	generation    uint64
	operationID   string
	leaseID       string
	expiry        time.Time
	timer         *time.Timer
	done          chan struct{}
	setKeepAlives func(bool)
}

func (s *Server) EnableNodeHandoff(listener net.Listener) net.Listener {
	handoff := &nodeHandoff{listener: newHandoffListener(listener), state: handoffIdle}
	s.handoff = handoff
	return handoff.listener
}

func (s *Server) setNodeHandoffHTTPServer(server *http.Server) {
	if s.handoff == nil {
		return
	}
	s.handoff.mu.Lock()
	s.handoff.setKeepAlives = server.SetKeepAlivesEnabled
	s.handoff.mu.Unlock()
}

type nodeHandoffRequest struct {
	Action       string `json:"action"`
	Reason       string `json:"reason"`
	WaitSeconds  int    `json:"wait_seconds"`
	LeaseSeconds int    `json:"lease_seconds"`
	OperationID  string `json:"operation_id"`
	LeaseID      string `json:"lease_id"`
}

type nodeHandoffResponse struct {
	SocketInherited bool              `json:"socket_inherited"`
	Ready           bool              `json:"ready"`
	State           string            `json:"state"`
	OperationID     string            `json:"operation_id,omitempty"`
	LeaseID         string            `json:"lease_id,omitempty"`
	ExpiresAt       string            `json:"expires_at,omitempty"`
	Counters        NodeDrainCounters `json:"counters"`
	Drain           NodeDrainStatus   `json:"drain"`
}

type handoffSnapshot struct {
	state       handoffState
	operationID string
	leaseID     string
	expiry      time.Time
	done        chan struct{}
	generation  uint64
}

func (h *nodeHandoff) snapshot() handoffSnapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	return handoffSnapshot{
		state:       h.state,
		operationID: h.operationID,
		leaseID:     h.leaseID,
		expiry:      h.expiry,
		done:        h.done,
		generation:  h.generation,
	}
}

func validHandoffOperationID(value string) bool {
	if len(value) < 8 || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '.' || r == '_' || r == ':' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func clampSeconds(value, fallback, minimum, maximum int) int {
	if value == 0 {
		return fallback
	}
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}

func (s *Server) handoffResponse() nodeHandoffResponse {
	snapshot := s.handoff.snapshot()
	drain := s.NodeDrainStatus()
	expires := ""
	if !snapshot.expiry.IsZero() {
		expires = snapshot.expiry.Format(time.RFC3339Nano)
	}
	return nodeHandoffResponse{
		SocketInherited: true,
		// Ready means the writer is fenced (listener paused, keep-alives
		// closed) and the successor may take the socket. It deliberately does
		// not require drain readiness: the executor shares this listener, so
		// drain counters can never reach zero during a handoff.
		Ready:       snapshot.state == handoffReady,
		State:       snapshot.state.String(),
		OperationID: snapshot.operationID,
		LeaseID:     snapshot.leaseID,
		ExpiresAt:   expires,
		Counters:    drain.Counters,
		Drain:       drain,
	}
}

func (s *Server) cancelNodeHandoff(generation uint64) bool {
	handoff := s.handoff
	handoff.mu.Lock()
	if generation != 0 && handoff.generation != generation {
		handoff.mu.Unlock()
		return false
	}
	if handoff.state == handoffIdle {
		handoff.mu.Unlock()
		return false
	}
	if handoff.timer != nil {
		handoff.timer.Stop()
		handoff.timer = nil
	}
	done := handoff.done
	keepAlives := handoff.setKeepAlives

	// Keep the handoff state non-idle until the listener is reopened, so a
	// concurrent begin cannot start a new generation while this cancellation
	// is still releasing the pause.
	_ = handoff.listener.Resume()
	if keepAlives != nil {
		keepAlives(true)
	}
	handoff.state = handoffIdle
	handoff.operationID = ""
	handoff.leaseID = ""
	handoff.expiry = time.Time{}
	handoff.done = nil
	handoff.mu.Unlock()

	signalChannel(done)
	return true
}

func (s *Server) waitExistingHandoff(ctx context.Context, operationID string, snapshot handoffSnapshot) (nodeHandoffResponse, int) {
	select {
	case <-snapshot.done:
		response := s.handoffResponse()
		if response.State == handoffReady.String() && response.OperationID == operationID {
			return response, http.StatusOK
		}
		return response, http.StatusConflict
	case <-ctx.Done():
		return s.handoffResponse(), http.StatusRequestTimeout
	}
}

func (s *Server) beginNodeHandoff(r *http.Request, req nodeHandoffRequest) (nodeHandoffResponse, int, string) {
	if !validHandoffOperationID(req.OperationID) {
		return s.handoffResponse(), http.StatusBadRequest, "operation_id must be 8..128 safe characters"
	}
	waitSeconds := clampSeconds(req.WaitSeconds, 30, 1, 300)
	leaseSeconds := clampSeconds(req.LeaseSeconds, 60, 1, 3600)

	handoff := s.handoff
	handoff.mu.Lock()
	switch handoff.state {
	case handoffReady:
		if handoff.operationID == req.OperationID {
			handoff.mu.Unlock()
			return s.handoffResponse(), http.StatusOK, ""
		}
		handoff.mu.Unlock()
		return s.handoffResponse(), http.StatusConflict, "another handoff is ready"
	case handoffPreparing:
		if handoff.operationID != req.OperationID {
			handoff.mu.Unlock()
			return s.handoffResponse(), http.StatusConflict, "another handoff is preparing"
		}
		snapshot := handoffSnapshot{
			state:       handoff.state,
			operationID: handoff.operationID,
			done:        handoff.done,
			generation:  handoff.generation,
		}
		handoff.mu.Unlock()
		response, status := s.waitExistingHandoff(r.Context(), req.OperationID, snapshot)
		return response, status, ""
	case handoffIdle:
		handoff.generation++
		generation := handoff.generation
		handoff.state = handoffPreparing
		handoff.operationID = req.OperationID
		handoff.done = make(chan struct{})
		keepAlives := handoff.setKeepAlives
		handoff.mu.Unlock()

		if keepAlives != nil {
			keepAlives(false)
		}
		// The pause alone fences this writer: PID 1 still owns the listening
		// socket, so new connections queue in its backlog and the successor
		// serves them. Deliberately NO drain here: the unified node's executor
		// dials this same listener for polls and results, so a paused listener
		// wedges the executor and ready_to_stop can never be observed (live
		// canary 2026-11-10). In-flight work finishes under the graceful stop;
		// durable queued work and queued result deliveries are completed by
		// the successor from the shared task store.
		pauseCtx, cancelPause := context.WithTimeout(context.Background(), time.Duration(waitSeconds)*time.Second)
		err := handoff.listener.Pause(pauseCtx)
		cancelPause()
		if err != nil {
			s.cancelNodeHandoff(generation)
			return s.handoffResponse(), http.StatusServiceUnavailable, "listener pause failed: " + err.Error()
		}
		handoff.mu.Lock()
		stillPreparing := handoff.generation == generation && handoff.state == handoffPreparing && handoff.operationID == req.OperationID
		var done chan struct{}
		if stillPreparing {
			var random [16]byte
			if _, err := rand.Read(random[:]); err != nil {
				handoff.mu.Unlock()
				s.cancelNodeHandoff(generation)
				return s.handoffResponse(), http.StatusInternalServerError, "lease generation failed"
			}
			handoff.state = handoffReady
			handoff.leaseID = hex.EncodeToString(random[:])
			handoff.expiry = time.Now().Add(time.Duration(leaseSeconds) * time.Second)
			handoff.timer = time.AfterFunc(time.Duration(leaseSeconds)*time.Second, func() {
				s.cancelNodeHandoff(generation)
			})
			done = handoff.done
		}
		handoff.mu.Unlock()
		if done == nil {
			return s.handoffResponse(), http.StatusConflict, "handoff was canceled"
		}
		signalChannel(done)
		return s.handoffResponse(), http.StatusOK, ""
	default:
		handoff.mu.Unlock()
		return s.handoffResponse(), http.StatusInternalServerError, "invalid handoff state"
	}
}

func (s *Server) cancelNodeHandoffRequest(req nodeHandoffRequest) (nodeHandoffResponse, int, string) {
	snapshot := s.handoff.snapshot()
	switch snapshot.state {
	case handoffIdle:
		return s.handoffResponse(), http.StatusOK, ""
	case handoffPreparing:
		if req.OperationID == "" || req.OperationID != snapshot.operationID {
			return s.handoffResponse(), http.StatusConflict, "operation mismatch"
		}
	case handoffReady:
		if req.LeaseID == "" || req.LeaseID != snapshot.leaseID {
			return s.handoffResponse(), http.StatusConflict, "lease mismatch"
		}
		if req.OperationID != "" && req.OperationID != snapshot.operationID {
			return s.handoffResponse(), http.StatusConflict, "operation mismatch"
		}
	}
	s.cancelNodeHandoff(snapshot.generation)
	return s.handoffResponse(), http.StatusOK, ""
}

func (s *Server) nodeHandoffHTTP(w http.ResponseWriter, r *http.Request) {
	if s.handoff == nil {
		writeJSON(w, http.StatusPreconditionFailed, map[string]any{"detail": "node handoff is unavailable without an activated listener"})
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, s.handoffResponse())
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "GET or POST required"})
		return
	}
	var req nodeHandoffRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	var response nodeHandoffResponse
	var status int
	var detail string
	switch req.Action {
	case "begin":
		response, status, detail = s.beginNodeHandoff(r, req)
	case "status":
		response, status = s.handoffResponse(), http.StatusOK
	case "cancel":
		response, status, detail = s.cancelNodeHandoffRequest(req)
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "action must be one of begin, status, cancel"})
		return
	}
	if detail != "" {
		writeJSON(w, status, map[string]any{"detail": detail, "handoff": response})
		return
	}
	writeJSON(w, status, response)
}
