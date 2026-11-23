package hub

import (
	"context"
	"errors"
	"os"
	"strings"
)

type frpDNSPublisher interface {
	publish(context.Context, string) error
	reconcile(context.Context, string) error
}

func frpDNSPublisherFromEnv() (frpDNSPublisher, error) {
	switch strings.TrimSpace(os.Getenv("GPTADMIN_FRP_DNS_PROVIDER")) {
	case "", "powerdns":
		return frpPowerDNSFromEnv()
	case "spaceweb":
		return frpSpaceWebFromEnv()
	default:
		return nil, errors.New("unknown FRP DNS provider")
	}
}

// PowerDNS already reads the zone and uses a full RRset replacement.
func (p frpPowerDNS) reconcile(ctx context.Context, address string) error {
	return p.publish(ctx, address)
}
