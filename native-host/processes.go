// Process operations: list, execute, kill.
//
// Command execution is enabled by default per the GPTAdmin Cloud OS spec.
package main

import (
        "bufio"
        "context"
        "encoding/json"
        "fmt"
        "os/exec"
        "runtime"
        "strconv"
        "strings"
        "syscall"
        "time"
)

// ProcessEntry represents a single running process.
type ProcessEntry struct {
        PID     int    `json:"pid"`
        Name    string `json:"name"`
        User    string `json:"user"`
        CPU     string `json:"cpu"`
        Memory  string `json:"memory"`
}

// processExecParams is the expected JSON params for processes.exec.
type processExecParams struct {
        Command string   `json:"command"`
        Args    []string `json:"args"`
        CWD     string   `json:"cwd"`
        Timeout int      `json:"timeout"` // seconds; default 30
}

// processExecResult is returned by processes.exec.
type processExecResult struct {
        ExitCode int    `json:"exit_code"`
        Stdout   string `json:"stdout"`
        Stderr   string `json:"stderr"`
}

// processKillResult is returned by processes.kill.
type processKillResult struct {
        Killed bool `json:"killed"`
        PID    int  `json:"pid"`
}

const maxOutputSize = 1 * 1024 * 1024 // 1 MB per stream

// handleProcessesList lists running processes.
// On Unix it parses `ps aux`; on Windows it parses `tasklist`.
func handleProcessesList(ctx context.Context, params json.RawMessage) (interface{}, error) {
        if runtime.GOOS == "windows" {
                return listProcessesWindows()
        }
        return listProcessesUnix()
}

// listProcessesUnix runs `ps aux` and parses the output.
func listProcessesUnix() ([]ProcessEntry, error) {
        out, err := exec.Command("ps", "aux").Output()
        if err != nil {
                return nil, fmt.Errorf("running ps aux: %w", err)
        }

        var entries []ProcessEntry
        scanner := bufio.NewScanner(strings.NewReader(string(out)))

        lineNum := 0
        for scanner.Scan() {
                lineNum++
                // Skip the header line (first line).
                if lineNum == 1 {
                        continue
                }

                fields := strings.Fields(scanner.Text())
                // ps aux columns: USER PID %CPU %MEM VSZ RSS TTY STAT START TIME COMMAND
                if len(fields) < 11 {
                        continue
                }

                pid, err := strconv.Atoi(fields[1])
                if err != nil {
                        continue // skip unparseable lines
                }

                // The command may contain spaces; rejoin everything after column 10.
                name := strings.Join(fields[10:], " ")

                entries = append(entries, ProcessEntry{
                        PID:    pid,
                        User:   fields[0],
                        CPU:    fields[2],
                        Memory: fields[3],
                        Name:   name,
                })
        }

        if err := scanner.Err(); err != nil {
                // Return partial results along with the error.
                return entries, fmt.Errorf("scanning ps output: %w", err)
        }

        return entries, nil
}

// listProcessesWindows runs `tasklist` and parses the output.
func listProcessesWindows() ([]ProcessEntry, error) {
        out, err := exec.Command("tasklist", "/FO", "CSV", "/NH").Output()
        if err != nil {
                return nil, fmt.Errorf("running tasklist: %w", err)
        }

        var entries []ProcessEntry
        scanner := bufio.NewScanner(strings.NewReader(string(out)))

        for scanner.Scan() {
                line := strings.TrimSpace(scanner.Text())
                if line == "" {
                        continue
                }

                // CSV format: "name","pid","session","session#","mem"
                fields := parseCSVLine(line)
                if len(fields) < 5 {
                        continue
                }

                pid, err := strconv.Atoi(fields[1])
                if err != nil {
                        continue
                }

                entries = append(entries, ProcessEntry{
                        PID:    pid,
                        Name:   fields[0],
                        Memory: fields[4],
                })
        }

        if err := scanner.Err(); err != nil {
                return entries, fmt.Errorf("scanning tasklist output: %w", err)
        }

        return entries, nil
}

