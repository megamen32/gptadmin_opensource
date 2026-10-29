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
	"encoding/hex"
	"encoding/json"
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

func TestQuorumPendingInputReplicatesEncryptedAndSurvivesLeaderLoss(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fixture-ca"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, template, template, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(root, "ca.pem")
	statePath := filepath.Join(root, "state.key")
	if err = os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(statePath, bytes.Repeat([]byte{73}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	peers := make([]quorum.Peer, 3)
	listeners := make([]net.Listener, 3)
	keys := make([]string, 3)
	certs := make([]string, 3)
	privs := make([]string, 3)
	for i := range peers {
		listeners[i], err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		name := fmt.Sprintf("node%d.test", i)
		keys[i] = strings.Repeat(string(rune(97+i)), 32)
		sum := sha256.Sum256([]byte(keys[i]))
		peers[i] = quorum.Peer{ID: fmt.Sprintf("n%d", i), Address: listeners[i].Addr().String(), ServerName: name, NodeKeyHash: hex.EncodeToString(sum[:]), FailureDomain: name}
		key, keyErr := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if keyErr != nil {
			t.Fatal(keyErr)
		}
		cert := &x509.Certificate{SerialNumber: big.NewInt(int64(i + 2)), DNSNames: []string{name}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
		der, certErr := x509.CreateCertificate(rand.Reader, cert, ca, &key.PublicKey, caKey)
		if certErr != nil {
			t.Fatal(certErr)
		}
		pk, marshalErr := x509.MarshalPKCS8PrivateKey(key)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		certs[i] = filepath.Join(root, name+".crt")
		privs[i] = filepath.Join(root, name+".key")
		if err = os.WriteFile(certs[i], pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(privs[i], pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pk}), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, l := range listeners {
		l.Close()
	}
	servers := make([]*Server, 3)
	nodes := make([]*quorum.Node, 3)
	for i := range servers {
		servers[i] = New(Config{ConfigDir: filepath.Join(root, peers[i].ID), QuorumConfigFile: "fixture"})
		defer servers[i].Close()
		servers[i].quorumClusterID = "fixture-cluster"
		nodes[i], err = quorum.OpenWithExternalState(quorum.Config{ClusterID: "fixture-cluster", NodeID: peers[i].ID, BootstrapperID: "n0", NodeKey: keys[i], BindAddress: peers[i].Address, Advertise: peers[i].Address, DataDir: filepath.Join(root, "raft-"+peers[i].ID), TLSCertFile: certs[i], TLSKeyFile: privs[i], TLSCAFile: caPath, StateKeyFile: statePath, Peers: peers, Bootstrap: i == 0, RPCDeadline: 2 * time.Second, ApplyDeadline: 3 * time.Second, BarrierDeadline: 3 * time.Second, ExternalSnapshotMaxBytes: 64 << 20, ExternalSnapshotFreeReserveBytes: 1 << 20}, servers[i].applyTaskQuorumMutations, quorum.ExternalSnapshotCallbacks{Snapshot: servers[i].snapshotTaskQuorumState, Restore: servers[i].restoreTaskQuorumState})
		if err != nil {
			t.Fatal(err)
		}
		servers[i].quorumNode = nodes[i]
	}
	deadline := time.Now().Add(15 * time.Second)
	for !nodes[0].Status().Leader && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if !nodes[0].Status().Leader {
		t.Fatal("initial leader not elected")
	}
	for i := 1; i < 3; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := nodes[0].PromoteVoter(ctx, peers[i].ID)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
	}
	for nodes[0].Status().VoterCount != 3 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if nodes[0].Status().VoterCount != 3 {
		t.Fatal("three voters not committed")
	}
	leader := servers[0]
	// Test fixture only: the real bootstrap commits these readiness flags.
	err = leader.withTaskDatabase(func(tx *taskTransaction) error {
		_, e := tx.tx.Exec(`INSERT INTO task_quorum_meta(id,cluster_id,applied_index,baseline_ready,authority_ready) VALUES(1,?,0,1,1) ON CONFLICT(id) DO UPDATE SET cluster_id=excluded.cluster_id,baseline_ready=1,authority_ready=1`, "fixture-cluster")
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	leader.quorumPrivateTLS = &quorumPrivateTLS{peers: map[string]quorum.Peer{"n0": peers[0], "n1": peers[1], "n2": peers[2]}}
	leader.quorumAuthorityActiveWriter.Store(true)
	leader.membership = &nodeMembershipState{Nodes: map[string]*nodeMember{"n1": {NodeID: "n1", Name: "shell:haos", State: nodeStateReady, Capabilities: []string{"executor"}}}}
	leader.nodePeers = map[string]nodePeerRoute{"shell:haos": {URL: "http://127.0.0.1:1"}}
	leader.agents["shell:haos"] = &Agent{AgentID: "shell:haos", Status: "online", Meta: map[string]any{"approved": true}}
	peerReq := httptest.NewRequest(http.MethodPost, "/mcp-relay/call", nil)
	peerReq.Header.Set("Authorization", "Bearer fixture")
	peerRec := httptest.NewRecorder()
	if ok, routeErr := leader.quorumExecutorUsesLeaderQueue(context.Background(), "shell:haos"); routeErr != nil || !ok {
		t.Fatalf("quorum queue route unavailable: %v", routeErr)
	}
	if leader.forwardNodePeerCall(peerRec, peerReq, "shell:haos", []byte(`{"target":"shell:haos","tool":"shell_exec"}`)) {
		t.Fatalf("quorum peer did not use the leader queue: HTTP %d", peerRec.Code)
	}
	noBearer := httptest.NewRequest(http.MethodPost, "/mcp-relay/call", nil)
	denied := httptest.NewRecorder()
	if !leader.forwardNodePeerCall(denied, noBearer, "shell:haos", nil) || denied.Code != http.StatusUnauthorized {
		t.Fatalf("quorum queue accepted request without bearer: %d", denied.Code)
	}
	loopReq := httptest.NewRequest(http.MethodPost, "/mcp-relay/call", nil)
	loopReq.Header.Set("Authorization", "Bearer fixture")
	loopReq.Header.Set(nodePeerHopHeader, "1")
	loopRec := httptest.NewRecorder()
	if !leader.forwardNodePeerCall(loopRec, loopReq, "shell:haos", nil) || loopRec.Code != http.StatusLoopDetected {
		t.Fatal("forwarded request bypassed owner hop guard")
	}
	leader.membership.Nodes["n2"] = &nodeMember{NodeID: "n2", Name: "shell:haos", State: nodeStateReady, Capabilities: []string{"executor"}}
	if _, duplicateErr := leader.quorumExecutorUsesLeaderQueue(context.Background(), "shell:haos"); duplicateErr == nil {
		t.Fatal("duplicate executor ownership accepted")
	}
	delete(leader.membership.Nodes, "n2")
	leader.membership.Nodes["n1"].State = nodeStateDisabled
	if use, disabledErr := leader.quorumExecutorUsesLeaderQueue(context.Background(), "shell:haos"); disabledErr == nil || use {
		t.Fatal("disabled executor did not fail closed")
	}
	leader.membership.Nodes["n1"].State = nodeStateDraining
	if use, drainingErr := leader.quorumExecutorUsesLeaderQueue(context.Background(), "shell:haos"); drainingErr == nil || use {
		t.Fatal("draining executor accepted new work")
	}
	leader.membership.Nodes["n1"].State = nodeStateReady
	childID := "mcp:shell:haos:grep"
	leader.agents[childID] = &Agent{AgentID: childID, Kind: "child_mcp", Meta: map[string]any{"approved": true, "parent_server_id": "shell:haos", "child_ref": "grep", "enabled": true}}
	childReq := httptest.NewRequest(http.MethodPost, "/mcp-relay/call", nil)
	childReq.Header.Set("Authorization", "Bearer fixture")
	childRec := httptest.NewRecorder()
	if leader.forwardNodePeerCall(childRec, childReq, childID, nil) {
		t.Fatalf("child did not use its parent queue: %d", childRec.Code)
	}
	response := leader.callShellTool("shell:haos", "shell_exec", map[string]any{"cmd": "replicated-secret-sentinel"}, true, time.Second)
	id := firstString(response, "job_id")
	if id == "" {
		t.Fatalf("leader did not accept queued task: %v", response)
	}
	leader.mu.Lock()
	job := leader.shellJobs[id]
	leader.mu.Unlock()
	if job == nil {
		t.Fatal("accepted task missing")
	}
	secondResponse := leader.callShellTool("shell:haos", "shell_exec", map[string]any{"cmd": "claimed-not-sent-sentinel"}, true, time.Second)
	secondID := firstString(secondResponse, "job_id")
	if secondID == "" || secondID == job.ID {
		t.Fatal("second task was not independently accepted")
	}
	var copied persistedShellTask
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		state, readErr := servers[1].readTaskRecords(job.ID)
		if readErr == nil {
			copied = state.Shell[job.ID]
			if len(copied.SealedPending) > 0 {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(copied.SealedPending) == 0 {
		t.Fatal("encrypted pending input was not replicated")
	}
	deadline = time.Now().Add(10 * time.Second)
	var secondCopied persistedShellTask
	for time.Now().Before(deadline) {
		state, readErr := servers[1].readTaskRecords(secondID)
		if readErr == nil {
			secondCopied = state.Shell[secondID]
			if len(secondCopied.SealedPending) > 0 {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(secondCopied.SealedPending) == 0 {
		t.Fatal("second encrypted task was not replicated")
	}
	dbBytes, err := os.ReadFile(servers[1].taskDatabasePath())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(dbBytes, []byte("replicated-secret-sentinel")) {
		t.Fatal("plaintext appeared in follower database")
	}
	for i := range servers {
		for _, path := range []string{servers[i].taskDatabasePath(), servers[i].taskDatabasePath() + "-wal", filepath.Join(root, "raft-"+peers[i].ID, "raft.db")} {
			data, readErr := os.ReadFile(path)
			if readErr != nil && !os.IsNotExist(readErr) {
				t.Fatal(readErr)
			}
			if bytes.Contains(data, []byte("replicated-secret-sentinel")) {
				t.Fatal("plaintext appeared in a replicated database or Raft log")
			}
		}
	}
	plain, err := nodes[1].OpenExternalValue(quorumPendingShellNamespace, job.ID, 1, copied.SealedPending)
	if err != nil {
		t.Fatal(err)
	}
	var input quorumPendingShellInput
	if json.Unmarshal(plain, &input) != nil || input.Arguments["cmd"] != "replicated-secret-sentinel" {
		t.Fatal("follower could not authenticate replicated input")
	}
	if err = servers[0].Close(); err != nil {
		t.Fatal(err)
	}
	next := -1
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		for _, i := range []int{1, 2} {
			if nodes[i].Status().Leader {
				next = i
				break
			}
		}
		if next >= 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if next < 0 {
		t.Fatal("no leader after voter loss")
	}
	leader = servers[next]
	leader.mu.Lock()
	err = leader.refreshTaskRecordsLocked(job.ID)
	restored := leader.shellJobs[job.ID]
	queued := len(leader.shellQueues["haos"])
	leader.mu.Unlock()
	if err != nil || restored == nil || restored.Status != "queued" || !restored.quorumPendingUnmaterialized || queued != 0 {
		t.Fatal("new leader did not quarantine replicated undispatched input without replay")
	}
	plain, err = nodes[next].OpenExternalValue(quorumPendingShellNamespace, job.ID, 1, restored.quorumPendingSealed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(plain, []byte("replicated-secret-sentinel")) {
		t.Fatal("new leader lost authenticated input")
	}
	leader.mu.Lock()
	unreadyErr := leader.recoverQuorumPendingShellLocked(context.Background(), "haos")
	queuedWhileUnready := len(leader.shellQueues["haos"])
	leader.mu.Unlock()
	if unreadyErr == nil || queuedWhileUnready != 0 {
		t.Fatal("an unready replica recovered executable work")
	}
	// Fixture establishes the already-seeded baseline flags; deployment must
	// establish them through the real quorum bootstrap, not by editing SQLite.
	err = leader.withTaskDatabase(func(tx *taskTransaction) error {
		_, updateErr := tx.tx.Exec(`UPDATE task_quorum_meta SET baseline_ready=1,authority_ready=1 WHERE id=1`)
		return updateErr
	})
	if err != nil {
		t.Fatal(err)
	}
	leader.mu.Lock()
	err = leader.recoverQuorumPendingShellLocked(context.Background(), "haos")
	leader.agents["shell:haos"] = &Agent{AgentID: "shell:haos", Status: "online", Meta: map[string]any{"approved": true}}
	leader.mu.Unlock()
	if err != nil {
		t.Fatalf("recover pending task: %v", err)
	}
	first := httptest.NewRecorder()
	leader.pollShellQueue(first, httptest.NewRequest("GET", "/queue/haos?timeout=1", nil), "haos")
	var delivered map[string]any
	if first.Code != 200 || json.Unmarshal(first.Body.Bytes(), &delivered) != nil || firstString(delivered, "id") != job.ID || mapValue(delivered["arguments"])["cmd"] != "replicated-secret-sentinel" {
		t.Fatalf("recovered input was not delivered once: status=%d", first.Code)
	}
	claimed, claimErr := leader.readTaskRecords(secondID)
	if claimErr != nil || claimed.Shell[secondID].Status != "queued" || len(claimed.Shell[secondID].SealedPending) == 0 {
		t.Fatal("second task was not retained after leader claim")
	}
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		state, readErr := servers[3-next].readTaskRecords(job.ID)
		if readErr == nil && state.Shell[job.ID].Status == "running" && len(state.Shell[job.ID].SealedPending) == 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	state, readErr := servers[3-next].readTaskRecords(job.ID)
	if readErr != nil || state.Shell[job.ID].Status != "running" || len(state.Shell[job.ID].SealedPending) != 0 {
		t.Fatal("durable dispatch and removal of replayable input did not replicate")
	}
	// Bring the first voter back so two voters can elect a new leader after
	// the dispatching leader is lost. This exercises a second real election.
	restarted := New(Config{ConfigDir: filepath.Join(root, peers[0].ID), QuorumConfigFile: "fixture"})
	defer restarted.Close()
	restarted.quorumClusterID = "fixture-cluster"
	restartedNode, openErr := quorum.OpenWithExternalState(quorum.Config{ClusterID: "fixture-cluster", NodeID: peers[0].ID, BootstrapperID: "n0", NodeKey: keys[0], BindAddress: peers[0].Address, Advertise: peers[0].Address, DataDir: filepath.Join(root, "raft-"+peers[0].ID), TLSCertFile: certs[0], TLSKeyFile: privs[0], TLSCAFile: caPath, StateKeyFile: statePath, Peers: peers, Bootstrap: true, RPCDeadline: 2 * time.Second, ApplyDeadline: 3 * time.Second, BarrierDeadline: 3 * time.Second, ExternalSnapshotMaxBytes: 64 << 20, ExternalSnapshotFreeReserveBytes: 1 << 20}, restarted.applyTaskQuorumMutations, quorum.ExternalSnapshotCallbacks{Snapshot: restarted.snapshotTaskQuorumState, Restore: restarted.restoreTaskQuorumState})
	if openErr != nil {
		t.Fatal(openErr)
	}
	restarted.quorumNode = restartedNode
	servers[0], nodes[0] = restarted, restartedNode
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		state, readErr = restarted.readTaskRecords(job.ID)
		if readErr == nil && state.Shell[job.ID].Status == "running" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if readErr != nil || state.Shell[job.ID].Status != "running" {
		_, applyErr := restartedNode.ExternalApplyError()
		t.Fatalf("returning voter did not receive durable dispatch: status=%q read=%v node=%+v external=%d apply=%v", state.Shell[job.ID].Status, readErr, restartedNode.Status(), restartedNode.ExternalAppliedIndex(), applyErr)
	}
	if err := servers[next].Close(); err != nil {
		t.Fatal(err)
	}
	lastLeader := -1
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		for _, i := range []int{0, 3 - next} {
			if nodes[i].Status().Leader {
				lastLeader = i
				break
			}
		}
		if lastLeader >= 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if lastLeader < 0 {
		t.Fatal("no leader after the second voter loss")
	}
	last := servers[lastLeader]
	// As above, seed only the fixture readiness flags; real bootstrap owns them.
	err = last.withTaskDatabase(func(tx *taskTransaction) error {
		_, e := tx.tx.Exec(`UPDATE task_quorum_meta SET baseline_ready=1,authority_ready=1 WHERE id=1`)
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	last.mu.Lock()
	last.agents["shell:haos"] = &Agent{AgentID: "shell:haos", Status: "online", Meta: map[string]any{"approved": true}}
	last.mu.Unlock()
	lastPoll := httptest.NewRecorder()
	last.pollShellQueue(lastPoll, httptest.NewRequest("GET", "/queue/haos?timeout=1", nil), "haos")
	var afterLoss map[string]any
	if lastPoll.Code != 200 || json.Unmarshal(lastPoll.Body.Bytes(), &afterLoss) != nil || firstString(afterLoss, "id") != secondID || mapValue(afterLoss["arguments"])["cmd"] != "claimed-not-sent-sentinel" {
		t.Fatalf("claimed but undispatched task was not recovered after leader loss: status=%d", lastPoll.Code)
	}
	lastAgain := httptest.NewRecorder()
	last.pollShellQueue(lastAgain, httptest.NewRequest("GET", "/queue/haos?timeout=1", nil), "haos")
	var again map[string]any
	if lastAgain.Code != 200 || json.Unmarshal(lastAgain.Body.Bytes(), &again) != nil || firstString(again, "id") != "" {
		t.Fatal("a started task was replayed after leader loss")
	}
	lastState, err := last.readTaskRecords(job.ID)
	if err != nil || lastState.Shell[job.ID].Status != "running" {
		t.Fatal("started task receipt was not preserved after second election")
	}
	last.mu.Lock()
	minorityOwner, err := last.ensureTaskOwnerLocked()
	if err != nil {
		last.mu.Unlock()
		t.Fatal(err)
	}
	minorityJob := &shellJob{ID: "minority-pending", OwnerID: minorityOwner, Server: "haos", ToolName: "shell_exec", Status: "queued", CreatedAt: nowFloat(), Arguments: map[string]any{"cmd": "do-not-dispatch-without-majority"}}
	last.shellJobs[minorityJob.ID] = minorityJob
	err = last.saveTaskStateLocked(minorityJob.ID)
	if err == nil {
		last.shellQueues["haos"] = append(last.shellQueues["haos"], minorityJob.ID)
	}
	last.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	otherVoter := 0
	if lastLeader == 0 {
		otherVoter = 3 - next
	}
	if err := servers[otherVoter].Close(); err != nil {
		t.Fatal(err)
	}
	minorityCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	minorityReq := httptest.NewRequest("GET", "/queue/haos?timeout=1", nil).WithContext(minorityCtx)
	minorityPoll := httptest.NewRecorder()
	last.pollShellQueue(minorityPoll, minorityReq, "haos")
	if minorityPoll.Code != 503 {
		t.Fatalf("minority dispatched or accepted work: status=%d", minorityPoll.Code)
	}
	minorityState, err := last.readTaskRecords(minorityJob.ID)
	if err != nil || minorityState.Shell[minorityJob.ID].Status != "queued" || len(minorityState.Shell[minorityJob.ID].SealedPending) == 0 {
		t.Fatal("minority changed the queued task")
	}
}
