package quorum

import (
	"context"
	"crypto/cipher"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/raft"
	raftboltdb "github.com/hashicorp/raft-boltdb/v2"
)

const maxRecordBytes = 16 << 20

type Node struct {
	config    Config
	raft      *raft.Raft
	transport *raft.NetworkTransport
	store     *raftboltdb.BoltStore
	fsm       *stateMachine
	stateAEAD cipher.AEAD
	applyMu   sync.Mutex
}

type Status struct {
	NodeID        string `json:"node_id"`
	ClusterID     string `json:"cluster_id"`
	State         string `json:"state"`
	Leader        bool   `json:"leader"`
	LeaderID      string `json:"leader_id,omitempty"`
	LeaderAddress string `json:"leader_address,omitempty"`
	VoterCount    int    `json:"voter_count"`
	CommitIndex   uint64 `json:"commit_index"`
	AppliedIndex  uint64 `json:"applied_index"`
}

// Open creates a durable Raft voter. Only one node in a new cluster may set
// Bootstrap; the other configured voters start empty and wait for that leader's
// log. Existing stores always resume their persisted membership and term.
func Open(input Config) (*Node, error) {
	return OpenWithApply(input, nil)
}

// OpenWithApply starts a voter and invokes apply for every locally applied
// record. The callback must be deterministic, local, bounded, and must not
// call back into this Node. It is used to persist replicated state on followers
// before they can be promoted to serve it.
func OpenWithApply(input Config, apply func(index, externalSequence uint64, mutations []Mutation) error) (*Node, error) {
	return OpenWithExternalState(input, apply, ExternalSnapshotCallbacks{})
}

