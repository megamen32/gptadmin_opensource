//go:build linux

package node

import "testing"

func TestNativeLifecycleReconcileOnlySystemNodeCgroup(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  bool
	}{
		{"0::/system.slice/gptadmin-hub.service", true},
		{"0::/system.slice/gptadmin-node@server-44.service", true},
		{"0::/system.slice/gptadmin-node@roomhacker-server-88.service", true},
		{`0::/system.slice/system-gptadmin\x2dnode.slice/gptadmin-node@server-44.service`, true},
		{"0::/user.slice/user@1000.service/gptadmin-node@dev.service", false},
		{"0::/system.slice/gptadmin-maintenance.service", false},
		{"0::/system.slice/gptadmin-node@server-44.service/child", false},
		{"0::/system.slice/other.service/gptadmin-hub.service", false},
		{"0::/docker/test", false},
		{"", false},
	} {
		if got := isNativeNodeService(tc.input); got != tc.want {
			t.Errorf("%q: got %v want %v", tc.input, got, tc.want)
		}
	}
}
