package job

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/shell"
)

type State string

const (
	Running State = "running"
	Done    State = "done"
	Failed  State = "failed"
)

type Job struct {
	ID        string        `json:"id"`
	State     State         `json:"state"`
	StartedAt time.Time     `json:"started_at"`
	EndedAt   *time.Time    `json:"ended_at,omitempty"`
	Request   shell.Request `json:"request"`
	Result    *shell.Result `json:"result,omitempty"`
	Error     string        `json:"error,omitempty"`
}

type Manager struct {
	mu    sync.Mutex
	jobs  map[string]*Job
	limit int64
}

func New(limitBytes int64) *Manager {
	return &Manager{jobs: map[string]*Job{}, limit: limitBytes}
}

func (m *Manager) Start(req shell.Request) *Job {
	j := &Job{ID: newID(), State: Running, StartedAt: time.Now(), Request: req}
	m.mu.Lock()
	m.jobs[j.ID] = j
	m.mu.Unlock()
	go func() {
		res := shell.Run(context.Background(), req, m.limit)
		now := time.Now()
		m.mu.Lock()
		defer m.mu.Unlock()
		j.EndedAt = &now
		j.Result = &res
		if res.Error != "" && res.ReturnCode == -1 {
			j.State = Failed
			j.Error = res.Error
		} else {
			j.State = Done
		}
	}()
	return j
}

func (m *Manager) Get(id string) (*Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return nil, false
	}
	cp := *j
	return &cp, true
}

func (m *Manager) List() []*Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Job, 0, len(m.jobs))
	for _, j := range m.jobs {
		cp := *j
		out = append(out, &cp)
	}
	return out
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "job_" + hex.EncodeToString(b)
}
