package hub

// Only these metadata fields bind a shell enrollment. Tool schemas, child
// catalogs, poll counters and execution history belong to the observing Hub.
var enrollmentIdentityFields = [...]string{"server_id", "public_key", "fingerprint", shellTokenHashMetaKey}

func registryEnrollments(state persistentRegistryState) persistentRegistryState {
	result := persistentRegistryState{
		Agents: make(map[string]Agent, len(state.Agents)), RelayCredentials: state.RelayCredentials,
		RelayEnrollments: state.RelayEnrollments,
	}
	for id, agent := range state.Agents {
		meta := map[string]any{}
		for _, field := range enrollmentIdentityFields {
			if value := firstString(agent.Meta, field); value != "" {
				meta[field] = value
			}
		}
		for _, field := range []string{"approved", "self_enrolled"} {
			if value, exists := agent.Meta[field]; exists {
				meta[field] = truthyAny(value)
			}
		}
		// Older self-enrollments can have only the local credential on disk.
		if raw := state.LocalExecutorTokens[id]; raw != "" && truthyAny(meta["self_enrolled"]) && meta[shellTokenHashMetaKey] == nil {
			meta[shellTokenHashMetaKey] = shellTokenHash(raw)
		}
		status := agent.Status
		switch status {
		case "", "online", "running", "offline", "stale":
			status = "offline" // liveness is learned from this executor's next poll
		}
		result.Agents[id] = Agent{AgentID: id, Name: agent.Name, Kind: agent.Kind, Transport: agent.Transport, Status: status, Meta: meta}
	}
	return result
}

// A promotion replaces the authorization fields for every enrollment. Only
// an identical identity may reuse this machine's local observations.
func restoreRegistryEnrollments(path string, committed persistentRegistryState) (persistentRegistryState, error) {
	var local persistentRegistryState
	if err := readAccessStateJSON(path, 16<<20, &local); err != nil {
		return local, err
	}
	agents := make(map[string]Agent, len(committed.Agents))
	for id, enrollment := range committed.Agents {
		previous, present := local.Agents[id]
		matches := present && previous.Kind == enrollment.Kind
		for _, field := range enrollmentIdentityFields {
			matches = matches && firstString(previous.Meta, field) == firstString(enrollment.Meta, field)
		}
		if matches {
			meta := cloneMap(previous.Meta)
			delete(meta, "approved")
			delete(meta, "self_enrolled")
			delete(meta, "local_executor")
			for key, value := range enrollment.Meta {
				meta[key] = value
			}
			enrollment.Meta = meta
			enrollment.Capabilities = previous.Capabilities
			enrollment.FirstSeen, enrollment.RegisteredAt = previous.FirstSeen, previous.RegisteredAt
			enrollment.LastSeen = previous.LastSeen
		} else if enrollment.Kind == "virtual_shell" {
			enrollment.Capabilities = []string{"shell", "system", "tasks", "logs"}
		}
		agents[id] = enrollment
	}
	local.Agents = agents
	local.RelayCredentials = committed.RelayCredentials
	local.RelayEnrollments = committed.RelayEnrollments
	local.LocalExecutorTokens = nil
	return local, nil
}
