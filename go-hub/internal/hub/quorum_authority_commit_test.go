package hub

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
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

func authorityTestHead(t *testing.T, s *Server) (sequence uint64, digest []byte, ready bool) {
	t.Helper()
	if err := s.withTaskReadDatabase(func(tx *sql.Tx) error {
		return tx.QueryRow("SELECT sequence,digest,baseline_ready FROM task_quorum_chain JOIN task_quorum_meta USING(id)").Scan(&sequence, &digest, &ready)
	}); err != nil {
		t.Fatal(err)
	}
	return
}

func authorityTestMarker(ctx context.Context, s *Server, revision, sequence uint64, digest []byte) error {
	payload, err := json.Marshal(map[string]any{
		"cluster_id": s.quorumClusterID, "authority_domains": quorumRequiredAuthorityDomains,
		"chain_sequence": sequence, "chain_digest": hex.EncodeToString(digest),
	})
	if err != nil {
		return err
	}
	return s.quorumNode.ApplyRecord(ctx, "cluster_meta", "task_baseline", quorum.Record{Revision: revision, Value: payload})
}

func TestQuorumBaselineMarkerVerifiesAppliedCheckpoint(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GPTADMIN_HOME", filepath.Join(root, "home"))
	s := reviewWriter(t, filepath.Join(root, "hub"))
	defer s.Close()
	node := reviewOpenQuorum(t, s, reviewQuorumConfig(t, root))
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	if _, err := node.WaitForLeader(ctx); err != nil {
		t.Fatal(err)
	}
	if err := node.ApplyBatch(ctx, []quorum.Mutation{reviewTask(t, "historical-receipt")}); err != nil {
		t.Fatal(err)
	}
	olderSequence, olderDigest, _ := authorityTestHead(t, s)
	if err := s.seedQuorumAuthorityBaseline(ctx); err != nil {
		t.Fatal(err)
	}
	sequence, digest, ready := authorityTestHead(t, s)
	if ready || sequence < 2 {
		t.Fatal("expected committed authority rows before the baseline marker")
	}
	other := bytes.Clone(digest)
	other[0] ^= 1
	for i, tc := range []struct {
		name   string
		index  uint64
		digest []byte
		accept bool
	}{
		{"ahead", sequence + 1, digest, false},
		{"wrong digest at head", sequence, other, false},
		{"matching head", sequence, digest, true},
		{"historical checkpoint", olderSequence, olderDigest, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := authorityTestMarker(ctx, s, uint64(i+1), tc.index, tc.digest)
			if (err == nil) != tc.accept {
				t.Errorf("marker accepted=%t, want %t (err=%v)", err == nil, tc.accept, err)
			}
			index, head, baseline := authorityTestHead(t, s)
			if index != sequence || !bytes.Equal(head, digest) || baseline != tc.accept {
				t.Errorf("marker changed the applied checkpoint or readiness: sequence=%d ready=%t", index, baseline)
			}
		})
	}
}

