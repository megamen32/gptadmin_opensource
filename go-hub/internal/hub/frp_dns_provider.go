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

func frpDNSPublisherFromEnv(pool *frpSpaceWebPool) (frpDNSPublisher, error) {
	provider := strings.TrimSpace(os.Getenv("GPTADMIN_FRP_DNS_PROVIDER"))
	if pool != nil {
		if provider != "" && provider != "spaceweb" {
			return nil, errors.New("local FRP DNS provider conflicts with the approved pool")
		}
		return frpSpaceWebFromEnv(pool)
	}
	switch provider {
	case "", "powerdns":
		return frpPowerDNSFromEnv()
	case "spaceweb":
		return frpSpaceWebFromEnv(nil)
	case "exec":
		return frpDNSExecFromEnv()
	default:
		return nil, errors.New("unknown FRP DNS provider")
	}
}

// PowerDNS already reads the zone and uses a full RRset replacement.
func (p frpPowerDNS) reconcile(ctx context.Context, address string) error {
	return p.publish(ctx, address)
}
