package hub

import (
	"errors"
	"reflect"
	"testing"
)

// Fast unit, expected1s/max10s: bounded capability selection only. No runtime claim.
func TestQueueChoiceLegacyHeadAllowsCompatibleTail(t *testing.T) {
	q := []string{"large", "small", "later"}
	choice, err := chooseShellQueueEnvelope(q, 8, func(id string) ([]byte, bool, error) {
		if id == "large" {
			return make([]byte, 8), true, nil
		}
		return []byte("{}"), true, nil
	})
	if err != nil || choice.ID != "small" || !reflect.DeepEqual(choice.Queue, []string{"large", "later"}) {
		t.Fatalf("compatible tail blocked: %+v %v", choice, err)
	}
	if !reflect.DeepEqual(q, []string{"large", "small", "later"}) {
		t.Fatal("original backing array mutated")
	}
	// Same original large ID becomes eligible after the reader update.
	updated, err := chooseShellQueueEnvelope(choice.Queue, 64, func(string) ([]byte, bool, error) { return make([]byte, 8), true, nil })
	if err != nil || updated.ID != "large" {
		t.Fatal("original incompatible job lost")
	}
}

func TestQueueChoiceUnknownEncodingFailsWithoutSelectingTail(t *testing.T) {
	calls := 0
	failure := errors.New("invalid envelope")
	choice, err := chooseShellQueueEnvelope([]string{"bad", "small"}, 8, func(string) ([]byte, bool, error) { calls++; return nil, true, failure })
	if err != failure || choice.ID != "" || calls != 1 {
		t.Fatal("unknown envelope bypassed")
	}
}

func TestQueueChoiceBoundsInspectionAndPreservesBlockedOrder(t *testing.T) {
	q := make([]string, 17)
	for i := range q {
		q[i] = string(rune('a' + i))
	}
	calls := 0
	choice, err := chooseShellQueueEnvelope(q, 8, func(string) ([]byte, bool, error) { calls++; return make([]byte, 8), true, nil })
	if err != nil || calls != 16 || !choice.WindowExhausted || !choice.Oversized || !reflect.DeepEqual(choice.Queue, q) {
		t.Fatalf("unbounded or reordered inspection: %+v calls=%d", choice, calls)
	}
}

func TestQueueChoicePrunesOnlyKnownTerminalWithinWindow(t *testing.T) {
	q := make([]string, 17)
	for i := range q {
		q[i] = "terminal"
	}
	choice, err := chooseShellQueueEnvelope(q, 8, func(string) ([]byte, bool, error) { return nil, false, nil })
	if err != nil || choice.ID != "" || !choice.WindowExhausted || !reflect.DeepEqual(choice.Queue, []string{"terminal"}) {
		t.Fatal("terminal window made no progress or dropped unknown tail")
	}
}
