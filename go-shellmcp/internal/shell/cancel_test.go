package shell

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestRunContextCancelKillsProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix process-group assertion")
	}
	marker := filepath.Join(t.TempDir(), "survived")
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	started := time.Now()
	res := Run(ctx, Request{Cmd: "(sleep 1; printf survived > " + marker + ") & wait", Timeout: 30}, 1024*1024)
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("cancel took %s", elapsed)
	}
	if res.Error != "cancelled" {
		t.Fatalf("error=%q result=%+v", res.Error, res)
	}
	time.Sleep(1200 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("child survived cancellation; stat err=%v", err)
	}
}

func TestRunReportsOutputByteCounts(t *testing.T) {
	res := Run(context.Background(), Request{Cmd: "printf abc; printf de >&2", Timeout: 5}, 1024)
	if res.StdoutBytes != 3 || res.StderrBytes != 2 {
		t.Fatalf("byte counts stdout=%d stderr=%d result=%+v", res.StdoutBytes, res.StderrBytes, res)
	}
}

func TestRunReportsFullByteCountsWhenSpilled(t *testing.T) {
	res := Run(context.Background(), Request{Cmd: "python3 -c 'print(\"x\"*5000, end=\"\")'", Timeout: 5}, 128)
	if !res.Spilled || res.StdoutBytes != 5000 || len(res.Stdout) > 128 {
		t.Fatalf("spill/count result=%+v", res)
	}
}
