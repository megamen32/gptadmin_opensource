// Package supervisor is the in-process lifecycle manager for MCP agent child
// processes tracked by go-shellmcp. It is the Go port of the Python shellmcp
// MCP supervisor (see client/shellmcp.py _mcp_supervisor_state / action).
//
// The supervisor owns:
//   - the agent registry (loaded from a JSON config file or directory),
//   - per-agent child processes (start / stop / restart / status),
//   - best-effort host-service-manager file writers (systemd unit, launchd
//     plist); enabling the unit is the caller's job and is intentionally not
//     performed here to keep tests side-effect free.
//
// Concurrency: Manager uses a single mutex around the process map. Status
// queries, Start/Stop/Restart, and the Wait/Reaper goroutine all touch
// process state under the mutex. The reaper updates ExitedAt/ExitCode without
// holding the mutex for I/O.
package supervisor

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Agent is one MCP agent definition tracked by the supervisor. It mirrors the
// shape loaded from the JSON config and exposed by Python shellmcp via the
// /capabilities/mcp/<ref> endpoint.
type Agent struct {
	// Ref is the stable identifier used by clients ("alpha", "github-mcp",
	// etc.). It is the primary key in Manager's process table.
	Ref string `json:"ref"`
	// Name is the human-readable label (may equal Ref).
	Name string `json:"name"`
	// Command is the executable to launch (absolute path or PATH lookup).
	Command string `json:"command"`
	// Args is passed to Command.Args; rendered into systemd / launchd unit
	// files when InstallSystemdUnit or InstallLaunchdPlist is called.
	Args []string `json:"args,omitempty"`
	// Env is merged with os.Environ when the child is started.
	Env map[string]string `json:"env,omitempty"`
	// Cwd, if non-empty, sets the child's working directory.
	Cwd string `json:"cwd,omitempty"`
	// User is forwarded to systemd / launchd when present; the in-process
	// manager ignores it because we run as the caller's uid.
	User string `json:"user,omitempty"`
	// Enabled corresponds to spec.get("enabled", True) in Python; kept here
	// so callers can introspect the registry verbatim.
	Enabled bool `json:"enabled"`
}

// AgentStatus is the snapshot returned by Manager.Status. ExitedAt is the
// zero time when the process is still running; ExitCode has its zero value
// until the reaper has observed the actual exit code.
type AgentStatus struct {
	Ref       string    `json:"ref"`
	Running   bool      `json:"running"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at,omitempty"`
	ExitedAt  time.Time `json:"exited_at,omitempty"`
	ExitCode  int       `json:"exit_code"`
	// ExitErr holds any error returned by cmd.Wait (signal-killed processes
	// translate to non-nil ExitErr under os/exec). Exposed for diagnostic use;
	// it is intentionally not serialized as part of the public JSON shape.
	ExitErr error `json:"-"`
}

// Manager is the goroutine-safe MCP agent registry + child-process table.
//
// The zero value is not usable; construct with New.
type Manager struct {
	mu       sync.Mutex
	agents   map[string]Agent
	process  map[string]*tracked
}

// tracked is the per-agent runtime record. process is nil while the agent is
// stopped.
type tracked struct {
	cmd       *exec.Cmd
	pid       int
	startedAt time.Time
	exitedAt  time.Time
	exitCode  int
	exitErr   error
	done      chan struct{} // closed by the reaper when Wait returns
}

// ErrUnknownRef is returned by Start/Stop/Restart/Status for a ref not present
// in the agent registry. Callers (e.g. an HTTP handler) can map this to a 404.
var ErrUnknownRef = errors.New("supervisor: unknown agent ref")

// New returns a Manager pre-loaded with the supplied agents. Stored agents
// are copied so caller-side mutation does not affect the registry.
func New(agents []Agent) *Manager {
	m := &Manager{
		agents:  make(map[string]Agent, len(agents)),
		process: make(map[string]*tracked, len(agents)),
	}
	for _, a := range agents {
		if a.Ref == "" {
			continue
		}
		// Defensive deep copy of the slice/map so later mutations from the
		// caller do not bleed into Manager state.
		cp := a
		cp.Args = append([]string(nil), a.Args...)
		if a.Env != nil {
			cp.Env = make(map[string]string, len(a.Env))
			for k, v := range a.Env {
				cp.Env[k] = v
			}
		}
		m.agents[cp.Ref] = cp
	}
	return m
}