// OpenWithExternalState installs durable application callbacks together with
// their streamed snapshot/restore path. A voter must not accept external rows
// unless it can also transfer that state across Raft log compaction.
func OpenWithExternalState(input Config, apply func(index, externalSequence uint64, mutations []Mutation) error, externalSnapshot ExternalSnapshotCallbacks) (*Node, error) {
	c, err := input.normalized()
	if err != nil {
		return nil, err
	}
	if (externalSnapshot.Snapshot == nil) != (externalSnapshot.Restore == nil) {
		return nil, errors.New("quorum: external snapshot export and restore callbacks must be configured together")
	}
	if externalSnapshot.Snapshot != nil && (c.ExternalSnapshotMaxBytes <= 0 || c.ExternalSnapshotFreeReserveBytes <= 0) {
		return nil, errors.New("quorum: external snapshot size and free-space reserve limits must be finite and positive")
	}
	if err := os.MkdirAll(c.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("quorum: create data directory: %w", err)
	}
	if info, err := os.Stat(c.DataDir); err != nil {
		return nil, err
	} else if info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("quorum: data directory permissions must be 0700 or stricter")
	}
	snapshotDir := filepath.Join(c.DataDir, "snapshots")
	if err := os.MkdirAll(snapshotDir, 0o700); err != nil {
		return nil, fmt.Errorf("quorum: create snapshot directory: %w", err)
	}
	store, err := raftboltdb.NewBoltStore(filepath.Join(c.DataDir, "raft.db"))
	if err != nil {
		return nil, fmt.Errorf("quorum: open stable store: %w", err)
	}
	closeStore := true
	defer func() {
		if closeStore {
			_ = store.Close()
		}
	}()
	snapshots, err := raft.NewFileSnapshotStore(snapshotDir, 1, io.Discard)
	if err != nil {
		return nil, fmt.Errorf("quorum: open snapshot store: %w", err)
	}
	stream, err := newSecureStream(c)
	if err != nil {
		return nil, err
	}
	transport := raft.NewNetworkTransportWithConfig(&raft.NetworkTransportConfig{
		Stream:  stream,
		MaxPool: 2,
		Timeout: c.RPCDeadline,
		Logger:  hclog.NewNullLogger(),
	})
	closeTransport := true
	defer func() {
		if closeTransport {
			_ = transport.Close()
		}
	}()
	config := raft.DefaultConfig()
	config.LocalID = raft.ServerID(c.NodeID)
	config.Logger = hclog.NewNullLogger()
	config.LogOutput = io.Discard
	config.SnapshotInterval = 30 * time.Second
	config.SnapshotThreshold = 1024
	config.TrailingLogs = 2048
	if err := raft.ValidateConfig(config); err != nil {
		return nil, fmt.Errorf("quorum: invalid Raft settings: %w", err)
	}
	hasState, err := raft.HasExistingState(store, store, snapshots)
	if err != nil {
		return nil, fmt.Errorf("quorum: inspect durable state: %w", err)
	}
	if !hasState && c.Bootstrap {
		configuration := raft.Configuration{Servers: make([]raft.Server, 0, len(c.Peers))}
		for _, peer := range c.Peers {
			suffrage := raft.Nonvoter
			if peer.ID == c.BootstrapperID {
				suffrage = raft.Voter
			}
			configuration.Servers = append(configuration.Servers, raft.Server{
				ID: raft.ServerID(peer.ID), Address: raft.ServerAddress(peer.Address), Suffrage: suffrage,
			})
		}
		if err := raft.BootstrapCluster(config, store, store, snapshots, transport, configuration); err != nil {
			return nil, fmt.Errorf("quorum: bootstrap cluster: %w", err)
		}
	} else if !hasState && !c.Bootstrap {
		// A non-bootstrap node is allowed to start empty. It cannot elect itself
		// unless the bootstrapped cluster's committed voter set includes its ID.
	}
	var stateAEAD cipher.AEAD
	stateApply := apply
	if apply != nil {
		stateAEAD, err = loadStateAEAD(c.StateKeyFile)
		if err != nil {
			return nil, err
		}
		stateApply = func(index, externalSequence uint64, mutations []Mutation) error {
			decoded := append([]Mutation(nil), mutations...)
			for i := range decoded {
				if !decoded[i].External {
					continue
				}
				mutation, decodeErr := openMutation(stateAEAD, c.ClusterID, decoded[i])
				if decodeErr != nil {
					return decodeErr
				}
				decoded[i] = mutation
			}
			return apply(index, externalSequence, decoded)
		}
	}
	fsm := newStateMachine(c.ClusterID, stateApply, externalSnapshot, c.ExternalSnapshotMaxBytes)
	r, err := raft.NewRaft(config, fsm, store, store, snapshots, transport)
	if err != nil {
		return nil, fmt.Errorf("quorum: start Raft: %w", err)
	}
	closeStore = false
	closeTransport = false
	return &Node{config: c, raft: r, transport: transport, store: store, fsm: fsm, stateAEAD: stateAEAD}, nil
}

// Status reports the local peer's view. Leadership is authoritative only when
// VerifyLeader succeeds; callers must not promote from this read-only snapshot.
func (n *Node) Status() Status {
	address, id := n.raft.LeaderWithID()
	voterCount := 0
	configuration := n.raft.GetConfiguration()
	if configuration.Error() == nil && configuration.Index() <= n.raft.AppliedIndex() {
		for _, server := range configuration.Configuration().Servers {
			if server.Suffrage == raft.Voter {
				voterCount++
			}
		}
	}
	return Status{
		NodeID: n.config.NodeID, ClusterID: n.config.ClusterID,
		State: n.raft.State().String(), Leader: n.raft.State() == raft.Leader,
		LeaderID: string(id), LeaderAddress: string(address), VoterCount: voterCount,
		CommitIndex: n.raft.CommitIndex(), AppliedIndex: n.raft.AppliedIndex(),
	}
}

func (n *Node) BootstrapperID() string { return n.config.BootstrapperID }

// IsVoter reports only a configuration already applied by this local Raft
// process. An appended but uncommitted configuration cannot make a candidate
// eligible for Hub readiness.
func (n *Node) IsVoter(nodeID string) bool {
	configuration := n.raft.GetConfiguration()
	if configuration.Error() != nil || configuration.Index() > n.raft.AppliedIndex() {
		return false
	}
	for _, server := range configuration.Configuration().Servers {
		if server.ID == raft.ServerID(nodeID) {
			return server.Suffrage == raft.Voter
		}
	}
	return false
}