func authorityTestWait(t *testing.T, condition string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", condition)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func authorityTestVoters(t *testing.T, ctx context.Context) []*Server {
	t.Helper()
	root := t.TempDir()
	t.Setenv("GPTADMIN_HOME", filepath.Join(root, "home"))
	cfg := reviewQuorumConfig(t, root)
	var listeners []net.Listener
	for i := range cfg.Peers {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		cfg.Peers[i].Address = listener.Addr().String()
		cfg.Peers[i].ServerName = "node0.test" // TLS name on the temporary test certificate
		cfg.Peers[i].FailureDomain = cfg.Peers[i].ID
		listeners = append(listeners, listener)
		t.Cleanup(func() { listener.Close() })
	}
	var voters []*Server
	for i, peer := range cfg.Peers {
		dir := filepath.Join(root, peer.ID)
		s := New(Config{ConfigDir: dir, AuthMode: "writer", QuorumConfigFile: "test",
			NodePublicURL: "https://" + peer.ID + ".test", PublicOrigin: "https://quorum.test",
			StartupInstructionsFile: filepath.Join(dir, "instructions.md"),
		})
		t.Cleanup(func() { s.Close() })
		nodeKey := strings.Repeat(string(rune('a'+i)), 32)
		if err := s.RegisterLocalExecutor(peer.ID, map[string]string{
			"server_id": peer.ID, "public_key": peer.ID, "fingerprint": peer.ID,
		}, nodeKey); err != nil {
			t.Fatal(err)
		}
		if err := s.InitializeAuthContinuity(); err != nil {
			t.Fatal(err)
		}
		s.ensureWriterSelfEntry()
		local := cfg
		local.Peers = append([]quorum.Peer(nil), cfg.Peers...)
		local.NodeID, local.NodeKey, local.Bootstrap = peer.ID, nodeKey, i == 0
		local.BindAddress, local.Advertise, local.DataDir = peer.Address, peer.Address, filepath.Join(dir, "raft")
		// Reserve the other addresses until their own Raft transports bind.
		if err := listeners[i].Close(); err != nil {
			t.Fatal(err)
		}
		reviewOpenQuorum(t, s, local)
		voters = append(voters, s)
	}
	writer := voters[0]
	if _, err := writer.quorumNode.WaitForLeader(ctx); err != nil {
		t.Fatal(err)
	}
	for _, peer := range cfg.Peers[1:] {
		if err := writer.quorumNode.PromoteVoter(ctx, peer.ID); err != nil {
			t.Fatal(err)
		}
		writer.membership.Nodes[peer.ID] = &nodeMember{NodeID: peer.ID, Name: "shell:" + peer.ID, State: nodeStateReady}
	}
	if err := writer.seedQuorumAuthorityBaseline(ctx); err != nil {
		t.Fatal(err)
	}
	sequence, digest, _ := authorityTestHead(t, writer)
	if err := authorityTestMarker(ctx, writer, 1, sequence, digest); err != nil {
		t.Fatal(err)
	}
	return voters
}

func authorityTestPutInstruction(s *Server, id, content, match string) int {
	body, _ := json.Marshal(map[string]string{"content": content})
	req := httptest.NewRequest(http.MethodPut, "/admin/api/instruction-sets/"+id, bytes.NewReader(body))
	req.Header.Set("If-Match", match)
	response := httptest.NewRecorder()
	if id == defaultInstructionSetID {
		s.adminDefaultInstructionSet(response, req)
	} else {
		s.adminInstructionSet(response, req)
	}
	return response.Code
}

