package hub

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/megamen32/gptadmin/go-hub/internal/hub/quorum"
)

func TestQuorumPrivateHTTPSAuthenticatesBothPeers(t *testing.T) {
	root := t.TempDir()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(root, "ca.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0600); err != nil {
		t.Fatal(err)
	}
	issue := func(name string, serial int64) (string, string) {
		t.Helper()
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		cert := &x509.Certificate{SerialNumber: big.NewInt(serial), DNSNames: []string{name}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
		der, err := x509.CreateCertificate(rand.Reader, cert, ca, &key.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		keyDER, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		certPath, keyPath := filepath.Join(root, name+".crt"), filepath.Join(root, name+".key")
		if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
			t.Fatal(err)
		}
		return certPath, keyPath
	}
	serverCert, serverKey := issue("server.test", 2)
	clientCert, clientKey := issue("client.test", 3)
	rogueCert, rogueKey := issue("rogue.test", 4)
	peers := []quorum.Peer{{ID: "server", ServerName: "server.test", InternalHubURL: "https://127.0.0.1:0"}, {ID: "client", ServerName: "client.test", InternalHubURL: "https://127.0.0.1:1"}}
	duplicateNames := append([]quorum.Peer(nil), peers...)
	duplicateNames[1].ServerName = duplicateNames[0].ServerName
	if _, err := prepareQuorumPrivateTLS(quorum.Config{NodeID: "server", TLSCertFile: serverCert, TLSKeyFile: serverKey, TLSCAFile: caPath, Peers: duplicateNames}); err == nil {
		t.Fatal("duplicate voter TLS identities were accepted")
	}
	mixed := append([]quorum.Peer(nil), peers...)
	mixed[1].InternalHubURL = "http://127.0.0.1:1"
	if _, err := prepareQuorumPrivateTLS(quorum.Config{NodeID: "server", TLSCertFile: serverCert, TLSKeyFile: serverKey, TLSCAFile: caPath, Peers: mixed}); err == nil {
		t.Fatal("mixed HTTP and HTTPS routes were accepted")
	}
	if _, err := prepareQuorumPrivateTLS(quorum.Config{NodeID: "server", TLSCertFile: serverCert, TLSKeyFile: serverKey, Peers: peers}); err == nil {
		t.Fatal("HTTPS routes without private CA were accepted")
	}
	if _, err := prepareQuorumPrivateTLS(quorum.Config{NodeID: "server", TLSCertFile: clientCert, TLSKeyFile: clientKey, TLSCAFile: caPath, Peers: peers}); err == nil {
		t.Fatal("wrong local certificate identity was accepted")
	}
	server, err := prepareQuorumPrivateTLS(quorum.Config{NodeID: "server", TLSCertFile: serverCert, TLSKeyFile: serverKey, TLSCAFile: caPath, Peers: peers})
	if err != nil {
		t.Fatal(err)
	}
	client, err := prepareQuorumPrivateTLS(quorum.Config{NodeID: "client", TLSCertFile: clientCert, TLSKeyFile: clientKey, TLSCAFile: caPath, Peers: peers})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := server.listen()
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	svc := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"node_id": "server", "cluster_id": "test-cluster", "replica_ready": true})
	})}
	defer svc.Close()
	go svc.Serve(listener)
	transport, err := client.transport("server")
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	endpoint := "https://" + listener.Addr().String() + "/healthz"
	response, err := (&http.Client{Transport: transport, Timeout: 3 * time.Second}).Get(endpoint)
	if err != nil {
		t.Fatalf("trusted client could not connect: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status %d", response.StatusCode)
	}
	peer := peers[0]
	peer.InternalHubURL = "https://" + listener.Addr().String()
	peer.HubURL = "https://public-route-unavailable.invalid"
	router := &Server{quorumClusterID: "test-cluster", quorumPrivateTLS: client}
	if !router.quorumPeerReplicaReady(peer) {
		t.Fatal("private replica health did not work while the public route was unavailable")
	}
	unauthenticated := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: client.roots, ServerName: "server.test", MinVersion: tls.VersionTLS12}}
	defer unauthenticated.CloseIdleConnections()
	if r, err := (&http.Client{Transport: unauthenticated, Timeout: 3 * time.Second}).Get(endpoint); err == nil {
		r.Body.Close()
		t.Fatal("server accepted a peer without a client certificate")
	}
	rogueIdentity, err := tls.LoadX509KeyPair(rogueCert, rogueKey)
	if err != nil {
		t.Fatal(err)
	}
	rogue := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: client.roots, ServerName: "server.test", MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{rogueIdentity}}}
	defer rogue.CloseIdleConnections()
	if r, err := (&http.Client{Transport: rogue, Timeout: 3 * time.Second}).Get(endpoint); err == nil {
		r.Body.Close()
		t.Fatal("server accepted a CA-signed but unenrolled client")
	}
	wrongName, err := client.transport("server")
	if err != nil {
		t.Fatal(err)
	}
	wrongName.TLSClientConfig.ServerName = "wrong.test"
	defer wrongName.CloseIdleConnections()
	if r, err := (&http.Client{Transport: wrongName, Timeout: 3 * time.Second}).Get(endpoint); err == nil {
		r.Body.Close()
		t.Fatal("client accepted an unenrolled server identity")
	}
}
