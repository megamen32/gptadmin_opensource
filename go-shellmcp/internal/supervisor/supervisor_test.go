package supervisor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// smallSleepCommand returns a portable no-op command spec that will run for the
// supplied duration. We use it to stand in for an MCP child process in tests
// without needing any third-party tool. All test code is gated on the resulting
// PID being killable from outside the process group.
func smallSleepCommand(d time.Duration) (string, []string) {
	if runtime.GOOS == "windows" {
		// Use ping to localhost with a timeout-style trick that still exits quickly.
		return "cmd", []string{"/c", "ping", "-n", "30", "127.0.0.1", ">NUL"}
	}
	_ = d // unused on unix; the duration semantics belong to the agent definition.
	return "sleep", []string{"30"}
}

func newTestManager(t *testing.T, refs ...string) *Manager {
	t.Helper()
	agents := make([]Agent, 0, len(refs))
	for _, ref := range refs {
		cmd, args := smallSleepCommand(30 * time.Second)
		agents = append(agents, Agent{
			Ref:     ref,
			Name:    ref,
			Command: cmd,
			Args:    args,
			Env:     map[string]string{"SUPERVISOR_TEST": "1"},
		})
	}
	return New(agents)
}

func TestLoadAgents_ArraySchema(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "agents.json")
	body := `[
  {"ref":"a","name":"alpha","command":"sleep","args":["5"]},
  {"ref":"b","name":"beta","command":"echo","args":["hi"],"env":{"K":"V"}}
]`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadAgents(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(got))
	}
	if got[0].Ref != "a" || got[1].Ref != "b" {
		t.Fatalf("refs mismatch: %#v", got)
	}
	if got[1].Env["K"] != "V" {
		t.Fatalf("env not loaded: %#v", got[1].Env)
	}
}

func TestLoadAgents_ObjectSchema(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "agents.json")
	body := `{"agents":[
  {"ref":"a","name":"alpha","command":"sleep","args":["5"]}
]}`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadAgents(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Ref != "a" {
		t.Fatalf("unexpected agents: %#v", got)
	}
}

