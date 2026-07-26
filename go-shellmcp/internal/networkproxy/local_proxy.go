package networkproxy

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	localproxy "github.com/megamen32/gptadmin/go-shellmcp/internal/proxy"
)

// LocalProxyConfig binds a local HTTP CONNECT/SOCKS5 listener to one authorized
// relay stream target. The relay ticket is intentionally one-use, so callers
// that need multiple connections should provide a fresh ticket per request.
type LocalProxyConfig struct {
	Target        string
	MaxFrameBytes int64
	WriteTimeout  time.Duration
	TicketSource  TicketSource
}

// TicketSource obtains a fresh, target-bound relay ticket for one local request.
type TicketSource func(context.Context, string) (relayURL, ticket string, err error)

// StaticTicketSource adapts one pre-issued client ticket for a single stream.
func StaticTicketSource(relayURL, ticket string) TicketSource {
	return func(_ context.Context, _ string) (string, string, error) {
		if strings.TrimSpace(relayURL) == "" || strings.TrimSpace(ticket) == "" {
			return "", "", ErrStreamConfig
		}
		return relayURL, ticket, nil
	}
}

// ServeLocalProxy serves HTTP CONNECT and SOCKS5 TCP CONNECT on an existing listener.
func ServeLocalProxy(ctx context.Context, listener net.Listener, config LocalProxyConfig) error {
	if ctx == nil || listener == nil || strings.TrimSpace(config.Target) == "" || config.MaxFrameBytes <= 0 || config.WriteTimeout <= 0 || config.TicketSource == nil {
		return ErrStreamConfig
	}
	if _, err := ParseTarget(config.Target); err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	for {
		connection, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go func() {
			_ = localproxy.Handle(connection, func(network, address string) (net.Conn, error) {
				if network != "tcp" || address != config.Target {
					return nil, fmt.Errorf("%w: target is not approved", ErrStreamConfig)
				}
				relayURL, ticket, err := config.TicketSource(ctx, address)
				if err != nil {
					return nil, err
				}
				return OpenStream(ctx, StreamConfig{RelayURL: relayURL, Ticket: ticket, Role: streamRoleClient, MaxFrameBytes: config.MaxFrameBytes, WriteTimeout: config.WriteTimeout})
			})
		}()
	}
}

// ListenAndServeLocalProxy binds a local proxy listener and serves until cancellation.
func ListenAndServeLocalProxy(ctx context.Context, address string, config LocalProxyConfig) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	defer listener.Close()
	return ServeLocalProxy(ctx, listener, config)
}
