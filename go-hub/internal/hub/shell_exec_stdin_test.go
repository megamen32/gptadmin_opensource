package hub

import (
	"strings"
	"testing"
)

func TestHubShellExecValidatesStructuredStdin(t *testing.T) {
	valid := []map[string]any{
		{"argv": []any{"python3", "-"}, "stdin": "print(\"it's <safe> 🌍\")\n"},
		{"cmd": "cat", "stdin": "quoted \"text\"\n"},
	}
	for _, args := range valid {
		if err := validateShellExecArguments(args); err != nil {
			t.Fatalf("valid stdin refused: %v", err)
		}
	}
	invalid := []map[string]any{
		{"argv": []any{"cat"}, "stdin": 12},
		{"argv": []any{"cat"}, "stdin": nil},
		{"argv": []any{"cat"}, "stdin": "nul" + string(rune(0))},
		{"argv": []any{"cat"}, "stdin": strings.Repeat("x", 262145)},
		{"argv": []any{"cat"}, "stdin": string([]byte{0xff})},
	}
	for _, args := range invalid {
		if err := validateShellExecArguments(args); err == nil {
			t.Fatalf("invalid stdin accepted")
		}
	}
}
