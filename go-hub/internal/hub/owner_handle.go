package hub

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"math"
	"strconv"
	"strings"
)

// A handle is a location hint. The complete string is the storage, dispatch
// and delivery key. LocalID is opaque and must never be tried as a second key.
type ownerHandle struct {
	Kind    string
	OwnerID string
	LocalID string
}

var errOwnerHandle = errors.New("invalid owner-scoped handle")

func validHandlePart(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func validOwnerID(id string) bool {
	return validHandlePart(strings.ReplaceAll(id, ".", "_"))
}

func encodeOwnerHandle(kind, ownerID, localID string) (string, error) {
	prefix := ""
	switch kind {
	case "task":
		prefix = "ot1."
	case "content":
		prefix = "oc1."
	default:
		return "", errOwnerHandle
	}
	if !validOwnerID(ownerID) || !validHandlePart(localID) {
		return "", errOwnerHandle
	}
	return prefix + base64.RawURLEncoding.EncodeToString([]byte(ownerID)) + "." + localID, nil
}

// A legacy opaque ID is returned as unscoped. A malformed reserved prefix is
// an error and never falls back to a legacy lookup.
func decodeOwnerHandle(id string) (ownerHandle, bool, error) {
	prefix, rest, _ := strings.Cut(id, ".")
	var h ownerHandle
	switch prefix {
	case "ot1":
		h.Kind = "task"
	case "oc1":
		h.Kind = "content"
	default:
		return h, false, nil
	}
	owner, local, ok := strings.Cut(rest, ".")
	if !ok || len(owner) > base64.RawURLEncoding.EncodedLen(64) || !validHandlePart(local) {
		return ownerHandle{}, true, errOwnerHandle
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(owner)
	if err != nil || !validOwnerID(string(raw)) || base64.RawURLEncoding.EncodeToString(raw) != owner {
		return ownerHandle{}, true, errOwnerHandle
	}
	h.OwnerID, h.LocalID = string(raw), local
	return h, true, nil
}

// localReadbackOwnerID identifies this Hub across promotions and restarts.
// The ordinary writer's membership self-entry uses the local server_id.
func (s *Server) localReadbackOwnerID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.localReadbackOwnerIDLocked()
}

func (s *Server) localReadbackOwnerIDLocked() string {
	if s.quorumNode != nil {
		return s.quorumNode.Status().NodeID
	}
	if id := s.selfNodeIDLocked(); id != "" {
		return id
	}
	if s.authLocalID != "" {
		return s.authLocalID
	}
	if len(s.localExecutors) == 1 {
		for _, local := range s.localExecutors {
			return local["server_id"]
		}
	}
	return ""
}

// Mint once when the task is created. Persist CreatedAt from the same clock
// sample. Polls and retried requests keep the stored ID, including legacy IDs.
func (s *Server) newOwnerTaskID(ownerID string) (id string, createdAt float64) {
	issued := s.now()
	var nonce [16]byte
	rand.Read(nonce[:])
	sec := issued.Unix()
	local := "t1_" + strconv.FormatInt(sec, 10) + "_" + hex.EncodeToString(nonce[:])
	// SQLite stores a float. Keep even the last nanosecond in the ID's second.
	start := float64(sec)
	createdAt = min(start+float64(issued.Nanosecond())/1e9, math.Nextafter(start+1, start))
	if !validOwnerID(ownerID) {
		return local, createdAt
	}
	id, _ = encodeOwnerHandle("task", ownerID, local)
	return id, createdAt
}
