package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sync"
	"time"
)

const DefaultLimitBytes int64 = 8192
const DefaultSpillThresholdBytes int64 = 1024 * 1024
const DefaultTimeout = 300 * time.Second

type Request struct {
	Cmd         string            `json:"cmd"`
	Env         map[string]string `json:"env,omitempty"`
	Cwd         string            `json:"cwd,omitempty"`
	Timeout     int               `json:"timeout,omitempty"`
	SpillDir    string            `json:"spill_dir,omitempty"`
	Background  bool              `json:"background,omitempty"`
	RunAsUser   string            `json:"run_as_user,omitempty"`
	User        string            `json:"user,omitempty"`
	DefaultUser string            `json:"-"`
	DefaultCwd  string            `json:"-"`
}

type Result struct {
	ReturnCode int      `json:"returncode"`
	Stdout     string   `json:"stdout"`
	Stderr     string   `json:"stderr"`
	Error      string   `json:"error,omitempty"`
	TimedOut   bool     `json:"timed_out,omitempty"`
	DurationMS int64    `json:"duration_ms"`
	Cwd        string   `json:"cwd_effective,omitempty"`
	RunAsUser  string   `json:"run_as_user,omitempty"`
	Spilled    bool     `json:"_spilled,omitempty"`
	StdoutPath string   `json:"stdout_path,omitempty"`
	StderrPath string   `json:"stderr_path,omitempty"`
	Files      []string `json:"files,omitempty"`
}

type Event struct {
	Type       string `json:"type"`
	Stream     string `json:"stream,omitempty"`
	Data       string `json:"data,omitempty"`
	ReturnCode int    `json:"returncode,omitempty"`
	Error      string `json:"error,omitempty"`
	TimedOut   bool   `json:"timed_out,omitempty"`
	Seq        int64  `json:"seq,omitempty"`
	Offset     int64  `json:"offset,omitempty"`
}

func Run(ctx context.Context, req Request, limitBytes int64) Result {
	res, _ := runInternal(ctx, req, limitBytes, nil)
	return res
}

func RunLive(ctx context.Context, req Request, limitBytes int64, emit func(Event)) Result {
	res, _ := runInternal(ctx, req, limitBytes, emit)
	return res
}

func runInternal(ctx context.Context, req Request, limitBytes int64, emit func(Event)) (Result, error) {
	started := time.Now()
	if limitBytes <= 0 {
		limitBytes = DefaultLimitBytes
	}
	if req.Cmd == "" {
		return Result{ReturnCode: -1, Error: "empty cmd", DurationMS: time.Since(started).Milliseconds()}, errors.New("empty cmd")
	}
	timeout := DefaultTimeout
	if req.Timeout > 0 {
		timeout = time.Duration(req.Timeout) * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd, runAsUser := buildCommand(ctx, req)
	if req.Cwd != "" {
		cmd.Dir = req.Cwd
	} else if req.DefaultCwd != "" {
		cmd.Dir = req.DefaultCwd
	}
	env := os.Environ()
	for k, v := range req.Env {
		env = append(env, k+"="+v)
	}
	cmd.Env = env
	setProcessGroup(cmd)

	spillDir := req.SpillDir
	if spillDir == "" {
		spillDir = filepath.Join(os.TempDir(), "shellmcp-go-spool")
	}
	spoolID := fmt.Sprintf("%d-%d", time.Now().UnixNano(), os.Getpid())
	stdoutPath := filepath.Join(spillDir, spoolID+".stdout")
	stderrPath := filepath.Join(spillDir, spoolID+".stderr")
	stdout, err := newCapture(limitBytes, stdoutPath, emit, "stdout")
	if err != nil {
		res := Result{ReturnCode: -1, Error: err.Error(), DurationMS: time.Since(started).Milliseconds()}
		return res, err
	}
	defer stdout.Close()
	stderr, err := newCapture(limitBytes, stderrPath, emit, "stderr")
	if err != nil {
		res := Result{ReturnCode: -1, Error: err.Error(), DurationMS: time.Since(started).Milliseconds()}
		return res, err
	}
	defer stderr.Close()

	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		res := Result{ReturnCode: -1, Error: err.Error(), DurationMS: time.Since(started).Milliseconds()}
		return res, err
	}

	waitErr := cmd.Wait()
	if ctx.Err() == context.DeadlineExceeded {
		killProcessGroup(cmd)
	}

	rc := 0
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			rc = exitErr.ExitCode()
		} else {
			rc = -1
		}
	}
	cwd := cmd.Dir
	if cwd == "" {
		cwd, _ = os.Getwd()
	} else if abs, err := filepath.Abs(cwd); err == nil {
		cwd = abs
	}
	res := Result{ReturnCode: rc, Stdout: stdout.Tail(), Stderr: stderr.Tail(), DurationMS: time.Since(started).Milliseconds(), Cwd: cwd, RunAsUser: runAsUser}
	files := make([]string, 0, 2)
	if stdout.Spilled() {
		res.Spilled = true
		res.StdoutPath = stdout.Path()
		files = append(files, stdout.Path())
	}
	if stderr.Spilled() {
		res.Spilled = true
		res.StderrPath = stderr.Path()
		files = append(files, stderr.Path())
	}
	res.Files = files
	if ctx.Err() == context.DeadlineExceeded {
		res.Error = "timeout"
		res.TimedOut = true
		if res.ReturnCode == 0 {
			res.ReturnCode = -1
		}
	} else if waitErr != nil && rc == -1 {
		res.Error = waitErr.Error()
	}
	if emit != nil {
		b, _ := json.Marshal(res)
		emit(Event{Type: "exit", ReturnCode: res.ReturnCode, Error: res.Error, TimedOut: res.TimedOut, Data: string(b)})
	}
	return res, nil
}

