package hub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestMergeRegistryAgentsPrefersNewerHeartbeat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "registry_state.json")
	disk := persistentRegistryState{SavedAt: 2, Agents: map[string]Agent{
		"shell:a": {AgentID: "shell:a", Status: "online", LastSeen: 200},
		"shell:b": {AgentID: "shell:b", Status: "online", LastSeen: 150},
	}}
	b, _ := json.Marshal(disk)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	state := persistentRegistryState{SavedAt: 3, Agents: map[string]Agent{
		"shell:a": {AgentID: "shell:a", Status: "offline", LastSeen: 100},
		"shell:c": {AgentID: "shell:c", Status: "online", LastSeen: 300},
	}}
	if err := mergeRegistryAgentsFromDisk(path, &state); err != nil {
		t.Fatal(err)
	}
	if got := state.Agents["shell:a"].LastSeen; got != 200 {
		t.Fatalf("newer disk heartbeat lost: %v", got)
	}
	if _, ok := state.Agents["shell:b"]; !ok {
		t.Fatal("disk-only agent lost")
	}
	if _, ok := state.Agents["shell:c"]; !ok {
		t.Fatal("current-only agent lost")
	}
}

func TestMergeRegistryDoesNotBypassAwaitingApproval(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "registry_state.json")
	disk := persistentRegistryState{Agents: map[string]Agent{
		"shell:a": {AgentID: "shell:a", Status: "online", LastSeen: 500},
	}}
	b, _ := json.Marshal(disk)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	state := persistentRegistryState{Agents: map[string]Agent{
		"shell:a": {AgentID: "shell:a", Status: "awaiting_approval", LastSeen: 100},
	}}
	if err := mergeRegistryAgentsFromDisk(path, &state); err != nil {
		t.Fatal(err)
	}
	if got := state.Agents["shell:a"].Status; got != "awaiting_approval" {
		t.Fatalf("approval gate bypassed: %q", got)
	}
}

func TestConcurrentHubStateSavesMergeInsteadOfOverwrite(t *testing.T) {
	dir := t.TempDir()
	s1 := New(Config{ConfigDir: dir})
	s2 := New(Config{ConfigDir: dir})
	now := nowFloat()
	s1.mu.Lock()
	s1.agents["shell:a"] = &Agent{AgentID: "shell:a", Kind: "virtual_shell", Status: "online", LastSeen: now}
	s1.shellJobs["task-a"] = &shellJob{ID: "task-a", Server: "a", CreatedAt: now, DoneAt: now + 1, Status: "completed"}
	s1.mu.Unlock()
	s2.mu.Lock()
	s2.agents["shell:b"] = &Agent{AgentID: "shell:b", Kind: "virtual_shell", Status: "online", LastSeen: now + 1}
	s2.shellJobs["task-b"] = &shellJob{ID: "task-b", Server: "b", CreatedAt: now, DoneAt: now + 2, Status: "completed"}
	s2.mu.Unlock()

	var wg sync.WaitGroup
	errCh := make(chan error, 4)
	for _, s := range []*Server{s1, s2} {
		wg.Add(1)
		go func(s *Server) {
			defer wg.Done()
			s.mu.Lock()
			if err := s.saveRegistryStateLocked(); err != nil {
				errCh <- err
			}
			if err := s.saveTaskStateLocked(); err != nil {
				errCh <- err
			}
			s.mu.Unlock()
		}(s)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}

	var reg persistentRegistryState
	b, err := os.ReadFile(filepath.Join(dir, "registry_state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &reg); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Agents["shell:a"]; !ok {
		t.Fatalf("shell:a lost: %v", reg.Agents)
	}
	if _, ok := reg.Agents["shell:b"]; !ok {
		t.Fatalf("shell:b lost: %v", reg.Agents)
	}

	tasks, err := s1.readTaskState()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := tasks.Shell["task-a"]; !ok {
		t.Fatalf("task-a lost: %v", tasks.Shell)
	}
	if _, ok := tasks.Shell["task-b"]; !ok {
		t.Fatalf("task-b lost: %v", tasks.Shell)
	}
}
