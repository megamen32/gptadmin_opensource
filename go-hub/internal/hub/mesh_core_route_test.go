//go:build integration

package hub

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// Fast unit: product forwarding must not dispatch after the configured core
// refuses/unavailable. Real transport/product acceptance is separate.
func TestMeshCoreUnavailablePreventsPeerDispatch(t *testing.T) {
	var calls atomic.Int32
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"status":"completed"}`))
	}))
	defer peer.Close()
	t.Setenv("GPTADMIN_CONFIG_DIR", t.TempDir())
	t.Setenv("GPTADMIN_ENV_FILE", filepath.Join(t.TempDir(), "no-runtime.env"))
	t.Setenv("GPTADMIN_MESH_CORE_SOCKET", filepath.Join(t.TempDir(), "missing.sock"))
	t.Setenv("GPTADMIN_MESH_CORE_HOST_ID", "local")
	cfg := FromEnv()
	cfg.ConfigDir = t.TempDir()
	cfg.NodePeersJSON = `{"shell:peer":"` + peer.URL + `"}`
	s := New(cfg)
	defer s.Close()
	r := httptest.NewRequest(http.MethodPost, "/mcp-relay/call", nil)
	r.Header.Set("Authorization", "Bearer unit-only")
	w := httptest.NewRecorder()
	if !s.forwardNodePeerCall(w, r, "shell:peer", []byte(`{"tool":"metadata"}`)) {
		t.Fatal("configured peer was not handled")
	}
	if w.Code != http.StatusServiceUnavailable || calls.Load() != 0 {
		t.Fatalf("core failure dispatched: status=%d calls=%d", w.Code, calls.Load())
	}
}
