package hub

import (
	"testing"
	"time"
)

func TestFromEnvNodeHeartbeatInterval(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  time.Duration
	}{
		{"", 30 * time.Second},
		{"1", time.Second},
		{"15", 15 * time.Second},
		{"30", 30 * time.Second},
		{"0", 30 * time.Second},
		{"-1", 30 * time.Second},
		{"31", 30 * time.Second},
		{"invalid", 30 * time.Second},
		{"1.5", 30 * time.Second},
		{"9223372036854775807", 30 * time.Second},
		{"9223372036854775808", 30 * time.Second},
	} {
		t.Run(tc.input, func(t *testing.T) {
			t.Setenv("GPTADMIN_NODE_HEARTBEAT_INTERVAL_S", tc.input)
			cfg := FromEnv()
			if cfg.NodeHeartbeatInterval != tc.want {
				t.Fatalf("heartbeat interval = %s, want %s", cfg.NodeHeartbeatInterval, tc.want)
			}
			if got := (&Server{cfg: cfg}).memberHeartbeatInterval(); got != tc.want {
				t.Fatalf("member timer interval = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestNodeMemberHeartbeatIntervalBounds(t *testing.T) {
	for _, tc := range []struct {
		input time.Duration
		want  time.Duration
	}{
		{0, 30 * time.Second},
		{-time.Second, 30 * time.Second},
		{time.Second - time.Nanosecond, 30 * time.Second},
		{time.Second, time.Second},
		{30 * time.Second, 30 * time.Second},
		{30*time.Second + time.Nanosecond, 30 * time.Second},
	} {
		server := &Server{cfg: Config{NodeHeartbeatInterval: tc.input}}
		if got := server.memberHeartbeatInterval(); got != tc.want {
			t.Fatalf("interval %s became %s, want %s", tc.input, got, tc.want)
		}
	}
}

func TestDefaultNodeMembershipTiming(t *testing.T) {
	if nodeHeartbeatInterval != 30*time.Second || nodeLeaseTTL != 90*time.Second ||
		nodeMembershipSyncEvery != 5*time.Minute {
		t.Fatal("production heartbeat, lease or full-sync cadence changed")
	}
	if got := (&Server{}).memberHeartbeatInterval(); got != nodeHeartbeatInterval {
		t.Fatalf("unset member interval = %s, want production default %s", got, nodeHeartbeatInterval)
	}
}
