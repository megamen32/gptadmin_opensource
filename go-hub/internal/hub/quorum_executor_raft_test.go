package hub

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/megamen32/gptadmin/go-hub/internal/hub/quorum"
)

func TestQuorumFollowerExecutorAuthenticatedForwarding(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	caKey, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	caT := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, e := x509.CreateCertificate(rand.Reader, caT, caT, &caKey.PublicKey, caKey)
	if e != nil {
		t.Fatal(e)
	}
	ca, e := x509.ParseCertificate(caDER)
	if e != nil {
		t.Fatal(e)
	}
	caPath := filepath.Join(root, "ca.pem")
	if e = os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0600); e != nil {
		t.Fatal(e)
	}
	localToken := strings.Repeat("l", 32)
	leaderHub := New(Config{ShellToken: strings.Repeat("g", 32)})
	defer leaderHub.Close()
	identity := map[string]string{"server_id": "haos-id", "public_key": "haos-key", "fingerprint": "haos-fp"}
	leaderHub.agents["shell:haos"] = &Agent{AgentID: "shell:haos", Meta: map[string]any{"approved": true, "server_id": "haos-id", "public_key": "haos-key", "fingerprint": "haos-fp"}}
	queueHandler := leaderHub.requireShell(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mcp-relay/call" {
			leaderHub.forwardNodePeerCall(w, r, "shell:haos", []byte(`{"target":"shell:haos","tool":"shell_exec"}`))
			return
		}
		queueHandler(w, r)
	}
	listeners := make([]net.Listener, 3)
	privateListeners := make([]net.Listener, 3)
	peers := make([]quorum.Peer, 3)
	keys := make([]string, 3)
	certs := make([]string, 3)
	privs := make([]string, 3)
	for i := range peers {
		listeners[i], e = net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		privateListeners[i], e = net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		keys[i] = strings.Repeat(string(rune(97+i)), 32)
		h := sha256.Sum256([]byte(keys[i]))
		name := fmt.Sprintf("node%d.test", i)
		peers[i] = quorum.Peer{ID: fmt.Sprintf("n%d", i), Address: listeners[i].Addr().String(), ServerName: name, NodeKeyHash: hex.EncodeToString(h[:]), FailureDomain: name, HubURL: "https://" + name, InternalHubURL: "https://" + privateListeners[i].Addr().String()}
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		leaf := &x509.Certificate{SerialNumber: big.NewInt(int64(i + 2)), DNSNames: []string{name}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
		der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		kd, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		certs[i] = filepath.Join(root, name+".crt")
		privs[i] = filepath.Join(root, name+".key")
		if err = os.WriteFile(certs[i], pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(privs[i], pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kd}), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, l := range listeners {
		l.Close()
	}
	for _, l := range privateListeners {
		l.Close()
	}
	nodes := make([]*quorum.Node, 3)
	for i := range nodes {
		nodes[i], e = quorum.Open(quorum.Config{ClusterID: "test", NodeID: peers[i].ID, BootstrapperID: "n0", NodeKey: keys[i], BindAddress: peers[i].Address, Advertise: peers[i].Address, DataDir: filepath.Join(root, peers[i].ID), TLSCertFile: certs[i], TLSKeyFile: privs[i], TLSCAFile: caPath, Peers: peers, Bootstrap: i == 0, RPCDeadline: 2 * time.Second, ApplyDeadline: 2 * time.Second, BarrierDeadline: 2 * time.Second})
		if e != nil {
			t.Fatal(e)
		}
		defer nodes[i].Close()
	}
	tlsPeers := make([]*quorumPrivateTLS, 3)
	for i := range tlsPeers {
		tlsPeers[i], e = prepareQuorumPrivateTLS(quorum.Config{NodeID: peers[i].ID, TLSCertFile: certs[i], TLSKeyFile: privs[i], TLSCAFile: caPath, Peers: peers})
		if e != nil {
			t.Fatal(e)
		}
	}
	for _, privateTLS := range tlsPeers {
		privateListener, err := privateTLS.listen()
		if err != nil {
			t.Fatal(err)
		}
		defer privateListener.Close()
		privateServer := &http.Server{Handler: http.HandlerFunc(handler)}
		defer privateServer.Close()
		go privateServer.Serve(privateListener)
	}
	leaderHub.quorumNode = nodes[0]
	leaderHub.quorumPrivateTLS = tlsPeers[0]
	leaderHub.membership = &nodeMembershipState{Nodes: map[string]*nodeMember{
		"n1": {NodeID: "n1", Name: "shell:haos", State: nodeStateReady, Capabilities: []string{"executor"}},
		"n2": {NodeID: "n2", Name: "shell:other", State: nodeStateReady, Capabilities: []string{"executor"}},
	}}
	deadline := time.Now().Add(15 * time.Second)
	for !nodes[0].Status().Leader && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if !nodes[0].Status().Leader {
		t.Fatal("leader not elected")
	}
	for i := 1; i < 3; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := nodes[0].PromoteVoter(ctx, peers[i].ID)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
	}
	for nodes[1].Status().LeaderID == "" && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	follower := New(Config{ShellToken: strings.Repeat("g", 32)})
	defer follower.Close()
	if err := follower.RegisterLocalExecutor("haos", identity, localToken); err != nil {
		t.Fatal(err)
	}
	follower.quorumNode = nodes[1]
	follower.quorumPrivateTLS = tlsPeers[1]
	check := func(method, path, body, token, forged string) int {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		if forged != "" {
			req.Header.Set(quorumExecutorHeader, forged)
		}
		rec := httptest.NewRecorder()
		follower.withQuorumAdmission(http.HandlerFunc(follower.requireShell(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))).ServeHTTP(rec, req)
		return rec.Code
	}
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/queue/haos?timeout=0", ""},
		{http.MethodPost, "/queue/haos/result", "{}"},
		{http.MethodPost, "/heartbeat", `{"name":"haos"}`},
	} {
		if got := check(tc.method, tc.path, tc.body, localToken, ""); got != http.StatusNoContent {
			t.Fatalf("authorized %s: got %d", tc.path, got)
		}
	}
	if got := check(http.MethodGet, "/queue/haos?timeout=0", "", strings.Repeat("x", 32), "shell:haos"); got != http.StatusUnauthorized {
		t.Fatalf("forged header accepted: %d", got)
	}
	if got := check(http.MethodPost, "/heartbeat", `{"name":"other"}`, localToken, "shell:haos"); got != http.StatusUnauthorized {
		t.Fatalf("heartbeat for another executor was accepted: %d", got)
	}
	leaderHub.mu.Lock()
	if leaderHub.localExecutors == nil {
		leaderHub.localExecutors = make(map[string]map[string]string)
	}
	leaderHub.localExecutors["shell:haos"] = map[string]string{"server_id": "leader-owned"}
	leaderHub.mu.Unlock()
	if got := check(http.MethodGet, "/queue/haos?timeout=0", "", localToken, ""); got != http.StatusUnauthorized {
		t.Fatalf("remote member accessed a leader-local executor: %d", got)
	}
	leaderHub.mu.Lock()
	delete(leaderHub.localExecutors, "shell:haos")
	leaderHub.mu.Unlock()
	attacker, e := tlsPeers[2].transport("n0")
	if e != nil {
		t.Fatal(e)
	}
	defer attacker.CloseIdleConnections()
	req, e := http.NewRequest(http.MethodGet, peers[0].InternalHubURL+"/queue/haos?timeout=0", nil)
	if e != nil {
		t.Fatal(e)
	}
	req.Header.Set("Authorization", "Bearer "+strings.Repeat("x", 32))
	req.Header.Set(quorumExecutorHeader, "shell:haos")
	req.Header.Set("X-GPTAdmin-Quorum-Forwarded", "n0")
	response, e := (&http.Client{Transport: attacker, Timeout: 3 * time.Second}).Do(req)
	if e != nil {
		t.Fatal(e)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("different member accessed queue: %d", response.StatusCode)
	}
	leaderHub.membership.Nodes["n1"].State = nodeStateDisabled
	if got := check(http.MethodGet, "/queue/haos?timeout=0", "", localToken, ""); got != http.StatusUnauthorized {
		t.Fatalf("disabled member accessed queue: %d", got)
	}
	leaderHub.membership.Nodes["n1"].State = nodeStateOffline
	if got := check(http.MethodGet, "/queue/haos?timeout=0", "", localToken, ""); got != http.StatusNoContent {
		t.Fatalf("previously enrolled offline member could not recover: %d", got)
	}
	leaderHub.membership.Nodes["n1"].State = nodeStateJoining
	if got := check(http.MethodGet, "/queue/haos?timeout=0", "", localToken, ""); got != http.StatusUnauthorized {
		t.Fatalf("unapproved joining member accessed queue: %d", got)
	}
	leaderHub.membership.Nodes["n1"].State = nodeStateReady
	if err := nodes[0].Close(); err != nil {
		t.Fatal(err)
	}
	nextLeader := -1
	electionDeadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(electionDeadline) {
		for _, i := range []int{1, 2} {
			if nodes[i].Status().Leader {
				nextLeader = i
				break
			}
		}
		if nextLeader >= 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if nextLeader < 0 {
		t.Fatal("no leader after primary loss")
	}
	other := 1
	if nextLeader == 1 {
		other = 2
	}
	for nodes[other].Status().LeaderID != peers[nextLeader].ID && time.Now().Before(electionDeadline) {
		time.Sleep(100 * time.Millisecond)
	}
	leaderHub.quorumNode = nodes[nextLeader]
	resumed := New(Config{ShellToken: strings.Repeat("g", 32)})
	defer resumed.Close()
	resumedName := "haos"
	resumedToken := localToken
	resumedIdentity := identity
	if other == 2 {
		resumedName = "other"
		resumedToken = strings.Repeat("z", 32)
		resumedIdentity = map[string]string{"server_id": "other-id", "public_key": "other-key", "fingerprint": "other-fp"}
	}
	if err := resumed.RegisterLocalExecutor(resumedName, resumedIdentity, resumedToken); err != nil {
		t.Fatal(err)
	}
	resumed.quorumNode = nodes[other]
	resumed.quorumPrivateTLS = tlsPeers[other]
	after := httptest.NewRequest(http.MethodGet, "/queue/"+resumedName+"?timeout=0", nil)
	after.Header.Set("Authorization", "Bearer "+resumedToken)
	afterRec := httptest.NewRecorder()
	resumed.withQuorumAdmission(http.HandlerFunc(resumed.requireShell(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))).ServeHTTP(afterRec, after)
	if afterRec.Code != http.StatusNoContent {
		t.Fatalf("local executor after leader loss: got %d", afterRec.Code)
	}
	t.Log("executor admission also succeeds after leader loss and reelection")
	t.Log("real three-voter Raft and mutual TLS: follower queue, result and heartbeat work; forged, foreign and disabled members rejected")
}
