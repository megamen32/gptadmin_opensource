package quorum

import (
	"encoding/json"
	"testing"
	"time"
)

func snapshotPolicyConfig(t *testing.T) Config {
	t.Helper()
	key := "0123456789abcdef0123456789abcdef"
	peers := []Peer{
		{ID: "a", Address: "127.0.0.1:19101", ServerName: "a.test", NodeKeyHash: nodeKeyHash(key)},
		{ID: "b", Address: "127.0.0.1:19102", ServerName: "b.test", NodeKeyHash: nodeKeyHash(key)},
		{ID: "c", Address: "127.0.0.1:19103", ServerName: "c.test", NodeKeyHash: nodeKeyHash(key)},
	}
	return Config{ClusterID: "snapshot-policy", NodeID: "a", BootstrapperID: "a",
		Bootstrap: true, NodeKey: key, BindAddress: peers[0].Address, Advertise: peers[0].Address,
		DataDir: t.TempDir(), TLSCertFile: "cert.pem", TLSKeyFile: "key.pem", Peers: peers}
}

func TestSnapshotPolicyPreservesProductionDefaults(t *testing.T) {
	actual, err := snapshotPolicyConfig(t).normalized()
	if err != nil {
		t.Fatal(err)
	}
	if actual.SnapshotInterval != 30*time.Second {
		t.Fatalf("unexpected default snapshot policy: interval=%v", actual.SnapshotInterval)
	}
}

func TestSnapshotPolicyHonorsBoundedExplicitConfig(t *testing.T) {
	input := snapshotPolicyConfig(t)
	if err := json.Unmarshal([]byte("{\"snapshot_interval\":1000000000}"), &input); err != nil {
		t.Fatal(err)
	}
	actual, err := input.normalized()
	if err != nil {
		t.Fatal(err)
	}
	if actual.SnapshotInterval != time.Second {
		t.Fatalf("explicit snapshot policy was lost: %+v", actual)
	}
}

func TestSnapshotPolicyRejectsUnboundedSettings(t *testing.T) {
	for _, interval := range []time.Duration{-time.Second, time.Millisecond, time.Hour + time.Second} {
		input := snapshotPolicyConfig(t)
		input.SnapshotInterval = interval
		if _, err := input.normalized(); err == nil {
			t.Errorf("accepted snapshot interval %v", interval)
		}
	}
}