// Agents returns a snapshot copy of the registered agent definitions. Safe to
// read from multiple goroutines.
func (m *Manager) Agents() []Agent {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Agent, 0, len(m.agents))
	for _, a := range m.agents {
		// Defensive copy of slices/maps on the way out.
		cp := a
		cp.Args = append([]string(nil), a.Args...)
		if a.Env != nil {
			cp.Env = make(map[string]string, len(a.Env))
			for k, v := range a.Env {
				cp.Env[k] = v
			}
		}
		out = append(out, cp)
	}
	return out
}

// Start launches the agent's command as a child of the supervisor and records
// its PID + start time. If the agent is already running, Start is a no-op and
// returns nil. ErrUnknownRef is returned for unknown refs.
func (m *Manager) Start(ref string) error {
	m.mu.Lock()
	a, ok := m.agents[ref]
	if !ok {
		m.mu.Unlock()
		return ErrUnknownRef
	}
	t, exists := m.process[ref]
	if exists && t != nil && t.cmd != nil && t.cmd.Process != nil {
		// Already running; nothing to do.
		m.mu.Unlock()
		return nil
	}
	// We release the mutex before launching the child so concurrent Stop
	// calls on other refs do not block on the OS fork. The reaper goroutine
	// itself only touches t under the mutex (closing the done chan is safe
	// without the lock because closing is idempotent).
	m.mu.Unlock()

	cmd := exec.Command(a.Command, a.Args...)
	if a.Cwd != "" {
		cmd.Dir = a.Cwd
	}
	if len(a.Env) > 0 {
		env := make([]string, 0, len(a.Env))
		for k, v := range a.Env {
			env = append(env, fmt.Sprintf("%s=%s", k, v))
		}
		cmd.Env = append(os.Environ(), env...)
	}
	// We deliberately don't set SysProcAttr here: stdlib-only with no platform
	// fork, and an in-process supervisor is fine to kill just the child PID.
	// If we ever need proper detachment we can add a small per-OS file with a
	// build tag — but tests in -race mode must not require it.

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("supervisor: start %q: %w", ref, err)
	}

	now := time.Now()
	tr := &tracked{
		cmd:       cmd,
		pid:       cmd.Process.Pid,
		startedAt: now,
		done:      make(chan struct{}),
	}

	m.mu.Lock()
	// Re-check: a concurrent Stop() could have raced us; if so, kill the
	// child we just spawned and report ErrUnknownRef-or-Stop-success.
	if existing, ok := m.process[ref]; ok && existing != nil && existing.cmd != nil && existing.cmd.Process != nil {
		m.mu.Unlock()
		_ = killProcessTree(cmd.Process)
		return nil
	}
	m.process[ref] = tr
	m.mu.Unlock()

	// Reaper: Wait() blocks until the child exits, then stores exit info and
	// clears m.process[ref] so a future Start() succeeds.
	go m.reap(ref, tr)
	return nil
}

// reaper per agent. Acquires the mutex briefly when updating tracked state;
// runs Wait outside the lock.
func (m *Manager) reap(ref string, tr *tracked) {
	waitErr := tr.cmd.Wait()

	// Update tracked state under the mutex BEFORE closing done so that any
	// goroutine waiting on done can immediately read coherent exitedAt /
	// exitCode via Status. Closing done is a happens-before edge that
	// guarantees the writes above are visible to the waiter.
	//
	// We keep the entry in m.process[ref] (instead of deleting it) so that
	// subsequent Status() calls can still report historical ExitedAt and
	// ExitCode. Start() handles the "was here, now exited" case by checking
	// whether cmd.Process is non-nil — if it is nil after Wait returns we
	// have to clear cmd so Start() can replace it.
	m.mu.Lock()
	tr.exitedAt = time.Now()
	if waitErr != nil {
		tr.exitErr = waitErr
	}
	if exitErr, ok := waitErr.(*exec.ExitError); ok {
		tr.exitCode = exitErr.ExitCode()
	} else if waitErr == nil {
		tr.exitCode = 0
	}
	// If a Start->Stop->Start cycle replaced us, leave the new entry alone.
	if cur, ok := m.process[ref]; ok && cur == tr {
		// Nullify cmd so a subsequent Start() sees "stopped" state and
		// can spawn a fresh child, while keeping exitedAt/exitCode for
		// historical Status reads.
		tr.cmd = nil
		tr.pid = 0
	}
	m.mu.Unlock()
	close(tr.done)
}

