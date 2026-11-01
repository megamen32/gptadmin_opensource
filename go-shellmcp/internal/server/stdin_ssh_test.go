package server

import (
	"context"
	"github.com/megamen32/gptadmin/go-shellmcp/internal/shell"
	"github.com/megamen32/gptadmin/go-shellmcp/internal/sshexec"
	"strings"
	"testing"
)

func TestStdinSSHRefusesInsteadOfDiscardingScript(t *testing.T) {
	s := &Server{sshClient: &sshexec.Client{}, cfg: Config{SpillDir: t.TempDir(), ExecTimeout: 3, LogLimit: 8192}}
	request := shell.Request{Cmd: "python3 -", Stdin: "print(\"hello\")\n"}
	result := s.runShell(context.Background(), request)
	if result.ReturnCode != -1 || !strings.Contains(result.Error, "SSH backend") {
		t.Fatalf("stdin silently lost on SSH: %+v", result)
	}
	streamed := s.runShellStream(context.Background(), request, func(shell.Event) {})
	if streamed.ReturnCode != -1 || !strings.Contains(streamed.Error, "SSH backend") {
		t.Fatalf("stdin silently lost on SSH stream: %+v", streamed)
	}
}
