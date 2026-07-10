package system

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
)

type Info struct {
	Host  string `json:"host"`
	Cores int    `json:"cores"`
	MemMB int64  `json:"mem_mb"`
	OS    string `json:"os"`
}

func Get() Info {
	host, _ := os.Hostname()
	return Info{Host: host, Cores: runtime.NumCPU(), MemMB: memTotalMB(), OS: runtime.GOOS + " " + runtime.GOARCH}
}

func memTotalMB() int64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "MemTotal:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				kb, _ := strconv.ParseInt(fields[1], 10, 64)
				return kb / 1024
			}
		}
	}
	return 0
}
