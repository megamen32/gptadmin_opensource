package hub

import "testing"

func TestFromEnvDefaultsHubToLoopbackAndHonorsHubBind(t *testing.T) {
	for _, key := range []string{"GPTADMIN_HUB_HOST", "HUB_HOST", "HUB_BIND"} {
		t.Setenv(key, "")
	}
	if got := FromEnv().Addr; got != "127.0.0.1:9001" {
		t.Fatalf("unset host binds to %q, want 127.0.0.1:9001", got)
	}

	t.Setenv("HUB_BIND", "192.0.2.10")
	if got := FromEnv().Addr; got != "192.0.2.10:9001" {
		t.Fatalf("HUB_BIND binds to %q, want 192.0.2.10:9001", got)
	}
}
