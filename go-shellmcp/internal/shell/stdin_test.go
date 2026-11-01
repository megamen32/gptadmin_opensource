package shell

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeArgvStdinIsExactAndDoesNotInvokeShell(t *testing.T) {
	if _, err := exec.LookPath("cat"); err != nil {
		t.Skip("cat not available")
	}
	marker := filepath.Join(t.TempDir(), "should-not-be-created")
	tick := string(rune(96))
	source := "multi\nline with 'single' and \"double\" quotes, $HOME, $(touch " + marker + "), " + tick + "id" + tick + " 🌍\\n"
	res := Run(context.Background(), Request{Argv: []string{"cat"}, Stdin: source, SpillDir: t.TempDir()}, 1<<20)
	if res.ReturnCode != 0 || res.Stdout != source || res.Error != "" {
		t.Fatalf("stdin corrupted: %+v", res)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("stdin executed as shell: %v", err)
	}
}

func TestNativePythonStdinScriptPreservesQuotes(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	code := "import json\nvalues = {\"apostrophe\": \"it's\", \"double\": '\"quoted\"', \"message\": \"Привет 🌍\"}\nprint(json.dumps(values, ensure_ascii=False))\n"
	res := Run(context.Background(), Request{Argv: []string{"python3", "-"}, Stdin: code, SpillDir: t.TempDir()}, 1<<20)
	if res.ReturnCode != 0 || !strings.Contains(res.Stdout, "Привет 🌍") || !strings.Contains(res.Stdout, "it's") {
		t.Fatalf("python stdin script failed: %+v", res)
	}
}

func TestNativeStdinValidationRejectsMalformedInput(t *testing.T) {
	for label, value := range map[string]string{"nul": "oops\\x00", "too_long": strings.Repeat("x", MaxStdinBytes+1), "bad_utf8": string([]byte{0xff})} {
		if label == "nul" {
			value = "oops" + string(rune(0))
		}
		req := Request{Argv: []string{"cat"}, Stdin: value, SpillDir: t.TempDir()}
		if err := ValidateCommandSpec(req); err == nil {
			t.Errorf("%s invalid stdin accepted", label)
		}
		res := Run(context.Background(), req, 512)
		if res.ReturnCode != -1 || res.Error == "" {
			t.Errorf("%s malformed stdin executed: %+v", label, res)
		}
	}
}

func TestLegacyCmdStillSupportsStdin(t *testing.T) {
	res := Run(context.Background(), Request{Cmd: "cat", Stdin: "line \"quote\"\n", SpillDir: t.TempDir()}, 1<<20)
	if res.ReturnCode != 0 || res.Stdout != "line \"quote\"\n" {
		t.Fatalf("legacy cmd with stdin failed: %+v", res)
	}
}
