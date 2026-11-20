package quorum

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"sort"
	"sync"

	"github.com/hashicorp/raft"
)

const maxSnapshotBytes = 64 << 20
const snapshotMagic = "GPTQSN1\n"
const controlSnapshotMagic = "GPTQCO1\n"

type Record struct {
	Revision uint64          `json:"revision"`
	Value    json.RawMessage `json:"value,omitempty"`
	Deleted  bool            `json:"deleted,omitempty"`
}

type Mutation struct {
	Namespace string `json:"namespace"`
	Key       string `json:"key"`
	Record    Record `json:"record"`
	External  bool   `json:"external,omitempty"`
}

type command struct {
	Operation        *operationCommand `json:"operation,omitempty"`
	ClusterID        string            `json:"cluster_id"`
	ExternalSequence uint64            `json:"external_sequence,omitempty"`
	Mutations        []Mutation        `json:"mutations"`
}

type applyResult struct {
	err       error
	operation OperationDecision
}

type stateMachine struct {
	controlOnly      bool
	controlOwners    map[string]bool
	operations       map[string]Operation
	clusterID        string
	onApply          func(index, externalSequence uint64, mutations []Mutation) error
	externalSnapshot ExternalSnapshotCallbacks
	maxExternalBytes int64
	mu               sync.RWMutex
	values           map[string]map[string]Record
	applyErr         error
	applyErrIndex    uint64
	externalSequence uint64
}

type stateImage struct {
	Operations              map[string]Operation         `json:"operations,omitempty"`
	ClusterID               string                       `json:"cluster_id"`
	Values                  map[string]map[string]Record `json:"values"`
	ExternalSequence        uint64                       `json:"external_sequence,omitempty"`
	ExternalApplyErrorIndex uint64                       `json:"external_apply_error_index,omitempty"`
}

// ExternalSnapshotCallbacks stream application-owned durable state alongside
// the Raft FSM image so log compaction cannot advance a voter past its database.
type ExternalSnapshotCallbacks struct {
	Snapshot func(sequence uint64) (io.ReadCloser, int64, error)
	Restore  func(sequence uint64, source io.Reader, size int64) error
}

func newStateMachine(clusterID string, onApply func(index, externalSequence uint64, mutations []Mutation) error, externalSnapshot ExternalSnapshotCallbacks, maxExternalBytes int64) *stateMachine {
	return &stateMachine{clusterID: clusterID, onApply: onApply, externalSnapshot: externalSnapshot, maxExternalBytes: maxExternalBytes, values: make(map[string]map[string]Record)}
}

