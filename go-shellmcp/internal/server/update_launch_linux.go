package server

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/sys/unix"
)

func currentUpdateLaunch() (updateLaunch, error) {
	launch, err := directUpdateLaunch()
	if err != nil || runtime.GOOS != "android" {
		return launch, err
	}
	name := filepath.Base(launch.program)
	if name != "linker64" && name != "linker" {
		return launch, nil
	}
	linker, err := os.Stat("/system/bin/" + name)
	if err != nil {
		return updateLaunch{}, err
	}
	running, err := os.Stat("/proc/self/exe")
	if err != nil || !os.SameFile(linker, running) {
		return updateLaunch{}, errors.New("unrecognized Android system linker")
	}
	cmdline, err := os.ReadFile("/proc/self/cmdline")
	if err != nil {
		return updateLaunch{}, err
	}
	maps, err := os.Open("/proc/self/maps")
	if err != nil {
		return updateLaunch{}, err
	}
	defer maps.Close()
	return termuxUpdateLaunch(launch.program, cmdline, maps)
}

// Termux can exec the system linker with the client path in the kernel's
// argv[1]. Bionic has already shifted argv by the time Go sees os.Args.
func termuxUpdateLaunch(linker string, cmdline []byte, maps io.Reader) (updateLaunch, error) {
	raw, terminated := strings.CutSuffix(string(cmdline), "\x00")
	argv := strings.Split(raw, "\x00")
	if !terminated || len(argv) < 2 || argv[0] == "" || !filepath.IsAbs(argv[1]) {
		return updateLaunch{}, errors.New("invalid Android linker command line")
	}
	binary, err := filepath.EvalSymlinks(argv[1])
	if err != nil {
		return updateLaunch{}, err
	}
	if binary == linker {
		return updateLaunch{}, errors.New("refusing to replace the Android system linker")
	}
	var st unix.Stat_t
	if err := unix.Stat(binary, &st); err != nil {
		return updateLaunch{}, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0111 == 0 {
		return updateLaunch{}, errors.New("Android linker argument is not an executable file")
	}
	// Other executable mappings include the linker and preloaded libraries.
	// Our own Go instruction address identifies the client's text mapping.
	pc, _, _, ok := runtime.Caller(0)
	if !ok {
		return updateLaunch{}, errors.New("cannot locate ShellMCP code")
	}
	lines := bufio.NewScanner(maps)
	for lines.Scan() {
		line := lines.Text()
		pathStart := strings.IndexByte(line, '/')
		if pathStart < 0 || (line[pathStart:] != binary && line[pathStart:] != argv[1]) {
			continue
		}
		var start, end, offset, inode uint64
		var major, minor uint32
		var perms string
		_, err := fmt.Sscanf(line[:pathStart], "%x-%x %s %x %x:%x %d", &start, &end, &perms, &offset, &major, &minor, &inode)
		if err == nil && len(perms) == 4 && perms[2] == 'x' && start <= uint64(pc) && uint64(pc) < end &&
			unix.Mkdev(major, minor) == uint64(st.Dev) && inode == st.Ino {
			return updateLaunch{binary: binary, program: linker, argv: argv}, nil
		}
	}
	if err := lines.Err(); err != nil {
		return updateLaunch{}, err
	}
	return updateLaunch{}, errors.New("Android linker argument does not match the running ShellMCP executable mapping")
}