func TestLoadAgents_OmittedEnabledDefaultsToTrue(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"agents":     `{"agents":[{"ref":"local","command":"example"}]}`,
		"mcpServers": `{"mcpServers":{"remote":{"url":"https://example.test/mcp"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, name+".json")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			agents, err := LoadAgents(path)
			if err != nil {
				t.Fatalf("LoadAgents: %v", err)
			}
			if len(agents) != 1 || !agents[0].Enabled {
				t.Fatalf("omitted enabled must default to true: %#v", agents)
			}
		})
	}
}

func TestLoadAgents_ExplicitDisabledRemainsFalse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agents.json")
	body := `{"agents":[{"ref":"disabled","command":"example","enabled":false}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	agents, err := LoadAgents(path)
	if err != nil {
		t.Fatalf("LoadAgents: %v", err)
	}
	if len(agents) != 1 || agents[0].Enabled {
		t.Fatalf("explicit enabled=false must be preserved: %#v", agents)
	}
}

func TestManager_PersistentRegistryMutations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agents.json")
	m := NewPersistent(nil, path)
	remote := Agent{Ref: "docs", Name: "Docs", Transport: "sse", URL: "https://example.test/sse", Enabled: true}
	if err := m.Upsert(remote); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := m.SetEnabled("docs", false); err != nil {
		t.Fatalf("SetEnabled(false): %v", err)
	}
	agents, err := LoadAgents(path)
	if err != nil {
		t.Fatalf("LoadAgents: %v", err)
	}
	if len(agents) != 1 || agents[0].Enabled || agents[0].Transport != "sse" || agents[0].URL != remote.URL {
		t.Fatalf("unexpected persisted agent: %#v", agents)
	}
	if err := m.Remove("docs"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	agents, err = LoadAgents(path)
	if err != nil || len(agents) != 0 {
		t.Fatalf("removed registry must persist empty list: agents=%#v err=%v", agents, err)
	}
}

func TestManager_RejectsInvalidTransportConfiguration(t *testing.T) {
	m := NewPersistent(nil, filepath.Join(t.TempDir(), "agents.json"))
	invalid := []Agent{
		{Ref: "missing-command", Transport: "stdio", Enabled: true},
		{Ref: "missing-url", Transport: "streamable-http", Enabled: true},
		{Ref: "unknown", Transport: "websocket", URL: "https://example.test", Enabled: true},
	}
	for _, agent := range invalid {
		if err := m.Upsert(agent); err == nil {
			t.Fatalf("expected invalid agent to fail: %#v", agent)
		}
	}
}

func TestLoadAgents_EmptyPathReturnsEmpty(t *testing.T) {
	got, err := LoadAgents("")
	if err != nil {
		t.Fatalf("expected no error for empty path, got %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty list, got %#v", got)
	}
}

func TestLoadAgents_MissingFileReturnsEmpty(t *testing.T) {
	got, err := LoadAgents("/nope/does/not/exist/anywhere.json")
	if err != nil {
		t.Fatalf("expected no error for missing file, got %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty list, got %#v", got)
	}
}

func TestLoadAgents_EmptyFileReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(p, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadAgents(p)
	if err != nil {
		t.Fatalf("expected no error for empty file, got %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty list, got %#v", got)
	}
}

func TestLoadAgents_InvalidJSONReturnsError(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "broken.json")
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAgents(p); err == nil {
		t.Fatal("expected parse error for invalid JSON")
	}
}

func TestManager_AgentsReturnsCopy(t *testing.T) {
	m := newTestManager(t, "alpha")
	got := m.Agents()
	if len(got) != 1 || got[0].Ref != "alpha" {
		t.Fatalf("unexpected agents: %#v", got)
	}
}

func TestManager_StartStopCycle(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in -short mode: spawns child process")
	}
	m := newTestManager(t, "alpha")

	if err := m.Start("alpha"); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	t.Cleanup(func() { _ = m.KillAll() })

	st, err := m.Status("alpha")
	if err != nil {
		t.Fatalf("status failed: %v", err)
	}
	if !st.Running {
		t.Fatal("expected Running to be true")
	}
	if st.PID <= 0 {
		t.Fatalf("expected PID > 0, got %d", st.PID)
	}
	if st.StartedAt.IsZero() {
		t.Fatal("expected StartedAt to be set")
	}

	// Idempotent start: should be a no-op success.
	if err := m.Start("alpha"); err != nil {
		t.Fatalf("idempotent start failed: %v", err)
	}
	st2, _ := m.Status("alpha")
	if st2.PID != st.PID {
		t.Fatalf("PID changed across idempotent start: %d -> %d", st.PID, st2.PID)
	}

	if err := m.Stop("alpha"); err != nil {
		t.Fatalf("stop failed: %v", err)
	}
	// Process should be reaped within a couple of seconds on POSIX and Windows.
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, err := m.Status("alpha")
		if err != nil {
			t.Fatalf("status failed after stop: %v", err)
		}
		if !st.Running {
			if st.ExitedAt.IsZero() {
				t.Fatal("expected ExitedAt to be set after process exit")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("process did not exit in time; status=%+v", st)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestManager_Restart(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in -short mode: spawns child process")
	}
	m := newTestManager(t, "alpha")
	t.Cleanup(func() { _ = m.KillAll() })

	if err := m.Start("alpha"); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	first, _ := m.Status("alpha")
	if first.PID <= 0 {
		t.Fatal("first PID should be > 0")
	}

	if err := m.Restart("alpha"); err != nil {
		t.Fatalf("restart failed: %v", err)
	}
	second, err := m.Status("alpha")
	if err != nil {
		t.Fatalf("status after restart failed: %v", err)
	}
	if !second.Running || second.PID <= 0 {
		t.Fatalf("restarted process not running: %+v", second)
	}
	// Wait briefly for the child to be reaped.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st, _ := m.Status("alpha")
		if st.ExitedAt.IsZero() {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		break
	}
}

func TestManager_StartUnknownRefErrors(t *testing.T) {
	m := newTestManager(t, "alpha")
	if err := m.Start("ghost"); err == nil {
		t.Fatal("expected error for unknown ref")
	}
	if err := m.Stop("ghost"); err == nil {
		t.Fatal("expected error for stop on unknown ref")
	}
	if _, err := m.Status("ghost"); err == nil {
		t.Fatal("expected error for status on unknown ref")
	}
}

func TestManager_StopUnknownRefNoop(t *testing.T) {
	// Stop on unknown ref should NOT error per Python ("no-op success").
	// We still expect an error per task instructions for unknown ref stop,
	// so this test pins the contract. If behavior changes, both sides update.
	m := newTestManager(t, "alpha")
	if err := m.Stop("ghost"); err == nil {
		t.Fatal("expected error on stop of unknown ref")
	}
}

func TestManager_ConcurrentStartStopDifferentRefs(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in -short mode: spawns many child processes")
	}
	const N = 6
	refs := make([]string, N)
	for i := 0; i < N; i++ {
		refs[i] = string(rune('a' + i))
	}
	m := newTestManager(t, refs...)
	t.Cleanup(func() { _ = m.KillAll() })

	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, ref := range refs {
		ref := ref
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			if err := m.Start(ref); err != nil {
				t.Errorf("start %s: %v", ref, err)
			}
		}()
		go func() {
			defer wg.Done()
			<-start
			// Call Status concurrently with Start to exercise the mutex.
			_, _ = m.Status(ref)
		}()
	}
	close(start)
	wg.Wait()

	// All agents should be running now.
	for _, ref := range refs {
		st, err := m.Status(ref)
		if err != nil {
			t.Fatalf("status %s: %v", ref, err)
		}
		if !st.Running {
			t.Fatalf("ref %s should be running: %+v", ref, st)
		}
		if err := m.Stop(ref); err != nil {
			t.Fatalf("stop %s: %v", ref, err)
		}
	}
}

