package quorum

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
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type testVoter struct {
	id, key, addr, serverName, certFile, keyFile string
}

func testCluster(t *testing.T) ([]*Node, []testVoter) {
	return testClusterMode(t, true)
}

func testClusterUnpromoted(t *testing.T) ([]*Node, []testVoter) {
	return testClusterMode(t, false)
}

func testClusterMode(t *testing.T, promote bool) ([]*Node, []testVoter) {
	t.Helper()
	root := t.TempDir()
	voters := make([]testVoter, 3)
	listeners := make([]net.Listener, 0, len(voters))
	defer func() {
		for _, listener := range listeners {
			_ = listener.Close()
		}
	}()
	for i, id := range []string{"node-a", "node-b", "node-c"} {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		listeners = append(listeners, listener)
		address := listener.Addr().String()
		keyBytes := make([]byte, 32)
		if _, err := rand.Read(keyBytes); err != nil {
			t.Fatal(err)
		}
		voters[i] = testVoter{id: id, key: hex.EncodeToString(keyBytes), addr: address,
			serverName: id + ".test", certFile: filepath.Join(root, id+".crt"), keyFile: filepath.Join(root, id+".key")}
	}
	caCert, caKey := testCA(t)
	caDER, err := x509.CreateCertificate(rand.Reader, caCert, caCert, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	caFile := filepath.Join(root, "ca.crt")
	if err := os.WriteFile(caFile, caPEM, 0600); err != nil {
		t.Fatal(err)
	}
	for _, voter := range voters {
		writeTestServerCert(t, voter, caCert, caKey)
	}
	for _, listener := range listeners {
		if err := listener.Close(); err != nil {
			t.Fatal(err)
		}
	}
	listeners = nil
	peers := make([]Peer, len(voters))
	for i, voter := range voters {
		sum := sha256.Sum256([]byte(voter.key))
		peers[i] = Peer{ID: voter.id, Address: voter.addr, ServerName: voter.serverName,
			NodeKeyHash: hex.EncodeToString(sum[:]), FailureDomain: "domain-" + voter.id}
	}
	nodes := make([]*Node, 0, len(voters))
	for i, voter := range voters {
		node, err := Open(Config{
			ClusterID: "test-cluster", NodeID: voter.id, BootstrapperID: "node-a", NodeKey: voter.key,
			BindAddress: voter.addr, Advertise: voter.addr,
			DataDir: filepath.Join(root, voter.id), TLSCertFile: voter.certFile,
			TLSKeyFile: voter.keyFile, TLSCAFile: caFile, Peers: peers,
			Bootstrap: i == 0, RPCDeadline: 2 * time.Second,
			ApplyDeadline: 2 * time.Second, BarrierDeadline: 2 * time.Second,
		})
		if err != nil {
			for _, running := range nodes {
				_ = running.Close()
			}
			t.Fatalf("open %s: %v", voter.id, err)
		}
		nodes = append(nodes, node)
	}
	t.Cleanup(func() {
		for _, node := range nodes {
			if node != nil {
				_ = node.Close()
			}
		}
	})
	if promote {
		leader := waitForLeader(t, nodes)
		for _, voter := range voters {
			if voter.id == leader.Status().NodeID {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err := leader.PromoteVoter(ctx, voter.id)
			cancel()
			if err != nil {
				t.Fatalf("promote %s: %v", voter.id, err)
			}
		}
		waitForVoterCount(t, nodes, 3)
	}
	return nodes, voters
}

func testCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "GPTAdmin quorum test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	return cert, key
}

func writeTestServerCert(t *testing.T, voter testVoter, ca *x509.Certificate, caKey *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: voter.serverName},
		DNSNames: []string{voter.serverName}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	priv, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(voter.certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(voter.keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: priv}), 0600); err != nil {
		t.Fatal(err)
	}
}

