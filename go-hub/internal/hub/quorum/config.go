package quorum

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Peer is one pre-enrolled Raft candidate. ServerName is checked against the
// TLS certificate even when Address is a private or pinned IP address.
type Peer struct {
	ID         string `json:"id"`
	Address    string `json:"address"`
	ServerName string `json:"server_name"`
	HubURL     string `json:"hub_url,omitempty"`
	// InternalHubURL is the private HTTP listener used for follower-to-leader
	// forwarding. HubURL remains the public HTTPS origin for client ingress.
	InternalHubURL string `json:"internal_hub_url,omitempty"`
	NodeKeyHash    string `json:"node_key_sha256"`
	// FailureDomain is an operator-assigned physical/network isolation scope.
	FailureDomain string `json:"failure_domain,omitempty"`
}

// Config is deliberately explicit. A public URL makes a machine reachable;
// only a voter in this authenticated configuration can elect or lead the Hub.
type Config struct {
	ClusterID                        string        `json:"cluster_id"`
	NodeID                           string        `json:"node_id"`
	BootstrapperID                   string        `json:"bootstrapper_id"`
	NodeKey                          string        `json:"node_key"`
	BindAddress                      string        `json:"bind_address"`
	Advertise                        string        `json:"advertise"`
	DataDir                          string        `json:"data_dir"`
	TLSCertFile                      string        `json:"tls_cert_file"`
	TLSKeyFile                       string        `json:"tls_key_file"`
	TLSCAFile                        string        `json:"tls_ca_file"`
	StateKeyFile                     string        `json:"state_key_file"`
	ExternalSnapshotMaxBytes         int64         `json:"external_snapshot_max_bytes,omitempty"`
	ExternalSnapshotFreeReserveBytes int64         `json:"external_snapshot_free_reserve_bytes,omitempty"`
	Peers                            []Peer        `json:"peers"`
	Bootstrap                        bool          `json:"bootstrap"`
	RPCDeadline                      time.Duration `json:"rpc_deadline,omitempty"`
	ApplyDeadline                    time.Duration `json:"apply_deadline,omitempty"`
	BarrierDeadline                  time.Duration `json:"barrier_deadline,omitempty"`
}

var (
	ErrNotLeader     = errors.New("quorum: this node is not the current leader")
	ErrNoQuorum      = errors.New("quorum: majority is unavailable")
	ErrStaleRevision = errors.New("quorum: record revision is not newer")
)

func nodeKeyHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (c Config) normalized() (Config, error) {
	c.ClusterID = strings.TrimSpace(c.ClusterID)
	c.NodeID = strings.TrimSpace(c.NodeID)
	c.BootstrapperID = strings.TrimSpace(c.BootstrapperID)
	c.BindAddress = strings.TrimSpace(c.BindAddress)
	c.Advertise = strings.TrimSpace(c.Advertise)
	c.DataDir = strings.TrimSpace(c.DataDir)
	c.TLSCertFile = strings.TrimSpace(c.TLSCertFile)
	c.TLSKeyFile = strings.TrimSpace(c.TLSKeyFile)
	c.TLSCAFile = strings.TrimSpace(c.TLSCAFile)
	c.StateKeyFile = strings.TrimSpace(c.StateKeyFile)
	if c.ClusterID == "" || c.NodeID == "" || c.BootstrapperID == "" || c.DataDir == "" {
		return Config{}, errors.New("quorum: cluster_id, node_id, bootstrapper_id and data_dir are required")
	}
	if c.ExternalSnapshotMaxBytes < 0 || c.ExternalSnapshotFreeReserveBytes < 0 {
		return Config{}, errors.New("quorum: external snapshot byte limits cannot be negative")
	}
	if len(c.NodeKey) < 32 {
		return Config{}, errors.New("quorum: node key must contain at least 32 characters")
	}
	if c.BindAddress == "" {
		return Config{}, errors.New("quorum: bind_address is required")
	}
	if _, _, err := net.SplitHostPort(c.BindAddress); err != nil {
		return Config{}, fmt.Errorf("quorum: invalid bind_address: %w", err)
	}
	if c.Advertise == "" {
		c.Advertise = c.BindAddress
	}
	if _, _, err := net.SplitHostPort(c.Advertise); err != nil {
		return Config{}, fmt.Errorf("quorum: invalid advertise address: %w", err)
	}
	if c.TLSCertFile == "" || c.TLSKeyFile == "" {
		return Config{}, errors.New("quorum: TLS certificate and key are required")
	}
	if len(c.Peers) < 3 || len(c.Peers)%2 == 0 {
		return Config{}, errors.New("quorum: voter set must contain an odd number of at least three peers")
	}
	seenID := map[string]bool{}
	seenAddress := map[string]bool{}
	seenFailureDomain := map[string]bool{}
	failureDomainsConfigured := false
	internalHubURLsConfigured := false
	internalHubURLCount := 0
	seenInternalHubURL := map[string]bool{}
	selfFound := false
	bootstrapperFound := false
	for i := range c.Peers {
		peer := &c.Peers[i]
		peer.ID = strings.TrimSpace(peer.ID)
		peer.Address = strings.TrimSpace(peer.Address)
		peer.ServerName = strings.TrimSpace(peer.ServerName)
		peer.HubURL = strings.TrimSpace(peer.HubURL)
		peer.InternalHubURL = strings.TrimSpace(peer.InternalHubURL)
		peer.NodeKeyHash = strings.ToLower(strings.TrimSpace(peer.NodeKeyHash))
		peer.FailureDomain = strings.TrimSpace(peer.FailureDomain)
		if peer.FailureDomain != "" {
			failureDomainsConfigured = true
		}
		if peer.ID == "" || peer.ServerName == "" || peer.Address == "" {
			return Config{}, errors.New("quorum: every voter requires id, address and server_name")
		}
		if peer.InternalHubURL != "" {
			internalHubURLsConfigured = true
			internalHubURLCount++
			if err := validateInternalHubURL(peer.InternalHubURL); err != nil {
				return Config{}, fmt.Errorf("quorum: invalid internal_hub_url for %s: %w", peer.ID, err)
			}
			if seenInternalHubURL[peer.InternalHubURL] {
				return Config{}, errors.New("quorum: internal hub URLs must be unique")
			}
			seenInternalHubURL[peer.InternalHubURL] = true
		}
		if _, _, err := net.SplitHostPort(peer.Address); err != nil {
			return Config{}, fmt.Errorf("quorum: invalid peer address for %s: %w", peer.ID, err)
		}
		if len(peer.NodeKeyHash) != sha256.Size*2 {
			return Config{}, fmt.Errorf("quorum: voter %s has an invalid node-key hash", peer.ID)
		}
		if _, err := hex.DecodeString(peer.NodeKeyHash); err != nil {
			return Config{}, fmt.Errorf("quorum: voter %s has an invalid node-key hash", peer.ID)
		}
		if seenID[peer.ID] || seenAddress[peer.Address] {
			return Config{}, errors.New("quorum: voter IDs and addresses must be unique")
		}
		seenID[peer.ID] = true
		seenAddress[peer.Address] = true
		if peer.ID == c.NodeID {
			selfFound = true
			if subtle.ConstantTimeCompare([]byte(peer.NodeKeyHash), []byte(nodeKeyHash(c.NodeKey))) != 1 {
				return Config{}, errors.New("quorum: local node key does not match enrolled voter identity")
			}
			if peer.Address != c.Advertise {
				return Config{}, errors.New("quorum: local advertised address differs from voter configuration")
			}
		}
		if peer.ID == c.BootstrapperID {
			bootstrapperFound = true
		}
	}
	if internalHubURLsConfigured && internalHubURLCount != len(c.Peers) {
		return Config{}, errors.New("quorum: every voter must declare internal_hub_url when internal forwarding is configured")
	}
	if failureDomainsConfigured {
		for _, peer := range c.Peers {
			if peer.FailureDomain == "" {
				return Config{}, errors.New("quorum: every voter must declare a failure_domain when domains are configured")
			}
			if seenFailureDomain[peer.FailureDomain] {
				return Config{}, errors.New("quorum: voter failure domains must be unique")
			}
			seenFailureDomain[peer.FailureDomain] = true
		}
	}
	if !bootstrapperFound || c.Bootstrap != (c.NodeID == c.BootstrapperID) {
		return Config{}, errors.New("quorum: exactly the configured bootstrapper node must enable bootstrap")
	}
	if !selfFound {
		return Config{}, errors.New("quorum: local node is not in the enrolled voter set")
	}
	if c.RPCDeadline <= 0 {
		c.RPCDeadline = 5 * time.Second
	}
	if c.ApplyDeadline <= 0 {
		c.ApplyDeadline = 10 * time.Second
	}
	if c.BarrierDeadline <= 0 {
		c.BarrierDeadline = 5 * time.Second
	}
	return c, nil
}

func validateInternalHubURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" || parsed.User != nil ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("must be a plain private HTTP origin without credentials or path")
	}
	host := net.ParseIP(parsed.Hostname())
	if host == nil || (!host.IsPrivate() && !host.IsLoopback()) {
		return errors.New("host must be a private or loopback IP address")
	}
	_, portText, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		return errors.New("host must include a port")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	return nil
}