// LeaderHubURL returns the public HTTPS endpoint for the current Raft leader.
func (n *Node) LeaderHubURL() (string, string) {
	leaderID := n.Status().LeaderID
	if leaderID == "" {
		return "", ""
	}
	for _, peer := range n.config.Peers {
		if peer.ID == leaderID {
			return peer.HubURL, leaderID
		}
	}
	return "", leaderID
}

// LeaderForwardURL returns the endpoint used by a follower to forward a Hub
// request. A configured private listener avoids routing internal traffic back
// through public ingress; older configurations retain their HTTPS HubURL.
func (n *Node) LeaderForwardURL() (string, string) {
	leaderID := n.Status().LeaderID
	if leaderID == "" {
		return "", ""
	}
	for _, peer := range n.config.Peers {
		if peer.ID == leaderID {
			if peer.InternalHubURL != "" {
				return peer.InternalHubURL, leaderID
			}
			return peer.HubURL, leaderID
		}
	}
	return "", leaderID
}

func (n *Node) TransferLeadershipTo(nodeID string) error {
	if nodeID == "" {
		return errors.New("quorum: leadership transfer target is empty")
	}
	if err := n.verifyLeader(context.Background()); err != nil {
		return err
	}
	for _, peer := range n.config.Peers {
		if peer.ID == nodeID {
			return n.raft.LeadershipTransferToServer(raft.ServerID(peer.ID), raft.ServerAddress(peer.Address)).Error()
		}
	}
	return errors.New("quorum: leadership transfer target is not an enrolled voter")
}

// PromoteVoter admits only a pre-enrolled peer from this node's authenticated
// configuration. HashiCorp Raft first stages the peer and promotes it only
// after it has caught up; the committed Raft configuration is the durable
// promotion receipt and is recovered from the local Raft store on restart.
func (n *Node) PromoteVoter(ctx context.Context, nodeID string) error {
	if strings.TrimSpace(nodeID) == "" {
		return errors.New("quorum: voter promotion target is empty")
	}
	if err := n.verifyLeader(ctx); err != nil {
		return err
	}
	var target Peer
	found := false
	domains := make(map[string]bool, len(n.config.Peers))
	for _, peer := range n.config.Peers {
		if peer.FailureDomain == "" || domains[peer.FailureDomain] {
			return errors.New("quorum: voter promotion requires unique configured failure domains")
		}
		domains[peer.FailureDomain] = true
		if peer.ID == nodeID {
			target, found = peer, true
		}
	}
	if !found {
		return errors.New("quorum: voter promotion target is not pre-enrolled")
	}
	configuration := n.raft.GetConfiguration()
	if err := configuration.Error(); err != nil {
		return fmt.Errorf("quorum: read voter configuration: %w", err)
	}
	if configuration.Index() > n.raft.AppliedIndex() {
		return errors.New("quorum: voter promotion waits for the pending configuration to apply")
	}
	for _, server := range configuration.Configuration().Servers {
		if server.ID == raft.ServerID(target.ID) && server.Suffrage == raft.Voter {
			return nil
		}
	}
	if err := n.verifyLeader(ctx); err != nil {
		return err
	}
	if err := n.raft.AddVoter(raft.ServerID(target.ID), raft.ServerAddress(target.Address),
		configuration.Index(), deadlineFor(ctx, n.config.ApplyDeadline)).Error(); err != nil {
		if n.raft.State() != raft.Leader {
			return fmt.Errorf("%w: stage voter %s: %v", ErrNotLeader, target.ID, err)
		}
		return fmt.Errorf("quorum: voter %s was not committed: %w", target.ID, err)
	}
	return nil
}

