package hub

import (
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
