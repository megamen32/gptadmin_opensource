package server

// Pure local admission checks only, below runtime/integration acceptance.
import (
	"strings"
	"testing"
)

func TestChildConnectionInstanceAdmission(t *testing.T) {
	current := strings.Repeat("a", 64)
	scope := strings.Repeat("c", 64)
	if err := validateChildConnectionInstance(current, map[string]any{"_connection_instance": current}, scope); err != nil {
		t.Fatal(err)
	}
	for _, args := range []map[string]any{nil, {"_connection_instance": strings.Repeat("b", 64)}, {"_connection_instance": "bad"}, {"_connection_instance": 7}} {
		if err := validateChildConnectionInstance(current, args, scope); err == nil {
			t.Fatal("missing, malformed or old connector instance admitted")
		}
	}
	if err := validateChildConnectionInstance("", map[string]any{"_connection_instance": current}, scope); err == nil {
		t.Fatal("empty connector instance admitted scoped operation")
	}
	if err := validateChildConnectionInstance("", nil, ""); err != nil {
		t.Fatal("legacy unscoped behavior changed")
	}
}
