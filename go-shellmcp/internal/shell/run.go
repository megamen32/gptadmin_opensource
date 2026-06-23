package shell

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"
)

const DefaultLimitBytes int64 = 8192
const DefaultTimeout = 300 * time.Second

type Request struct {
	Cmd     string            `json:"cmd"`
	Env     map[string]string `json:"env,omitempty"`
	Cwd     string            `json:"cwd,omitempty"`
	Timeout int               `json:"timeout,omitempty"`
}

type Result struct {
	ReturnCode int    `json:"returncode"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	Error      string `json:"error,omitempty"`
	TimedOut   bool   `json:"timed_out,omitempty"`
	DurationMS int64  `json:"duration_ms"`
	Cwd        string `json:"cwd_effective,omitempty"`
}

func Run(ctx context.Context, req Request, limitBytes int64) Result {
	started := time.Now()
	if limitBytes <= 0 {
		limitBytes = DefaultLimitBytes
	}
	if req.Cmd == "" {
		return Result{ReturnCode: -1, Error: "empty cmd", DurationMS: time.Since(started).Milliseconds()}
	}
	timeout := DefaultTimeout
	if req.Timeout > 0 {
		timeout = time.Duration(req.Timeout) * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, shellName(), shellArg(), req.Cmd)
	if req.Cwd != "" {
		cmd.Dir = req.Cwd
	}
	env := os.Environ()
	for k, v := range req.Env {
		env = append(env, k+"="+v)
	}
	cmd.Env = env
	setProcessGroup(cmd)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return Result{ReturnCode: -1, Error: err.Error(), DurationMS: time.Since(started).Milliseconds()}
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return Result{ReturnCode: -1, Error: err.Error(), DurationMS: time.Since(started).Milliseconds()}
	}

	var stdout, stderr tailBuffer
	stdout.limit = limitBytes
	stderr.limit = limitBytes
	if err := cmd.Start(); err != nil {
		return Result{ReturnCode: -1, Error: err.Error(), DurationMS: time.Since(started).Milliseconds()}
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(&stdout, stdoutPipe) }()
	go func() { defer wg.Done(); _, _ = io.Copy(&stderr, stderrPipe) }()

	waitErr := cmd.Wait()
	if ctx.Err() == context.DeadlineExceeded {
		killProcessGroup(cmd)
	}
	wg.Wait()

	rc := 0
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			rc = exitErr.ExitCode()
		} else {
			rc = -1
		}
	}
	cwd := req.Cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	} else if abs, err := filepath.Abs(cwd); err == nil {
		cwd = abs
	}
	res := Result{ReturnCode: rc, Stdout: stdout.String(), Stderr: stderr.String(), DurationMS: time.Since(started).Milliseconds(), Cwd: cwd}
	if ctx.Err() == context.DeadlineExceeded {
		res.Error = "timeout"
		res.TimedOut = true
		if res.ReturnCode == 0 {
			res.ReturnCode = -1
		}
	} else if waitErr != nil && rc == -1 {
		res.Error = waitErr.Error()
	}
	return res
}

type tailBuffer struct {
	limit int64
	buf   bytes.Buffer
}

func (r *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if r.limit <= 0 {
		return n, nil
	}
	if int64(len(p)) >= r.limit {
		r.buf.Reset()
		r.buf.Write(p[int64(len(p))-r.limit:])
		return n, nil
	}
	r.buf.Write(p)
	over := int64(r.buf.Len()) - r.limit
	if over > 0 {
		b := r.buf.Bytes()
		kept := append([]byte(nil), b[over:]...)
		r.buf.Reset()
		r.buf.Write(kept)
	}
	return n, nil
}

func (r *tailBuffer) String() string { return r.buf.String() }

func shellName() string {
	if runtime.GOOS == "windows" {
		return "cmd"
	}
	return "/bin/bash"
}
func shellArg() string {
	if runtime.GOOS == "windows" {
		return "/C"
	}
	return "-c"
}

func setProcessGroup(cmd *exec.Cmd) {
	if runtime.GOOS != "windows" {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
}
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	if runtime.GOOS != "windows" {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	} else {
		_ = cmd.Process.Kill()
	}
}
