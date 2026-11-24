package hub

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/megamen32/gptadmin/go-hub/internal/hub/quorum"
)

func TestOwnerLocalControlBootstrap(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GPTADMIN_HOME", filepath.Join(root, "home"))
	cfg := reviewQuorumConfig(t, root)
	listeners := make([]net.Listener, 3)
	for i := range cfg.Peers {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		listeners[i] = l
		t.Cleanup(func() { l.Close() })
		p := &cfg.Peers[i]
		p.Address = l.Addr().String()
		p.HubURL = "https://" + p.Address
		p.ServerName = "node0.test"
		p.FailureDomain = p.ID
	}
	servers := make([]*Server, 3)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for i, p := range cfg.Peers {
		dir := filepath.Join(root, p.ID)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		local := cfg
		local.NodeID, local.Bootstrap, local.NodeKey = p.ID, i == 0, strings.Repeat(string(rune('a'+i)), 32)
		local.BindAddress, local.Advertise, local.DataDir = p.Address, p.Address, filepath.Join(dir, "raft")
		raw, err := json.Marshal(local)
		if err != nil {
			t.Fatal(err)
		}
		configFile := filepath.Join(dir, "quorum.json")
		if err = os.WriteFile(configFile, raw, 0600); err != nil {
			t.Fatal(err)
		}
		s := New(Config{ConfigDir: dir, QuorumConfigFile: configFile, AuthMode: "writer", NodePublicURL: p.HubURL, PublicOrigin: p.HubURL, StartupInstructionsFile: filepath.Join(dir, "instructions.md")})
		servers[i] = s
		t.Cleanup(func() { s.Close() })
		if err = s.RegisterLocalExecutor(p.ID, map[string]string{"server_id": p.ID, "public_key": p.ID, "fingerprint": p.ID}, local.NodeKey); err != nil {
			t.Fatal(err)
		}
		if err = s.InitializeAuthContinuity(); err != nil {
			t.Fatal(err)
		}
		s.ensureWriterSelfEntry()
		// Deliberately unusable history and a large completed response. Any
		// legacy task seed or content seed fails on these independent fixtures.
		db, err := openTaskDatabase(s.taskDatabasePath())
		if err != nil {
			t.Fatal(err)
		}
		_, err = db.Exec(`CREATE TABLE task_requests(key TEXT PRIMARY KEY,fingerprint TEXT,owner TEXT,created REAL,job_id TEXT,response BLOB,status INTEGER);
			CREATE TABLE task_records(kind TEXT,key TEXT,done REAL NOT NULL,PRIMARY KEY(kind,key)) WITHOUT ROWID;
			INSERT INTO task_requests VALUES('closed','fp','process',1,'closed-job',zeroblob(4194304),200)`)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec("INSERT INTO task_requests VALUES(?,?,?,?,?,NULL,0)", p.ID, "legacy-fingerprint", "local-process", 1.5, "local-job"); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			now := float64(time.Now().Unix())
			for _, row := range []struct {
				key, job      string
				created, done float64
				status        int
			}{
				{"cached-active", "still-live", 1, 0, 202}, {"cached-terminal", "done-recent", 1, now - 5, 200},
				{"expired", "done-long-ago", 1, 1, 200}, {"missing", "lost-handle", now, -1, 202}, {"cached-sync", "", now, -1, 200},
			} {
				if _, err = db.Exec("INSERT INTO task_requests VALUES(?,?,?, ?,?,zeroblob(2048),?)", row.key, "legacy-fingerprint", "local-process", row.created, row.job, row.status); err != nil {
					t.Fatal(err)
				}
				if row.done >= 0 {
					if _, err = db.Exec("INSERT INTO task_records VALUES('shell',?,?)", row.job, row.done); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err = db.Exec("INSERT INTO task_records VALUES('relay','still-live',?)", now); err != nil {
				t.Fatal(err)
			}
		}
		if err = db.Close(); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, "exec-content.sqlite"), []byte("local content only"), 0600); err != nil {
			t.Fatal(err)
		}
		listeners[i].Close()
		if err = s.configureQuorum(); err != nil {
			t.Fatal(err)
		}
		if !s.quorumControlOnly() {
			t.Fatal("new Hub did not default to owner_local")
		}
	}
	leader := servers[0]
	if _, err := leader.quorumNode.WaitForLeader(ctx); err != nil {
		t.Fatal(err)
	}
	for i, p := range cfg.Peers[1:] {
		member := *servers[i+1].membership.Nodes[p.ID]
		leader.membership.Nodes[p.ID] = &member
	}
	if err := leader.seedOutstandingRequests(ctx); err != nil {
		t.Fatal(err)
	} // interrupted before marker
	if err := leader.seedTaskQuorumBaseline(ctx); err != nil {
		t.Fatal(err)
	}
	for i, s := range servers {
		authorityTestWait(t, "replica baseline", func() bool { return s.taskQuorumReplicaReady() == nil })
		op, found := s.quorumNode.LocalOperation(sha256Hex([]byte("n0")))
		if !found || op.Owner != "n0" || op.JobID != "local-job" || op.Fingerprint != sha256Hex([]byte("legacy-fingerprint")) || op.Witness != sha256Hex([]byte("local-process\x001.5")) || op.Expires != 0 {
			t.Fatal("unresolved owner witness changed")
		}
		for _, k := range []string{"closed", "expired", "n1", "n2"} {
			if _, found = s.quorumNode.LocalOperation(sha256Hex([]byte(k))); found {
				t.Fatal("foreign or completed reservation entered the baseline")
			}
		}
		for key, terminal := range map[string]bool{"cached-active": false, "cached-terminal": true, "missing": false, "cached-sync": true} {
			res, ok := s.quorumNode.LocalOperation(sha256Hex([]byte(key)))
			if !ok || (res.Expires > 0) != terminal {
				t.Fatalf("protected request %s terminal=%v found=%v", key, res.Expires > 0, ok)
			}
		}
		if i != 0 {
			if err := leader.quorumNode.PromoteVoter(ctx, cfg.Peers[i].ID); err != nil {
				t.Fatal(err)
			}
		}
		err := s.withQuorumReadDatabase(func(tx *sql.Tx) error {
			var n int
			if err := tx.QueryRow("SELECT count(*) FROM sqlite_schema WHERE type='table'").Scan(&n); err != nil {
				return err
			}
			if n != 4 {
				return errors.New("control schema contains non-control tables")
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := leader.ensureQuorumAuthorityMaterialized(ctx); err != nil {
		t.Fatal(err)
	}
	if got := leader.memberControlSourceID("physical-writer"); got != "n0" {
		t.Fatalf("fresh owner_local source = %q", got)
	}
	// Emulate an already committed 1153 membership with no stable source.
	legacy := membershipQuorumAuthorityState(leader.membership)
	legacy.ControlSourceID = ""
	legacy.Nodes["n0"].KeyHash = cfg.Peers[0].NodeKeyHash
	if err := leader.commitQuorumAuthorityState("membership", "writer", legacy); err != nil {
		t.Fatal(err)
	}
	leader.cfg.LegacyMemberControlSource = "n0"
	if err := leader.ensureQuorumAuthorityMaterialized(ctx); err != nil {
		t.Fatal(err)
	}
	var migrated nodeMembershipState
	if err := leader.readQuorumAuthorityState("membership", "writer", &migrated); err != nil || migrated.ControlSourceID != "n0" {
		t.Fatalf("legacy pin was not committed: %v", err)
	}
	probe := membershipQuorumAuthorityState(legacy)
	for _, bad := range []string{"", "other-writer"} {
		if err := leader.pinLegacyMemberControlSource(probe, bad); err == nil || probe.ControlSourceID != "" {
			t.Fatal("migrated without corroborating committed auth")
		}
	}
	probe.Nodes["n0"].KeyHash = hashNodeSecret("wrong-key")
	if err := leader.pinLegacyMemberControlSource(probe, "n0"); err == nil || probe.ControlSourceID != "" {
		t.Fatal("migrated without quorum enrollment proof")
	}
	leader.cfg.LegacyMemberControlSource = "n1"
	if err := leader.pinLegacyMemberControlSource(&migrated, "n1"); err == nil || migrated.ControlSourceID != "n0" {
		t.Fatal("operator replaced the committed logical source")
	}
	leader.cfg.LegacyMemberControlSource = ""
	for _, ns := range []string{"task_records/shell", "task_controls", "task_requests", "content_sources", "authority/webhooks", "authority/secret_ingress", "other"} {
		m := quorum.Mutation{Namespace: ns, Key: "current", External: true, Record: quorum.Record{Revision: 1, Value: []byte(`{"stdout":"private-local-only"}`)}}
		if err := leader.quorumNode.ApplyBatch(ctx, []quorum.Mutation{m}); err == nil {
			t.Fatalf("accepted prohibited namespace %s", ns)
		}
	}
	if err := leader.quorumNode.TransferLeadershipTo("n1"); err != nil {
		t.Fatal(err)
	}
	leader = servers[1]
	authorityTestWait(t, "transferred leadership", func() bool { return leader.quorumNode.Status().Leader })
	if err := leader.ensureQuorumAuthorityMaterialized(ctx); err != nil {
		t.Fatal(err)
	}
	if leader.authLocalID != "n1" || leader.membership.WriterID != "n1" || leader.memberControlSourceID(leader.authLocalID) != "n0" {
		t.Fatal("physical election replaced the committed member control source")
	}
	if err := leader.taskQuorumReady(); err != nil {
		t.Fatal(err)
	}
	seq := leader.quorumNode.ExternalAppliedIndex()
	image, size, err := leader.snapshotTaskQuorumState(seq)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(image)
	if err != nil {
		t.Fatal(err)
	}
	image.Close()
	if len(raw) != int(size) || size >= 1<<20 || !bytes.HasPrefix(raw, []byte(controlImageMagic)) {
		t.Fatal("unexpected external image")
	}
	dest := &Server{cfg: Config{ConfigDir: leader.cfg.ConfigDir}, quorumMode: quorum.OwnerLocal, quorumClusterID: cfg.ClusterID, quorumSnapshotDataDir: t.TempDir(), quorumSnapshotMaxBytes: quorum.ControlSnapshotLimit, quorumSnapshotFreeReserve: 1 << 20}
	localBefore, err := os.ReadFile(dest.taskDatabasePath())
	if err != nil {
		t.Fatal(err)
	}
	if err = dest.restoreTaskQuorumState(seq, bytes.NewReader(raw), size); err != nil {
		t.Fatal(err)
	}
	again, _, err := dest.snapshotTaskQuorumState(seq)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := io.ReadAll(again)
	again.Close()
	if err != nil || !bytes.Equal(raw, restored) {
		t.Fatal("restored control state differs")
	}
	raw[len(raw)-1] ^= 1
	if err = dest.restoreTaskQuorumState(seq, bytes.NewReader(raw), size); err == nil {
		t.Fatal("bad checksum accepted")
	}
	if err = dest.restoreTaskQuorumState(seq, nil, 1<<30); err == nil {
		t.Fatal("accepted oversized snapshot")
	}
	localAfter, err := os.ReadFile(dest.taskDatabasePath())
	if err != nil || !bytes.Equal(localBefore, localAfter) {
		t.Fatal("restore changed owner-local history")
	}
	if err = checkControlDatabaseFile(dest.taskDatabasePath()); err == nil {
		t.Fatal("accepted foreign task store as control SQLite")
	}
	// Rolling the historical writer can elect a different node before its
	// new binary returns. The original must be able to migrate on re-election
	// without requiring an offline edit or opening adoption to other voters.
	legacy = membershipQuorumAuthorityState(leader.membership)
	legacy.ControlSourceID = ""
	if err := leader.commitQuorumAuthorityState("membership", "writer", legacy); err != nil {
		t.Fatal(err)
	}
	servers[0].cfg.LegacyMemberControlSource = "n0"
	if err := leader.quorumNode.TransferLeadershipTo("n0"); err != nil {
		t.Fatal(err)
	}
	original := servers[0]
	authorityTestWait(t, "historical writer re-elected", func() bool { return original.quorumNode.Status().Leader })
	err = original.ensureQuorumAuthorityMaterialized(ctx)
	if err == nil || original.quorumAuthorityActiveWriter.Load() {
		t.Fatal("admission opened before the re-elected writer verified its committed source")
	}
	if err = original.ensureQuorumAuthorityMaterialized(ctx); err != nil {
		t.Fatal(err)
	}
	if err = original.readQuorumAuthorityState("membership", "writer", &migrated); err != nil || migrated.ControlSourceID != "n0" {
		t.Fatalf("re-elected historical source was not committed: %v", err)
	}
}

// A live legacy cluster keeps a committed baseline, committed authority rows
// and a sealed history checkpoint. Its last applied task sequence and the
// checkpoint sequence are independent counters, so an upgraded node must
// reopen that cluster as replicated instead of refusing to start.
func TestLegacyReplicatedClusterReopensWithDivergentCounters(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GPTADMIN_HOME", filepath.Join(root, "home"))
	s := reviewWriter(t, filepath.Join(root, "writer"))
	defer s.Close()
	cfg := reviewQuorumConfig(t, root)
	if err := s.withTaskDatabase(func(tx *taskTransaction) error {
		if _, err := tx.tx.Exec(`INSERT INTO task_quorum_meta(id,cluster_id,applied_index,baseline_ready,authority_ready) VALUES(1,?,22,1,1)`, cfg.ClusterID); err != nil {
			return err
		}
		_, err := tx.tx.Exec(`INSERT INTO task_quorum_chain(id,sequence,digest) VALUES(1,1021,?)`, make([]byte, 32))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cfg.DataDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.DataDir, "raft.db"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	mode, reopen, err := s.resolveQuorumMode(cfg)
	if err != nil || mode != quorum.Replicated || !reopen {
		t.Fatalf("legacy cluster did not reopen as replicated: mode=%q reopen=%v err=%v", mode, reopen, err)
	}
}

func TestOwnerLocalModeRequiresLegacyEvidence(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GPTADMIN_HOME", filepath.Join(root, "home"))
	s := reviewWriter(t, filepath.Join(root, "writer"))
	defer s.Close()
	cfg := reviewQuorumConfig(t, root)
	mode, reopen, err := s.resolveQuorumMode(cfg)
	if err != nil || mode != quorum.OwnerLocal || reopen {
		t.Fatal("fresh default", mode, err)
	}
	cfg.Mode = quorum.Replicated
	mode, _, err = s.resolveQuorumMode(cfg)
	if err != nil || mode != quorum.Replicated {
		t.Fatal("explicit opt-in", mode, err)
	}
	cfg.Mode = ""
	n := reviewOpenQuorum(t, s, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err = n.WaitForLeader(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.resolveQuorumMode(cfg); err == nil {
		t.Fatal("unmarked store selected a mode without a committed marker")
	}
	if err = s.seedTaskQuorumBaseline(ctx); err != nil {
		t.Fatal(err)
	}
	mode, reopen, err = s.resolveQuorumMode(cfg)
	if err != nil || mode != quorum.Replicated || !reopen || !s.legacyMarkerCommitted() {
		t.Fatal("lost committed legacy selection", err)
	}
	if err = n.Close(); err != nil {
		t.Fatal(err)
	}
	s.quorumNode = nil
	cfg.Peers[0].HubURL = "https://" + cfg.Peers[0].Address
	cfg.Peers[1].HubURL = "https://127.0.0.1:1"
	cfg.Peers[2].HubURL = "https://127.0.0.1:2"
	s.cfg.NodePublicURL = cfg.Peers[0].HubURL
	raw, _ := json.Marshal(cfg)
	if err = os.WriteFile(s.cfg.QuorumConfigFile, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err = s.configureQuorum(); err != nil {
		t.Fatal(err)
	}
	if _, err = s.quorumNode.WaitForLeader(ctx); err != nil {
		t.Fatal(err)
	}
	authorityTestWait(t, "legacy marker replay", func() bool { return s.taskQuorumReplicaReady() == nil && s.legacyMarkerCommitted() })
	cfg.Mode = quorum.OwnerLocal
	if _, _, err = s.resolveQuorumMode(cfg); err == nil {
		t.Fatal("silently changed legacy mode")
	}
	// The Raft directory holds the control identity, so it must be private.
	// t.TempDir() is group readable and the privacy guard rejects it.
	cfg.DataDir = filepath.Join(root, "owner-local-raft")
	if err = os.Mkdir(cfg.DataDir, 0700); err != nil {
		t.Fatal(err)
	}
	cfg.Mode = ""
	if mode, _, err = s.resolveQuorumMode(cfg); err != nil || mode != quorum.OwnerLocal {
		t.Fatal("dormant local task marker selected legacy in a fresh Raft directory")
	}
	if err = pinQuorumMode(cfg.DataDir, quorumStorageIdentity{cfg.ClusterID, quorum.OwnerLocal}); err != nil {
		t.Fatal(err)
	}
	cfg.Mode = quorum.Replicated
	if _, _, err = s.resolveQuorumMode(cfg); err == nil {
		t.Fatal("silently changed pinned owner_local mode")
	}
	cfg.Mode = "future"
	if _, _, err = s.resolveQuorumMode(cfg); err == nil {
		t.Fatal("accepted unknown mode")
	}
}
