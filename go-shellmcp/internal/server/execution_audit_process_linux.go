//go:build linux

package server

import (
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func executionProcess(pid int) map[string]any {
	result := map[string]any{"pid": pid, "ppid": nil, "exe": "unknown", "cgroup": "unknown", "status": "not_available"}
	root := "/proc/" + strconv.Itoa(pid)
	if exe, err := os.Readlink(root + "/exe"); err == nil {
		result["exe"] = auditText(exe)
	}
	if file, err := os.Open(root + "/cgroup"); err == nil {
		data, _ := io.ReadAll(io.LimitReader(file, 4096))
		file.Close()
		result["cgroup"] = auditText(string(data))
	}
	if data, err := os.ReadFile(root + "/stat"); err == nil {
		if end := strings.LastIndexByte(string(data), ')'); end >= 0 {
			fields := strings.Fields(string(data)[end+1:])
			if len(fields) > 1 {
				if parent, err := strconv.Atoi(fields[1]); err == nil {
					result["ppid"] = parent
					result["status"] = "observed"
				}
			}
		}
	}
	return result
}
func executionAncestors(pid int) []map[string]any {
	result := []map[string]any{}
	seen := map[int]bool{}
	for len(result) < 6 && pid > 0 && !seen[pid] {
		seen[pid] = true
		entry := executionProcess(pid)
		result = append(result, entry)
		parent, ok := entry["ppid"].(int)
		if !ok {
			break
		}
		pid = parent
	}
	return result
}
func executionPGID(pid int) any {
	value, err := syscall.Getpgid(pid)
	if err != nil {
		return nil
	}
	return value
}
