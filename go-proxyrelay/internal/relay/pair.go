package relay

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/megamen32/gptadmin/go-proxyrelay/internal/ticket"
)

const defaultResetWriteTimeout = 250 * time.Millisecond

type frameConn interface {
	Read(context.Context, int64) (Frame, error)
	Write(context.Context, Frame) error
	Close()
}

type pairEvent struct {
	kind   pairEventKind
	reason string
}

type pairEventKind uint8

const (
	pairEventFIN pairEventKind = iota + 1
	pairEventReset
)

type streamPair struct {
	claims       ticket.Claims
	client       frameConn
	agent        frameConn
	audit        AuditFunc
	onStop       func()
	resetTimeout time.Duration
	resetCh      chan string
	stopOnce     sync.Once
	maxQueue     atomic.Int64
	bytes        atomic.Int64
	lastActivity atomic.Int64
	bandwidthMu  sync.Mutex
	nextWriteAt  time.Time
}

func newStreamPair(claims ticket.Claims, client, agent frameConn, audit AuditFunc, onStop func()) *streamPair {
	if onStop == nil {
		onStop = func() {}
	}
	pair := &streamPair{
		claims:       claims,
		client:       client,
		agent:        agent,
		audit:        audit,
		onStop:       onStop,
		resetTimeout: defaultResetWriteTimeout,
		resetCh:      make(chan string, 1),
	}
	pair.lastActivity.Store(time.Now().UnixNano())
	pair.emit("pair_created", "", "")
	return pair
}

func (p *streamPair) run(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	defer p.client.Close()
	defer p.agent.Close()
	defer p.onStop()

	events := make(chan pairEvent, 8)
	clientToAgent := make(chan Frame, p.claims.Limits.MaxPendingFrames)
	agentToClient := make(chan Frame, p.claims.Limits.MaxPendingFrames)

	go p.readDirection(ctx, p.client, clientToAgent, events)
	go p.readDirection(ctx, p.agent, agentToClient, events)
	go p.writeDirection(ctx, p.agent, clientToAgent, events)
	go p.writeDirection(ctx, p.client, agentToClient, events)

	idleTick := time.NewTicker(minDuration(50*time.Millisecond, time.Duration(p.claims.Limits.IdleTimeoutSeconds)*time.Second/4))
	defer idleTick.Stop()
	lifetime := time.NewTimer(time.Duration(p.claims.Limits.MaxStreamLifetimeSeconds) * time.Second)
	defer lifetime.Stop()

	finishedDirections := 0
	for {
		select {
		case <-ctx.Done():
			return
		case reason := <-p.resetCh:
			p.writeReset(reason)
			return
		case event := <-events:
			switch event.kind {
			case pairEventFIN:
				finishedDirections++
				if finishedDirections == 2 {
					return
				}
			case pairEventReset:
				p.writeReset(event.reason)
				return
			}
		case <-idleTick.C:
			idleFor := time.Since(time.Unix(0, p.lastActivity.Load()))
			if idleFor >= time.Duration(p.claims.Limits.IdleTimeoutSeconds)*time.Second {
				p.writeReset("idle_timeout")
				return
			}
		case <-lifetime.C:
			p.writeReset("lifetime_exceeded")
			return
		}
	}
}

func (p *streamPair) readDirection(ctx context.Context, source frameConn, queue chan<- Frame, events chan<- pairEvent) {
	for {
		frame, err := source.Read(ctx, p.claims.Limits.MaxFrameBytes)
		if err != nil {
			p.sendEvent(ctx, events, pairEvent{kind: pairEventReset, reason: "read_error"})
			return
		}
		p.lastActivity.Store(time.Now().UnixNano())
		if frame.Type == FrameReset {
			p.sendEvent(ctx, events, pairEvent{kind: pairEventReset, reason: "peer_reset"})
			return
		}
		if frame.Type == FrameData && p.bytes.Add(int64(len(frame.Payload))) > p.claims.Limits.MaxBytes {
			p.sendEvent(ctx, events, pairEvent{kind: pairEventReset, reason: "byte_limit"})
			return
		}
		select {
		case queue <- frame:
			p.observeQueueDepth(int64(len(queue)))
		case <-ctx.Done():
			return
		default:
			p.sendEvent(ctx, events, pairEvent{kind: pairEventReset, reason: "slow_consumer"})
			return
		}
		if frame.Type == FrameFIN {
			return
		}
	}
}

func (p *streamPair) writeDirection(ctx context.Context, destination frameConn, queue <-chan Frame, events chan<- pairEvent) {
	for {
		select {
		case <-ctx.Done():
			return
		case frame := <-queue:
			if !p.pace(ctx, frame) {
				return
			}
			if err := destination.Write(ctx, frame); err != nil {
				p.sendEvent(ctx, events, pairEvent{kind: pairEventReset, reason: "write_error"})
				return
			}
			if frame.Type == FrameFIN {
				p.sendEvent(ctx, events, pairEvent{kind: pairEventFIN})
				return
			}
		}
	}
}

func (p *streamPair) pace(ctx context.Context, frame Frame) bool {
	rate := p.claims.Limits.BandwidthBytesPerSecond
	if frame.Type != FrameData || rate <= 0 || len(frame.Payload) == 0 {
		return true
	}
	delay := time.Duration(int64(time.Second) * int64(len(frame.Payload)) / rate)
	if delay <= 0 {
		return true
	}
	// Reserve one shared schedule for both directions. Separate writer
	// goroutines must not double the advertised per-stream bandwidth.
	p.bandwidthMu.Lock()
	now := time.Now()
	start := now
	if p.nextWriteAt.After(start) {
		start = p.nextWriteAt
	}
	finish := start.Add(delay)
	p.nextWriteAt = finish
	p.bandwidthMu.Unlock()

	timer := time.NewTimer(time.Until(finish))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (p *streamPair) requestReset(reason string) {
	p.stopOnce.Do(func() { p.resetCh <- reason })
}

func (p *streamPair) writeReset(reason string) {
	frame := Frame{Type: FrameReset}
	ctx, cancel := context.WithTimeout(context.Background(), p.resetTimeout)
	defer cancel()
	done := make(chan struct{}, 2)
	for _, connection := range []frameConn{p.client, p.agent} {
		go func(conn frameConn) {
			_ = conn.Write(ctx, frame)
			done <- struct{}{}
		}(connection)
	}
	for range 2 {
		select {
		case <-done:
		case <-ctx.Done():
			return
		}
	}
}

func (p *streamPair) sendEvent(ctx context.Context, events chan<- pairEvent, event pairEvent) {
	select {
	case events <- event:
	case <-ctx.Done():
	}
}

func (p *streamPair) observeQueueDepth(depth int64) {
	for {
		current := p.maxQueue.Load()
		if depth <= current || p.maxQueue.CompareAndSwap(current, depth) {
			return
		}
	}
}

func (p *streamPair) maxObservedQueueDepth() int {
	return int(p.maxQueue.Load())
}

func (p *streamPair) emit(event, reason, role string) {
	if p.audit == nil {
		return
	}
	p.audit(AuditEvent{
		Time:         time.Now().UTC(),
		Event:        event,
		CapabilityID: p.claims.CapabilityID,
		StreamID:     p.claims.StreamID,
		ProfileID:    p.claims.ProfileID,
		AgentID:      p.claims.AgentID,
		Role:         role,
		Reason:       reason,
	})
}

func minDuration(left, right time.Duration) time.Duration {
	if left < right {
		return left
	}
	return right
}