// WaitForLeader is useful for startup readiness and integration checks. It does
// not create a leader or weaken the configured voter set.
func (n *Node) WaitForLeader(ctx context.Context) (Status, error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		status := n.Status()
		if status.LeaderID != "" {
			return status, nil
		}
		select {
		case <-ctx.Done():
			return Status{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

// ApplyRecord commits a versioned record to a majority before returning. A
// timeout is ambiguous: callers must reconcile by the same record key/revision,
// never repeat an external side effect because ApplyRecord returned late.
func (n *Node) ApplyRecord(ctx context.Context, namespace, key string, record Record) error {
	return n.ApplyBatch(ctx, []Mutation{{Namespace: namespace, Key: key, Record: record}})
}

// ApplyBatch atomically commits related versioned records in one Raft entry.
// This is required for transactions that update a task and its idempotency or
// executor-control rows together.
func (n *Node) ApplyBatch(ctx context.Context, mutations []Mutation) error {
	if len(mutations) == 0 {
		return errors.New("quorum: mutation batch is empty")
	}
	mutations = append([]Mutation(nil), mutations...)
	hasExternal := false
	seen := make(map[string]bool, len(mutations))
	for i := range mutations {
		mutations[i].Record.Value = append(json.RawMessage(nil), mutations[i].Record.Value...)
		mutations[i].Namespace = strings.TrimSpace(mutations[i].Namespace)
		mutations[i].Key = strings.TrimSpace(mutations[i].Key)
		record := mutations[i].Record
		if mutations[i].Namespace == "" || mutations[i].Key == "" || len(mutations[i].Namespace) > 128 || len(mutations[i].Key) > 512 || record.Revision == 0 {
			return errors.New("quorum: invalid mutation identity or revision")
		}
		identity := mutations[i].Namespace + "\x00" + mutations[i].Key
		if seen[identity] {
			return errors.New("quorum: duplicate mutation key in batch")
		}
		seen[identity] = true
		hasExternal = hasExternal || mutations[i].External
		if record.Deleted && len(record.Value) != 0 {
			return errors.New("quorum: deleted record cannot carry a value")
		}
		if !record.Deleted && (len(record.Value) == 0 || len(record.Value) > maxRecordBytes || !json.Valid(record.Value)) {
			return errors.New("quorum: record value is empty, oversized or invalid JSON")
		}
	}
	if hasExternal && !n.fsm.externalStateReady() {
		return errors.New("quorum: external application state requires a streamed snapshot and restore path")
	}
	if hasExternal {
		n.applyMu.Lock()
		defer n.applyMu.Unlock()
	}
	if err := n.verifyLeader(ctx); err != nil {
		return err
	}
	var externalSequence uint64
	if hasExternal {
		externalSequence = n.fsm.externalIndex() + 1
	}
	for i := range mutations {
		if !mutations[i].External || mutations[i].Record.Deleted {
			continue
		}
		var err error
		mutations[i], err = sealMutation(n.stateAEAD, n.config.ClusterID, mutations[i])
		if err != nil {
			return err
		}
	}
	data, err := json.Marshal(command{ClusterID: n.config.ClusterID, ExternalSequence: externalSequence, Mutations: mutations})
	if err != nil {
		return err
	}
	if len(data) > maxRecordBytes {
		return errors.New("quorum: encoded record exceeds its byte limit")
	}
	deadline := deadlineFor(ctx, n.config.ApplyDeadline)
	future := n.raft.Apply(data, deadline)
	if err := future.Error(); err != nil {
		if n.raft.State() != raft.Leader {
			return fmt.Errorf("%w: %v", ErrNotLeader, err)
		}
		return fmt.Errorf("%w: %v", ErrNoQuorum, err)
	}
	response, ok := future.Response().(applyResult)
	if !ok {
		return errors.New("quorum: unexpected FSM response")
	}
	return response.err
}

// ReadRecord provides a leader-linearized read after a quorum confirmation and
// a barrier over all earlier committed entries.
func (n *Node) ReadRecord(ctx context.Context, namespace, key string) (Record, bool, error) {
	if err := n.verifyLeader(ctx); err != nil {
		return Record{}, false, err
	}
	if err := n.raft.Barrier(deadlineFor(ctx, n.config.BarrierDeadline)).Error(); err != nil {
		if n.raft.State() != raft.Leader {
			return Record{}, false, fmt.Errorf("%w: %v", ErrNotLeader, err)
		}
		return Record{}, false, fmt.Errorf("%w: %v", ErrNoQuorum, err)
	}
	record, ok := n.fsm.get(namespace, key)
	return record, ok, nil
}

// LocalRecord returns the latest record applied by this Raft process without
// claiming linearizability. Use it only for local replica recovery, never to
// authorize a leadership decision or an external response.
func (n *Node) LocalRecord(namespace, key string) (Record, bool) {
	return n.fsm.get(namespace, key)
}

// ExternalApplyError reports a failed local durable-state callback. Such a
// voter must not serve as Hub until the owning application has reconciled its
// replica and explicitly clears the error.
func (n *Node) ExternalApplyError() (uint64, error) {
	return n.fsm.externalApplyError()
}

// ExternalAppliedIndex is the last committed Raft entry whose application
// state was persisted through the external-state callback.
func (n *Node) ExternalAppliedIndex() uint64 {
	return n.fsm.externalIndex()
}

// VerifyLeader checks both local leadership and current majority contact.
func (n *Node) VerifyLeader(ctx context.Context) error {
	return n.verifyLeader(ctx)
}

// SealExternalValue protects sensitive application-owned authority state
// before it is written to the local task database. Raft then seals the row
// again for its replicated log and transport.
func (n *Node) SealExternalValue(namespace, key string, revision uint64, value []byte) ([]byte, error) {
	if namespace == "" || key == "" || revision == 0 || len(value) == 0 || !json.Valid(value) {
		return nil, errors.New("quorum: invalid external authority value")
	}
	sealed, err := sealMutation(n.stateAEAD, n.config.ClusterID, Mutation{
		Namespace: namespace, Key: key, External: true,
		Record: Record{Revision: revision, Value: append(json.RawMessage(nil), value...)},
	})
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), sealed.Record.Value...), nil
}

// OpenExternalValue is the inverse of SealExternalValue for a committed local
// authority row. Callers must still validate the decoded domain object.
func (n *Node) OpenExternalValue(namespace, key string, revision uint64, value []byte) ([]byte, error) {
	if namespace == "" || key == "" || revision == 0 || len(value) == 0 || !json.Valid(value) {
		return nil, errors.New("quorum: invalid sealed external authority value")
	}
	opened, err := openMutation(n.stateAEAD, n.config.ClusterID, Mutation{
		Namespace: namespace, Key: key, External: true,
		Record: Record{Revision: revision, Value: append(json.RawMessage(nil), value...)},
	})
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), opened.Record.Value...), nil
}

// MarkExternalStateReconciled clears the fail-closed application marker only
// after the caller has verified its durable replica through the applied index.
func (n *Node) MarkExternalStateReconciled() {
	n.fsm.clearExternalApplyError()
}

func (n *Node) verifyLeader(ctx context.Context) error {
	if n.raft.State() != raft.Leader {
		return ErrNotLeader
	}
	future := n.raft.VerifyLeader()
	if err := future.Error(); err != nil {
		if n.raft.State() != raft.Leader {
			return fmt.Errorf("%w: %v", ErrNotLeader, err)
		}
		return fmt.Errorf("%w: %v", ErrNoQuorum, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func deadlineFor(ctx context.Context, fallback time.Duration) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining > 0 && remaining < fallback {
			return remaining
		}
	}
	return fallback
}

func (n *Node) Close() error {
	var first error
	if err := n.raft.Shutdown().Error(); err != nil {
		first = err
	}
	if err := n.transport.Close(); err != nil && first == nil {
		first = err
	}
	if err := n.store.Close(); err != nil && first == nil {
		first = err
	}
	n.stateAEAD = nil
	return first
}
