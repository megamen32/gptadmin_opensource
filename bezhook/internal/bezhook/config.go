// Package bezhook implements the bezhook tunnel client: a thin, friendly
// wrapper over the frp client library that fans one local HTTP port out to
// every gateway of the self-hosted frps fleet and exposes it as
// https://<sub>.<domain>.
package bezhook

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// Gateway is one frps endpoint of the fleet.
type Gateway struct {
	Addr string // host or host:port
	Port int
}

func (g Gateway) String() string { return g.Addr + ":" + strconv.Itoa(g.Port) }

// Options is the fully resolved run configuration.
type Options struct {
	LocalHost    string
	LocalPort    int
	Subdomain    string
	Domain       string // public suffix, e.g. t.became.bezrabotnyi.com
	Token        string
	TLSServerName string
	Endpoints    []Gateway
	TTL          time.Duration // 0 = unlimited
	Quiet        bool
}

// Baked-in fleet defaults. The token already ships in cli.py
// (FRPC_TOKEN_DEFAULT); keeping the same value here means bezhook works with
// zero configuration on the existing frps fleet.
const (
	DefaultDomain        = "t.became.bezrabotnyi.com"
	DefaultTLSServerName = "t.became.bezrabotnyi.com"
	DefaultToken         = "E10WCLE7ZFT+0NDgOFWwyPV8fb7hG7cLn320aHL0fVk="
	DefaultTTL           = 72 * time.Hour
)

var DefaultEndpoints = []Gateway{
	// The home edge (server-100 frps via the home NAT) reliably works from the
	// LAN (server-100's resolver overrides became.bezrabotnyi.com to the home
	// ingress). From the internet it is best-effort: public DNS for the name
	// points at server01 (port 7000 closed there) and the wildcard home entry on
	// port 7000 is not always forwarded — external clients simply keep this
	// edge retrying in the background while server01/server01 carry the tunnel.
	{Addr: "became.bezrabotnyi.com", Port: 7000}, // server-100 (home edge, best-effort)
	{Addr: "server01.bezrabotnyi.com", Port: 27000},    // server01 (always-on)
	{Addr: "server01.bezrabotnyi.com", Port: 27001},    // server01 (always-on)
}

// fileConfig mirrors ~/.bezhook.toml (all fields optional).
type fileConfig struct {
	Token         string   `toml:"token"`
	Domain        string   `toml:"domain"`
	TLSServerName string   `toml:"tls_server_name"`
	Endpoints     []string `toml:"endpoints"`
	DefaultTTL    string   `toml:"default_ttl"`
}

// DefaultConfigPath returns ~/.bezhook.toml (empty string if HOME unknown).
func DefaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".bezhook.toml")
}

// LoadFileConfig reads an optional TOML config; missing file is not an error.
func LoadFileConfig(path string) (fileConfig, error) {
	var fc fileConfig
	if path == "" {
		return fc, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fc, nil
		}
		return fc, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := toml.Unmarshal(data, &fc); err != nil {
		return fc, fmt.Errorf("parse config %s: %w", path, err)
	}
	return fc, nil
}

var subdomainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ValidateSubdomain checks the frp vhost routing constraint (single DNS label).
func ValidateSubdomain(sub string) error {
	if !subdomainRe.MatchString(sub) {
		return fmt.Errorf("subdomain %q must be lowercase letters/digits/hyphens (max 63), not start/end with hyphen", sub)
	}
	return nil
}

// RandomSubdomain mirrors the cli.py gen_subdomain pattern: "u-f" + 7 digits.
func RandomSubdomain() (string, error) {
	max := big.NewInt(10000000) // 0..9999999
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("u-f%07d", n.Int64()), nil
}

// ParseEndpoints parses "host:port,host:port" (port defaults to 7000).
func ParseEndpoints(list string) ([]Gateway, error) {
	var out []Gateway
	for _, part := range strings.Split(list, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		addr, port := part, 7000
		if i := strings.LastIndex(part, ":"); i >= 0 {
			p, err := strconv.Atoi(part[i+1:])
			if err != nil || p < 1 || p > 65535 {
				return nil, fmt.Errorf("bad endpoint %q: invalid port", part)
			}
			addr, port = part[:i], p
		}
		if addr == "" {
			return nil, fmt.Errorf("bad endpoint %q", part)
		}
		out = append(out, Gateway{Addr: addr, Port: port})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no endpoints given")
	}
	return out, nil
}

// ParseTTL accepts Go durations ("72h", "30m") or bare hours ("72").
func ParseTTL(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty ttl")
	}
	if d, err := time.ParseDuration(s); err == nil {
		if d < 0 {
			return 0, fmt.Errorf("bad ttl %q: negative durations are not allowed", s)
		}
		return d, nil
	}
	if h, err := strconv.Atoi(s); err == nil && h >= 0 {
		return time.Duration(h) * time.Hour, nil
	}
	return 0, fmt.Errorf("bad ttl %q (use e.g. 72h, 30m, or 0)", s)
}

// Resolve layers defaults <- file <- environment for everything except the
// per-run values (port, subdomain, ttl) that come from flags.
type Env map[string]string

func (e Env) get(key string) string { return strings.TrimSpace(e[key]) }

// FleetSettings resolves token/domain/endpoints/tls from file config and
// environment (environment wins).
func FleetSettings(fc fileConfig, env Env) (token, domain, tlsName string, endpoints []Gateway, err error) {
	token, domain = DefaultToken, DefaultDomain
	endpoints = DefaultEndpoints
	tlsName = ""

	if fc.Token != "" {
		token = fc.Token
	}
	if fc.Domain != "" {
		domain = fc.Domain
	}
	if fc.TLSServerName != "" {
		tlsName = fc.TLSServerName
	}
	if len(fc.Endpoints) > 0 {
		if endpoints, err = ParseEndpoints(strings.Join(fc.Endpoints, ",")); err != nil {
			return
		}
	}
	if v := env.get("BEZHOOK_TOKEN"); v != "" {
		token = v
	}
	if v := env.get("BEZHOOK_DOMAIN"); v != "" {
		domain = v
	}
	if v := env.get("BEZHOOK_TLS_SERVER_NAME"); v != "" {
		tlsName = v
	}
	if v := env.get("BEZHOOK_ENDPOINTS"); v != "" {
		if endpoints, err = ParseEndpoints(v); err != nil {
			return
		}
	}
	if tlsName == "" {
		tlsName = domain
	}
	return
}

// FileDefaultTTL returns the optional default_ttl from the config file.
func FileDefaultTTL(fc fileConfig) time.Duration {
	if fc.DefaultTTL == "" {
		return DefaultTTL
	}
	d, err := ParseTTL(fc.DefaultTTL)
	if err != nil {
		return DefaultTTL
	}
	return d
}

// PublicURL builds the stable public URL for a subdomain.
func PublicURL(sub, domain string) string { return "https://" + sub + "." + domain }
