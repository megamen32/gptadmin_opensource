package hub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

type hubHandoverState struct {
	Status string `json:"status"`
	Action string `json:"action"`
	TS     int64  `json:"ts"`
	Detail string `json:"detail"`
}

func readHubHandoverState() hubHandoverState {
	b, err := os.ReadFile("/run/gptadmin-handover.json")
	if err != nil {
		return hubHandoverState{Status: "idle"}
	}
	var st hubHandoverState
	if json.Unmarshal(b, &st) != nil {
		return hubHandoverState{Status: "unknown", Detail: "invalid handover state"}
	}
	if st.Status == "" {
		st.Status = "idle"
	}
	return st
}

func localHealth(url string) map[string]any {
	c := http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(strings.TrimRight(url, "/") + "/healthz")
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	defer resp.Body.Close()
	return map[string]any{"ok": resp.StatusCode >= 200 && resp.StatusCode < 300, "status": resp.StatusCode}
}

func (s *Server) startHubHandover(actor string) (map[string]any, int) {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return map[string]any{"error": "zero-downtime handover requires a system Linux Hub"}, http.StatusNotImplemented
	}
	home := strings.TrimSpace(os.Getenv("GPTADMIN_HOME"))
	if home == "" {
		home = "/opt/gptadmin"
	}
	helper := strings.TrimSpace(os.Getenv("GPTADMIN_HANDOVER_HELPER"))
	if helper == "" {
		helper = filepath.Join(home, "bin", "gptadmin-handover")
	}
	if _, err := os.Stat(helper); err != nil {
		return map[string]any{"error": "handover helper is not installed", "helper": helper}, http.StatusPreconditionFailed
	}
	current := readHubHandoverState()
	if current.Status == "running" {
		return map[string]any{"error": "handover already running", "state": current}, http.StatusConflict
	}
	unit := fmt.Sprintf("gptadmin-handover-api-%d", time.Now().UnixNano())
	cmd := exec.Command("systemd-run", "--unit="+unit, "--on-active=5s", helper, "restart-primary")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return map[string]any{"error": "failed to schedule handover", "detail": string(out)}, http.StatusInternalServerError
	}
	s.mu.Lock()
	s.addAuditLocked("hub_handover_scheduled", map[string]any{"actor": actor, "unit": unit})
	s.mu.Unlock()
	return map[string]any{"accepted": true, "unit": unit, "delay_seconds": 5, "state_path": "/run/gptadmin-handover.json"}, http.StatusAccepted
}

