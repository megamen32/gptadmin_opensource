package server

import (
	"context"
	"fmt"
	"time"
)

// LocalIdentity returns public enrollment fields; private keys never leave the
// executor. A combined node must fail startup if identity loading failed.
func (s *Server) LocalIdentity() (string, map[string]string, error) {
	if s.identity == nil {
		return "", nil, fmt.Errorf("local executor identity unavailable")
	}
	return s.identity.Name, map[string]string{
		"server_id":   s.identity.ServerID,
		"public_key":  s.identity.PublicKey,
		"fingerprint": s.identity.Fingerprint,
	}, nil
}

func (s *Server) beginCallback(id string, acceptedPoll bool) (context.Context, func(), bool) {
	s.callbackMu.Lock()
	defer s.callbackMu.Unlock()
	if s.callbackClosed || (s.queueDraining && !acceptedPoll) {
		return nil, nil, false
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.callbackWG.Add(1)
	s.callbackCancels[id] = cancel
	if s.callbackCancelled[id] {
		cancel()
	}
	delete(s.callbackCancelled, id)
	return ctx, func() {
		cancel()
		s.callbackMu.Lock()
		delete(s.callbackCancels, id)
		s.callbackMu.Unlock()
		s.callbackWG.Done()
	}, true
}

// StopCallbacks fences new callbacks and waits for durable results before
// the node closes its local router. With ShutdownGrace set (planned stops),
// already-running commands may finish inside the grace window; only the
// stragglers are cancelled. The legacy behavior (no grace) cancels at once.
func (s *Server) StopCallbacks() {
	s.callbackMu.Lock()
	s.callbackClosed = true
	grace := s.cfg.ShutdownGrace
	if grace <= 0 {
		for _, cancel := range s.callbackCancels {
			cancel()
		}
	}
	s.callbackMu.Unlock()
	if grace > 0 {
		finished := make(chan struct{})
		go func() {
			s.callbackWG.Wait()
			close(finished)
		}()
		select {
		case <-finished:
		case <-time.After(grace):
			s.callbackMu.Lock()
			for _, cancel := range s.callbackCancels {
				cancel()
			}
			s.callbackMu.Unlock()
		}
	}
	s.callbackWG.Wait()
}

func (s *Server) callbackResultContext() (context.Context, context.CancelFunc) {
	if s.cfg.DisableSelfUpdate {
		return context.WithTimeout(context.Background(), 5*time.Second)
	}
	return context.WithCancel(context.Background())
}

// ActiveCallbacks reports in-flight executor callbacks under callbackMu.
func (s *Server) ActiveCallbacks() int {
	s.callbackMu.Lock()
	defer s.callbackMu.Unlock()
	return len(s.callbackCancels) + s.queuePollsInFlight
}

// DirectChildTools reads a child MCP schema without enqueueing a ShellMCP
// callback or durable GPTAdmin Job. The caller (Hub) authorizes the target and
// connection; mcpChildTools still validates the child's generation/scope and
// enforces its tool allowlist.
func (s *Server) DirectChildTools(ctx context.Context, args map[string]any) (map[string]any, error) {
	return s.mcpChildTools(ctx, args)
}

// OutboxPending reports undelivered outbox entries, the same depth the
// metrics endpoint publishes.
func (s *Server) OutboxPending() int {
	return s.storageStats().OutboxDepth
}

// SetQueueDraining closes poll admission under the same lock used to count
// polls, so accepted responses remain visible until their callback is reserved.
func (s *Server) SetQueueDraining(draining bool) {
	s.callbackMu.Lock()
	s.queueDraining = draining
	if !draining {
		s.queueDrainPending = false
	}
	s.callbackMu.Unlock()
}

// SetQueueDrainPending permits polling for already admitted work while
// direct callbacks remain fenced. Each poll remains visible to drain.
func (s *Server) SetQueueDrainPending(pending bool) {
	s.callbackMu.Lock()
	s.queueDrainPending = s.queueDraining && pending
	s.callbackMu.Unlock()
}

func (s *Server) beginQueuePoll() bool {
	s.callbackMu.Lock()
	defer s.callbackMu.Unlock()
	if (s.queueDraining && !s.queueDrainPending) || s.callbackClosed {
		return false
	}
	s.queuePollsInFlight++
	return true
}
func (s *Server) endQueuePoll() {
	s.callbackMu.Lock()
	defer s.callbackMu.Unlock()
	s.queuePollsInFlight--
}