func (s *stateMachine) Apply(entry *raft.Log) interface{} {
	if entry == nil || len(entry.Data) == 0 {
		return applyResult{}
	}
	if s.controlOnly && len(entry.Data) > maxRecordBytes {
		return applyResult{err: errors.New("quorum: oversized control log entry")}
	}
	var cmd command
	if err := json.Unmarshal(entry.Data, &cmd); err != nil {
		return applyResult{err: err}
	}
	if cmd.Operation != nil && cmd.ClusterID == s.clusterID && len(cmd.Mutations) == 0 && cmd.ExternalSequence == 0 {
		if s.controlOnly && !s.controlOwners[cmd.Operation.Owner] {
			return applyResult{err: ErrOperationOwner}
		}
		return s.applyOperation(*cmd.Operation)
	}
	if (s.controlOnly && len(cmd.Mutations) != 1) || cmd.Operation != nil || cmd.ClusterID != s.clusterID || len(cmd.Mutations) == 0 {
		return applyResult{err: errors.New("quorum: invalid committed record")}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := make(map[string]bool, len(cmd.Mutations))
	hasExternal := false
	for _, mutation := range cmd.Mutations {
		if mutation.Namespace == "" || mutation.Key == "" || mutation.Record.Revision == 0 ||
			(mutation.Record.Deleted && len(mutation.Record.Value) != 0) ||
			(!mutation.Record.Deleted && (len(mutation.Record.Value) == 0 || !json.Valid(mutation.Record.Value))) ||
			(mutation.External && s.onApply == nil) {
			return applyResult{err: errors.New("quorum: invalid committed mutation")}
		}
		if s.controlOnly && !mutation.External {
			if err := ValidateControlMutation(mutation, s.clusterID); err != nil {
				return applyResult{err: err}
			}
		}
		identity := mutation.Namespace + "\x00" + mutation.Key
		if seen[identity] {
			return applyResult{err: errors.New("quorum: duplicate mutation key in atomic batch")}
		}
		seen[identity] = true
		hasExternal = hasExternal || mutation.External
		if !mutation.External {
			if previous, ok := s.values[mutation.Namespace][mutation.Key]; ok {
				if mutation.Record.Revision < previous.Revision ||
					(mutation.Record.Revision == previous.Revision &&
						(previous.Deleted != mutation.Record.Deleted || !bytes.Equal(previous.Value, mutation.Record.Value))) {
					return applyResult{err: ErrStaleRevision}
				}
			}
		}
	}
	if (hasExternal && cmd.ExternalSequence != s.externalSequence+1) || (!hasExternal && cmd.ExternalSequence != 0) {
		return applyResult{err: errors.New("quorum: external state sequence is stale or discontinuous")}
	}
	for i := range cmd.Mutations {
		cmd.Mutations[i].Record.Value = append(json.RawMessage(nil), cmd.Mutations[i].Record.Value...)
	}
	if s.onApply != nil {
		if err := s.onApply(entry.Index, cmd.ExternalSequence, cmd.Mutations); err != nil {
			log.Printf("quorum external apply failed raft_index=%d external_sequence=%d: %v", entry.Index, cmd.ExternalSequence, err)
			for _, mutation := range cmd.Mutations {
				if mutation.External {
					s.applyErr = err
					s.applyErrIndex = entry.Index
					break
				}
			}
			// The Raft entry is already committed. Keep it in memory so the
			// callback can be retried with the identical batch for reconciliation.
			for _, mutation := range cmd.Mutations {
				if !mutation.External {
					s.put(mutation)
				}
			}
			return applyResult{err: err}
		}
	}
	for _, mutation := range cmd.Mutations {
		if !mutation.External {
			s.put(mutation)
		} else {
			s.externalSequence = cmd.ExternalSequence
		}
	}
	return applyResult{}
}

func (s *stateMachine) externalApplyError() (uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.applyErrIndex, s.applyErr
}

func (s *stateMachine) externalIndex() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.externalSequence
}

func (s *stateMachine) externalStateReady() bool {
	return s.externalSnapshot.Snapshot != nil && s.externalSnapshot.Restore != nil && s.maxExternalBytes > 0
}

func (s *stateMachine) clearExternalApplyError() {
	s.mu.Lock()
	s.applyErr = nil
	s.applyErrIndex = 0
	s.mu.Unlock()
}

func (s *stateMachine) put(mutation Mutation) {
	bucket := s.values[mutation.Namespace]
	if bucket == nil {
		bucket = make(map[string]Record)
		s.values[mutation.Namespace] = bucket
	}
	mutation.Record.Value = append(json.RawMessage(nil), mutation.Record.Value...)
	bucket[mutation.Key] = mutation.Record
}

func (s *stateMachine) Snapshot() (raft.FSMSnapshot, error) {
	s.mu.RLock()
	if s.applyErr != nil {
		err := s.applyErr
		s.mu.RUnlock()
		return nil, fmt.Errorf("quorum: refuse snapshot while external state is unreconciled: %w", err)
	}
	copyValues := make(map[string]map[string]Record, len(s.values))
	for namespace, bucket := range s.values {
		copyBucket := make(map[string]Record, len(bucket))
		for key, value := range bucket {
			value.Value = append(json.RawMessage(nil), value.Value...)
			copyBucket[key] = value
		}
		copyValues[namespace] = copyBucket
	}
	operations := make(map[string]Operation, len(s.operations))
	for key, entry := range s.operations {
		operations[key] = entry
	}
	image := stateImage{ClusterID: s.clusterID, Values: copyValues, Operations: operations,
		ExternalSequence: s.externalSequence, ExternalApplyErrorIndex: s.applyErrIndex}
	s.mu.RUnlock()
	return &stateSnapshot{controlOnly: s.controlOnly, image: image, externalSnapshot: s.externalSnapshot, maxExternalBytes: s.maxExternalBytes}, nil
}

