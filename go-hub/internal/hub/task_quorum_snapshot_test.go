package hub

import (
	"database/sql"
	"testing"
)

func TestTaskQuorumSQLiteSnapshotRestoresExactSequenceAndRows(t *testing.T) {
	const clusterID = "cluster-snapshot-test"
	source := New(Config{ConfigDir: t.TempDir(), QuorumConfigFile: "enabled-for-test"})
	source.quorumClusterID = clusterID
	source.quorumSnapshotMaxBytes = 8 << 20
	source.quorumSnapshotFreeReserve = 1 << 20
	source.quorumSnapshotDataDir = source.cfg.ConfigDir
	if err := source.withTaskDatabase(func(tx *taskTransaction) error {
		if _, err := tx.tx.Exec(`INSERT INTO task_records(kind,key,updated,done,payload,revision) VALUES('shell','job-1',10,11,?,1)`, []byte(`{"id":"job-1","status":"completed"}`)); err != nil {
			return err
		}
		if _, err := tx.tx.Exec(`INSERT INTO task_quorum_revisions(namespace,row_key,revision) VALUES('task_records/shell','job-1',1)`); err != nil {
			return err
		}
		if _, err := tx.tx.Exec(`INSERT INTO task_quorum_chain(id,sequence,digest) VALUES(1,7,?)`, make([]byte, 32)); err != nil {
			return err
		}
		_, err := tx.tx.Exec(`INSERT INTO task_quorum_meta(id,cluster_id,applied_index,baseline_ready,authority_ready) VALUES(1,?,7,1,1)`, clusterID)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	image, size, err := source.snapshotTaskQuorumState(7)
	if err != nil {
		t.Fatalf("export SQLite snapshot: %v", err)
	}
	defer image.Close()

	restored := New(Config{ConfigDir: t.TempDir(), QuorumConfigFile: "enabled-for-test"})
	restored.quorumClusterID = clusterID
	restored.quorumSnapshotMaxBytes = 8 << 20
	restored.quorumSnapshotFreeReserve = 1 << 20
	restored.quorumSnapshotDataDir = restored.cfg.ConfigDir
	if err := restored.withTaskDatabase(func(*taskTransaction) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := restored.restoreTaskQuorumState(7, image, size); err != nil {
		t.Fatalf("restore SQLite snapshot: %v", err)
	}
	var payload []byte
	var cluster string
	var sequence uint64
	var baseline, authority int
	if err := restored.withTaskReadDatabase(func(tx *sql.Tx) error {
		if err := tx.QueryRow(`SELECT payload FROM task_records WHERE kind='shell' AND key='job-1'`).Scan(&payload); err != nil {
			return err
		}
		return tx.QueryRow(`SELECT cluster_id,applied_index,baseline_ready,authority_ready FROM task_quorum_meta WHERE id=1`).Scan(&cluster, &sequence, &baseline, &authority)
	}); err != nil {
		t.Fatal(err)
	}
	if string(payload) != `{"id":"job-1","status":"completed"}` || cluster != clusterID || sequence != 7 || baseline != 1 || authority != 1 {
		t.Fatalf("restored payload=%s cluster=%s sequence=%d baseline=%d authority=%d", payload, cluster, sequence, baseline, authority)
	}
}
