package hub

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/megamen32/gptadmin/go-hub/internal/hub/quorum"
)

// quorumPrivateTLS provides mutually authenticated HTTPS for direct LAN Hub
// traffic. It reuses the enrolled Raft identities without relying on the
// public edge or the primary machine.
type quorumPrivateTLS struct {
	address     string
	certificate tls.Certificate
	roots       *x509.CertPool
	peers       map[string]quorum.Peer
}

func prepareQuorumPrivateTLS(c quorum.Config) (*quorumPrivateTLS, error) {
	var local quorum.Peer
	for _, p := range c.Peers {
		if p.ID == c.NodeID {
			local = p
			break
		}
	}
	u, err := url.Parse(local.InternalHubURL)
	if err != nil {
		return nil, errors.New("invalid local quorum private route")
	}
	hasHTTPS := false
	for _, peer := range c.Peers {
		if strings.HasPrefix(peer.InternalHubURL, "https://") {
			hasHTTPS = true
		}
	}
	if !hasHTTPS {
		return nil, nil
	}
	if u.Scheme != "https" {
		return nil, errors.New("all quorum voters must use private HTTPS")
	}
	if c.TLSCAFile == "" {
		return nil, errors.New("private quorum HTTPS requires an explicit CA")
	}
	cert, err := tls.LoadX509KeyPair(c.TLSCertFile, c.TLSKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load private quorum HTTPS identity: %w", err)
	}
	ca, err := os.ReadFile(c.TLSCAFile)
	if err != nil {
		return nil, fmt.Errorf("read private quorum HTTPS CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, errors.New("private quorum HTTPS CA contains no certificates")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, errors.New("invalid private quorum HTTPS certificate")
	}
	if err := leaf.VerifyHostname(local.ServerName); err != nil {
		return nil, errors.New("private quorum HTTPS certificate does not match enrolled local identity")
	}
	intermediates := x509.NewCertPool()
	for _, der := range cert.Certificate[1:] {
		intermediate, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, errors.New("invalid private quorum HTTPS certificate chain")
		}
		intermediates.AddCert(intermediate)
	}
	for _, usage := range []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth} {
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, KeyUsages: []x509.ExtKeyUsage{usage}}); err != nil {
			return nil, errors.New("private quorum HTTPS identity must be trusted for client and server authentication")
		}
	}
	p := &quorumPrivateTLS{address: u.Host, certificate: cert, roots: roots, peers: make(map[string]quorum.Peer, len(c.Peers))}
	seenNames := make(map[string]bool, len(c.Peers))
	for _, peer := range c.Peers {
		target, parseErr := url.Parse(peer.InternalHubURL)
		if parseErr != nil || target.Scheme != "https" || target.Host == "" || strings.TrimSpace(peer.ServerName) == "" {
			return nil, errors.New("all quorum voters must have HTTPS private routes and enrolled TLS names")
		}
		name := strings.ToLower(strings.TrimSpace(peer.ServerName))
		if seenNames[name] {
			return nil, errors.New("quorum HTTPS voter TLS names must be unique")
		}
		seenNames[name] = true
		p.peers[peer.ID] = peer
	}
	return p, nil
}

func (p *quorumPrivateTLS) listen() (net.Listener, error) {
	listener, err := net.Listen("tcp", p.address)
	if err != nil {
		return nil, err
	}
	return tls.NewListener(listener, &tls.Config{
		MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{p.certificate},
		ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: p.roots,
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return errors.New("missing quorum client certificate")
			}
			for _, peer := range p.peers {
				if state.PeerCertificates[0].VerifyHostname(peer.ServerName) == nil {
					return nil
				}
			}
			return errors.New("quorum client certificate is not an enrolled voter")
		},
	}), nil
}

func (p *quorumPrivateTLS) transport(peerID string) (*http.Transport, error) {
	peer, ok := p.peers[peerID]
	if !ok {
		return nil, errors.New("quorum HTTPS peer is not enrolled")
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	t.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: p.roots, ServerName: peer.ServerName, Certificates: []tls.Certificate{p.certificate}}
	return t, nil
}
