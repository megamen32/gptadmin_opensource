package networkproxy

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	localproxy "github.com/megamen32/gptadmin/go-shellmcp/internal/proxy"
)

// LocalProxyConfig binds a local HTTP CONNECT/SOCKS5 listener to an authorized
// relay stream target. The relay ticket is intentionally one-use, so callers
// that need multiple connections should provide a fresh ticket per request.
type LocalProxyConfig struct {
	// Target fixes the listener to one target. Leave it empty only with
	// AllowAnyTarget, which delegates target authorization to TicketSource and
	// the remote signed capability on every CONNECT request.
	Target         string
	AllowAnyTarget bool
	MaxFrameBytes  int64
	WriteTimeout   time.Duration
	TicketSource   TicketSource
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
	target := strings.TrimSpace(config.Target)
	if ctx == nil || listener == nil || config.MaxFrameBytes <= 0 || config.WriteTimeout <= 0 || config.TicketSource == nil || (target == "" && !config.AllowAnyTarget) || (target != "" && config.AllowAnyTarget) {
		return ErrStreamConfig
	}
	if target != "" {
		if _, err := ParseTarget(target); err != nil {
			return err
		}
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
				if network != "tcp" {
					return nil, fmt.Errorf("%w: network is not approved", ErrStreamConfig)
				}
				streamTarget := target
				if config.AllowAnyTarget {
					streamTarget = address
					if _, err := ParseTarget(streamTarget); err != nil {
						return nil, fmt.Errorf("%w: target is invalid", err)
					}
				} else if address != target {
					return nil, fmt.Errorf("%w: target is not approved", ErrStreamConfig)
				}
				relayURL, ticket, err := config.TicketSource(ctx, streamTarget)
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
