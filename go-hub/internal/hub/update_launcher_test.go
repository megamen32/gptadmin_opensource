package hub

import (
	"os"
	"runtime"
	"strconv"
	"testing"
)

func TestDefaultUpdateLauncher(t *testing.T) {
	// Make sure no leftover env from the parent shell leaks into this test.
	t.Setenv("GPTADMIN_SERVICE_SUFFIX", "")

	l := DefaultUpdateLauncher()
	if l.ServiceUnit != "gptadmin-auto-update.service" {
		t.Errorf("unexpected service unit: %q", l.ServiceUnit)
	}
	// Label MUST match SVC_AUTO_UPDATE_LABEL in cli.py so launchctl can find
	// the loaded plist. Both default to "com.gptadmin.auto-update".
	if l.Label != "com.gptadmin.auto-update" {
		t.Errorf("unexpected launchd label: %q", l.Label)
	}
	if l.WrapperPath == "" {
		t.Error("wrapper path should not be empty")
	}
}

func TestDefaultUpdateLauncherHonorsServiceSuffix(t *testing.T) {
	// Parallel e2e installs use GPTADMIN_SERVICE_SUFFIX to namespace the
	// launchd labels. The hub MUST mirror the Python construction or its
	// `launchctl kickstart` will hit "Could not find service" on a suffixed
	// install.
	cases := []struct {
		raw   string
		want  string
	}{
		{".e2e42", "com.gptadmin.e2e42.auto-update"},
		{"-staging", "com.gptadmin-staging.auto-update"},
		{"_ci", "com.gptadmin_ci.auto-update"},
		// Leading/trailing whitespace is stripped by the Python side too.
		{"  .trimme  ", "com.gptadmin.trimme.auto-update"},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			t.Setenv("GPTADMIN_SERVICE_SUFFIX", tc.raw)
			l := DefaultUpdateLauncher()
			if l.Label != tc.want {
				t.Errorf("Label with suffix %q = %q, want %q", tc.raw, l.Label, tc.want)
			}
		})
	}
}

func TestDefaultUpdateLauncherDropsMalformedSuffix(t *testing.T) {
	// cli.py rejects any suffix that does not match [A-Za-z0-9_.-]+; Go
	// cannot replicate the validator exactly (the regex is shared between
	// sides in spirit) but it must fall back to the default label rather
	// than splice garbage into the launchd target.
	cases := []string{"bad space", "semi;colon", "slash/infix"}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			t.Setenv("GPTADMIN_SERVICE_SUFFIX", raw)
			l := DefaultUpdateLauncher()
			if l.Label != "com.gptadmin.auto-update" {
				t.Errorf("malformed suffix %q produced label %q, want default",
					raw, l.Label)
			}
		})
	}
}

func TestDefaultUpdateLauncherSuffixMatchesPython(t *testing.T) {
	// Cross-check the Go construction against the Python one for a few
	// canonical inputs. If Python ever changes its construction (e.g.,
	// changes the prefix, alters the join character) this test will fire
	// and force the two sides back into lockstep.
	py := []struct {
		env   string
		label string
	}{
		{"", "com.gptadmin.auto-update"},
		{".e2e42", "com.gptadmin.e2e42.auto-update"},
		{"-foo_bar", "com.gptadmin-foo_bar.auto-update"},
	}
	for _, tc := range py {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv("GPTADMIN_SERVICE_SUFFIX", tc.env)
			if got := DefaultUpdateLauncher().Label; got != tc.label {
				t.Errorf("Label mismatch with Python: env=%q got=%q want=%q",
					tc.env, got, tc.label)
			}
		})
	}
}

func TestDomainUserInstall(t *testing.T) {
	// Domain is built from os.Getuid() in user mode. Just assert the prefix
	// and that the uid actually appears at the end.
	l := &UpdateLauncher{IsUserInstall: true, Label: "com.gptadmin.auto-update"}
	got := l.domain()
	want := "gui/" + strconv.Itoa(os.Getuid())
	if got != want {
		t.Errorf("user domain = %q, want %q", got, want)
	}
}

func TestDomainSystemInstall(t *testing.T) {
	l := &UpdateLauncher{IsUserInstall: false, Label: "com.gptadmin.auto-update"}
	got := l.domain()
	if got != "system" {
		t.Errorf("system domain = %q, want %q", got, "system")
	}
}

func TestLaunchUpdateUnsupportedOS(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return // test runs on linux/darwin
	}
	// Create a minimal wrapper.
	dir := t.TempDir()
	wrapper := dir + "/run_auto_update.sh"
	os.WriteFile(wrapper, []byte("#!/bin/sh\necho ok\nexit 0"), 0755)

	l := &UpdateLauncher{
		ServiceUnit:   "gptadmin-auto-update.service",
		Label:         "com.gptadmin.auto-update",
		WrapperPath:   wrapper,
		LogPath:       dir + "/log.txt",
		IsUserInstall: os.Getenv("GPTADMIN_INSTALL_MODE") == "user",
	}

	// CheckRunning should not crash.
	_ = l.CheckUpdateRunning()

	// LaunchUpdate through systemd might not work in test (no systemd --user in CI).
	// But it should not panic.
	if runtime.GOOS == "linux" {
		err := l.LaunchUpdate()
		// May fail if systemd not available — that's fine.
		t.Logf("LaunchUpdate result: %v", err)
	}
}