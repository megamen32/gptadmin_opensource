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

func (s *Server) beginCallback(id string) (context.Context, func(), bool) {
	s.callbackMu.Lock()
	defer s.callbackMu.Unlock()
	if s.callbackClosed {
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

// StopCallbacks cancels owned command groups and waits for their durable
// results before the node closes its local router. No new callback may start.
func (s *Server) StopCallbacks() {
	s.callbackMu.Lock()
	s.callbackClosed = true
	for _, cancel := range s.callbackCancels {
		cancel()
	}
	s.callbackMu.Unlock()
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
	return len(s.callbackCancels)
}

// OutboxPending reports undelivered outbox entries, the same depth the
// metrics endpoint publishes.
func (s *Server) OutboxPending() int {
	return s.storageStats().OutboxDepth
}
