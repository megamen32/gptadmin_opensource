package hub

import "testing"

type pendingDrainGate struct{ draining, pending bool }

func (g *pendingDrainGate) ActiveCallbacks() int        { return 0 }
func (g *pendingDrainGate) OutboxPending() int          { return 0 }
func (g *pendingDrainGate) SetQueueDraining(v bool)     { g.draining = v }
func (g *pendingDrainGate) SetQueueDrainPending(v bool) { g.pending = v }

func TestDrainPreservesDurableQueueAndClosesPollGate(t *testing.T) {
	s := drainTestHub(t, nil)
	drainLocalExecutor(t, s)
	gate := &pendingDrainGate{}
	s.SetNodeRuntimeCounters(gate)
	w := peerTestCall(s, `{"target":"shell:local","tool":"shell_exec","arguments":{"cmd":"printf prior"},"background":true}`, "drain-owner", "")
	if w.Code != 200 {
		t.Fatalf("enqueue: %d %s", w.Code, w.Body.String())
	}
	s.mu.Lock()
	id := ""
	for k := range s.shellJobs {
		id = k
	}
	s.mu.Unlock()
	if id == "" {
		t.Fatal("missing queued job")
	}
	before, err := s.readTaskRecords(id)
	if err != nil {
		t.Fatal(err)
	}
	s.EnableNodeDrain("durable")
	status := s.NodeDrainStatus()
	if !gate.draining || gate.pending || !status.ReadyToStop {
		t.Fatalf("durable queue held poll gate: %+v %+v", gate, status)
	}
	after, err := s.readTaskRecords(id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Shell[id].Status != "queued" || after.Shell[id].StartedAt != 0 || after.Shell[id].Revision != before.Shell[id].Revision || len(after.Shell[id].SealedPending) == 0 {
		t.Fatal("drain changed or lost pending work")
	}
	if release, ok := s.beginNodeSubmission("shell:local", "system_inspect"); ok {
		release()
		t.Fatal("admitted during drain")
	}
	// Work without a proven durable snapshot must still keep polling enabled.
	s.mu.Lock()
	s.shellJobs["volatile"] = &shellJob{ID: "volatile", Server: "local", Status: "queued"}
	s.mu.Unlock()
	status = s.NodeDrainStatus()
	if !gate.pending || status.ReadyToStop {
		t.Fatal("unpersisted job bypassed drain")
	}
	s.mu.Lock()
	delete(s.shellJobs, "volatile")
	s.mu.Unlock()
	status = s.NodeDrainStatus()
	if gate.pending || !status.ReadyToStop {
		t.Fatal("poll gate did not settle")
	}
	s.CancelNodeDrain()
	if gate.draining || gate.pending {
		t.Fatal("cancel did not reopen admission")
	}
}
