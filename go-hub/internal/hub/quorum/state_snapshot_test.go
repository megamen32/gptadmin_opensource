package quorum

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/hashicorp/raft"
)

type memorySnapshotSink struct {
	bytes.Buffer
	closed   bool
	canceled bool
}

func (s *memorySnapshotSink) ID() string { return "memory" }

func (s *memorySnapshotSink) Close() error {
	s.closed = true
	return nil
}

func (s *memorySnapshotSink) Cancel() error {
	s.canceled = true
	return nil
}

func TestSnapshotStreamsAndRestoresExternalStateAtMatchingSequence(t *testing.T) {
	var exportedSequence uint64
	state := newStateMachine("cluster-a", nil, ExternalSnapshotCallbacks{
		Snapshot: func(sequence uint64) (io.ReadCloser, int64, error) {
			exportedSequence = sequence
			data := []byte("sqlite-image")
			return io.NopCloser(bytes.NewReader(data)), int64(len(data)), nil
		},
	}, 1024)
	state.mu.Lock()
	state.externalSequence = 7
	state.values["meta"] = map[string]Record{"ready": {Revision: 1, Value: []byte(`{"ok":true}`)}}
	state.mu.Unlock()

	snapshot, err := state.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	sink := &memorySnapshotSink{}
	if err := snapshot.Persist(sink); err != nil {
		t.Fatalf("persist snapshot: %v", err)
	}
	if !sink.closed || sink.canceled || exportedSequence != 7 {
		t.Fatalf("snapshot lifecycle: closed=%v canceled=%v external_sequence=%d", sink.closed, sink.canceled, exportedSequence)
	}

	var restored bytes.Buffer
	var restoredSequence uint64
	target := newStateMachine("cluster-a", nil, ExternalSnapshotCallbacks{
		Restore: func(sequence uint64, source io.Reader, size int64) error {
			restoredSequence = sequence
			if _, err := io.CopyN(&restored, source, size); err != nil {
				return err
			}
			return nil
		},
	}, 1024)
	if err := target.Restore(io.NopCloser(bytes.NewReader(sink.Bytes()))); err != nil {
		t.Fatalf("restore snapshot: %v", err)
	}
	if restoredSequence != 7 || restored.String() != "sqlite-image" || target.externalIndex() != 7 {
		t.Fatalf("restored sequence=%d database=%q fsm_sequence=%d", restoredSequence, restored.String(), target.externalIndex())
	}
	if record, ok := target.get("meta", "ready"); !ok || record.Revision != 1 {
		t.Fatalf("internal FSM record missing after restore: present=%v record=%+v", ok, record)
	}
	var _ raft.SnapshotSink = sink
}

func TestSnapshotRefusesToCompactAnUnreconciledExternalApply(t *testing.T) {
	state := newStateMachine("cluster-a", func(uint64, uint64, []Mutation) error {
		return errors.New("sqlite commit failed")
	}, ExternalSnapshotCallbacks{}, 0)
	data, err := json.Marshal(command{
		ClusterID: "cluster-a", ExternalSequence: 1,
		Mutations: []Mutation{{Namespace: "tasks", Key: "job-1", External: true, Record: Record{Revision: 1, Value: []byte(`{"status":"queued"}`)}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := state.Apply(&raft.Log{Index: 4, Data: data}).(applyResult)
	if result.err == nil {
		t.Fatal("expected external application failure")
	}
	if _, err := state.Snapshot(); err == nil {
		t.Fatal("snapshot must not compact an unreconciled external apply")
	}
}