// Stop terminates the agent's child process. If no process is recorded, Stop
// returns ErrUnknownRef only when the ref itself is unknown; for known refs
// the operation is treated as a no-op success (Stop on a stopped agent
// succeeds silently, matching Python idempotency).
func (m *Manager) Stop(ref string) error {
	m.mu.Lock()
	a, ok := m.agents[ref]
	if !ok {
		m.mu.Unlock()
		return ErrUnknownRef
	}
	t, ok := m.process[ref]
	if !ok || t == nil || t.cmd == nil || t.cmd.Process == nil {
		m.mu.Unlock()
		return nil
	}
	proc := t.cmd.Process
	done := t.done
	m.mu.Unlock()

	if err := terminateProcessTree(proc); err != nil && !isProcessAlreadyGone(err) {
		return fmt.Errorf("supervisor: stop %q: %w", ref, err)
	}

	// Wait for the reaper to publish the exit (max 10s). We deliberately
	// avoid a context here to keep this stdlib-only package.
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		return fmt.Errorf("supervisor: stop %q timed out", ref)
	}
	_ = a // keep linter quiet; a is used implicitly by ref lookup
	return nil
}

// Restart is Stop followed by Start. Returns the first non-nil error.
func (m *Manager) Restart(ref string) error {
	if err := m.Stop(ref); err != nil {
		return err
	}
	return m.Start(ref)
}

// Status returns the AgentStatus snapshot for ref. ErrUnknownRef when the ref
// is not registered.
func (m *Manager) Status(ref string) (AgentStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.agents[ref]; !ok {
		return AgentStatus{}, ErrUnknownRef
	}
	t, ok := m.process[ref]
	if !ok || t == nil {
		return AgentStatus{Ref: ref, Running: false}, nil
	}
	st := AgentStatus{
		Ref:       ref,
		Running:   t.cmd != nil && t.cmd.Process != nil,
		PID:       t.pid,
		StartedAt: t.startedAt,
		ExitedAt:  t.exitedAt,
		ExitCode:  t.exitCode,
		ExitErr:   t.exitErr,
	}
	// If the process already exited but the reaper hasn't cleared the entry
	// yet, prefer Reporting running=false so callers see a coherent view.
	if !t.exitedAt.IsZero() {
		st.Running = false
	}
	return st, nil
}

