package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type persistedWebhookDelivery struct {
	Key         string `json:"key"`
	Fingerprint string `json:"fingerprint"`
	JobID       string `json:"job_id"`
	CreatedAt   int64  `json:"created_at"`
}

type persistedWebhookState struct {
	Jobs       map[string]*webhookJob     `json:"jobs"`
	Deliveries []persistedWebhookDelivery `json:"deliveries"`
}

func (s *Server) webhookStatePath() string {
	if s.cfg.WebhookStateFile != "" {
		return s.cfg.WebhookStateFile
	}
	if s.cfg.ConfigDir == "" {
		return ""
	}
	return filepath.Join(s.cfg.ConfigDir, "webhook_state.json")
}

func (s *Server) loadWebhookState() error {
	path := s.webhookStatePath()
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var state persistedWebhookState
	if err := json.Unmarshal(b, &state); err != nil {
		return err
	}
	for id, job := range state.Jobs {
		if id == "" || job == nil || job.ID != id || job.RouteID == "" {
			continue
		}
		if _, ok := s.webhookRoutes[job.RouteID]; !ok {
			continue
		}
		s.webhookJobs[id] = cloneWebhookJob(job)
	}
	for _, delivery := range state.Deliveries {
		if delivery.Key == "" || delivery.JobID == "" || delivery.Fingerprint == "" {
			continue
		}
		if _, ok := s.webhookJobs[delivery.JobID]; !ok {
			continue
		}
		s.webhookDeliveries[delivery.Key] = &webhookDelivery{
			Fingerprint: delivery.Fingerprint,
			JobID:       delivery.JobID,
			CreatedAt:   timeFromUnix(delivery.CreatedAt),
		}
	}
	return nil
}

func (s *Server) saveWebhookStateLocked() error {
	s.webhookStateWriteMu.Lock()
	defer s.webhookStateWriteMu.Unlock()
	path := s.webhookStatePath()
	if path == "" {
		return nil
	}
	state := persistedWebhookState{Jobs: map[string]*webhookJob{}}
	for id, job := range s.webhookJobs {
		state.Jobs[id] = cloneWebhookJob(job)
	}
	keys := make([]string, 0, len(s.webhookDeliveries))
	for key := range s.webhookDeliveries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		delivery := s.webhookDeliveries[key]
		if delivery == nil {
			continue
		}
		state.Deliveries = append(state.Deliveries, persistedWebhookDelivery{
			Key:         key,
			Fingerprint: delivery.Fingerprint,
			JobID:       delivery.JobID,
			CreatedAt:   delivery.CreatedAt.Unix(),
		})
	}
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename webhook state: %w", err)
	}
	return nil
}

func timeFromUnix(value int64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	return time.Unix(value, 0)
}
