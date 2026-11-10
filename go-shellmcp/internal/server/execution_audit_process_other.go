//go:build !linux

package server

func executionProcess(pid int) map[string]any {
	return map[string]any{"pid": pid, "ppid": nil, "exe": "unknown", "cgroup": "unknown", "status": "not_available_on_this_platform"}
}
func executionAncestors(int) []map[string]any { return []map[string]any{} }
func executionPGID(int) any                   { return nil }
