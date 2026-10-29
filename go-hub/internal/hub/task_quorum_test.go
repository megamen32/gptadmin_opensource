package hub

import (
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/megamen32/gptadmin/go-hub/internal/hub/quorum"
)

func TestTaskQuorumReconcilesAnAmbiguousSQLiteCommitOnlyForExactBatch(t *testing.T) {
	s := New(Config{ConfigDir: t.TempDir(), QuorumConfigFile: "enabled-for-test"})
	s.quorumClusterID = "cluster-a"
	makeMutation := func(result string) quorum.Mutation {
		value, err := json.Marshal(quorumTaskRecordRow{
			Kind: "shell", Key: "job-1", Updated: 10, Done: 11,
			Payload: []byte(`{"id":"job-1","status":"completed","result":"` + result + `"}`), Revision: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		return quorum.Mutation{
			Namespace: "task_records/shell", Key: "job-1", External: true,
			Record: quorum.Record{Revision: 1, Value: value},
		}
	}

	committed := makeMutation("done")
	if err := s.applyTaskQuorumMutations(10, 1, []quorum.Mutation{committed}); err != nil {
		t.Fatalf("apply first committed batch: %v", err)
	}
	if err := s.applyTaskQuorumMutations(10, 1, []quorum.Mutation{committed}); err != nil {
		t.Fatalf("reconcile the identical already-committed batch: %v", err)
	}
	if err := s.applyTaskQuorumMutations(10, 1, []quorum.Mutation{makeMutation("different")}); err == nil {
		t.Fatal("reconciliation accepted a different batch at the committed sequence")
	}
}

func TestTaskQuorumHistoricalReplayRequiresMatchingChain(t *testing.T) {
	makeMutation := func(revision uint64, value string) quorum.Mutation {
		row, err := json.Marshal(quorumTaskRecordRow{Kind: "shell", Key: "job", Updated: float64(revision), Payload: []byte(`{"id":"job","status":"queued","result":"` + value + `"}`), Revision: int64(revision)})
		if err != nil {
			t.Fatal(err)
		}
		return quorum.Mutation{Namespace: "task_records/shell", Key: "job", External: true, Record: quorum.Record{Revision: revision, Value: row}}
	}
	for _, tc := range []struct {
		name   string
		tamper bool
		gap    bool
	}{{"exact", false, false}, {"tampered-history", true, false}, {"missing-prefix", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			s := New(Config{ConfigDir: t.TempDir(), QuorumConfigFile: "fixture"})
			defer s.Close()
			s.quorumClusterID = "cluster"
			first, second := makeMutation(1, "first"), makeMutation(2, "second")
			if err := s.applyTaskQuorumMutations(1, 1, []quorum.Mutation{first}); err != nil {
				t.Fatal(err)
			}
			if err := s.applyTaskQuorumMutations(2, 2, []quorum.Mutation{second}); err != nil {
				t.Fatal(err)
			}
			if tc.gap {
				if err := s.applyTaskQuorumMutations(1, 0, []quorum.Mutation{first}); err == nil {
					t.Fatal("missing replay prefix accepted")
				}
				return
			}
			if tc.tamper {
				first = makeMutation(1, "altered")
			}
			if err := s.applyTaskQuorumMutations(1, 1, []quorum.Mutation{first}); err != nil {
				t.Fatal(err)
			}
			err := s.applyTaskQuorumMutations(2, 2, []quorum.Mutation{second})
			if tc.tamper {
				if err == nil {
					t.Fatal("altered historical batch accepted")
				}
				return
			}
			if err != nil {
				t.Fatalf("identical history rejected: %v", err)
			}
			third := makeMutation(3, "third")
			if err = s.applyTaskQuorumMutations(3, 3, []quorum.Mutation{third}); err != nil {
				t.Fatalf("new sequence after replay: %v", err)
			}
			var sequence uint64
			var digest []byte
			if err = s.withTaskReadDatabase(func(tx *sql.Tx) error {
				return tx.QueryRow(`SELECT sequence,digest FROM task_quorum_chain WHERE id=1`).Scan(&sequence, &digest)
			}); err != nil {
				t.Fatal(err)
			}
			if sequence != 3 || len(digest) != 32 {
				t.Fatal("history checkpoint not advanced")
			}
		})
	}
}
