package quorum

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

func operationKey(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }

func TestOperationReservationSurvivesSnapshotAndOwnerLoss(t *testing.T) {
	state := newStateMachine("c", nil, ExternalSnapshotCallbacks{}, 0)
	source := strings.Repeat("private command and output ", 100000)
	cmd := operationCommand{Action: "claim", Key: operationKey("scoped key"), Now: 1000,
		Operation: Operation{Owner: "server-44", Fingerprint: operationKey(source), Witness: operationKey("first local row")}}
	res := state.applyOperation(cmd)
	if res.err != nil || !res.operation.Created {
		t.Fatal(res)
	}
	cmd.Action, cmd.JobID = "bind", "original-task"
	if res := state.applyOperation(cmd); res.err != nil {
		t.Fatal(res.err)
	}
	snap, err := state.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	sink := &memorySnapshotSink{}
	if err := snap.Persist(sink); err != nil {
		t.Fatal(err)
	}
	if sink.Len() > 1024 || bytes.Contains(sink.Bytes(), []byte("private command")) {
		t.Fatalf("invalid compact snapshot size %d", sink.Len())
	}
	other := newStateMachine("c", nil, ExternalSnapshotCallbacks{}, 0)
	if err := other.Restore(io.NopCloser(bytes.NewReader(sink.Bytes()))); err != nil {
		t.Fatal(err)
	}
	node := &Node{fsm: other}
	if got := node.LocalOperationOwner("original-task", time.Unix(cmd.Now, 0)); got != cmd.Owner ||
		node.LocalOperationOwner("unrelated", time.Unix(cmd.Now, 0)) != "" {
		t.Fatal("restored exact task owner was not preserved")
	}
	retry := cmd
	retry.Action, retry.JobID, retry.Owner, retry.Witness = "claim", "", "server-88", operationKey("other row")
	retry.Now += int64(OperationRetention.Seconds()) + 10
	res = other.applyOperation(retry)
	if res.err != nil || res.operation.Created || res.operation.Operation.JobID != cmd.JobID || res.operation.Operation.Owner != "server-44" {
		t.Fatal(res)
	}
	retry.Fingerprint = operationKey("different arguments")
	if got := other.applyOperation(retry); got.err != ErrOperationConflict {
		t.Fatal(got)
	}
	encoded, _ := json.Marshal(cmd)
	if len(encoded) > 512 {
		t.Fatalf("entry size %d", len(encoded))
	}
	cmd.Action = "terminal"
	if got := other.applyOperation(cmd); got.err != nil {
		t.Fatal(got)
	}
	if node.LocalOperationOwner("original-task", time.Unix(retry.Now, 0)) != "" {
		t.Fatal("expired legacy reservation identified an owner")
	}
	retry.Fingerprint = cmd.Fingerprint
	if got := other.applyOperation(retry); !got.operation.Created || got.err != nil {
		t.Fatal(got)
	}
	if got := other.applyOperation(cmd); got.err != ErrOperationOwner {
		t.Fatal("stale completion changed the new claim", got)
	}
}

func TestOperationBudgetPreservesUnresolvedAndAdmitsOtherOwners(t *testing.T) {
	s := newStateMachine("c", nil, ExternalSnapshotCallbacks{}, 0)
	c := operationCommand{Action: "claim", Now: 42, Operation: Operation{Owner: "a", Fingerprint: operationKey("args"), Witness: operationKey("w")}}
	for i := 0; i < OperationLimitPerOwner; i++ {
		c.Key = operationKey(fmt.Sprint(i))
		if got := s.applyOperation(c); got.err != nil {
			t.Fatal(i, got.err)
		}
	}
	c.Key = operationKey("extra")
	c.Now += int64(OperationRetention.Seconds()) + 1
	if got := s.applyOperation(c); got.err != ErrOperationCapacity {
		t.Fatal(got)
	}
	c.Owner = "b"
	if got := s.applyOperation(c); got.err != nil || !got.operation.Created {
		t.Fatal(got)
	}
}

func TestLostLeaderOnlySharesOriginalOperationReference(t *testing.T) {
	nodes, _ := testCluster(t)
	original := waitForLeader(t, nodes)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	key := operationKey("scope and request")
	op := Operation{Owner: original.config.NodeID, Fingerprint: operationKey("arguments"), Witness: operationKey("owner row")}
	result, err := original.UpdateOperation(ctx, "claim", key, op)
	if err != nil || !result.Created {
		t.Fatal(result, err)
	}
	op.JobID = "original-job"
	if _, err := original.UpdateOperation(ctx, "bind", key, op); err != nil {
		t.Fatal(err)
	}
	if err := original.Close(); err != nil {
		t.Fatal(err)
	}
	var leader *Node
	for leader == nil {
		for _, node := range nodes {
			if node != original && node.Status().Leader {
				leader = node
				break
			}
		}
		if leader == nil {
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(40 * time.Millisecond):
			}
		}
	}
	retry := Operation{Owner: leader.config.NodeID, Fingerprint: op.Fingerprint, Witness: operationKey("new owner row")}
	result, err = leader.UpdateOperation(ctx, "claim", key, retry)
	if err != nil || result.Created || result.Operation != op {
		t.Fatal(result, err)
	}
	result, err = leader.UpdateOperation(ctx, "claim", operationKey("new independent request"), retry)
	if err != nil || !result.Created {
		t.Fatal(result, err)
	}
}
