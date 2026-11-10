package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime/metrics"
	"runtime/pprof"
	"strings"
	"sync"
	"syscall"
	"time"
)

const diagnosticsSocketEnv = "GPTADMIN_NODE_DIAGNOSTICS_SOCKET"
const maxHeapProfileBytes = 16 << 20
const minProfileInterval = 30 * time.Second
const minGoroutineProfileInterval = 2 * time.Second

// startDiagnostics is opt-in. The socket is private to the process owner and
// lives only for the Node lifecycle. No diagnostics are written to logs.
func startDiagnostics() (func() error, error) {
	path := strings.TrimSpace(os.Getenv(diagnosticsSocketEnv))
	if path == "" {
		return func() error { return nil }, nil
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("%s must be a clean absolute path", diagnosticsSocketEnv)
	}
	parent := filepath.Dir(path)
	st, err := os.Lstat(parent)
	if err != nil {
		return nil, fmt.Errorf("diagnostics socket directory: %w", err)
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("diagnostics socket parent must be a real directory")
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || int(sys.Uid) != os.Geteuid() || st.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("diagnostics socket parent must be owned by the Node user and private (mode 0700 or stricter)")
	}
	if err := removeStaleDiagnosticsSocket(path); err != nil {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		os.Remove(path)
		return nil, err
	}
	socketInfo, err := os.Lstat(path)
	if err != nil {
		ln.Close()
		os.Remove(path)
		return nil, err
	}
	srv := &http.Server{Handler: diagnosticsHandler(), ReadHeaderTimeout: 3 * time.Second, WriteTimeout: 20 * time.Second, MaxHeaderBytes: 8 << 10}
	go func() { _ = srv.Serve(ln) }()
	var once sync.Once
	var closeErr error
	closeFn := func() error {
		once.Do(func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			closeErr = srv.Shutdown(shutdownCtx)
			_ = ln.Close()
			if current, e := os.Lstat(path); e == nil && os.SameFile(socketInfo, current) {
				if e = os.Remove(path); closeErr == nil {
					closeErr = e
				}
			}
		})
		return closeErr
	}
	return closeFn, nil
}

func removeStaleDiagnosticsSocket(path string) error {
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if st.Mode()&os.ModeSocket == 0 {
		return errors.New("diagnostics socket path already exists and is not a socket")
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || int(sys.Uid) != os.Geteuid() || st.Mode().Perm() != 0o600 {
		return errors.New("existing diagnostics socket is not owned by the Node user with mode 0600")
	}
	conn, dialErr := net.DialTimeout("unix", path, 200*time.Millisecond)
	if dialErr == nil {
		_ = conn.Close()
		return errors.New("diagnostics socket is already active")
	}
	if !errors.Is(dialErr, syscall.ECONNREFUSED) {
		return fmt.Errorf("cannot verify existing diagnostics socket is stale: %w", dialErr)
	}
	current, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !os.SameFile(st, current) {
		return errors.New("diagnostics socket changed while checking stale path")
	}
	return os.Remove(path)
}

func diagnosticsHandler() http.Handler {
	mux := http.NewServeMux()
	var goroutineMu sync.Mutex
	var lastGoroutine time.Time
	var profileMu sync.Mutex
	var lastProfile time.Time
	mux.HandleFunc("GET /debug/pprof/heap", func(w http.ResponseWriter, r *http.Request) {
		profileMu.Lock()
		defer profileMu.Unlock()
		if !lastProfile.IsZero() && time.Since(lastProfile) < minProfileInterval {
			http.Error(w, "profile rate limited", http.StatusTooManyRequests)
			return
		}
		lastProfile = time.Now()
		var body cappedBuffer
		if err := pprof.Lookup("heap").WriteTo(&body, 0); err != nil && !body.exceeded {
			http.Error(w, "heap profile unavailable", http.StatusInternalServerError)
			return
		}
		if body.exceeded {
			http.Error(w, "heap profile exceeds 16 MiB limit", http.StatusRequestEntityTooLarge)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(body.Bytes())
	})
	mux.HandleFunc("GET /debug/pprof/goroutine", func(w http.ResponseWriter, r *http.Request) {
		goroutineMu.Lock()
		defer goroutineMu.Unlock()
		if !lastGoroutine.IsZero() && time.Since(lastGoroutine) < minGoroutineProfileInterval {
			http.Error(w, "goroutine profile rate limited", http.StatusTooManyRequests)
			return
		}
		lastGoroutine = time.Now()
		var body cappedBuffer
		if err := pprof.Lookup("goroutine").WriteTo(&body, 1); err != nil && !body.exceeded {
			http.Error(w, "goroutine profile unavailable", http.StatusInternalServerError)
			return
		}
		if body.exceeded {
			http.Error(w, "goroutine profile exceeds 16 MiB limit", http.StatusRequestEntityTooLarge)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(body.Bytes())
	})
	mux.HandleFunc("GET /debug/metrics", func(w http.ResponseWriter, _ *http.Request) {
		names := []string{"/gc/cycles/total:gc-cycles", "/gc/heap/allocs:bytes", "/gc/heap/frees:bytes", "/memory/classes/heap/objects:bytes", "/memory/classes/heap/released:bytes", "/memory/classes/total:bytes", "/sched/goroutines:goroutines", "/cpu/classes/gc/total:cpu-seconds"}
		samples := make([]metrics.Sample, len(names))
		for i, name := range names {
			samples[i].Name = name
		}
		metrics.Read(samples)
		result := make(map[string]any, len(samples))
		for _, sample := range samples {
			switch sample.Value.Kind() {
			case metrics.KindUint64:
				result[sample.Name] = sample.Value.Uint64()
			case metrics.KindFloat64:
				result[sample.Name] = sample.Value.Float64()
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(result)
	})
	return mux
}

type cappedBuffer struct {
	bytes.Buffer
	exceeded bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	remaining := maxHeapProfileBytes - b.Len()
	if remaining <= 0 || len(p) > remaining {
		b.exceeded = true
		return 0, errors.New("heap profile size limit exceeded")
	}
	return b.Buffer.Write(p)
}