func (s *Server) diagnoseHub() map[string]any {
	s.mu.Lock()
	statuses := map[string]int{}
	kinds := map[string]int{}
	oldestRunning := float64(0)
	runningJobs := 0
	failedJobs := 0
	for _, a := range s.agents {
		if a == nil {
			continue
		}
		statuses[a.Status]++
		kinds[a.Kind]++
	}
	for _, j := range s.shellJobs {
		if j == nil {
			continue
		}
		if j.Status == "running" || j.Status == "queued" {
			runningJobs++
			if oldestRunning == 0 || j.CreatedAt < oldestRunning {
				oldestRunning = j.CreatedAt
			}
		}
		if j.Status == "failed" {
			failedJobs++
		}
	}
	for _, j := range s.relayJobs {
		if j == nil {
			continue
		}
		if j.Status == "running" || j.Status == "queued" {
			runningJobs++
			if oldestRunning == 0 || j.CreatedAt < oldestRunning {
				oldestRunning = j.CreatedAt
			}
		}
		if j.Status == "failed" {
			failedJobs++
		}
	}
	settings := s.hubSettingsLocked()
	revision := s.settingsRevision
	historyCount := len(s.settingsHistory)
	tombstoneCount := len(s.tombstones)
	candidates := s.cleanupCandidatesLocked()
	shellObs := map[string]map[string]any{}
	desiredBuild := s.hubSettingIntLocked("fleet_desired_build_version")
	if desiredBuild <= 0 {
		desiredBuild = intFromAny(BuildVersion)
	}
	selfRepairEnabled := s.hubSettingBoolLocked("fleet_auto_update_enabled")
	outdatedAgents := []string{}
	failedRepairAgents := []string{}
	totals := map[string]int64{"storage_bytes": 0, "spool_bytes": 0, "outbox_depth": 0, "outbox_retry_attempts": 0, "queue_poll_errors": 0, "outbox_retry_failures": 0, "outbox_delivered": 0}
	for id, a := range s.agents {
		if a == nil || a.Kind != "virtual_shell" || a.Meta == nil {
			continue
		}
		item := map[string]any{}
		for _, key := range []string{"storage_bytes", "spool_bytes", "outbox_depth", "outbox_retry_attempts", "queue_poll_count", "queue_poll_errors", "queue_poll_latency_ms", "outbox_retry_failures", "outbox_delivered"} {
			if v, ok := a.Meta[key]; ok {
				item[key] = v
				if _, sum := totals[key]; sum {
					totals[key] += int64(intFromAny(v))
				}
			}
		}
		if len(item) > 0 {
			shellObs[id] = item
		}
		build := intFromAny(a.Meta["build_version"])
		if desiredBuild > 0 && build > 0 && build < desiredBuild {
			outdatedAgents = append(outdatedAgents, id)
		}
		if firstString(a.Meta, "self_repair_state") == "failed" {
			failedRepairAgents = append(failedRepairAgents, id)
		}
	}
	candidateEligible := 0
	for _, item := range candidates {
		if item["eligible"] == true {
			candidateEligible++
		}
	}
	s.mu.Unlock()
	warnings := []string{}
	if statuses["stale"] > 0 {
		warnings = append(warnings, fmt.Sprintf("%d stale agents", statuses["stale"]))
	}
	if failedJobs > 0 {
		warnings = append(warnings, fmt.Sprintf("%d failed jobs retained", failedJobs))
	}
	if candidateEligible > 0 {
		warnings = append(warnings, fmt.Sprintf("%d stale MCP eligible for cleanup", candidateEligible))
	}
	if len(outdatedAgents) > 0 {
		warnings = append(warnings, fmt.Sprintf("%d ShellMCP agents below desired build %d", len(outdatedAgents), desiredBuild))
	}
	if len(failedRepairAgents) > 0 {
		warnings = append(warnings, fmt.Sprintf("%d ShellMCP self-repair failures", len(failedRepairAgents)))
	}
	sort.Strings(warnings)
	return map[string]any{
		"ok":                  true,
		"build":               map[string]any{"version": BuildVersion, "git_commit": GitCommit},
		"health":              map[string]any{"primary": localHealth("http://127.0.0.1:9001"), "standby": localHealth("http://127.0.0.1:19001")},
		"handover":            readHubHandoverState(),
		"agents":              map[string]any{"total": lenFromCounts(statuses), "by_status": statuses, "by_kind": kinds},
		"jobs":                map[string]any{"running_or_queued": runningJobs, "failed_retained": failedJobs, "oldest_running_created_at": oldestRunning},
		"settings":            map[string]any{"revision": revision, "history_count": historyCount, "values": settings},
		"registry":            map[string]any{"cleanup_candidates": len(candidates), "cleanup_eligible": candidateEligible, "tombstones": tombstoneCount},
		"shell_observability": map[string]any{"totals": totals, "by_agent": shellObs},
		"self_repair":         map[string]any{"enabled": selfRepairEnabled, "desired_build": desiredBuild, "release_repo": "megamen32/gptadmin_opensource", "outdated_agents": outdatedAgents, "failed_agents": failedRepairAgents},
		"warnings":            warnings,
		"checked_at":          s.now().Format(time.RFC3339),
	}
}

func lenFromCounts(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}
