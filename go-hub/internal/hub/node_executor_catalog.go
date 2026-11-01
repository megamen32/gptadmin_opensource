package hub

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

const nodeExecutorCatalogLimit = 256
const nodeHeartbeatBodyLimit = 256 << 10

// The wire format intentionally has no endpoint, credentials or tool schemas.
type nodeExecutorDescriptor struct {
	Ref                     string `json:"ref"`
	Name                    string `json:"name,omitempty"`
	Transport               string `json:"transport,omitempty"`
	Enabled                 bool   `json:"enabled"`
	OwnerProfileID          string `json:"owner_profile_id,omitempty"`
	ConnectionMode          string `json:"connection_mode,omitempty"`
	ConnectionScopesVersion int    `json:"connection_scopes_version,omitempty"`
	ConnectionInstance      string `json:"connection_instance,omitempty"`
	ManagedStdioOwner       bool   `json:"managed_stdio_owner,omitempty"`
	ManagedStdioPlatform    bool   `json:"managed_stdio_platform,omitempty"`
	ConfigDigest            string `json:"catalog_config_digest,omitempty"`
	CatalogDigest           string `json:"catalog_digest,omitempty"`
	ProtocolState           string `json:"protocol_state,omitempty"`
	ProcessState            string `json:"process_state,omitempty"`
}

func decodeNodeExecutorCatalog(raw json.RawMessage) ([]nodeExecutorDescriptor, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var items []nodeExecutorDescriptor
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&items); err != nil || items == nil {
		return nil, errors.New("executor_catalog must be a complete descriptor array")
	}
	if len(items) > nodeExecutorCatalogLimit {
		return nil, errors.New("executor_catalog descriptor limit exceeded")
	}
	seen := map[string]bool{}
	for _, item := range items {
		if item.Ref == "" || strings.TrimSpace(item.Ref) != item.Ref || strings.ContainsAny(item.Ref, ":/\\") || seen[item.Ref] {
			return nil, errors.New("invalid or duplicate executor catalog ref")
		}
		seen[item.Ref] = true
		for _, value := range []string{item.Ref, item.Name, item.Transport, item.OwnerProfileID, item.ConnectionMode, item.ConnectionInstance, item.ConfigDigest, item.CatalogDigest, item.ProtocolState, item.ProcessState} {
			if len(value) > 256 || strings.ContainsAny(value, "\x00\r\n") {
				return nil, errors.New("executor catalog field limit exceeded")
			}
		}
		if item.ConnectionScopesVersion < 0 {
			return nil, errors.New("invalid connection scopes version")
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Ref < items[j].Ref })
	return items, nil
}

func nodeExecutorCatalogMaps(items []nodeExecutorDescriptor) []any {
	out := make([]any, 0, len(items))
	for _, item := range items {
		raw, _ := json.Marshal(item)
		descriptor := map[string]any{}
		_ = json.Unmarshal(raw, &descriptor)
		delete(descriptor, "protocol_state")
		delete(descriptor, "process_state")
		if item.ProtocolState != "" || item.ProcessState != "" {
			descriptor["health"] = map[string]any{"protocol": map[string]any{"state": item.ProtocolState}, "process": map[string]any{"state": item.ProcessState}}
		}
		out = append(out, descriptor)
	}
	return out
}

