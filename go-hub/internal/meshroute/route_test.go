package meshroute

import (
	"context"
	"errors"
	"testing"

	meshcore "github.com/megamen32/mesh-core/bindings/go"
)

type unitNormalizer struct {
	mode  string
	calls int
}

func (c *unitNormalizer) Normalize(_ context.Context, in meshcore.RouteInput) (meshcore.Route, error) {
	c.calls++
	if c.mode == "offline" {
		return meshcore.Route{}, errors.New("unit-only")
	}
	r := meshcore.Route{RequestID: *in.RequestID, OriginHost: *in.OriginHost, HopCount: *in.HopCount, Hosts: []string{in.Hosts.(string)}}
	switch c.mode {
	case "owner":
		r.Hosts = []string{"foreign"}
	case "request":
		r.RequestID = "foreign"
	case "origin":
		r.OriginHost = "foreign"
	case "hop":
		r.HopCount = 1
	case "extra":
		r.Hosts = append(r.Hosts, "foreign")
	}
	return r, nil
}
func TestSharedCoreRouteFences(t *testing.T) {
	for _, mode := range []string{"ok", "offline", "owner", "request", "origin", "hop", "extra"} {
		t.Run(mode, func(t *testing.T) {
			c := &unitNormalizer{mode: mode}
			v := &Verifier{core: c, local: "local"}
			err := v.Verify(context.Background(), "peer")
			if (err == nil) != (mode == "ok") || c.calls != 1 {
				t.Fatalf("wrong route acceptance mode=%s calls=%d err=%v", mode, c.calls, err)
			}
		})
	}
}
func TestSharedCoreExplicitConfiguration(t *testing.T) {
	if v, e := New("", "local"); e != nil || v != nil {
		t.Fatal("legacy explicit disabled config changed")
	}
	if _, e := New("/private/core.sock", ""); e == nil {
		t.Fatal("missing native identity accepted")
	}
	if _, e := New("relative", "local"); e == nil {
		t.Fatal("relative core socket accepted")
	}
	if e := (*Verifier)(nil).Verify(context.Background(), "peer"); e != nil {
		t.Fatal("disabled config changed")
	}
}
