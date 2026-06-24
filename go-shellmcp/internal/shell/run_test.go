package shell

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestRunSuccess(t *testing.T) {
	res := Run(context.Background(), Request{Cmd: "printf hello", SpillDir: t.TempDir()}, 8192)
	if res.ReturnCode != 0 || res.Stdout != "hello" {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestRunExitCodeAndStderr(t *testing.T) {
	res := Run(context.Background(), Request{Cmd: "echo bad >&2; exit 7", SpillDir: t.TempDir()}, 8192)
	if res.ReturnCode != 7 {
		t.Fatalf("want rc 7 got %+v", res)
	}
	if !strings.Contains(res.Stderr, "bad") {
		t.Fatalf("stderr missing: %+v", res)
	}
}

func TestRunTimeout(t *testing.T) {
	res := Run(context.Background(), Request{Cmd: "sleep 2", Timeout: 1, SpillDir: t.TempDir()}, 8192)
	if !res.TimedOut {
		t.Fatalf("want timeout got %+v", res)
	}
}

func TestOutputLimitKeepsTailAndSpills(t *testing.T) {
	dir := t.TempDir()
	res := Run(context.Background(), Request{Cmd: "printf 123456789", SpillDir: dir}, 4)
	if res.Stdout != "6789" {
		t.Fatalf("want tail got %q", res.Stdout)
	}
	if !res.Spilled || res.StdoutPath == "" {
		t.Fatalf("want spill got %+v", res)
	}
	b, err := os.ReadFile(res.StdoutPath)
	if err != nil || string(b) != "123456789" {
		t.Fatalf("bad spill file err=%v body=%q", err, string(b))
	}
}

func TestRunLiveEvents(t *testing.T) {
	var events []Event
	res := RunLive(context.Background(), Request{Cmd: "echo out; echo err >&2", SpillDir: t.TempDir()}, 8192, func(e Event) { events = append(events, e) })
	if res.ReturnCode != 0 {
		t.Fatalf("bad res %+v", res)
	}
	seenOut, seenErr, seenExit := false, false, false
	for _, e := range events {
		if e.Type == "chunk" && e.Stream == "stdout" && strings.Contains(e.Data, "out") {
			seenOut = true
		}
		if e.Type == "chunk" && e.Stream == "stderr" && strings.Contains(e.Data, "err") {
			seenErr = true
		}
		if e.Type == "exit" {
			seenExit = true
		}
	}
	if !seenOut || !seenErr || !seenExit {
		t.Fatalf("missing events: %#v", events)
	}
}

func TestDefaultUserSelection(t *testing.T) {
	if got, explicit := targetRunUser(Request{Cmd: "id", DefaultUser: "admin"}); got != "admin" || explicit {
		t.Fatalf("default user not selected: got=%q explicit=%v", got, explicit)
	}
	if got, _ := targetRunUser(Request{Cmd: "sudo id", DefaultUser: "admin"}); got != "" {
		t.Fatalf("sudo command should not use default user, got %q", got)
	}
	if got, explicit := targetRunUser(Request{Cmd: "id", RunAsUser: "root", DefaultUser: "admin"}); got != "root" || !explicit {
		t.Fatalf("explicit user not selected: got=%q explicit=%v", got, explicit)
	}
}
