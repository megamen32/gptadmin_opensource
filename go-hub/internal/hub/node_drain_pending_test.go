package hub

import "testing"

type pendingDrainGate struct{ draining, pending bool }

func (g *pendingDrainGate) ActiveCallbacks() int        { return 0 }
func (g *pendingDrainGate) OutboxPending() int          { return 0 }
func (g *pendingDrainGate) SetQueueDraining(v bool)     { g.draining = v }
func (g *pendingDrainGate) SetQueueDrainPending(v bool) { g.pending = v }

func TestDrainAllowsAlreadyQueuedExecutionThenClosesPollGate(t *testing.T) {
	s := drainTestHub(t, nil)
	drainLocalExecutor(t, s)
	gate := &pendingDrainGate{}
	s.SetNodeRuntimeCounters(gate)
	w := peerTestCall(s, `{"target":"shell:local","tool":"shell_exec","arguments":{"cmd":"printf prior"},"background":true}`, "drain-owner", "")
	if w.Code != 200 {
		t.Fatalf("pre-drain enqueue: %d %s", w.Code, w.Body.String())
	}
	s.mu.Lock()
	jobID := ""
	for id := range s.shellJobs {
		jobID = id
	}
	s.mu.Unlock()
	if jobID == "" {
		t.Fatal("missing pre-drain job")
	}
	s.EnableNodeDrain("pending")
	status := s.NodeDrainStatus()
	if !gate.draining || !gate.pending || status.ReadyToStop {
		t.Fatalf("pending queue stranded: gate=%+v status=%+v", gate, status)
	}
	if release, ok := s.beginNodeSubmission("shell:local", "system_inspect"); ok {
		release()
		t.Fatal("new executor read admitted during drain")
	}
	completeLocalJob(t, s, jobID)
	status = s.NodeDrainStatus()
	if gate.pending || !gate.draining || !status.ReadyToStop {
		t.Fatalf("gate did not close after completion: gate=%+v status=%+v", gate, status)
	}
	s.CancelNodeDrain()
}