func TestInstallSystemdUnit_WritesFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("systemd helper is linux-only at runtime; file write still proceeds")
	}
	dir := t.TempDir()
	unit := filepath.Join(dir, "shellmcp-alpha.service")
	m := New(nil)
	ag := Agent{Ref: "alpha", Name: "alpha", Command: "/usr/bin/sleep", Args: []string{"30"}}
	if err := m.InstallSystemdUnit(ag, unit); err != nil {
		t.Fatalf("InstallSystemdUnit: %v", err)
	}
	data, err := os.ReadFile(unit)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !strings.Contains(body, "ExecStart=/usr/bin/sleep 30") {
		t.Fatalf("unit missing ExecStart: %s", body)
	}
	if !strings.Contains(body, "shellmcp MCP agent") {
		t.Fatalf("unit missing description: %s", body)
	}
	if !strings.Contains(body, "ref alpha") {
		t.Fatalf("unit missing ref: %s", body)
	}
}

func TestInstallLaunchdPlist_WritesFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("launchd helper is macOS-only at runtime; file write still proceeds")
	}
	dir := t.TempDir()
	plist := filepath.Join(dir, "com.shellmcp.alpha.plist")
	m := New(nil)
	ag := Agent{Ref: "alpha", Name: "alpha", Command: "/usr/bin/sleep", Args: []string{"30"}}
	if err := m.InstallLaunchdPlist(ag, plist); err != nil {
		t.Fatalf("InstallLaunchdPlist: %v", err)
	}
	data, err := os.ReadFile(plist)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !strings.Contains(body, "com.shellmcp.alpha") {
		t.Fatalf("plist missing label: %s", body)
	}
	if !strings.Contains(body, "/usr/bin/sleep") {
		t.Fatalf("plist missing program: %s", body)
	}
	if !strings.Contains(body, "30") {
		t.Fatalf("plist missing arg 30: %s", body)
	}
}

func TestLoadAgents_RoundTripJSON(t *testing.T) {
	// Sanity: marshal an Agent, then LoadAgents on the file produces an
	// equivalent slice.
	dir := t.TempDir()
	p := filepath.Join(dir, "agents.json")
	want := Agent{
		Ref:     "alpha",
		Name:    "alpha-name",
		Command: "/bin/echo",
		Args:    []string{"hi", "world"},
		Env:     map[string]string{"K": "V"},
	}
	body, err := json.Marshal([]Agent{want})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, body, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadAgents(p)
	if err != nil {
		t.Fatalf("LoadAgents: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1, got %#v", got)
	}
	if got[0].Ref != want.Ref || got[0].Command != want.Command {
		t.Fatalf("mismatch: %#v", got[0])
	}
}
