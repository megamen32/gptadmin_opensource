package hub

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/megamen32/gptadmin/go-hub/internal/hub/quorum"
)

// Local queues reuse the installed secret store's AES-GCM key. Domain and
// immutable task ID are authenticated; ciphertext cannot move between tasks.
func (s *Server) sealPendingValue(namespace, id string, plain []byte) (json.RawMessage, error) {
	if s.quorumNode != nil && !s.quorumControlOnly() {
		return s.quorumNode.SealExternalValue(namespace, id, 1, plain)
	}
	if s.secretStore == nil || s.secretStoreErr != nil {
		return nil, errors.New("pending local cipher unavailable")
	}
	a := s.secretStore.cipher
	nonce := make([]byte, a.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Version    int    `json:"version"`
		Nonce      []byte `json:"nonce"`
		Ciphertext []byte `json:"ciphertext"`
	}{1, nonce, a.Seal(nil, nonce, plain, []byte("gptadmin/local-pending/v1/"+namespace+"/"+id))})
}
func (s *Server) openPendingValue(namespace, id string, sealed json.RawMessage) ([]byte, error) {
	if s.quorumControlOnly() {
		// The ordinary owner keeps its existing pending input and local key.
		return s.openLocalPendingValue(namespace, id, sealed)
	}
	if s.quorumNode != nil {
		return s.quorumNode.OpenExternalValue(namespace, id, 1, sealed)
	}
	if s.cfg.QuorumConfigFile != "" {
		configBytes, err := os.ReadFile(s.cfg.QuorumConfigFile)
		if err != nil {
			configBytes, err = os.ReadFile(filepath.Join(s.cfg.ConfigDir, "quorum", "quorum.json"))
		}
		if err == nil {
			var config quorum.Config
			if json.Unmarshal(configBytes, &config) == nil {
				if opened, openErr := quorum.OpenExternalValueWithKey(config.StateKeyFile, config.ClusterID, namespace, id, 1, sealed); openErr == nil {
					return opened, nil
				}
			}
		}
	}
	// A queued envelope created during a staged quorum can outlive the
	// quorum process itself. Try the durable quorum key before interpreting it
	// as a local AES-GCM envelope; failure falls through to the normal local
	// cipher path.
	if configBytes, err := os.ReadFile(filepath.Join(s.cfg.ConfigDir, "quorum", "quorum.json")); err == nil {
		var config quorum.Config
		if json.Unmarshal(configBytes, &config) == nil {
			if opened, err := quorum.OpenExternalValueWithKey(config.StateKeyFile, config.ClusterID, namespace, id, 1, sealed); err == nil {
				return opened, nil
			}
		}
	}
	return s.openLocalPendingValue(namespace, id, sealed)
}

func (s *Server) openLocalPendingValue(namespace, id string, sealed json.RawMessage) ([]byte, error) {
	if s.secretStore == nil || s.secretStoreErr != nil {
		return nil, errors.New("pending local cipher unavailable")
	}
	var e struct {
		Version    int    `json:"version"`
		Nonce      []byte `json:"nonce"`
		Ciphertext []byte `json:"ciphertext"`
	}
	a := s.secretStore.cipher
	if err := json.Unmarshal(sealed, &e); err != nil || e.Version != 1 || len(e.Nonce) != a.NonceSize() {
		return nil, errors.New("invalid pending envelope")
	}
	return a.Open(nil, e.Nonce, e.Ciphertext, []byte("gptadmin/local-pending/v1/"+namespace+"/"+id))
}

func (s *Server) durablePendingShellLocked(j *shellJob) bool {
	if j.Status != "queued" || j.StartedAt != 0 || j.DoneAt != 0 || j.Revision <= 0 {
		return false
	}
	state, err := s.readTaskRecords(j.ID)
	if err != nil {
		return false
	}
	p, ok := state.Shell[j.ID]
	if !ok || p.Revision != j.Revision || p.Status != "queued" || p.StartedAt != 0 || len(p.SealedPending) == 0 {
		return false
	}
	_, err = s.openPendingValue(quorumPendingShellNamespace, j.ID, p.SealedPending)
	return err == nil
}
