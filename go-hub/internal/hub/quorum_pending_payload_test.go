package hub

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/megamen32/gptadmin/go-hub/internal/hub/quorum"
)

func TestQuorumPendingInputEncryptedAndQuarantinedAfterRestart(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "hub")
	s := New(Config{ConfigDir: configDir, QuorumConfigFile: "fixture"})
	now := time.Now()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fixture-ca"}, DNSNames: []string{"node0.test"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	certDER, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath, statePath := filepath.Join(root, "cert.pem"), filepath.Join(root, "key.pem"), filepath.Join(root, "state.key")
	for path, data := range map[string][]byte{certPath: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}), keyPath: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), statePath: bytes.Repeat([]byte{71}, 32)} {
		if err = os.WriteFile(path, data, 0600); err != nil {
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
		secret := strings.Repeat(string(rune(97+i)), 32)
		sum := sha256.Sum256([]byte(secret))
		address := []string{addr, "127.0.0.1:1", "127.0.0.1:2"}[i]
		peers[i] = quorum.Peer{ID: []string{"n0", "n1", "n2"}[i], Address: address, ServerName: []string{"node0.test", "node1.test", "node2.test"}[i], NodeKeyHash: hex.EncodeToString(sum[:])}
	}
	n, err := quorum.OpenWithExternalState(quorum.Config{ClusterID: "fixture", NodeID: "n0", BootstrapperID: "n0", NodeKey: strings.Repeat("a", 32), BindAddress: addr, Advertise: addr, DataDir: filepath.Join(root, "raft"), TLSCertFile: certPath, TLSKeyFile: keyPath, TLSCAFile: certPath, StateKeyFile: statePath, Peers: peers, Bootstrap: true, RPCDeadline: time.Second, ApplyDeadline: time.Second, BarrierDeadline: time.Second, ExternalSnapshotMaxBytes: 64 << 20, ExternalSnapshotFreeReserveBytes: 1 << 20}, s.applyTaskQuorumMutations, quorum.ExternalSnapshotCallbacks{Snapshot: s.snapshotTaskQuorumState, Restore: s.restoreTaskQuorumState})
	if err != nil {
		t.Fatal(err)
	}
	s.quorumNode = n
	s.quorumClusterID = "fixture"
	deadline := time.Now().Add(10 * time.Second)
	for !n.Status().Leader && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if !n.Status().Leader {
		t.Fatal("fixture leader not elected")
	}
	s.mu.Lock()
	owner, err := s.ensureTaskOwnerLocked()
	if err != nil {
		s.mu.Unlock()
		t.Fatal(err)
	}
	pending := &shellJob{ID: "pending-1", OwnerID: owner, Server: "haos", ToolName: "shell_exec", Status: "queued", CreatedAt: nowFloat(), Arguments: map[string]any{"cmd": "private-command-sentinel"}, Env: map[string]any{"PASSWORD": "private-env-sentinel"}, SecretValues: []string{"private-redaction-sentinel"}}
	s.shellJobs[pending.ID] = pending
	err = s.saveTaskStateLocked(pending.ID)
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	records, err := s.readTaskRecords(pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	saved := records.Shell[pending.ID]
	encoded, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-command-sentinel", "private-env-sentinel", "private-redaction-sentinel"} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("plaintext leaked into persisted record: %s", secret)
		}
	}
	for _, path := range []string{s.taskDatabasePath(), s.taskDatabasePath() + "-wal", s.taskDatabasePath() + "-journal"} {
		data, readErr := os.ReadFile(path)
		if readErr != nil && !os.IsNotExist(readErr) {
			t.Fatal(readErr)
		}
		for _, secret := range []string{"private-command-sentinel", "private-env-sentinel", "private-redaction-sentinel"} {
			if bytes.Contains(data, []byte(secret)) {
				t.Fatal("plaintext leaked into SQLite or its journal")
			}
		}
	}
	if len(saved.SealedPending) == 0 {
		t.Fatal("queued input was not stored encrypted")
	}
	plain, err := n.OpenExternalValue(quorumPendingShellNamespace, pending.ID, 1, saved.SealedPending)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := n.OpenExternalValue(quorumPendingShellNamespace, "another-job", 1, saved.SealedPending); err == nil {
		t.Fatal("sealed input accepted for a different task")
	}
	var sealed map[string]any
	if err := json.Unmarshal(saved.SealedPending, &sealed); err != nil {
		t.Fatal(err)
	}
	ciphertext, ok := sealed["ciphertext"].(string)
	if !ok || len(ciphertext) < 2 {
		t.Fatal("encrypted input has no ciphertext")
	}
	if ciphertext[0] == 65 {
		ciphertext = "B" + ciphertext[1:]
	} else {
		ciphertext = "A" + ciphertext[1:]
	}
	sealed["ciphertext"] = ciphertext
	tampered, err := json.Marshal(sealed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := n.OpenExternalValue(quorumPendingShellNamespace, pending.ID, 1, tampered); err == nil {
		t.Fatal("tampered encrypted input was accepted")
	}
	var decoded quorumPendingShellInput
	if json.Unmarshal(plain, &decoded) != nil || decoded.Arguments["cmd"] != "private-command-sentinel" || decoded.Env["PASSWORD"] != "private-env-sentinel" || len(decoded.SecretValues) != 1 {
		t.Fatal("encrypted queued input did not round-trip")
	}
	firstRevision := saved.Revision
	firstCipher := append([]byte(nil), saved.SealedPending...)
	s.mu.Lock()
	err = s.saveTaskStateLocked(pending.ID)
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	records, err = s.readTaskRecords(pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := records.Shell[pending.ID]; got.Revision != firstRevision || !bytes.Equal(got.SealedPending, firstCipher) {
		t.Fatal("unchanged queued input was unnecessarily resealed")
	}
	s.mu.Lock()
	started := &shellJob{ID: "started-1", OwnerID: owner, Server: "haos", ToolName: "shell_exec", Status: "queued", CreatedAt: nowFloat(), Arguments: map[string]any{"cmd": "once-only"}}
	s.shellJobs[started.ID] = started
	err = s.saveTaskStateLocked(started.ID)
	if err == nil {
		started.Status = "running"
		started.StartedAt = nowFloat()
		err = s.saveTaskStateLocked(started.ID)
	}
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	records, err = s.readTaskRecords(started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(records.Shell[started.ID].SealedPending) != 0 {
		t.Fatal("started task retained a replayable input")
	}
	s.mu.Lock()
	pending.Revision = 0
	err = s.refreshTaskRecordsLocked(pending.ID)
	s.mu.Unlock()
	if err != nil || pending.Arguments["cmd"] != "private-command-sentinel" || pending.quorumPendingUnmaterialized {
		t.Fatal("refresh lost live owner input")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	restarted := New(Config{ConfigDir: configDir, QuorumConfigFile: "fixture"})
	defer restarted.Close()
	got := restarted.shellJobs[pending.ID]
	if got == nil || got.Status != "queued" || !got.quorumPendingUnmaterialized || !bytes.Equal(got.quorumPendingSealed, firstCipher) {
		t.Fatal("never-dispatched input was not safely quarantined")
	}
	restarted.mu.Lock()
	restarted.shellJobs[pending.ID] = &shellJob{ID: pending.ID}
	err = restarted.refreshTaskRecordsLocked(pending.ID)
	got = restarted.shellJobs[pending.ID]
	restarted.mu.Unlock()
	if err != nil || got == nil || !got.quorumPendingUnmaterialized || !bytes.Equal(got.quorumPendingSealed, firstCipher) {
		t.Fatal("replica refresh lost quarantined input")
	}
	if len(restarted.shellQueues["haos"]) != 0 {
		t.Fatal("quarantined input was automatically replayed")
	}
	restarted.mu.Lock()
	err = restarted.recoverOrphanedTaskLocked(pending.ID)
	restarted.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "queued" {
		t.Fatal("quarantined input was incorrectly failed")
	}
	if startedRestored := restarted.shellJobs[started.ID]; startedRestored == nil || startedRestored.Status != "running" {
		t.Fatal("started task was not preserved without replay")
	}
}

func TestQuorumPendingInputFailsClosedWithoutCipher(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir(), QuorumConfigFile: "configured"})
	defer s.Close()
	s.mu.Lock()
	s.shellJobs["task"] = &shellJob{ID: "task", Server: "haos", Status: "queued", CreatedAt: nowFloat(), Arguments: map[string]any{"cmd": "must-not-write"}}
	err := s.saveTaskStateLocked("task")
	s.mu.Unlock()
	if err == nil {
		t.Fatal("queued task was accepted without the quorum cipher")
	}
	records, readErr := s.readTaskRecords("task")
	if readErr != nil {
		t.Fatal(readErr)
	}
	if _, ok := records.Shell["task"]; ok {
		t.Fatal("unencrypted queued task was persisted")
	}
}
