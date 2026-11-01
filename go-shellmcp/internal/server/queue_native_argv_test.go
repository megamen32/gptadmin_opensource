package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/hub"
	"github.com/megamen32/gptadmin/go-shellmcp/internal/shell"
)

func TestShellQueueNativeArgvPassesLiteralArgumentsToProcess(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "injection-must-not-run")
	payload := "single ' double \" $HOME $(touch " + marker + ") Привет"
	job := hub.QueueJob{
		ID: "argv-task", ToolName: "shell_exec", Cmd: "",
		Arguments: map[string]any{"argv": []any{"printf", "%s", payload}, "run_as_user": ""},
		Timeout:   10,
	}
	req := shellRequestFromQueueJob(job, t.TempDir())
	if req.Cmd != "" || len(req.Argv) != 3 || req.Argv[2] != payload {
		t.Fatalf("queue lost native argv: %+v", req)
	}
	res := shell.Run(context.Background(), req, 8192)
	if res.ReturnCode != 0 || res.Stdout != payload {
		t.Fatalf("queue native argv was changed: %+v", res)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("queue argv invoked a shell: %v", err)
	}
}

func TestShellQueueMalformedArgvNeverFallsBackToCmd(t *testing.T) {
	job := hub.QueueJob{Cmd: "printf unsafe", ToolName: "shell_exec", Arguments: map[string]any{"argv": []any{"printf", 5}}}
	req := shellRequestFromQueueJob(job, t.TempDir())
	if req.Argv == nil {
		t.Fatal("invalid argv was silently treated as an old cmd")
	}
	res := shell.Run(context.Background(), req, 8192)
	if res.ReturnCode == 0 || !strings.Contains(res.Error, "cmd or argv") {
		t.Fatalf("invalid queue argv ran legacy cmd: %+v", res)
	}
}
