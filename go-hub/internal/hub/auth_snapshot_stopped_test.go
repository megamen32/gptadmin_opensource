package hub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestStoppedWriterSnapshotRequiresExclusiveRuntimeLock(t *testing.T) {
	writer := authContinuityNode(t, "writer", "")
	reader := authContinuityNode(t, "reader", "writer-identity")
	token, _, err := writer.issueManagedMCPToken("ordinary", 7, writer.cfg.PublicOrigin, writer.cfg.MCPResource)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "auth.json")
	if err = ExportStoppedWriterAuthSnapshot(writer.cfg.ConfigDir, "writer-identity", out, authSnapshotMaxBudget, "fixture-owner"); err == nil {
		t.Fatal("snapshot succeeded while source writer held runtime lock")
	}
	if _, err = os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("failed export left output")
	}
	generation := writer.authState.Generation
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err = ExportStoppedWriterAuthSnapshot(writer.cfg.ConfigDir, "writer-identity", out, authSnapshotMaxBudget, "fixture-owner"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var bundle authSnapshot
	if err = json.Unmarshal(data, &bundle); err != nil {
		t.Fatal(err)
	}
	if bundle.Generation != generation+1 || bundle.WriterID != "writer-identity" {
		t.Fatalf("wrong snapshot generation or identity: %d", bundle.Generation)
	}
	applied := authContinuityHTTP(reader, "POST", "/admin/api/auth-snapshot/apply", "fixture-owner", data)
	if applied.Code != 200 {
		t.Fatalf("apply: %d %s", applied.Code, applied.Body.String())
	}
	if got := authContinuityHTTP(reader, "GET", "/mcp", token, nil); got.Code == 401 {
		t.Fatal("preserved token rejected")
	}
}

func TestStoppedWriterSnapshotRejectsForeignAndUnsafeTargets(t *testing.T) {
	writer := authContinuityNode(t, "writer", "")
	config := writer.cfg.ConfigDir
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "snapshot.json")
	if err := ExportStoppedWriterAuthSnapshot(config, "foreign", output, authSnapshotMaxBudget, "fixture-owner"); err == nil {
		t.Fatal("foreign writer accepted")
	}
	if err := ExportStoppedWriterAuthSnapshot(config, "writer-identity", filepath.Join(config, "unsafe.json"), authSnapshotMaxBudget, "fixture-owner"); err == nil {
		t.Fatal("output inside writer config accepted")
	}
	if err := os.WriteFile(output, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ExportStoppedWriterAuthSnapshot(config, "writer-identity", output, authSnapshotMaxBudget, "fixture-owner"); err == nil {
		t.Fatal("existing output overwritten")
	}
	content, err := os.ReadFile(output)
	if err != nil || string(content) != "preserve" {
		t.Fatal("existing output changed")
	}
}
