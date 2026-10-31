package hub

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// This is intentionally a read-only, bounded view of the GPTAdmin Hub unit.
// Never accept a service name, command, cursor or filesystem path from the UI.
const consoleUnit = "gptadmin-hub.service"
const consoleMaxBytes = 2 << 20

type consoleEntry struct {
	Time    string `json:"time"`
	Level   string `json:"level"`
	Message string `json:"message"`
	Unit    string `json:"unit"`
}

var (
	consoleBearer      = regexp.MustCompile(`(?i)\bbearer\s+[^\s,;"]+`)
	consoleJWT         = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b`)
	consoleSecret      = regexp.MustCompile(`(?i)((?:["']?)(?:password|access[_-]?token|refresh[_-]?token|api[_-]?key|secret|authorization|cookie|jwt|token)(?:["']?)\s*[:=]\s*["']?)[^\s,;"'}&]+`)
	consoleQuerySecret = regexp.MustCompile(`(?i)([?&](?:api[_-]?key|token|key|password|secret|code)=)[^&#\s]+`)
)

func redactConsoleMessage(message string) string {
	message = consoleBearer.ReplaceAllString(message, "Bearer [redacted]")
	message = consoleJWT.ReplaceAllString(message, "[redacted-jwt]")
	message = consoleSecret.ReplaceAllString(message, "${1}[redacted]")
	message = consoleQuerySecret.ReplaceAllString(message, "${1}[redacted]")
	runes := []rune(message)
	if len(runes) > 4000 {
		return string(runes[:4000]) + "… [truncated]"
	}
	return message
}

func consolePriority(raw string) string {
	switch raw {
	case "0", "1", "2", "3":
		return "error"
	case "4":
		return "warn"
	case "7":
		return "debug"
	default:
		return "info"
	}
}

func parseConsoleEntries(input []byte) []consoleEntry {
	results := make([]consoleEntry, 0)
	scanner := bufio.NewScanner(bytes.NewReader(input))
	scanner.Buffer(make([]byte, 0, 8192), 512*1024)
	for scanner.Scan() {
		var raw struct {
			Message      string `json:"MESSAGE"`
			Priority     string `json:"PRIORITY"`
			Microseconds string `json:"__REALTIME_TIMESTAMP"`
		}
		if json.Unmarshal(scanner.Bytes(), &raw) != nil || raw.Message == "" {
			continue
		}
		stamp := ""
		if n, err := strconv.ParseInt(raw.Microseconds, 10, 64); err == nil && n > 0 {
			stamp = time.UnixMicro(n).UTC().Format(time.RFC3339Nano)
		}
		results = append(results, consoleEntry{
			Time:    stamp,
			Level:   consolePriority(raw.Priority),
			Message: redactConsoleMessage(raw.Message),
			Unit:    consoleUnit,
		})
	}
	return results
}

func (s *Server) adminConsole(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	limit := 200
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 500 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "limit must be between 1 and 500"})
			return
		}
		limit = n
	}

	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "journalctl", "--unit="+consoleUnit,
		"--lines="+strconv.Itoa(limit), "--output=json", "--no-pager", "--quiet")
	stdout, err := command.StdoutPipe()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "journal unavailable"})
		return
	}
	if err = command.Start(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "journal unavailable"})
		return
	}
	output, err := io.ReadAll(io.LimitReader(stdout, consoleMaxBytes+1))
	if err != nil || len(output) > consoleMaxBytes {
		_ = command.Process.Kill()
		_ = command.Wait()
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "journal result exceeded safe limit"})
		return
	}
	if err = command.Wait(); err != nil || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "journal unavailable"})
		return
	}
	entries := parseConsoleEntries(output)
	writeJSON(w, http.StatusOK, map[string]any{"unit": consoleUnit, "entries": entries, "count": len(entries)})
}
