package hub

import (
	"errors"
	"os"
	"path/filepath"
)

// Only the zone and constrained record name travel with the owner's approval.
// Executables and credentials stay in each provider's local trust boundary.
type frpSpaceWebPool struct {
	Zone string `json:"zone"`
	Name string `json:"name"`
}

func (p frpSpaceWebPool) valid() bool {
	return validFRPDNSPool(p.Zone, p.Name)
}

func frpSpaceWebFromEnv(pool *frpSpaceWebPool) (frpDNSHelper, error) {
	const (
		helperKey    = "GPTADMIN_FRP_DNS_SPACEWEB_HELPER"
		nameKey      = "GPTADMIN_FRP_DNS_SPACEWEB_NAME"
		zoneKey      = "GPTADMIN_FRP_DNS_ZONE"
		credsKey     = "GPTADMIN_FRP_DNS_SPACEWEB_CREDENTIALS_FILE"
		transportKey = "GPTADMIN_FRP_DNS_SPACEWEB_TRANSPORT"
	)
	helper, creds, transport := os.Getenv(helperKey), os.Getenv(credsKey), os.Getenv(transportKey)
	name := frpDNSNameFromEnv(nameKey)
	zone := frpDNSNameFromEnv(zoneKey)
	if pool != nil {
		if (zone != "" && zone != pool.Zone) || (name != "" && name != pool.Name) {
			return frpDNSHelper{}, errors.New("local FRP DNS scope conflicts with the approved pool")
		}
		name, zone = pool.Name, pool.Zone
		if helper == "" {
			helper = "/usr/local/lib/gptadmin/gptadmin_dns_sweb.py"
		}
		if transport == "" && os.Getenv(transportKey) == "" {
			transport = "/usr/local/bin/swebmimic"
		}
		if dir := os.Getenv("CREDENTIALS_DIRECTORY"); creds == "" && filepath.IsAbs(dir) {
			creds = filepath.Join(dir, "gptadmin-spaceweb")
		}
	}
	if !filepath.IsAbs(helper) || !filepath.IsAbs(creds) || (transport != "" && !filepath.IsAbs(transport)) ||
		!(frpSpaceWebPool{Zone: zone, Name: name}).valid() {
		return frpDNSHelper{}, errors.New("FRP SpaceWeb settings are missing or invalid")
	}
	return newFRPDNSHelper("SpaceWeb", helper, name, creds, []string{"--allow-append-a"}, []string{
		"PATH=/usr/bin:/bin", nameKey + "=" + name, zoneKey + "=" + zone,
		credsKey + "=" + creds, transportKey + "=" + transport,
	})
}