func TestQuorumConcurrentWritesDuringPromotion(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	voters := authorityTestVoters(t, ctx)
	old, next, peer := voters[0], voters[1], voters[2]
	if err := old.ensureQuorumAuthorityMaterialized(ctx); err != nil {
		t.Fatal(err)
	}
	const initial = "committed instructions"
	const updated = "instructions committed during leadership change"
	if code := authorityTestPutInstruction(old, defaultInstructionSetID, "canonical default", instructionSetETag(old.instructionSetSnapshot().Version)); code != http.StatusOK {
		t.Fatalf("default instruction update: HTTP %d", code)
	}
	if code := authorityTestPutInstruction(old, "ops", initial, "*"); code != http.StatusOK {
		t.Fatalf("named instruction update: HTTP %d", code)
	}

	// New writes advance the two baseline rows. An upgraded node must also
	// preserve edits previously committed under the historical keys.
	var canonical int
	if err := old.withTaskReadDatabase(func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT count(*) FROM task_quorum_authority WHERE namespace IN ('authority/instruction_sets_default','authority/instruction_sets_named') AND row_key='current' AND revision=2`).Scan(&canonical)
	}); err != nil || canonical != 2 {
		t.Errorf("updated canonical instruction rows=%d, err=%v", canonical, err)
	}
	legacyDefault := InstructionSet{ID: defaultInstructionSetID, Content: initial, Version: instructionSetVersion(initial)}
	if err := old.commitQuorumAuthorityState("instruction_sets", "default", legacyDefault); err != nil {
		t.Fatal(err)
	}
	legacy := InstructionSet{ID: "ops", Content: "existing named instructions", Version: instructionSetVersion("existing named instructions")}
	if err := old.commitQuorumAuthorityState("instruction_sets", "named", instructionSetsState{Sets: map[string]InstructionSet{"ops": legacy}}); err != nil {
		t.Fatal(err)
	}
	authorityTestTransfer(t, ctx, old, next)
	// Let the real promotion project both instruction files, then pause it
	// at the secret store's domain lock, after its SQLite snapshot was read.
	next.secretStore.mu.Lock()
	promoted := make(chan error, 1)
	go func() { promoted <- next.ensureQuorumAuthorityMaterialized(ctx) }()
	resume := func() error {
		next.secretStore.mu.Unlock()
		return <-promoted
	}
	defer func() {
		if resume != nil {
			_ = resume()
		}
	}()
	authorityTestWait(t, "instruction file from the committed image", func() bool {
		b, _ := os.ReadFile(next.cfg.StartupInstructionsFile)
		return string(b) == initial
	})
	if set, ok := next.namedInstructionSetSnapshot("ops"); !ok || set.Version != legacy.Version {
		t.Fatal("promotion lost the committed named instructions")
	}

	// A task admitted on the leader still needs a majority during projection.
	before := next.quorumNode.ExternalAppliedIndex()
	if _, err := next.reserveDurableTaskRequest("during-promotion", "receipt"); err != nil {
		t.Fatal(err)
	}
	if next.quorumNode.ExternalAppliedIndex() != before+1 {
		t.Fatal("acknowledged task reservation did not advance the Raft sequence")
	}
	requestCount := func(s *Server, key string) int {
		t.Helper()
		var count int
		if err := s.withTaskReadDatabase(func(tx *sql.Tx) error {
			return tx.QueryRow("SELECT count(*) FROM task_requests WHERE key=?", key).Scan(&count)
		}); err != nil {
			t.Fatal(err)
		}
		return count
	}
	authorityTestWait(t, "task receipt on another voter", func() bool { return requestCount(peer, "during-promotion") == 1 })
	if code := authorityTestPutInstruction(next, defaultInstructionSetID, "unpublished", instructionSetETag(instructionSetVersion(initial))); code != http.StatusServiceUnavailable {
		t.Fatalf("concurrent authority edit during promotion: HTTP %d", code)
	}
	if next.instructionSetSnapshot().Content != initial {
		t.Fatal("rejected authority edit was published locally")
	}

	authorityTestTransfer(t, ctx, next, old)
	if err := old.ensureQuorumAuthorityMaterialized(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := next.reserveDurableTaskRequest("former-leader", "receipt"); err == nil || requestCount(next, "former-leader") != 0 {
		t.Fatal("former leader acknowledged or retained a new task")
	}
	if code := authorityTestPutInstruction(old, defaultInstructionSetID, updated, instructionSetETag(instructionSetVersion(initial))); code != http.StatusOK {
		t.Fatalf("new leader instruction update: HTTP %d", code)
	}
	authorityTestWait(t, "new authority revision in the promoting node's SQLite", func() bool {
		var set InstructionSet
		return next.readQuorumAuthorityState("instruction_sets_default", "current", &set) == nil && set.Content == updated
	})
	authorityTestTransfer(t, ctx, old, next)
	err := resume()
	resume = nil
	if err == nil || next.quorumAuthorityActiveWriter.Load() {
		t.Fatalf("promotion did not fence the superseded authority revision: %v", err)
	}
	var committedAuth authSnapshot
	if err := next.readQuorumAuthorityState("auth", "bundle", &committedAuth); err != nil || committedAuth.WriterID != old.authLocalID {
		t.Fatal("superseded promotion changed the authoritative auth writer")
	}
	if err := next.ensureQuorumAuthorityMaterialized(ctx); err != nil {
		t.Fatal(err)
	}
	if next.instructionSetSnapshot().Content != updated || next.effectiveAuthMode() != "writer" {
		t.Fatal("retry did not install the current image and writer identity")
	}
	if code := authorityTestPutInstruction(next, "ops", updated, instructionSetETag(legacy.Version)); code != http.StatusOK {
		t.Fatalf("upgraded named instruction update: HTTP %d", code)
	}
	authorityTestWait(t, "updated instructions and promoted identities on another voter", func() bool {
		var named instructionSetsState
		var bundle authSnapshot
		var membership nodeMembershipState
		return peer.readQuorumAuthorityState("instruction_sets_named", "current", &named) == nil && named.Sets["ops"].Content == updated &&
			peer.readQuorumAuthorityState("auth", "bundle", &bundle) == nil && bundle.WriterID == next.authLocalID && bundle.Generation == next.authState.Generation &&
			peer.readQuorumAuthorityState("membership", "writer", &membership) == nil && membership.WriterID == next.authLocalID
	})

	// Exercise the normal auth store before and after losing the majority.
	saveAuth := func(id string) error {
		next.authMu.RLock()
		defer next.authMu.RUnlock()
		next.mu.Lock()
		defer next.mu.Unlock()
		next.managedMCP[id] = managedMCPToken{ID: id, Status: "revoked"}
		return next.saveManagedMCPStateLocked()
	}
	if err := saveAuth("retired"); err != nil {
		t.Fatal(err)
	}
	authorityTestWait(t, "replicated auth mutation", func() bool {
		var bundle authSnapshot
		return peer.readQuorumAuthorityState("auth", "bundle", &bundle) == nil && bundle.Tokens.Tokens["retired"].Status == "revoked"
	})
	for _, voter := range []*Server{old, peer} {
		if err := voter.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := next.reserveDurableTaskRequest("minority", "receipt"); err == nil || requestCount(next, "minority") != 0 {
		t.Fatal("isolated voter acknowledged or retained a task")
	}
	if err := saveAuth("minority"); err == nil {
		t.Fatal("isolated voter accepted an auth mutation")
	}
	var bundle authSnapshot
	if err := next.readQuorumAuthorityState("auth", "bundle", &bundle); err != nil || bundle.Tokens.Tokens["minority"].ID != "" {
		t.Fatal("failed auth mutation changed the replicated bundle")
	}
}

func authorityTestTransfer(t *testing.T, ctx context.Context, from, to *Server) {
	t.Helper()
	if err := from.quorumNode.TransferLeadershipTo(to.authLocalID); err != nil {
		t.Fatal(err)
	}
	authorityTestWait(t, "leadership transfer", func() bool {
		return to.quorumNode.Status().Leader && to.quorumNode.VerifyLeader(ctx) == nil
	})
}

func TestQuorumLocalTransactionDuringFollowerApply(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	voters := authorityTestVoters(t, ctx)
	old, writer := voters[0], voters[1]
	authorityTestTransfer(t, ctx, old, writer)
	if _, _, err := writer.quorumNode.ReadRecord(ctx, "cluster_meta", "task_baseline"); err != nil {
		t.Fatal(err)
	}
	authorityTestWait(t, "replica at the current checkpoint", func() bool {
		return old.quorumNode.ExternalAppliedIndex() == writer.quorumNode.ExternalAppliedIndex()
	})

	// Hold a local SQLite writer while Raft delivers another committed
	// transaction. The FSM takes taskQuorumDBMu for reading before it
	// waits on SQLite. No other caller on this voter owns that lock.
	db, err := openTaskDatabase(old.taskDatabasePath())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	local := &taskTransaction{tx: tx, quorumEnabled: true}
	if _, err := tx.Exec("INSERT INTO task_controls(task_id,server) VALUES('local-control','test')"); err != nil {
		t.Fatal(err)
	}
	if err := local.captureTaskControl("local-control"); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.reserveDurableTaskRequest("replicated", "receipt"); err != nil {
		t.Fatal(err)
	}
	authorityTestWait(t, "replica FSM waiting on SQLite", func() bool {
		if !old.taskQuorumDBMu.TryLock() {
			return true
		}
		old.taskQuorumDBMu.Unlock()
		return false
	})
	if err := old.commitTaskQuorumChanges(local); !errors.Is(err, quorum.ErrNotLeader) {
		t.Fatalf("local proposal while applying a committed entry: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	authorityTestWait(t, "exactly the committed receipt on the former leader", func() bool {
		var local, replicated int
		err := old.withTaskReadDatabase(func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT (SELECT count(*) FROM task_controls WHERE task_id='local-control'), (SELECT count(*) FROM task_requests WHERE key='replicated')`).Scan(&local, &replicated)
		})
		return err == nil && local == 0 && replicated == 1
	})
}
