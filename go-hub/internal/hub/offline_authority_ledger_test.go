package hub

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestAuthorityPreflightReportsPendingControlsWithoutMutation(t *testing.T) {
	s := authContinuityNode(t, "writer", "")
	defer s.Close()
	s.ensureWriterSelfEntry()
	if _, err := writeInstructionContentAtomic(s.cfg.StartupInstructionsFile, "Fixture authority instructions.\n"); err != nil {
		t.Fatal(err)
	}
	inspect := func() map[string]any {
		t.Helper()
		view, err := InspectAuthorityConfiguration(s.cfg, "shell:writer", "writer-identity")
		if err != nil {
			t.Fatal(err)
		}
		return view
	}
	view := inspect()
	if view["pending_task_records"] != 0 || view["pending_task_controls"] != 0 {
		t.Fatalf("empty authority ledger was not reported: tasks=%v controls=%v", view["pending_task_records"], view["pending_task_controls"])
	}
	job := persistedShellTask{ID: "retained-durable-queued", Server: "writer", ReceiptTarget: "shell:writer", OwnerID: "fixture-owner", Status: "queued", ToolName: "shell_exec", CreatedAt: 1}
	if err := s.withTaskDatabase(func(tx *taskTransaction) error {
		if err := tx.save(persistedTaskState{Shell: map[string]persistedShellTask{job.ID: job}}); err != nil {
			return err
		}
		_, err := tx.tx.Exec(`INSERT INTO task_controls(task_id,server,next_attempt) VALUES(?,?,0)`, "retained-control", "writer")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	view = inspect()
	if view["pending_task_records"] != 1 || view["pending_task_controls"] != 1 || view["read_only"] != true {
		t.Fatalf("live authority ledger omitted pending state: tasks=%v controls=%v", view["pending_task_records"], view["pending_task_controls"])
	}
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wire); err != nil || string(wire["pending_task_controls"]) != "1" {
		t.Fatal("canonical helper receipt lost the pending controls field")
	}
	db, err := openTaskReadDatabase(filepath.Join(s.cfg.ConfigDir, taskDatabaseFilename))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var status string
	var controls int
	if err := db.QueryRow(`SELECT json_extract(payload,'$.status') FROM task_records WHERE kind='shell' AND key=?`, job.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM task_controls WHERE task_id='retained-control'`).Scan(&controls); err != nil {
		t.Fatal(err)
	}
	if status != "queued" || controls != 1 {
		t.Fatal("read-only preflight changed the retained queued task or control")
	}
}
