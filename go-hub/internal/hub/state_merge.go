package hub

import (
	"encoding/json"
	"errors"
	"os"
)

func mergeRegistryAgentsFromDisk(path string, state *persistentRegistryState) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var disk persistentRegistryState
	if err := json.Unmarshal(b, &disk); err != nil {
		return err
	}
	if state.Agents == nil {
		state.Agents = map[string]Agent{}
	}
	if state.AgentPolicies == nil {
		state.AgentPolicies = map[string]agentCleanupPolicy{}
	}
	// Preserve the newest settings revision when primary/standby share the state file.
	if disk.SettingsRevision > state.SettingsRevision {
		state.SettingsRevision = disk.SettingsRevision
		state.Settings = disk.Settings
		state.SettingsHistory = append([]settingsRevision(nil), disk.SettingsHistory...)
	}
	// Merge tombstones by newest deletion timestamp and use them to prevent a
	// stale writer from resurrecting a deleted agent.
	tombs := map[string]agentTombstone{}
	for _, t := range disk.Tombstones {
		if old, ok := tombs[t.AgentID]; !ok || t.DeletedAt > old.DeletedAt {
			tombs[t.AgentID] = t
		}
	}
	for _, t := range state.Tombstones {
		if old, ok := tombs[t.AgentID]; !ok || t.DeletedAt > old.DeletedAt {
			tombs[t.AgentID] = t
		}
	}
	state.Tombstones = state.Tombstones[:0]
	for _, t := range tombs {
		state.Tombstones = append(state.Tombstones, t)
	}
	for id, policy := range disk.AgentPolicies {
		if _, ok := state.AgentPolicies[id]; !ok {
			state.AgentPolicies[id] = policy
		}
	}
	for id, diskAgent := range disk.Agents {
		if tomb, ok := tombs[id]; ok && tomb.DeletedAt >= diskAgent.LastSeen {
			continue
		}
		current, exists := state.Agents[id]
		if !exists {
			state.Agents[id] = diskAgent
			continue
		}
		// Never let a stale writer bypass a fresh enrollment decision.
		if current.Status == "awaiting_approval" {
			continue
		}
		if diskAgent.Status == "awaiting_approval" && diskAgent.LastSeen >= current.LastSeen {
			state.Agents[id] = diskAgent
			continue
		}
		if diskAgent.LastSeen > current.LastSeen {
			state.Agents[id] = diskAgent
		}
	}
	return nil
}

func persistedTaskUpdated(created, started, done float64) float64 {
	if done > 0 {
		return done
	}
	if started > 0 {
		return started
	}
	return created
}
