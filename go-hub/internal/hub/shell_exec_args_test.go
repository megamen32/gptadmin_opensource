package hub

import (
	"strings"
	"testing"
)

func TestValidateShellExecArgumentsAllowsLiteralArgvAndLegacyCmd(t *testing.T) {
	for _, args := range []map[string]any{
		{"cmd": "printf legacy"},
		{"command": "printf legacy-alias"},
		{"argv": []any{"printf", "%s", "one ' and \"two\" $HOME $(echo ignored) 🌍"}},
		{"argv": []string{"printf", ""}},
	} {
		if err := validateShellExecArguments(args); err != nil {
			t.Fatalf("valid arguments rejected: %v %#v", err, args)
		}
	}
}

func TestValidateShellExecArgumentsRejectsBadRequestsWithoutQueueing(t *testing.T) {
	many := make([]any, 257)
	for i := range many {
		many[i] = "ok"
	}
	for label, args := range map[string]map[string]any{
		"missing":   {},
		"both":      {"cmd": "true", "argv": []any{"printf", "bad"}},
		"empty":     {"argv": []any{}},
		"number":    {"argv": []any{"printf", 5}},
		"null":      {"argv": nil},
		"program":   {"argv": []any{""}},
		"nul":       {"argv": []any{"printf", "bad\x00arg"}},
		"too_many":  {"argv": many},
		"too_large": {"argv": []any{"printf", strings.Repeat("A", 65537)}},
	} {
		if err := validateShellExecArguments(args); err == nil {
			t.Errorf("%s invalid arguments accepted: %#v", label, args)
		}
	}
}