func (s *stateMachine) Restore(reader io.ReadCloser) error {
	defer reader.Close()
	br := bufio.NewReader(reader)
	var magic [len(snapshotMagic)]byte
	if _, err := io.ReadFull(br, magic[:]); err != nil {
		return fmt.Errorf("quorum: read snapshot header: %w", err)
	}
	expectedMagic := snapshotMagic
	if s.controlOnly {
		expectedMagic = controlSnapshotMagic
	}
	if string(magic[:]) != expectedMagic {
		return errors.New("quorum: unsupported snapshot format")
	}
	var headerLength [8]byte
	if _, err := io.ReadFull(br, headerLength[:]); err != nil {
		return fmt.Errorf("quorum: read snapshot length: %w", err)
	}
	headerSize := binary.BigEndian.Uint64(headerLength[:])
	if headerSize == 0 || headerSize > maxSnapshotBytes || (s.controlOnly && headerSize > uint64(controlMetadataLimit+len(s.controlOwners)*OperationLimitPerOwner*1280)) {
		return errors.New("quorum: snapshot metadata exceeds its byte limit")
	}
	data := make([]byte, int(headerSize))
	if _, err := io.ReadFull(br, data); err != nil {
		return fmt.Errorf("quorum: read snapshot metadata: %w", err)
	}
	var envelope struct {
		Image        stateImage `json:"image"`
		ExternalSize int64      `json:"external_size"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return err
	}
	image := envelope.Image
	if image.ClusterID != s.clusterID {
		return errors.New("quorum: snapshot cluster identity mismatch")
	}
	if s.controlOnly {
		if len(image.Values) > 1 || envelope.ExternalSize > ControlSnapshotLimit {
			return errors.New("quorum: invalid control snapshot size or namespaces")
		}
		for ns, bucket := range image.Values {
			if ns != "cluster_meta" || len(bucket) != 1 {
				return errors.New("quorum: invalid control snapshot namespace")
			}
			r, ok := bucket["task_baseline"]
			if !ok || ValidateControlMutation(Mutation{Namespace: ns, Key: "task_baseline", Record: r}, s.clusterID) != nil {
				return errors.New("quorum: invalid control snapshot baseline")
			}
		}
		counts := map[string]int{}
		for key, op := range image.Operations {
			expires := op.Expires
			op.Expires = 0
			if expires < 0 || !s.controlOwners[op.Owner] || !(operationCommand{Action: "claim", Key: key, Now: 1, Operation: op}).valid() {
				return errors.New("quorum: invalid control snapshot operation")
			}
			counts[op.Owner]++
			if counts[op.Owner] > OperationLimitPerOwner {
				return ErrOperationCapacity
			}
		}
	}
	if image.Values == nil {
		image.Values = make(map[string]map[string]Record)
	}
	for namespace, bucket := range image.Values {
		if namespace == "" || bucket == nil {
			return errors.New("quorum: invalid snapshot namespace")
		}
		for key, value := range bucket {
			if key == "" || value.Revision == 0 {
				return errors.New("quorum: invalid snapshot record")
			}
		}
	}
	if envelope.ExternalSize < 0 || envelope.ExternalSize > s.maxExternalBytes {
		return errors.New("quorum: external snapshot exceeds its configured byte limit")
	}
	if image.ExternalSequence == 0 {
		if envelope.ExternalSize != 0 {
			return errors.New("quorum: unexpected external data in empty-state snapshot")
		}
		if _, err := br.ReadByte(); !errors.Is(err, io.EOF) {
			return errors.New("quorum: trailing bytes after snapshot metadata")
		}
	} else {
		if envelope.ExternalSize == 0 || s.externalSnapshot.Restore == nil {
			return errors.New("quorum: external snapshot restore is not configured")
		}
		limited := &io.LimitedReader{R: br, N: envelope.ExternalSize}
		if err := s.externalSnapshot.Restore(image.ExternalSequence, limited, envelope.ExternalSize); err != nil {
			return fmt.Errorf("quorum: restore external snapshot: %w", err)
		}
		if limited.N != 0 {
			return errors.New("quorum: external snapshot restore did not consume the full database image")
		}
		if _, err := br.ReadByte(); !errors.Is(err, io.EOF) {
			return errors.New("quorum: trailing bytes after external snapshot")
		}
	}
	s.mu.Lock()
	s.values = image.Values
	s.operations = image.Operations
	s.externalSequence = image.ExternalSequence
	s.applyErrIndex = image.ExternalApplyErrorIndex
	if image.ExternalApplyErrorIndex != 0 {
		s.applyErr = fmt.Errorf("quorum: external state application failed at log index %d", image.ExternalApplyErrorIndex)
	} else {
		s.applyErr = nil
	}
	s.mu.Unlock()
	return nil
}

func (s *stateMachine) get(namespace, key string) (Record, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	bucket := s.values[namespace]
	value, ok := bucket[key]
	value.Value = append(json.RawMessage(nil), value.Value...)
	return value, ok
}

type stateSnapshot struct {
	controlOnly      bool
	image            stateImage
	externalSnapshot ExternalSnapshotCallbacks
	maxExternalBytes int64
}

func (s *stateSnapshot) Persist(sink raft.SnapshotSink) error {
	namespaces := make([]string, 0, len(s.image.Values))
	for namespace := range s.image.Values {
		namespaces = append(namespaces, namespace)
	}
	sort.Strings(namespaces)
	ordered := make(map[string]map[string]Record, len(s.image.Values))
	for _, namespace := range namespaces {
		bucket := s.image.Values[namespace]
		keys := make([]string, 0, len(bucket))
		for key := range bucket {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		copyBucket := make(map[string]Record, len(keys))
		for _, key := range keys {
			value := bucket[key]
			value.Value = append(json.RawMessage(nil), value.Value...)
			copyBucket[key] = value
		}
		ordered[namespace] = copyBucket
	}
	image := s.image
	image.Values = ordered
	var external io.ReadCloser
	var externalSize int64
	if image.ExternalSequence != 0 {
		if s.externalSnapshot.Snapshot == nil {
			_ = sink.Cancel()
			return errors.New("quorum: external snapshot export is not configured")
		}
		var err error
		external, externalSize, err = s.externalSnapshot.Snapshot(image.ExternalSequence)
		if err != nil {
			_ = sink.Cancel()
			return fmt.Errorf("quorum: export external snapshot: %w", err)
		}
		if external == nil || externalSize <= 0 || externalSize > s.maxExternalBytes {
			_ = sink.Cancel()
			return errors.New("quorum: external snapshot size is invalid or exceeds its configured byte limit")
		}
		defer func() {
			if external != nil {
				_ = external.Close()
			}
		}()
	}
	data, err := json.Marshal(struct {
		Image        stateImage `json:"image"`
		ExternalSize int64      `json:"external_size"`
	}{image, externalSize})
	if err != nil {
		_ = sink.Cancel()
		return err
	}
	if len(data) > maxSnapshotBytes {
		_ = sink.Cancel()
		return errors.New("quorum: snapshot exceeds its byte limit")
	}
	magic := snapshotMagic
	if s.controlOnly {
		magic = controlSnapshotMagic
	}
	if _, err := io.WriteString(sink, magic); err != nil {
		_ = sink.Cancel()
		return err
	}
	var headerLength [8]byte
	binary.BigEndian.PutUint64(headerLength[:], uint64(len(data)))
	if _, err := sink.Write(headerLength[:]); err != nil {
		_ = sink.Cancel()
		return err
	}
	if _, err := io.Copy(sink, bytes.NewReader(data)); err != nil {
		_ = sink.Cancel()
		return err
	}
	if external != nil {
		written, err := io.CopyN(sink, external, externalSize)
		if err != nil {
			_ = sink.Cancel()
			return fmt.Errorf("quorum: write external snapshot (%d/%d bytes): %w", written, externalSize, err)
		}
		var extra [1]byte
		if n, err := external.Read(extra[:]); n != 0 || !errors.Is(err, io.EOF) {
			_ = sink.Cancel()
			return errors.New("quorum: external snapshot source exceeded its declared size")
		}
		if err := external.Close(); err != nil {
			external = nil
			_ = sink.Cancel()
			return fmt.Errorf("quorum: close external snapshot source: %w", err)
		}
		external = nil
	}
	return sink.Close()
}

func (*stateSnapshot) Release() {}