// KillAll terminates every tracked child process. Used in test cleanup so
// stray sleeps don't linger after the binary exits.
func (m *Manager) KillAll() error {
	m.mu.Lock()
	procs := make([]struct {
		ref  string
		t    *tracked
	}, 0, len(m.process))
	for ref, t := range m.process {
		if t != nil && t.cmd != nil && t.cmd.Process != nil {
			procs = append(procs, struct {
				ref  string
				t    *tracked
			}{ref: ref, t: t})
		}
	}
	m.mu.Unlock()

	var firstErr error
	for _, p := range procs {
		if err := terminateProcessTree(p.t.cmd.Process); err != nil && !isProcessAlreadyGone(err) {
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// LoadAgents reads an MCP agent config from configPath.
//
// Supported schemas (auto-detected):
//
//  1. JSON array:  [{"ref":"a",...}, {"ref":"b",...}]
//  2. Object:      {"agents":[{"ref":"a",...}]}
//  3. Object:      {"mcpServers": {"<name>": {"ref|name": "...", "command": ..., "args": [...], "env": {...}}}}
//
// configPath == "" → (nil, nil). Missing file → ([], nil). Empty file →
// ([], nil). Malformed JSON → ([], error).
func LoadAgents(configPath string) ([]Agent, error) {
	if configPath == "" {
		return nil, nil
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("supervisor: read %s: %w", configPath, err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, nil
	}

	// Try the array shape first; if it doesn't parse, fall through to the
	// object shape. We do not return early on json.Unmarshal errors so the
	// object form can still be detected.
	if arr, ok := tryDecodeArray(data); ok {
		return normalizeAgents(arr), nil
	}
	if obj, ok := tryDecodeObject(data); ok {
		return normalizeAgents(obj), nil
	}
	if mcp, ok := tryDecodeMCPServers(data); ok {
		return normalizeAgents(mcp), nil
	}
	return nil, fmt.Errorf("supervisor: %s: unrecognized MCP agent schema", configPath)
}

func tryDecodeArray(data []byte) ([]Agent, bool) {
	var arr []Agent
	if err := json.Unmarshal(data, &arr); err != nil {
		return nil, false
	}
	return arr, true
}

func tryDecodeObject(data []byte) ([]Agent, bool) {
	var obj struct {
		Agents []Agent `json:"agents"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, false
	}
	if len(obj.Agents) == 0 {
		return nil, false
	}
	return obj.Agents, true
}

// tryDecodeMCPServers decodes the legacy mcpServers map-of-name-to-spec shape
// used by Python shellmcp. The Map key becomes Agent.Name, the value's
// `agent_id` (or `ref` or `name`) becomes Agent.Ref.
func tryDecodeMCPServers(data []byte) ([]Agent, bool) {
	var obj struct {
		MCPServers map[string]struct {
			Ref      string            `json:"ref"`
			AgentID  string            `json:"agent_id"`
			Name     string            `json:"name"`
			Command  string            `json:"command"`
			Args     []string          `json:"args"`
			Env      map[string]string `json:"env"`
			Cwd      string            `json:"cwd"`
			User     string            `json:"user"`
			Enabled  bool              `json:"enabled"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, false
	}
	if len(obj.MCPServers) == 0 {
		return nil, false
	}
	keys := make([]string, 0, len(obj.MCPServers))
	for k := range obj.MCPServers {
		keys = append(keys, k)
	}
	// Stable ordering via sort later if needed.
	out := make([]Agent, 0, len(keys))
	// Use sorted keys for deterministic output.
	if !sortStrings(keys) {
		// best-effort; no sort possible means we still iterate in some order
		for k, v := range obj.MCPServers {
			out = append(out, Agent{
				Ref:     firstNonEmpty(v.AgentID, v.Ref, k),
				Name:    firstNonEmpty(v.Name, k),
				Command: v.Command,
				Args:    v.Args,
				Env:     v.Env,
				Cwd:     v.Cwd,
				User:    v.User,
				Enabled: v.Enabled,
			})
		}
		return out, true
	}
	for _, k := range keys {
		v := obj.MCPServers[k]
		out = append(out, Agent{
			Ref:     firstNonEmpty(v.AgentID, v.Ref, k),
			Name:    firstNonEmpty(v.Name, k),
			Command: v.Command,
			Args:    v.Args,
			Env:     v.Env,
			Cwd:     v.Cwd,
			User:    v.User,
			Enabled: v.Enabled,
		})
	}
	return out, true
}

func normalizeAgents(in []Agent) []Agent {
	out := make([]Agent, 0, len(in))
	for _, a := range in {
		if a.Ref == "" {
			continue
		}
		out = append(out, a)
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// sortStrings sorts a small slice of strings in place and returns true.
// Implemented here to avoid pulling in the "sort" package for one call site;
// on second thought, sort is stdlib and clearly fine — but this inline keeps
// the file self-contained as a fallback. Always returns true (path used only
// when callers want a deterministic order; we always want it).
func sortStrings(s []string) bool {
	// Bubble sort — n is small (a handful of MCP server names) and this
	// avoids adding an import dependency.
	for i := 0; i < len(s); i++ {
		for j := i + 1; j < len(s); j++ {
			if s[j] < s[i] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
	return true
}

// InstallSystemdUnit writes a minimal systemd unit file describing how to run
// the agent. This is best-effort: it writes the file only and does NOT invoke
// systemctl / enable it (caller's job, intentionally so tests stay side-effect
// free).
//
// The unit is suitable for `systemctl daemon-reload && systemctl enable --now
// <unit>` after review. Description and service name derive from agent.Ref.
func (m *Manager) InstallSystemdUnit(agent Agent, unitPath string) error {
	if agent.Ref == "" {
		return errors.New("supervisor: agent.Ref required for systemd unit")
	}
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		return err
	}
	// Build ExecStart. We escape any embedded spaces/quotes in args by
	// wrapping each arg in double quotes and backslash-escaping interior
	// quotes/backslashes. This is the conventional systemd ExecStart form.
	parts := make([]string, 0, 1+len(agent.Args))
	parts = append(parts, systemdQuote(agent.Command))
	for _, a := range agent.Args {
		parts = append(parts, systemdQuote(a))
	}
	execStart := strings.Join(parts, " ")
	body := fmt.Sprintf(`[Unit]
Description=shellmcp MCP agent %s (ref %s)
After=network.target

[Service]
Type=simple
ExecStart=%s
Restart=on-failure
RestartSec=2
`, agent.Name, agent.Ref, execStart)
	if agent.Cwd != "" {
		body += fmt.Sprintf("WorkingDirectory=%s\n", agent.Cwd)
	}
	if agent.User != "" {
		body += fmt.Sprintf("User=%s\n", agent.User)
	}
	if len(agent.Env) > 0 {
		for k, v := range agent.Env {
			body += fmt.Sprintf("Environment=%s=%s\n", k, v)
		}
	}
	return os.WriteFile(unitPath, []byte(body), 0o644)
}

// InstallLaunchdPlist writes a minimal launchd plist for the agent. Does not
// call launchctl; the caller is expected to `launchctl load` after review.
func (m *Manager) InstallLaunchdPlist(agent Agent, plistPath string) error {
	if agent.Ref == "" {
		return errors.New("supervisor: agent.Ref required for launchd plist")
	}
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return err
	}
	label := fmt.Sprintf("com.shellmcp.%s", agent.Ref)
	argsXml := "<string>" + agent.Command + "</string>"
	for _, a := range agent.Args {
		argsXml += "\n  <string>" + escapeXML(a) + "</string>"
	}
	envXml := ""
	for k, v := range agent.Env {
		envXml += fmt.Sprintf("\n  <key>%s</key>\n  <string>%s</string>", escapeXML(k), escapeXML(v))
	}
	body := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>%s</string>
  <key>ProgramArguments</key>
  <array>
  %s
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>%s
</dict>
</plist>
`, label, argsXml, envXml)
	return os.WriteFile(plistPath, []byte(body), 0o644)
}

// systemdQuote wraps a single argument for ExecStart, escaping embedded
// backslashes and double quotes. Args without whitespace or quotes are
// returned verbatim to keep the unit file readable. Suitable for
// /etc/systemd/system/*.service ExecStart lines.
func systemdQuote(s string) string {
	if !strings.ContainsAny(s, " \t\"'\\$`") {
		return s
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}

func escapeXML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// isProcessAlreadyGone suppresses ESRCH-equivalent errors so a benign race
// (process already exited) does not fail the test.
func isProcessAlreadyGone(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrProcessDone) {
		return true
	}
	if errors.Is(err, syscall.ESRCH) {
		return true
	}
	// Windows ERROR_INVALID_PARAMETER / ERROR_ACCESS_DENIED on a dead handle.
	msg := err.Error()
	if strings.Contains(msg, "exit status") {
		return false
	}
	if strings.Contains(msg, "process already finished") {
		return true
	}
	return false
}

// killProcessTree asks the OS to terminate the supplied process. Falls back to
// Kill on every platform (cross-platform code only). Kept as a tiny wrapper so
// future platforms can swap in tree-aware termination without churning call
// sites.
func killProcessTree(p *os.Process) error {
	return p.Kill()
}

// terminateProcessTree signals the process politely first (SIGTERM on POSIX,
// interrupt-style hint on Windows) and then escalates to Kill after a short
// grace period. Returns immediately if the process is already gone.
func terminateProcessTree(p *os.Process) error {
	if p == nil {
		return nil
	}
	// Best-effort polite termination. On Windows there is no SIGTERM, so
	// Kill() itself is the polite form.
	sig := os.Interrupt
	if runtime.GOOS != "windows" {
		sig = syscall.SIGTERM
	}
	if err := p.Signal(sig); err != nil && !isProcessAlreadyGone(err) {
		// Fall through to Kill.
	} else if err == nil {
		// Give the process a brief window to exit cleanly.
		done := make(chan struct{})
		go func() {
			_, _ = p.Wait()
			close(done)
		}()
		select {
		case <-done:
			return nil
		case <-time.After(500 * time.Millisecond):
		}
	}
	return killProcessTree(p)
}
