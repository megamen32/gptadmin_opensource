// Package meshroute verifies authorized product routes against the shared
// Rust core. It neither owns credentials nor sends/retries product operations.
package meshroute

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"

	meshcore "github.com/megamen32/mesh-core/bindings/go"
)

type normalizer interface {
	Normalize(context.Context, meshcore.RouteInput) (meshcore.Route, error)
}
type Verifier struct {
	core  normalizer
	local string
}

func New(socket, local string) (*Verifier, error) {
	if socket == "" {
		return nil, nil
	} // Explicit legacy configuration until deployment.
	if strings.TrimSpace(local) == "" {
		return nil, errors.New("mesh core local identity required")
	}
	c, err := meshcore.New(socket)
	if err != nil {
		return nil, err
	}
	return &Verifier{core: c, local: local}, nil
}

// The product has already authenticated/authorized target. Never learn a
// topology from a user-supplied URL, change owner, or replay an operation.
func (v *Verifier) Verify(ctx context.Context, target string) error {
	if v == nil {
		return nil
	}
	if target == "" || len(target) > 512 || strings.ContainsAny(target, "\x00\r\n") {
		return errors.New("invalid mesh target")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return errors.New("mesh request identity unavailable")
	}
	id := hex.EncodeToString(nonce[:])
	origin := v.local
	hop := uint8(0)
	result, err := v.core.Normalize(ctx, meshcore.RouteInput{LocalHostID: v.local, KnownHosts: []string{target}, Hosts: target, RequestID: &id, OriginHost: &origin, HopCount: &hop})
	if err != nil {
		return errors.New("shared mesh core unavailable")
	}
	if result.RequestID != id || result.OriginHost != origin || result.HopCount != 0 || len(result.Hosts) != 1 || result.Hosts[0] != target {
		return errors.New("shared mesh route identity mismatch")
	}
	return nil
}