func waitForLeader(t *testing.T, nodes []*Node) *Node {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		var leader *Node
		count := 0
		for _, node := range nodes {
			if node != nil && node.Status().Leader {
				leader = node
				count++
			}
		}
		if count == 1 {
			return leader
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("expected exactly one leader; statuses=%+v %+v %+v", nodes[0].Status(), nodes[1].Status(), nodes[2].Status())
	return nil
}

func waitForVoterCount(t *testing.T, nodes []*Node, count int) {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		ready := true
		for _, node := range nodes {
			if node == nil || node.Status().VoterCount != count {
				ready = false
				break
			}
		}
		if ready {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("expected %d voters; statuses=%+v %+v %+v", count, nodes[0].Status(), nodes[1].Status(), nodes[2].Status())
}

func TestQuorumPromotionRestoresCommittedVotersAfterRestart(t *testing.T) {
	nodes, _ := testClusterUnpromoted(t)
	leader := waitForLeader(t, nodes)
	if got := leader.Status().VoterCount; got != 1 {
		t.Fatalf("bootstrap must begin with only its trusted voter; got %d", got)
	}
	for _, node := range nodes {
		if node == leader {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := leader.PromoteVoter(ctx, node.Status().NodeID)
		cancel()
		if err != nil {
			t.Fatalf("promote staged voter %s: %v", node.Status().NodeID, err)
		}
	}
	waitForVoterCount(t, nodes, 3)

	configs := make([]Config, len(nodes))
	for i, node := range nodes {
		configs[i] = node.config
		if err := node.Close(); err != nil {
			t.Fatalf("close voter before restart: %v", err)
		}
	}
	restarted := make([]*Node, 0, len(configs))
	for _, config := range configs {
		node, err := Open(config)
		if err != nil {
			t.Fatalf("reopen voter %s: %v", config.NodeID, err)
		}
		restarted = append(restarted, node)
	}
	t.Cleanup(func() {
		for _, node := range restarted {
			_ = node.Close()
		}
	})
	waitForLeader(t, restarted)
	waitForVoterCount(t, restarted, 3)
}

func TestQuorumPromotionRejectsUnknownPeerAndSharedFailureDomain(t *testing.T) {
	nodes, _ := testClusterUnpromoted(t)
	leader := waitForLeader(t, nodes)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := leader.PromoteVoter(ctx, "not-pre-enrolled"); err == nil {
		t.Fatal("public reachability alone must not enroll a voter")
	}
	leader.config.Peers[2].FailureDomain = leader.config.Peers[1].FailureDomain
	if err := leader.PromoteVoter(ctx, "node-b"); err == nil {
		t.Fatal("nodes sharing a failure domain must not be promoted as independent voters")
	}
	if got := leader.Status().VoterCount; got != 1 {
		t.Fatalf("rejected promotion changed the committed voter set: %d", got)
	}
}

func TestQuorumCommitReplicatesAndMinorityCannotWrite(t *testing.T) {
	nodes, _ := testCluster(t)
	leader := waitForLeader(t, nodes)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err := leader.ApplyRecord(ctx, "task", "request-1", Record{Revision: 1, Value: []byte(`{"status":"queued"}`)})
	cancel()
	if err != nil {
		t.Fatalf("majority commit: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		allReplicated := true
		for _, node := range nodes {
			record, ok := node.fsm.get("task", "request-1")
			if !ok || record.Revision != 1 {
				allReplicated = false
				break
			}
		}
		if allReplicated {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	for _, node := range nodes {
		if record, ok := node.fsm.get("task", "request-1"); !ok || record.Revision != 1 {
			t.Fatalf("committed record did not reach %s: present=%v record=%+v", node.config.NodeID, ok, record)
		}
	}
	for _, node := range nodes {
		if node != leader {
			if err := node.Close(); err != nil {
				t.Fatalf("close minority peer: %v", err)
			}
		}
	}
	ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
	err = leader.ApplyRecord(ctx, "task", "request-2", Record{Revision: 1, Value: []byte(`{"status":"must-not-commit"}`)})
	cancel()
	if err == nil {
		t.Fatal("minority leader acknowledged a write without quorum")
	}
	if _, ok := leader.fsm.get("task", "request-2"); ok {
		t.Fatal("minority-only record was applied locally")
	}
}

func TestQuorumApplyRecordRetryIsIdempotent(t *testing.T) {
	nodes, _ := testCluster(t)
	leader := waitForLeader(t, nodes)
	record := Record{Revision: 1, Value: []byte(`{"status":"queued"}`)}
	for attempt := 0; attempt < 2; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := leader.ApplyRecord(ctx, "task", "request-retry", record)
		cancel()
		if err != nil {
			t.Fatalf("identical retry %d should reconcile an already committed record: %v", attempt+1, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err := leader.ApplyRecord(ctx, "task", "request-retry", Record{Revision: 1, Value: []byte(`{"status":"different"}`)})
	cancel()
	if err == nil {
		t.Fatal("same revision with different content must be rejected")
	}
}

func TestQuorumLeaderHubURLPrefersPrivateForwardEndpoint(t *testing.T) {
	nodes, _ := testCluster(t)
	leader := waitForLeader(t, nodes)
	leaderID := leader.Status().NodeID
	const publicURL = "https://quorum44-node.example"
	const internalURL = "http://192.168." + "2.44:9001"
	for i := range leader.config.Peers {
		if leader.config.Peers[i].ID == leaderID {
			leader.config.Peers[i].HubURL = publicURL
			leader.config.Peers[i].InternalHubURL = internalURL
		}
	}
	gotPublic, publicID := leader.LeaderHubURL()
	if publicID != leaderID || gotPublic != publicURL {
		t.Fatalf("public leader URL = (%q, %q), want (%q, %q)", gotPublic, publicID, publicURL, leaderID)
	}
	gotForward, forwardID := leader.LeaderForwardURL()
	if forwardID != leaderID || gotForward != internalURL {
		t.Fatalf("leader forward URL = (%q, %q), want private route (%q, %q)", gotForward, forwardID, internalURL, leaderID)
	}
	for i := range leader.config.Peers {
		if leader.config.Peers[i].ID == leaderID {
			leader.config.Peers[i].InternalHubURL = ""
		}
	}
	legacyForward, _ := leader.LeaderForwardURL()
	if legacyForward != publicURL {
		t.Fatalf("legacy forward URL = %q, want public compatibility route %q", legacyForward, publicURL)
	}
}

func TestValidateInternalHubURLRequiresPrivateOrigin(t *testing.T) {
	privateURL := "http://192.168." + "2.44:9001"
	if err := validateInternalHubURL(privateURL); err != nil {
		t.Fatalf("valid private URL rejected: %v", err)
	}
	if err := validateInternalHubURL("https://192.168." + "2.44:9001"); err != nil {
		t.Fatalf("valid private HTTPS URL rejected: %v", err)
	}
	for _, raw := range []string{
		"http://8.8.8.8:9001",
		"http://leader.example:9001",
		"http://192.168." + "2.44",
		"http://192.168." + "2.44:9001/path",
		"http://user@192.168." + "2.44:9001",
	} {
		if err := validateInternalHubURL(raw); err == nil {
			t.Errorf("internal_hub_url %q was accepted", raw)
		}
	}
}

func TestQuorumApplyBatchIsAtomicAndReplicatesEveryRow(t *testing.T) {
	nodes, _ := testCluster(t)
	leader := waitForLeader(t, nodes)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err := leader.ApplyBatch(ctx, []Mutation{
		{Namespace: "task_records", Key: "shell:task-1", Record: Record{Revision: 1, Value: []byte(`{"status":"queued"}`)}},
		{Namespace: "task_requests", Key: "request-1", Record: Record{Revision: 1, Value: []byte(`{"job_id":"task-1"}`)}},
	})
	cancel()
	if err != nil {
		t.Fatalf("majority batch commit: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		allReplicated := true
		for _, node := range nodes {
			for namespace, key := range map[string]string{"task_records": "shell:task-1", "task_requests": "request-1"} {
				if record, ok := node.fsm.get(namespace, key); !ok || record.Revision != 1 {
					allReplicated = false
				}
			}
		}
		if allReplicated {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	for _, node := range nodes {
		for namespace, key := range map[string]string{"task_records": "shell:task-1", "task_requests": "request-1"} {
			if record, ok := node.fsm.get(namespace, key); !ok || record.Revision != 1 {
				t.Fatalf("committed batch row missing on %s: %s/%s present=%v record=%+v", node.config.NodeID, namespace, key, ok, record)
			}
		}
	}
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	err = leader.ApplyBatch(ctx, []Mutation{
		{Namespace: "task_records", Key: "shell:must-not-half-commit", Record: Record{Revision: 1, Value: []byte(`{"status":"queued"}`)}},
		{Namespace: "task_requests", Key: "request-1", Record: Record{Revision: 1, Value: []byte(`{"job_id":"different"}`)}},
	})
	cancel()
	if !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("conflicting batch should be rejected, got %v", err)
	}
	if _, ok := leader.fsm.get("task_records", "shell:must-not-half-commit"); ok {
		t.Fatal("rejected batch partially committed its first row")
	}
}

func TestQuorumRejectsTwoVoterConfiguration(t *testing.T) {
	localKey := "0123456789abcdef0123456789abcdef"
	peerKey := "abcdef0123456789abcdef0123456789"
	local := Peer{ID: "node-a", Address: "127.0.0.1:19001", ServerName: "a.test", NodeKeyHash: nodeKeyHash(localKey)}
	peer := Peer{ID: "node-b", Address: "127.0.0.1:19002", ServerName: "b.test", NodeKeyHash: nodeKeyHash(peerKey)}
	c := Config{ClusterID: "test", NodeID: local.ID, NodeKey: localKey,
		BindAddress: local.Address, Advertise: local.Address, DataDir: t.TempDir(),
		TLSCertFile: "cert.pem", TLSKeyFile: "key.pem", Peers: []Peer{local, peer}}
	if _, err := c.normalized(); err == nil {
		t.Fatal("two voters must not form an eligible quorum cluster")
	}
}

func TestQuorumRejectsDuplicateFailureDomains(t *testing.T) {
	localKey := "0123456789abcdef0123456789abcdef"
	peerKeyA := "abcdef0123456789abcdef0123456789"
	peerKeyB := "fedcba9876543210fedcba9876543210"
	local := Peer{ID: "node-a", Address: "127.0.0.1:19001", ServerName: "a.test", NodeKeyHash: nodeKeyHash(localKey), FailureDomain: "host-a"}
	peerA := Peer{ID: "node-b", Address: "127.0.0.1:19002", ServerName: "b.test", NodeKeyHash: nodeKeyHash(peerKeyA), FailureDomain: "shared-host"}
	peerB := Peer{ID: "node-c", Address: "127.0.0.1:19003", ServerName: "c.test", NodeKeyHash: nodeKeyHash(peerKeyB), FailureDomain: "shared-host"}
	c := Config{ClusterID: "test", NodeID: local.ID, BootstrapperID: local.ID, NodeKey: localKey,
		BindAddress: local.Address, Advertise: local.Address, DataDir: t.TempDir(),
		TLSCertFile: "cert.pem", TLSKeyFile: "key.pem", Peers: []Peer{local, peerA, peerB}, Bootstrap: true}
	if _, err := c.normalized(); err == nil {
		t.Fatal("voters in the same failure domain must not count toward quorum")
	}
}