// Only the in-process installed executor can supply the member's catalog.
// Missing metadata means not initialized, never an authoritative empty list.
func (s *Server) localNodeExecutorCatalog() (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var parent *Agent
	for id := range s.localExecutors {
		candidate := s.agents[id]
		if candidate == nil || !truthyAny(candidate.Meta["approved"]) {
			continue
		}
		if parent != nil {
			return nil, errors.New("multiple local executor catalogs")
		}
		parent = candidate
	}
	if parent == nil {
		return nil, nil
	}
	raw, present := parent.Meta["mcp_agents"]
	if !present {
		return nil, nil
	}
	descriptors := sliceValue(raw)
	if len(descriptors) > nodeExecutorCatalogLimit {
		return nil, errors.New("executor catalog descriptor limit exceeded")
	}
	items := make([]nodeExecutorDescriptor, 0, len(descriptors))
	for _, value := range descriptors {
		m := mapValue(value)
		health := mapValue(m["health"])
		items = append(items, nodeExecutorDescriptor{
			Ref: firstString(m, "ref"), Name: firstString(m, "name"), Transport: firstString(m, "transport"), Enabled: truthyAny(m["enabled"]),
			OwnerProfileID: firstString(m, "owner_profile_id"), ConnectionMode: firstString(m, "connection_mode"),
			ConnectionScopesVersion: intFromAny(m["connection_scopes_version"]), ConnectionInstance: firstString(m, "connection_instance"),
			ManagedStdioOwner: truthyAny(m["managed_stdio_owner"]), ManagedStdioPlatform: truthyAny(m["managed_stdio_platform"]),
			ConfigDigest: firstString(m, "catalog_config_digest"), CatalogDigest: firstString(m, "catalog_digest"),
			ProtocolState: firstString(mapValue(health["protocol"]), "state"), ProcessState: firstString(mapValue(health["process"]), "state"),
		})
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return nil, err
	}
	if _, err = decodeNodeExecutorCatalog(encoded); err != nil {
		return nil, err
	}
	if len(encoded) > nodeHeartbeatBodyLimit-2048 {
		return nil, errors.New("executor catalog byte limit exceeded")
	}
	return encoded, nil
}

// Lock order matches registry readers: registry first, then membership.
// Revocation cannot race registry publication.
// Names are taken solely from the authenticated enrollment, never the body.
func (s *Server) applyNodeExecutorCatalog(member *nodeMember, items []nodeExecutorDescriptor) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.membershipMu.Lock()
	defer s.membershipMu.Unlock()
	current := s.membership.Nodes[member.NodeID]
	if current == nil || current.KeyHash != member.KeyHash || current.Name != member.Name || current.State == nodeStateDisabled {
		return false, errors.New("executor catalog member no longer authorized")
	}
	member = current

	parent := s.agents[member.Name]
	if parent == nil || !truthyAny(parent.Meta["approved"]) || parent.Status == "awaiting_approval" || s.localExecutors[member.Name] != nil {
		return false, errors.New("executor catalog requires an existing approved remote parent")
	}
	if member.LocalID != "" && firstString(parent.Meta, "server_id") != member.LocalID {
		return false, errors.New("executor catalog parent identity mismatch")
	}
	if items == nil {
		if firstString(parent.Meta, "membership_catalog_status") != "incomplete" {
			previous := parent.Meta["membership_catalog_status"]
			parent.Meta["membership_catalog_status"] = "incomplete"
			if err := s.saveRegistryStateLocked(); err != nil {
				parent.Meta["membership_catalog_status"] = previous
				return false, err
			}
		}
		return false, nil
	}
	// Keep owner tombstones across removals. A descriptor heartbeat is not an
	// administrative owner provisioning or transfer operation.
	owners := map[string]any{}
	for ref, owner := range mapValue(parent.Meta["membership_catalog_owners"]) {
		owners[ref] = owner
	}
	for _, value := range sliceValue(parent.Meta["mcp_agents"]) {
		m := mapValue(value)
		if owner := firstString(m, "owner_profile_id"); owner != "" {
			owners[firstString(m, "ref")] = owner
		}
	}
	if len(owners) > 1024 {
		return false, errors.New("executor catalog owner history limit exceeded")
	}
	for _, item := range items {
		expected := firstString(owners, item.Ref)
		if item.OwnerProfileID != expected {
			return false, fmt.Errorf("executor catalog owner mapping differs for %s", item.Ref)
		}
	}
	next := nodeExecutorCatalogMaps(items)
	oldJSON, _ := json.Marshal(parent.Meta["mcp_agents"])
	newJSON, _ := json.Marshal(next)
	if bytes.Equal(oldJSON, newJSON) && firstString(parent.Meta, "membership_catalog_status") == "complete" {
		return false, nil
	}
	oldMeta := parent.Meta
	meta := map[string]any{}
	for k, v := range oldMeta {
		meta[k] = v
	}
	meta["mcp_agents"] = next
	meta["membership_catalog_owners"] = owners
	meta["membership_catalog_status"] = "complete"
	parent.Meta = meta
	if err := s.saveRegistryStateLocked(); err != nil {
		parent.Meta = oldMeta
		return false, err
	}
	return true, nil
}
