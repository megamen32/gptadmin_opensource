package security

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestReloadPreservesPublishedDeviceIdentity(t *testing.T) {
	root := t.TempDir()
	device, err := LoadIdentity(root, "node")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "shellmcp_identity.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	stored["created_at"] = 42
	raw, err = json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}

	reloaded, err := LoadIdentity(root, "node")
	if err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var after struct {
		CreatedAt int64 `json:"created_at"`
	}
	if err := json.Unmarshal(raw, &after); err != nil {
		t.Fatal(err)
	}
	if reloaded.PublicKey != device.PublicKey || reloaded.ServerID != device.ServerID || after.CreatedAt != 42 {
		t.Fatal("reloading changed the device identity or its creation time")
	}
}
