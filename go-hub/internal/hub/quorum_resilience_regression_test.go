package hub

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/megamen32/gptadmin/go-hub/internal/hub/quorum"
)

// These isolated regressions exercise current implementation code, temporary
// SQLite files and a temporary loopback Raft process. They are not fleet or
// public-ingress acceptance tests and never read production auth/state.
func reviewWriter(t *testing.T, dir string) *Server {
	t.Helper()
	s := New(Config{
		ConfigDir: dir, AuthMode: "writer", QuorumConfigFile: filepath.Join(dir, "fixture-quorum.json"),
		CtlToken: "review-fixture-owner", OAuthClientSecret: "review-fixture-signing",
		PublicOrigin: "https://review.invalid", MCPResource: "https://review.invalid/mcp",
	})
	if s.secretStore == nil || s.secretStoreErr != nil {
		t.Fatalf("temporary secret store: %v", s.secretStoreErr)
	}
	if err := s.RegisterLocalExecutor("review", map[string]string{
		"server_id": "n0", "public_key": "review-public", "fingerprint": "review-fingerprint",
	}, strings.Repeat("x", 32)); err != nil {
		t.Fatal(err)
	}
	if err := s.InitializeAuthContinuity(); err != nil {
		t.Fatal(err)
	}
	s.ensureWriterSelfEntry()
	return s
}

func reviewQuorumConfig(t *testing.T, root string) quorum.Config {
	t.Helper()
	now := time.Now()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "review-ca"},
		DNSNames: []string{"node0.test"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage:    x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath, statePath := filepath.Join(root, "cert.pem"), filepath.Join(root, "key.pem"), filepath.Join(root, "state.key")
	for path, data := range map[string][]byte{
		certPath:  pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		keyPath:   pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
		statePath: bytes.Repeat([]byte{71}, 32),
	} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	peers := make([]quorum.Peer, 3)
	for i := range peers {
		secret := strings.Repeat(string(rune('a'+i)), 32)
		sum := sha256.Sum256([]byte(secret))
		peers[i] = quorum.Peer{
			ID:          []string{"n0", "n1", "n2"}[i],
			Address:     []string{addr, "127.0.0.1:1", "127.0.0.1:2"}[i],
			ServerName:  []string{"node0.test", "node1.test", "node2.test"}[i],
			NodeKeyHash: hex.EncodeToString(sum[:]),
		}
	}
	return quorum.Config{
		ClusterID: "review-cluster", NodeID: "n0", BootstrapperID: "n0",
		NodeKey: strings.Repeat("a", 32), BindAddress: addr, Advertise: addr,
		DataDir: filepath.Join(root, "raft"), TLSCertFile: certPath, TLSKeyFile: keyPath, TLSCAFile: certPath,
		StateKeyFile: statePath, Peers: peers, Bootstrap: true,
		RPCDeadline: time.Second, ApplyDeadline: 3 * time.Second, BarrierDeadline: time.Second,
		ExternalSnapshotMaxBytes: 64 << 20, ExternalSnapshotFreeReserveBytes: 1 << 20,
	}
}

func reviewOpenQuorum(t *testing.T, s *Server, cfg quorum.Config) *quorum.Node {
	t.Helper()
	s.quorumClusterID = cfg.ClusterID
	s.quorumBootstrapperID = cfg.BootstrapperID
	apply := func(logIndex, externalSequence uint64, mutations []quorum.Mutation) error {
		marker := false
		for _, m := range mutations {
			if m.Namespace == "cluster_meta" && m.Key == "task_baseline" {
				marker = true
			}
		}
		logCheckpoint := func(label string) {
			var applied, chain uint64
			err := s.withTaskReadDatabase(func(tx *sql.Tx) error {
				return tx.QueryRow("SELECT m.applied_index,c.sequence FROM task_quorum_meta m JOIN task_quorum_chain c ON m.id=c.id WHERE m.id=1").Scan(&applied, &chain)
			})
			t.Logf("%s marker callback: raft_log_index=%d external_sequence=%d meta=%d chain=%d read_error=%v", label, logIndex, externalSequence, applied, chain, err)
		}
		if marker {
			logCheckpoint("before")
		}
		err := s.applyTaskQuorumMutations(logIndex, externalSequence, mutations)
		if marker {
			logCheckpoint("after")
		}
		return err
	}
	n, err := quorum.OpenWithExternalState(cfg, apply, quorum.ExternalSnapshotCallbacks{
		Snapshot: s.snapshotTaskQuorumState, Restore: s.restoreTaskQuorumState,
	})
	if err != nil {
		t.Fatal(err)
	}
	s.quorumNode = n
	return n
}

