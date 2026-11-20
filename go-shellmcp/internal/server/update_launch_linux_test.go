package server

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestTermuxUpdateLaunchPreservesKernelArgv(t *testing.T) {
	// Use the real mapping that contains this test binary's Go code.
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(exe) == "linker64" {
		raw, err := os.ReadFile("/proc/self/cmdline")
		if err != nil {
			t.Fatal(err)
		}
		fields := bytes.Split(raw, []byte{0})
		if len(fields) < 3 {
			t.Fatal("missing test binary in linker command line")
		}
		exe = string(fields[1])
	}
	maps, err := os.ReadFile("/proc/self/maps")
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "client with spaces")
	if err := os.Symlink(exe, alias); err != nil {
		t.Fatal(err)
	}
	const linker = "/system/bin/linker64"
	for _, args := range [][]string{{"gptadmin-shellmcp", exe}, {"gptadmin-shellmcp", alias, "two words", ""}} {
		cmdline := []byte(strings.Join(args, "\x00") + "\x00")
		launch, err := termuxUpdateLaunch(linker, cmdline, bytes.NewReader(maps))
		if err != nil || launch.binary != exe || launch.program != linker || !slices.Equal(launch.argv, args) {
			t.Fatalf("launch=%+v err=%v, want binary %q and argv %q", launch, err, exe, args)
		}
	}
}

func TestTermuxUpdateLaunchRejectsUnverifiedTarget(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "gptadmin-shellmcp")
	if err := os.WriteFile(exe, []byte("test"), 0755); err != nil {
		t.Fatal(err)
	}
	var st unix.Stat_t
	if err := unix.Stat(exe, &st); err != nil {
		t.Fatal(err)
	}
	dev := fmt.Sprintf("%02x:%02x", unix.Major(uint64(st.Dev)), unix.Minor(uint64(st.Dev)))
	row := fmt.Sprintf("0-ffffffffffffffff r-xp 00000000 %s %d %s\n", dev, st.Ino, exe)
	cmdline := "gptadmin-shellmcp\x00" + exe + "\x00"
	const linker = "/system/bin/linker64"
	for _, tc := range []struct{ name, args, maps string }{
		{"missing client", "gptadmin-shellmcp\x00", row},
		{"relative client", "gptadmin-shellmcp\x00gptadmin-shellmcp\x00", row},
		{"truncated cmdline", strings.TrimSuffix(cmdline, "\x00"), row},
		{"linker as client", "gptadmin-shellmcp\x00" + linker + "\x00", row},
		{"missing mapping", cmdline, ""},
		{"data mapping", cmdline, strings.Replace(row, "r-xp", "r--p", 1)},
		{"different code address", cmdline, strings.Replace(row, "0-ffffffffffffffff", "0-1000", 1)},
		{"different device", cmdline, strings.Replace(row, " "+dev+" ", " ff:ffff ", 1)},
		{"replaced inode", cmdline, strings.Replace(row, fmt.Sprintf(" %d ", st.Ino), fmt.Sprintf(" %d ", st.Ino+1), 1)},
		{"different path", cmdline, strings.Replace(row, exe, exe+".so", 1)},
		{"deleted mapping", cmdline, strings.Replace(row, exe, exe+" (deleted)", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			launch, err := termuxUpdateLaunch(linker, []byte(tc.args), strings.NewReader(tc.maps))
			if err == nil || launch.binary != "" || launch.program != "" {
				t.Fatalf("unverified launch=%+v, err=%v", launch, err)
			}
		})
	}
}