// parseCSVLine is a minimal CSV parser for simple quoted fields.
func parseCSVLine(line string) []string {
        var fields []string
        inQuote := false
        var buf strings.Builder

        for _, r := range line {
                switch r {
                case '"':
                        inQuote = !inQuote
                case ',':
                        if !inQuote {
                                fields = append(fields, buf.String())
                                buf.Reset()
                        } else {
                                buf.WriteRune(r)
                        }
                default:
                        buf.WriteRune(r)
                }
        }

        if buf.Len() > 0 {
                fields = append(fields, buf.String())
        }

        return fields
}

// handleProcessesExec executes a command with an optional timeout.
func handleProcessesExec(ctx context.Context, params json.RawMessage) (interface{}, error) {
        var p processExecParams
        if err := json.Unmarshal(params, &p); err != nil {
                return nil, fmt.Errorf("invalid params: %w", err)
        }

        if p.Command == "" {
                return nil, fmt.Errorf("command is required")
        }

        // Default timeout: 30 seconds.
        if p.Timeout <= 0 {
                p.Timeout = 30
        }

        timeoutCtx, cancel := context.WithTimeout(ctx, time.Duration(p.Timeout)*time.Second)
        defer cancel()

        cmd := exec.CommandContext(timeoutCtx, p.Command, p.Args...)
        if p.CWD != "" {
                cmd.Dir = p.CWD
        }

        // Capture stdout and stderr, limited to maxOutputSize each.
        var stdoutBuf, stderrBuf limitedBuffer
        cmd.Stdout = &stdoutBuf
        cmd.Stderr = &stderrBuf

        err := cmd.Run()

        exitCode := 0
        if err != nil {
                // Try to extract the exit code from the error.
                if exitErr, ok := err.(*exec.ExitError); ok {
                        exitCode = exitErr.ExitCode()
                } else {
                        // Context timeout or other failure.
                        exitCode = -1
                }
        }

        return processExecResult{
                ExitCode: exitCode,
                Stdout:   stdoutBuf.String(),
                Stderr:   stderrBuf.String(),
        }, nil
}

// handleProcessesKill sends SIGTERM (Unix) or taskkill (Windows) to a process.
func handleProcessesKill(ctx context.Context, params json.RawMessage) (interface{}, error) {
        var p struct {
                PID int `json:"pid"`
        }
        if err := json.Unmarshal(params, &p); err != nil {
                return nil, fmt.Errorf("invalid params: %w", err)
        }

        if p.PID <= 0 {
                return nil, fmt.Errorf("invalid pid: %d", p.PID)
        }

        if runtime.GOOS == "windows" {
                err := exec.Command("taskkill", "/F", "/PID", strconv.Itoa(p.PID)).Run()
                if err != nil {
                        return processKillResult{Killed: false, PID: p.PID},
                                fmt.Errorf("taskkill failed for pid %d: %w", p.PID, err)
                }
        } else {
                // Send SIGTERM.
                if err := syscall.Kill(p.PID, syscall.SIGTERM); err != nil {
                        return processKillResult{Killed: false, PID: p.PID},
                                fmt.Errorf("kill failed for pid %d: %w", p.PID, err)
                }
        }

        return processKillResult{Killed: true, PID: p.PID}, nil
}

// limitedBuffer is a bytes.Buffer that stops writing after maxOutputSize bytes.
type limitedBuffer struct {
        buf    []byte
        truncated bool
}

func (lb *limitedBuffer) Write(p []byte) (int, error) {
        if lb.truncated {
                return len(p), nil // silently discard after truncation
        }
        remaining := maxOutputSize - len(lb.buf)
        if remaining <= 0 {
                lb.truncated = true
                lb.buf = append(lb.buf, []byte("\n... [output truncated]")...)
                return len(p), nil
        }
        if len(p) > remaining {
                lb.buf = append(lb.buf, p[:remaining]...)
                lb.truncated = true
                lb.buf = append(lb.buf, []byte("\n... [output truncated]")...)
                return len(p), nil
        }
        lb.buf = append(lb.buf, p...)
        return len(p), nil
}

func (lb *limitedBuffer) String() string {
        return string(lb.buf)
}