func reviewTask(t *testing.T, id string) quorum.Mutation {
	t.Helper()
	row, err := json.Marshal(quorumTaskRecordRow{
		Kind: "shell", Key: id, Updated: 1, Done: 2, Revision: 1,
		Payload: []byte("{\"id\":\"" + id + "\",\"status\":\"completed\",\"created_at\":1,\"done_at\":2}"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return quorum.Mutation{Namespace: "task_records/shell", Key: id, External: true, Record: quorum.Record{Revision: 1, Value: row}}
}

func TestQuorumBaselineCheckpointNeverRewinds(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GPTADMIN_HOME", filepath.Join(root, "home"))
	s := reviewWriter(t, filepath.Join(root, "hub"))
	alive := true
	defer func() {
		if alive {
			s.Close()
		}
	}()
	s.quorumClusterID = "review-cluster"
	existing := reviewTask(t, "review-existing-receipt")
	if err := s.applyTaskQuorumMutations(1, 1, []quorum.Mutation{existing}); err != nil {
		t.Fatal(err)
	}
	qcfg := reviewQuorumConfig(t, root)
	qpath := filepath.Join(root, "quorum.json")
	qbytes, err := json.Marshal(qcfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(qpath, qbytes, 0600); err != nil {
		t.Fatal(err)
	}
	s.cfg.QuorumConfigFile = qpath
	n := reviewOpenQuorum(t, s, qcfg)
	deadline := time.Now().Add(10 * time.Second)
	for !n.Status().Leader && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	if !n.Status().Leader {
		t.Fatal("temporary leader not elected")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	err = s.seedTaskQuorumBaseline(ctx)
	cancel()
	if err != nil {
		t.Fatalf("actual seedTaskQuorumBaseline: %v", err)
	}
	var applied, chain uint64
	var authorities int
	if err = s.withTaskReadDatabase(func(tx *sql.Tx) error {
		if e := tx.QueryRow("SELECT m.applied_index,c.sequence FROM task_quorum_meta m JOIN task_quorum_chain c ON m.id=c.id WHERE m.id=1").Scan(&applied, &chain); e != nil {
			return e
		}
		return tx.QueryRow("SELECT count(*) FROM task_quorum_authority").Scan(&authorities)
	}); err != nil {
		t.Fatal(err)
	}
	t.Logf("actual bootstrap: external=%d meta=%d chain=%d authority_domains=%d", n.ExternalAppliedIndex(), applied, chain, authorities)
	if authorities != len(quorumRequiredAuthorityRecords) {
		t.Fatalf("incomplete authority fixture: %d", authorities)
	}
	if chain != applied {
		t.Errorf("baseline marker rewound checkpoint: chain=%d applied=%d", chain, applied)
	}
	next := reviewTask(t, "review-next-receipt")
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	err = n.ApplyBatch(ctx, []quorum.Mutation{next})
	cancel()
	if err != nil {
		t.Errorf("next real Raft task mutation after baseline failed: %v", err)
	}
	target := n.ExternalAppliedIndex()
	restartCfg := s.cfg
	s.Close()
	alive = false
	restarted := New(restartCfg)
	defer restarted.Close()
	n2 := reviewOpenQuorum(t, restarted, qcfg)
	deadline = time.Now().Add(6 * time.Second)
	var applyErr error
	for time.Now().Before(deadline) {
		_, applyErr = n2.ExternalApplyError()
		if applyErr != nil || (n2.Status().Leader && n2.ExternalAppliedIndex() >= target) {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if applyErr != nil {
		t.Errorf("restart replay rejected durable state: %v", applyErr)
	}
	if n2.ExternalAppliedIndex() < target {
		t.Errorf("restart did not regain external index: got=%d want=%d", n2.ExternalAppliedIndex(), target)
	}
	var existingCount int
	if err = restarted.withTaskReadDatabase(func(tx *sql.Tx) error {
		return tx.QueryRow("SELECT count(*) FROM task_records WHERE kind='shell' AND key='review-existing-receipt' AND revision=1").Scan(&existingCount)
	}); err != nil {
		t.Fatal(err)
	}
	if existingCount != 1 {
		t.Fatal("original receipt was not retained")
	}
	t.Logf("restart: external=%d target=%d original_receipt_count=%d", n2.ExternalAppliedIndex(), target, existingCount)
}

func TestPendingLocalRoundTripWithDormantQuorumConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GPTADMIN_HOME", filepath.Join(dir, "home"))
	statePath := filepath.Join(dir, "quorum-state.key")
	if err := os.WriteFile(statePath, bytes.Repeat([]byte{37}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	qpath := filepath.Join(dir, "quorum.json")
	qbytes, err := json.Marshal(quorum.Config{ClusterID: "review-cluster", StateKeyFile: statePath})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(qpath, qbytes, 0600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{ConfigDir: filepath.Join(dir, "hub"), QuorumConfigFile: qpath}
	s := New(cfg)
	if s.quorumNode != nil {
		t.Fatal("fixture unexpectedly has live quorum")
	}
	plain := []byte("{\"argv\":[\"true\"]}")
	sealed, err := s.sealPendingValue(quorumPendingShellNamespace, "review-local-job", plain)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.openPendingValue(quorumPendingShellNamespace, "review-local-job", sealed)
	if err != nil || !bytes.Equal(got, plain) {
		t.Errorf("new local envelope fails with dormant quorum path: %v", err)
	}
	if _, err = s.openPendingValue(quorumPendingShellNamespace, "another-job", sealed); err == nil {
		t.Error("wrong task ID accepted")
	}
	s.cfg.QuorumConfigFile = ""
	got, err = s.openPendingValue(quorumPendingShellNamespace, "review-local-job", sealed)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("local-cipher control case failed: %v", err)
	}
	t.Log("same envelope decrypts with local-cipher selection; ciphertext and local key are valid")
	s.Close()
	restarted := New(cfg)
	defer restarted.Close()
	got, err = restarted.openPendingValue(quorumPendingShellNamespace, "review-local-job", sealed)
	if err != nil || !bytes.Equal(got, plain) {
		t.Errorf("restart cannot open preserved local pending input: %v", err)
	}
}

func TestPendingQuorumEnvelopeRoundTripAndRejectsIdentity(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GPTADMIN_HOME", filepath.Join(root, "home"))
	s := reviewWriter(t, filepath.Join(root, "hub"))
	defer s.Close()
	cfg := reviewQuorumConfig(t, root)
	qpath := filepath.Join(root, "quorum.json")
	qbytes, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(qpath, qbytes, 0600); err != nil {
		t.Fatal(err)
	}
	s.cfg.QuorumConfigFile = qpath
	n := reviewOpenQuorum(t, s, cfg)
	deadline := time.Now().Add(10 * time.Second)
	for !n.Status().Leader && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	if !n.Status().Leader {
		t.Fatal("temporary quorum leader not elected")
	}
	plain := []byte(`{"argv":["true"]}`)
	sealed, err := s.sealPendingValue(quorumPendingShellNamespace, "review-quorum-job", plain)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.openPendingValue(quorumPendingShellNamespace, "review-quorum-job", sealed)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("quorum envelope round-trip failed: %v", err)
	}
	if _, err := s.openPendingValue(quorumPendingShellNamespace, "other-job", sealed); err == nil {
		t.Fatal("quorum envelope accepted a different task ID")
	}
	if _, err := s.openPendingValue("different-namespace", "review-quorum-job", sealed); err == nil {
		t.Fatal("quorum envelope accepted a different namespace")
	}
}
func TestReviewOrdinaryQuorumRecoveryRejectsWrongIdentityAndKey(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GPTADMIN_HOME", filepath.Join(root, "home"))
	s := reviewWriter(t, filepath.Join(root, "hub"))
	closed := false
	defer func() {
		if !closed {
			s.Close()
		}
	}()
	cfg := reviewQuorumConfig(t, root)
	qpath := filepath.Join(root, "quorum.json")
	writeConfig := func() {
		b, e := json.Marshal(cfg)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(qpath, b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	writeConfig()
	s.cfg.QuorumConfigFile = qpath
	n := reviewOpenQuorum(t, s, cfg)
	plain := []byte("{\"argv\":[\"true\"]}")
	sealed, e := s.sealPendingValue(quorumPendingShellNamespace, "review-retained-job", plain)
	if e != nil {
		t.Fatal(e)
	}
	n.Close()
	s.quorumNode = nil
	assertOpens := func(label string) {
		got, e := s.openPendingValue(quorumPendingShellNamespace, "review-retained-job", sealed)
		if e != nil || !bytes.Equal(got, plain) {
			t.Fatalf("%s ordinary quorum recovery: %v", label, e)
		}
	}
	assertOpens("correct key")
	for _, identity := range [][2]string{{quorumPendingShellNamespace, "wrong-id"}, {"wrong-namespace", "review-retained-job"}} {
		if _, e = s.openPendingValue(identity[0], identity[1], sealed); e == nil {
			t.Fatalf("ordinary recovery accepted wrong identity: %v", identity)
		}
	}
	goodPath := cfg.StateKeyFile
	badPath := filepath.Join(root, "wrong-state.key")
	if e = os.WriteFile(badPath, bytes.Repeat([]byte{99}, 32), 0600); e != nil {
		t.Fatal(e)
	}
	cfg.StateKeyFile = badPath
	writeConfig()
	if _, e = s.openPendingValue(quorumPendingShellNamespace, "review-retained-job", sealed); e == nil {
		t.Fatal("ordinary recovery accepted a wrong cryptographic key")
	}
	local, e := s.sealPendingValue(quorumPendingShellNamespace, "review-new-local-job", plain)
	if e != nil {
		t.Fatal(e)
	}
	got, e := s.openPendingValue(quorumPendingShellNamespace, "review-new-local-job", local)
	if e != nil || !bytes.Equal(got, plain) {
		t.Fatalf("local fallback blocked by unrelated quorum key: %v", e)
	}
	cfg.StateKeyFile = goodPath
	writeConfig()
	assertOpens("restored key")
	restartCfg := s.cfg
	s.Close()
	closed = true
	restarted := New(restartCfg)
	defer restarted.Close()
	if restarted.quorumNode != nil {
		t.Fatal("restart fixture unexpectedly starts quorum")
	}
	got, e = restarted.openPendingValue(quorumPendingShellNamespace, "review-retained-job", sealed)
	if e != nil || !bytes.Equal(got, plain) {
		t.Fatalf("ordinary restarted recovery rejected retained quorum envelope: %v", e)
	}
	t.Log("quorumNode=nil: correct key and restart open retained quorum envelope; wrong ID/namespace/key rejected; new local envelope remains usable")
}

func TestQuorumBaselineMultiBatchKeepsExternalCheckpoint(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GPTADMIN_HOME", filepath.Join(root, "home"))
	s := reviewWriter(t, filepath.Join(root, "hub"))
	defer s.Close()
	s.quorumClusterID = "review-cluster"
	rows := make([]quorum.Mutation, 0, 2)
	for _, id := range []string{"receipt-large-one", "receipt-large-two"} {
		payload, err := json.Marshal(map[string]any{"id": id, "status": "completed", "created_at": 1, "done_at": 2, "result": strings.Repeat("x", 3<<20)})
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(quorumTaskRecordRow{Kind: "shell", Key: id, Updated: 1, Done: 2, Revision: 1, Payload: payload})
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, quorum.Mutation{Namespace: "task_records/shell", Key: id, External: true, Record: quorum.Record{Revision: 1, Value: encoded}})
	}
	if err := s.applyTaskQuorumMutations(1, 1, rows); err != nil {
		t.Fatal(err)
	}
	qcfg := reviewQuorumConfig(t, root)
	n := reviewOpenQuorum(t, s, qcfg)
	deadline := time.Now().Add(10 * time.Second)
	for !n.Status().Leader && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	if !n.Status().Leader {
		t.Fatal("fixture leader was not elected")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	err := s.seedTaskQuorumBaseline(ctx)
	cancel()
	var applied, chain uint64
	if readErr := s.withTaskReadDatabase(func(tx *sql.Tx) error {
		return tx.QueryRow("SELECT m.applied_index,c.sequence FROM task_quorum_meta m JOIN task_quorum_chain c ON m.id=c.id WHERE m.id=1").Scan(&applied, &chain)
	}); readErr != nil {
		t.Fatal(readErr)
	}
	t.Logf("multi-batch baseline external=%d applied=%d chain=%d seed_error=%v", n.ExternalAppliedIndex(), applied, chain, err)
	if err != nil {
		t.Fatalf("valid multi-batch baseline must preserve coherent sequence: %v", err)
	}
	if applied != chain || applied != n.ExternalAppliedIndex() {
		t.Fatalf("baseline coordinates disagree: applied=%d chain=%d external=%d", applied, chain, n.ExternalAppliedIndex())
	}
}

func TestQuorumBaselineRetainedCheckpointPreservesProportionalBatches(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GPTADMIN_HOME", filepath.Join(root, "home"))
	s := reviewWriter(t, filepath.Join(root, "hub"))
	defer s.Close()
	s.quorumClusterID = "review-cluster"
	for sequence := uint64(1); sequence <= 22; sequence++ {
		if err := s.applyTaskQuorumMutations(sequence, sequence, []quorum.Mutation{reviewTask(t, fmt.Sprintf("retained-receipt-%02d", sequence))}); err != nil {
			t.Fatal(err)
		}
	}
	qcfg := reviewQuorumConfig(t, root)
	n := reviewOpenQuorum(t, s, qcfg)
	deadline := time.Now().Add(10 * time.Second)
	for !n.Status().Leader && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	if !n.Status().Leader {
		t.Fatal("fixture leader was not elected")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	err := s.seedTaskQuorumBaseline(ctx)
	cancel()
	var applied, chain uint64
	if readErr := s.withTaskReadDatabase(func(tx *sql.Tx) error {
		return tx.QueryRow("SELECT m.applied_index,c.sequence FROM task_quorum_meta m JOIN task_quorum_chain c ON m.id=c.id WHERE m.id=1").Scan(&applied, &chain)
	}); readErr != nil {
		t.Fatal(readErr)
	}
	t.Logf("retained baseline external=%d applied=%d chain=%d seed_error=%v ready=%v", n.ExternalAppliedIndex(), applied, chain, err, s.taskQuorumReplicaReady())
	if err != nil {
		t.Fatalf("seed valid retained baseline: %v", err)
	}
	if applied != chain || applied != n.ExternalAppliedIndex() {
		t.Fatalf("baseline coordinates disagree: applied=%d chain=%d external=%d", applied, chain, n.ExternalAppliedIndex())
	}
	next := reviewTask(t, "retained-next")
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	if err = n.ApplyBatch(ctx, []quorum.Mutation{next}); err != nil {
		t.Fatalf("next mutation after retained baseline: %v", err)
	}
	cancel()
}
