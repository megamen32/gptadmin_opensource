package node

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func diagnosticsTestDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "gptdiag-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestDiagnosticsOptInUnixSocketAndPrivateMode(t *testing.T) {
	t.Setenv(diagnosticsSocketEnv, "")
	if _, err := startDiagnostics(); err != nil {
		t.Fatal(err)
	}
	dir := diagnosticsTestDir(t)
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "node.sock")
	t.Setenv(diagnosticsSocketEnv, socket)
	closeDiagnostics, err := startDiagnostics()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(socket)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("socket mode = %04o, want 0600", got)
	}
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}
	for _, path := range []string{"/debug/metrics", "/debug/pprof/heap"} {
		resp, err := client.Get("http://unix" + path)
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, resp.StatusCode, body)
		}
		if path == "/debug/metrics" {
			var values map[string]any
			if err := json.Unmarshal(body, &values); err != nil {
				t.Fatal(err)
			}
			if _, ok := values["/sched/goroutines:goroutines"]; !ok {
				t.Fatalf("missing goroutine metric: %s", body)
			}
		} else if len(body) == 0 {
			t.Fatal("empty heap profile")
		} else {
			limited, err := client.Get("http://unix/debug/pprof/heap")
			if err != nil {
				t.Fatal(err)
			}
			limited.Body.Close()
			if limited.StatusCode != http.StatusTooManyRequests {
				t.Fatalf("second heap profile status=%d, want 429", limited.StatusCode)
			}
		}
	}
	if err := closeDiagnostics(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(socket); !os.IsNotExist(err) {
		t.Fatalf("socket remains after close: %v", err)
	}
}

func TestDiagnosticsRejectsInsecureParentAndExistingSocket(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(diagnosticsSocketEnv, filepath.Join(dir, "bad.sock"))
	if _, err := startDiagnostics(); err == nil || !strings.Contains(err.Error(), "private") {
		t.Fatalf("insecure parent error = %v", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "exists.sock")
	if err := os.WriteFile(path, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(diagnosticsSocketEnv, path)
	if _, err := startDiagnostics(); err == nil || !strings.Contains(err.Error(), "not a socket") {
		t.Fatalf("existing path error = %v", err)
	}
}

func TestDiagnosticsRemovesOnlyOwnedStaleSocket(t *testing.T) {
	dir := diagnosticsTestDir(t)
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "stale.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	listener.(*net.UnixListener).SetUnlinkOnClose(false)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(diagnosticsSocketEnv, path)
	closeDiagnostics, err := startDiagnostics()
	if err != nil {
		t.Fatal(err)
	}
	if err := closeDiagnostics(); err != nil {
		t.Fatal(err)
	}
}

func TestCappedBufferRejectsOverflow(t *testing.T) {
	var b cappedBuffer
	if _, err := b.Write(make([]byte, maxHeapProfileBytes)); err != nil {
		t.Fatal(err)
	}
	if n, err := b.Write([]byte("x")); err == nil || n != 0 || !b.exceeded {
		t.Fatalf("overflow = n:%d err:%v exceeded:%v", n, err, b.exceeded)
	}
}
