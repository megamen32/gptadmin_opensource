package server

import "testing"

func TestDrainPollForPendingWorkKeepsDirectCallbacksClosed(t *testing.T) {
	s := New(Config{SpillDir: t.TempDir()})
	defer s.Close()
	s.SetQueueDraining(true)
	s.SetQueueDrainPending(true)
	if _, _, ok := s.beginCallback("direct", false); ok {
		t.Fatal("direct callback admitted")
	}
	if !s.beginQueuePoll() {
		t.Fatal("existing queued work cannot be polled")
	}
	s.SetQueueDrainPending(false)
	if s.beginQueuePoll() {
		t.Fatal("new poll admitted after queue emptied")
	}
	if s.ActiveCallbacks() != 1 {
		t.Fatal("admitted poll disappeared during fence")
	}
	s.endQueuePoll()
	if s.ActiveCallbacks() != 0 {
		t.Fatal("completed poll remains counted")
	}
}
