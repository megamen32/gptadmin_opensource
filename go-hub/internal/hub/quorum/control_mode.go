package quorum

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
)

const (
	OwnerLocal            = "owner_local"
	Replicated            = "replicated"
	ControlSnapshotLimit  = 32 << 20
	controlMetadataLimit  = 16 << 10
	ControlAuthorityLimit = 4 << 20 // sealed authority value, before log encryption
)

// Only these control domains are projected by an owner-local Hub. In
// particular the legacy webhook job store and secret ingress contain inputs.
var controlAuthority = map[string]string{
	"registry": "enrollment",
	"auth":     "bundle", "membership": "writer", "instruction_sets_named": "current",
	"instruction_sets_default": "current", "security": "settings", "webauthn": "credentials", "virtual_mcp": "current",
}

func ControlAuthorityKey(domain string) string { return controlAuthority[domain] }

type ControlBaseline struct {
	Mode             string   `json:"mode"`
	ClusterID        string   `json:"cluster_id"`
	AuthorityDomains []string `json:"authority_domains"`
	ChainSequence    uint64   `json:"chain_sequence"`
	ChainDigest      string   `json:"chain_digest"`
}

func validControlBaseline(raw []byte, cluster string) bool {
	if len(raw) > controlMetadataLimit {
		return false
	}
	var v ControlBaseline
	if json.Unmarshal(raw, &v) != nil || v.Mode != OwnerLocal || v.ClusterID != cluster || !operationDigest(v.ChainDigest) || v.ChainSequence == 0 || len(v.AuthorityDomains) != len(controlAuthority) {
		return false
	}
	for d := range controlAuthority {
		if !slices.Contains(v.AuthorityDomains, d) {
			return false
		}
	}
	exact, _ := json.Marshal(v)
	return bytes.Equal(exact, raw)
}

func ValidateControlMutation(m Mutation, cluster string) error {
	if m.Record.Revision == 0 || m.Record.Deleted {
		return errors.New("quorum: invalid control revision or deletion")
	}
	switch {
	case m.Namespace == "cluster_meta" && m.Key == "task_baseline" && !m.External && m.Record.Revision == 1 && validControlBaseline(m.Record.Value, cluster):
		return nil
	case m.External && strings.HasPrefix(m.Namespace, "authority/") && m.Key != "" && controlAuthority[strings.TrimPrefix(m.Namespace, "authority/")] == m.Key:
		if len(m.Record.Value) <= ControlAuthorityLimit {
			var v sealedMutationValue
			d := json.NewDecoder(bytes.NewReader(m.Record.Value))
			d.DisallowUnknownFields()
			if d.Decode(&v) == nil && d.Decode(new(any)) == io.EOF && v.Version == 1 && v.Nonce != "" && v.Ciphertext != "" {
				b, _ := json.Marshal(v)
				if bytes.Equal(b, m.Record.Value) {
					return nil
				}
			}
		}
	}
	return errors.New("quorum: value outside owner_local control schema")
}
