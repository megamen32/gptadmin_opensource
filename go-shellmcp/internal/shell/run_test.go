package shell

import (
	"context"
	"strings"
	"testing"
)

func TestRunSuccess(t *testing.T) {
	res := Run(context.Background(), Request{Cmd: "printf hello"}, 8192)
	if res.ReturnCode != 0 || res.Stdout != "hello" {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestRunExitCodeAndStderr(t *testing.T) {
	res := Run(context.Background(), Request{Cmd: "echo bad >&2; exit 7"}, 8192)
	if res.ReturnCode != 7 {
		t.Fatalf("want rc 7 got %+v", res)
	}
	if !strings.Contains(res.Stderr, "bad") {
		t.Fatalf("stderr missing: %+v", res)
	}
}

func TestRunTimeout(t *testing.T) {
	res := Run(context.Background(), Request{Cmd: "sleep 2", Timeout: 1}, 8192)
	if !res.TimedOut {
		t.Fatalf("want timeout got %+v", res)
	}
}

func TestOutputLimitKeepsTail(t *testing.T) {
	res := Run(context.Background(), Request{Cmd: "printf 123456789"}, 4)
	if res.Stdout != "6789" {
		t.Fatalf("want tail got %q", res.Stdout)
	}
}