type capture struct {
	limit int64
	buf   bytes.Buffer
	path  string
	file  *os.File
	total int64
	seq   int64
	emit  func(Event)
	name  string
	mu    sync.Mutex
}

func newCapture(limit int64, path string, emit func(Event), name string) (*capture, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &capture{limit: limit, path: path, file: f, emit: emit, name: name}, nil
}

func (c *capture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(p)
	_, err := c.file.Write(p)
	c.total += int64(n)
	if c.limit > 0 {
		if int64(len(p)) >= c.limit {
			c.buf.Reset()
			c.buf.Write(p[int64(len(p))-c.limit:])
		} else {
			c.buf.Write(p)
			over := int64(c.buf.Len()) - c.limit
			if over > 0 {
				b := c.buf.Bytes()
				kept := append([]byte(nil), b[over:]...)
				c.buf.Reset()
				c.buf.Write(kept)
			}
		}
	}
	if c.emit != nil && n > 0 {
		c.seq++
		c.emit(Event{Type: "chunk", Stream: c.name, Data: string(p), Seq: c.seq, Offset: c.total})
	}
	return n, err
}

func (c *capture) Tail() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}
func (c *capture) Path() string  { return c.path }
func (c *capture) Spilled() bool { return c.total > c.limit }
func (c *capture) Close() error {
	if c.file != nil {
		return c.file.Close()
	}
	return nil
}

var sudoTokenRE = regexp.MustCompile(`(^|[^A-Za-z0-9_./-])sudo([^A-Za-z0-9_-]|$)`)

func commandMentionsSudo(cmd string) bool {
	return sudoTokenRE.MatchString(cmd)
}

func targetRunUser(req Request) (string, bool) {
	if req.RunAsUser != "" {
		return req.RunAsUser, true
	}
	if req.User != "" {
		return req.User, true
	}
	if req.DefaultUser != "" && !commandMentionsSudo(req.Cmd) {
		return req.DefaultUser, false
	}
	return "", false
}

func buildCommand(ctx context.Context, req Request) (*exec.Cmd, string) {
	user, explicit := targetRunUser(req)
	if runtime.GOOS != "windows" && user != "" && user != "root" && (explicit || os.Geteuid() == 0) {
		return exec.CommandContext(ctx, "sudo", "-H", "-u", user, "--", shellName(), shellArg(), req.Cmd), user
	}
	return exec.CommandContext(ctx, shellName(), shellArg(), req.Cmd), ""
}

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
