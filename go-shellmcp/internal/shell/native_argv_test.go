package shell

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestNativeArgvExecutesWithoutShellParsing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell command utilities")
	}
	marker := filepath.Join(t.TempDir(), "should-not-exist")
	tick := string(rune(96))
	payload := "single ' double \" JSON {\"name\":\"test\"} backslash \\\\ tabs \t emoji 🌍\n" +
		"a=$(touch " + marker + "); " + tick + "uname" + tick + " && echo EXPANDED ; | >"
	req := Request{Argv: []string{"printf", "%s", payload}, SpillDir: t.TempDir()}
	res := Run(context.Background(), req, 1<<20)
	if res.ReturnCode != 0 || res.Stdout != payload || res.Stderr != "" || res.TimedOut {
		t.Fatalf("argv was parsed or corrupted: %+v", res)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("shell command substitution executed: %v", err)
	}
	cmd, _ := buildCommand(context.Background(), req)
	if filepath.Base(cmd.Path) == "sh" || filepath.Base(cmd.Path) == "bash" ||
		strings.Contains(strings.Join(cmd.Args, " "), "-c") {
		t.Fatalf("argv unexpectedly used a shell: %+v", cmd.Args)
	}
}

func TestNativeArgvValidatesArguments(t *testing.T) {
	many := make([]string, MaxArgvCount+1)
	for i := range many {
		many[i] = "x"
	}
	invalidUTF8 := string([]byte{0xff, 0xfe})
	samples := []Request{
		{},
		{Argv: []string{}},
		{Argv: []string{"printf"}, Cmd: "echo unsafe"},
		{Argv: []string{""}},
		{Argv: []string{" "}},
		{Argv: []string{"printf", "hi\x00there"}},
		{Argv: []string{"printf", invalidUTF8}},
		{Argv: many},
		{Argv: []string{"printf", strings.Repeat("a", MaxArgvBytes+1)}},
	}
	for i, req := range samples {
		if err := ValidateCommandSpec(req); err == nil {
			t.Errorf("case %d invalid request accepted: %+v", i, req)
		}
		req.SpillDir = t.TempDir()
		res := Run(context.Background(), req, 1024)
		if res.ReturnCode != -1 || res.Error == "" {
			t.Errorf("case %d invalid request executed: %+v", i, res)
		}
	}
	if err := ValidateCommandSpec(Request{Cmd: "printf plain"}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCommandSpec(Request{Argv: []string{"printf", ""}}); err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString("Привет") {
		t.Fatal("bad test literal")
	}
}

func TestNativeArgvKeepsCwdAndNonZeroExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX commands")
	}
	cwd := t.TempDir()
	res := Run(context.Background(), Request{Argv: []string{"pwd"}, Cwd: cwd, SpillDir: t.TempDir()}, 4096)
	if res.ReturnCode != 0 || strings.TrimSpace(res.Stdout) != cwd {
		t.Fatalf("working directory was lost: %+v", res)
	}
	failed := Run(context.Background(), Request{Argv: []string{"sh", "-c", "echo invalid >&2; exit 7"}, SpillDir: t.TempDir()}, 4096)
	if failed.ReturnCode != 7 || !strings.Contains(failed.Stderr, "invalid") {
		t.Fatalf("nonzero exit not preserved: %+v", failed)
	}
	if _, err := exec.LookPath("printf"); err != nil {
		t.Fatal(err)
	}
}

func TestNativeArgvUserSelectionPreservesPrivilegeBoundaries(t *testing.T) {
	ordinary := Request{Argv: []string{"printf", "hello"}, DefaultUser: "admin"}
	if user, explicit := TargetRunUser(ordinary); user != "admin" || explicit {
		t.Fatalf("wrong default user: %s %v", user, explicit)
	}
	elevated := Request{Argv: []string{"sudo", "-n", "id"}, DefaultUser: "admin"}
	if user, _ := TargetRunUser(elevated); user != "" {
		t.Fatalf("explicit sudo should not be downshifted: %s", user)
	}
	explicit := Request{Argv: []string{"printf", "%s", "arg ' $HOME"}, RunAsUser: "unprivileged"}
	cmd, _ := buildCommand(context.Background(), explicit)
	if runtime.GOOS != "windows" {
		if filepath.Base(cmd.Path) != "sudo" || strings.Join(cmd.Args, " ") == "" ||
			cmd.Args[len(cmd.Args)-1] != "arg ' $HOME" {
			t.Fatalf("sudo argv was shell-rewritten: %#v", cmd.Args)
		}
	}
	if os.Geteuid() == 0 {
		res := Run(context.Background(), Request{Argv: []string{"id", "-u"}, SpillDir: t.TempDir()}, 1024)
		if res.ReturnCode == 0 || !strings.Contains(res.Error, "default user") {
			t.Fatalf("implicit root execution was not rejected: %+v", res)
		}
	}
}
