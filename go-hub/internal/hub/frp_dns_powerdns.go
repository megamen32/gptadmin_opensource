package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"
)

const frpDNSRecordTTL = 5

// Serialize the PowerDNS read-modify-write across joins in this Hub process.
var frpDNSPublication = make(chan struct{}, 1)

type frpDNSRecord struct {
	Content  string `json:"content"`
	Disabled bool   `json:"disabled"`
}

type frpDNSRRset struct {
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	TTL        int            `json:"ttl"`
	ChangeType string         `json:"changetype,omitempty"`
	Records    []frpDNSRecord `json:"records"`
}

type frpDNSZone struct {
	RRsets []frpDNSRRset `json:"rrsets"`
}

type frpPowerDNS struct {
	zoneURL   string
	apiKey    string
	members   string
	public    string
	healthURL string
}

func validFRPRelayIPv4(value string) bool {
	addr, err := netip.ParseAddr(value)
	return err == nil && addr.Is4() && (addr.IsGlobalUnicast() || addr.IsLoopback())
}

func frpPowerDNSFromEnv() (frpPowerDNS, error) {
	apiURL := strings.TrimRight(os.Getenv("GPTADMIN_FRP_DNS_API_URL"), "/")
	key := os.Getenv("GPTADMIN_FRP_DNS_API_KEY")
	zone := strings.ToLower(strings.TrimSuffix(os.Getenv("GPTADMIN_FRP_DNS_ZONE"), "."))
	health := os.Getenv("GPTADMIN_FRP_DNS_HEALTH_URL")
	if !validFRPDNSAPIURL(apiURL) || key == "" || !validFRPDNSZone(zone) || !validFRPDNSURL(health) {
		return frpPowerDNS{}, errors.New("FRP PowerDNS settings are missing or invalid")
	}
	return frpPowerDNS{
		zoneURL: apiURL + "/api/v1/servers/localhost/zones/" + zone + ".",
		apiKey:  key, members: "frp-members." + zone + ".", public: "frp." + zone + ".", healthURL: health,
	}, nil
}

func validFRPDNSURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Hostname() != "" &&
		parsed.User == nil && parsed.RawQuery == "" && !parsed.ForceQuery && parsed.Fragment == "" &&
		!strings.ContainsAny(value, "'\"\\ \t\r\n")
}

func validFRPDNSAPIURL(value string) bool {
	if !validFRPDNSURL(value) {
		return false
	}
	parsed, _ := url.Parse(value)
	addr, err := netip.ParseAddr(parsed.Hostname())
	return parsed.Scheme == "https" || (err == nil && addr.IsLoopback()) ||
		strings.EqualFold(parsed.Hostname(), "localhost")
}

func validFRPDNSZone(zone string) bool {
	if len(zone) > 241 {
		return false
	}
	for _, label := range strings.Split(zone, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' ||
			strings.Trim(label, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" {
			return false
		}
	}
	return true
}

// Publish both records in one PowerDNS zone transaction. The Lua script uses
// the same enabled addresses as the A RRset; existing seed relays are retained.
func (p frpPowerDNS) publish(ctx context.Context, address string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	select {
	case frpDNSPublication <- struct{}{}:
		defer func() { <-frpDNSPublication }()
	case <-ctx.Done():
		return errors.New("timed out waiting for FRP DNS publication")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	var current frpDNSZone
	if err := p.request(ctx, client, http.MethodGet, http.StatusOK, nil, &current); err != nil {
		return err
	}
	var oldMembers, oldPublic frpDNSRRset
	for _, rrset := range current.RRsets {
		switch {
		case rrset.Name == p.members && rrset.Type == "A":
			oldMembers = rrset
		case rrset.Name == p.public && rrset.Type == "LUA":
			oldPublic = rrset
		case rrset.Name == p.public && (rrset.Type == "A" || rrset.Type == "CNAME"):
			return errors.New("FRP public DNS name conflicts with an existing A or CNAME RRset")
		}
	}
	records := slices.Clone(oldMembers.Records)
	found := false
	addresses := make([]string, 0, len(records)+1)
	for _, record := range records {
		if !validFRPRelayIPv4(record.Content) {
			return errors.New("FRP membership DNS RRset contains an invalid IPv4 address")
		}
		if record.Content == address {
			if record.Disabled {
				return errors.New("FRP relay address is disabled in the membership DNS RRset")
			}
			found = true
		}
		if !record.Disabled {
			addresses = append(addresses, "'"+record.Content+"'")
		}
	}
	if !found {
		records = append(records, frpDNSRecord{Content: address})
		addresses = append(addresses, "'"+address+"'")
	}
	slices.Sort(addresses)
	lua := fmt.Sprintf(`A "ifurlup('%s', {{%s}}, {stringmatch='gptadmin-go-hub'})"`, p.healthURL, strings.Join(addresses, ","))
	publicRecords := []frpDNSRecord{{Content: lua}}
	if found && oldMembers.TTL == frpDNSRecordTTL && oldPublic.TTL == frpDNSRecordTTL &&
		slices.Equal(oldPublic.Records, publicRecords) {
		return nil
	}

	patch, err := json.Marshal(frpDNSZone{RRsets: []frpDNSRRset{
		{Name: p.members, Type: "A", TTL: frpDNSRecordTTL, ChangeType: "REPLACE", Records: records},
		{Name: p.public, Type: "LUA", TTL: frpDNSRecordTTL, ChangeType: "REPLACE", Records: publicRecords},
	}})
	if err != nil {
		return errors.New("encode FRP DNS RRsets")
	}
	return p.request(ctx, client, http.MethodPatch, http.StatusNoContent, patch, nil)
}

func (p frpPowerDNS) request(ctx context.Context, client *http.Client, method string, status int, body []byte, zone *frpDNSZone) error {
	req, err := http.NewRequestWithContext(ctx, method, p.zoneURL, bytes.NewReader(body))
	if err != nil {
		return errors.New("create PowerDNS request")
	}
	req.Header.Set("X-API-Key", p.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("PowerDNS %s transport failed", method)
	}
	defer resp.Body.Close()
	if resp.StatusCode != status {
		return fmt.Errorf("PowerDNS %s returned HTTP %d", method, resp.StatusCode)
	}
	if zone != nil && json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(zone) != nil {
		return errors.New("decode PowerDNS zone")
	}
	return nil
}
