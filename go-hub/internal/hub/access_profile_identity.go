package hub

import "strings"

func approvedTargetIdentity(target string, agents map[string]Agent) string {
	parentID := target
	if strings.HasPrefix(target, "file:") || strings.HasPrefix(target, "control:") {
		parentID = "shell:" + strings.SplitN(target, ":", 2)[1]
	} else if strings.HasPrefix(target, "mcp:") {
		parentID = firstString(agents[target].Meta, "parent_server_id")
	}
	if !truthy(agents[parentID].Meta["approved"]) {
		return ""
	}
	return fullTargetIdentityFromAgents(target, agents)
}

// Profile grants are pinned while the named device still exists. A vanished
// legacy name cannot safely be associated with a new device by guessing.
func (s *Server) pinAccessProfileTargets(profile, previous AccessProfile) AccessProfile {
	s.mu.Lock()
	agents := targetAgentMap(s.publicAgentsLocked(nil))
	s.mu.Unlock()
	profile = cloneAccessProfile(profile)
	profile.TargetIdentities = make(map[string]string)
	targets := append([]string(nil), profile.AllowedTargets...)
	for _, rule := range profile.ToolRules {
		targets = append(targets, rule.Target)
	}
	for _, ref := range profile.WorkspaceRefs {
		targets = append(targets, ref.ShellTarget)
	}
	for _, target := range targets {
		if pinned := previous.TargetIdentities[target]; pinned != "" {
			profile.TargetIdentities[target] = pinned
			continue
		}
		identity := ""
		for canonical := range agents {
			if canonical != target && shortTargetFromAgents(canonical, agents) != target {
				continue
			}
			// A device must already be approved; a name alone is not authority.
			candidate := approvedTargetIdentity(canonical, agents)
			if candidate == "" {
				continue
			}
			if identity != "" && identity != candidate {
				identity = ""
				break
			}
			identity = candidate
		}
		if identity != "" {
			profile.TargetIdentities[target] = identity
		}
	}
	return profile
}

// Resolve only the request copy. Persisted labels and bindings remain stable;
// old names and replacement keys cannot inherit identity-bound permissions.
func (s *Server) resolveAccessProfileTargets(profile AccessProfile) AccessProfile {
	profile = cloneAccessProfile(profile)
	if len(profile.TargetIdentities) == 0 {
		return profile
	}
	s.mu.Lock()
	agents := targetAgentMap(s.publicAgentsLocked(nil))
	s.mu.Unlock()
	resolved := make(map[string]string)
	for target, identity := range profile.TargetIdentities {
		match := ""
		for canonical := range agents {
			if approvedTargetIdentity(canonical, agents) != identity {
				continue
			}
			if match != "" && match != canonical {
				match = ""
				break
			}
			match = canonical
		}
		resolved[target] = match
	}
	resolve := func(target string) string {
		if value, pinned := resolved[target]; pinned {
			return value
		}
		return target
	}
	allowed := make([]string, 0, len(profile.AllowedTargets))
	for _, target := range profile.AllowedTargets {
		if current := resolve(target); current != "" {
			allowed = append(allowed, current)
		}
	}
	profile.AllowedTargets = allowed
	for i := range profile.ToolRules {
		// Keep an unresolved rule as a nonmatching restriction. Dropping the
		// last rule would make an otherwise wildcard profile unrestricted.
		profile.ToolRules[i].Target = resolve(profile.ToolRules[i].Target)
	}
	for i := range profile.WorkspaceRefs {
		profile.WorkspaceRefs[i].ShellTarget = resolve(profile.WorkspaceRefs[i].ShellTarget)
	}
	return profile
}
