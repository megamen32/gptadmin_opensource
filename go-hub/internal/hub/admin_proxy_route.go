package hub

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	proxyRouteProfilePath         = "/var/lib/gptadmin/proxy-route/profile.json"
	proxyRouteProbePath           = "/var/lib/gptadmin/proxy-route/last-probe.json"
	proxyRouteUnit                = "gptadmin-proxy-route-relay02.service"
	proxyRoutePort                = 24836
	proxyRouteExitAddress         = "203.0.113.10:3128"
	proxyRouteProbeURL            = "https://api.ipify.org"
	proxyRoutePathReadbackTimeout = 11 * time.Second
)

type proxyRouteProfile struct {
	Schema   string `json:"schema"`
	ID       string `json:"id"`
	Consumer string `json:"consumer"`
	Exit     string `json:"exit"`
	Listener struct {
		BindAddress string `json:"bind_address"`
		Port        int    `json:"port"`
	} `json:"listener"`
	SSHIdentityRef string `json:"ssh_identity_ref"`
	DesiredState   string `json:"desired_state"`
}

type proxyRouteProbe struct {
	State                string  `json:"state"`
	CheckedAt            *string `json:"checked_at"`
	DirectExitIP         *string `json:"direct_exit_ip"`
	ProxyExitIP          *string `json:"proxy_exit_ip"`
	Server44PathVerified *bool   `json:"server44_path_verified"`
	Server44PathReason   *string `json:"server44_path_reason"`
}

type proxyRouteState struct {
	Profile proxyRouteProfile `json:"profile"`
	Runtime struct {
		ProfileSaved  bool             `json:"profile_saved"`
		ServiceState  string           `json:"service_state"`
		ListenerState string           `json:"listener_state"`
		LastProbe     *proxyRouteProbe `json:"last_probe"`
	} `json:"runtime"`
}

type proxyRouteRunner func(context.Context, string, ...string) ([]byte, error)

type proxyRouteController struct {
	mu          sync.Mutex
	profilePath string
	probePath   string
	run         proxyRouteRunner
}

func newProxyRouteController(profilePath, probePath string, runner proxyRouteRunner) *proxyRouteController {
	if runner == nil {
		runner = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		}
	}
	return &proxyRouteController{profilePath: profilePath, probePath: probePath, run: runner}
}

func defaultProxyRouteController() *proxyRouteController {
	return proxyRouteDefaultController
}

var proxyRouteDefaultController = newProxyRouteController(proxyRouteProfilePath, proxyRouteProbePath, nil)

func defaultProxyRouteProfile() proxyRouteProfile {
	var profile proxyRouteProfile
	profile.Schema = "gptadmin.proxy-route/v1"
	profile.ID = "relay02-server44-de-http"
	profile.Consumer = "relay02"
	profile.Exit = "node-44-de-http"
	profile.Listener.BindAddress = "127.0.0.1"
	profile.Listener.Port = proxyRoutePort
	profile.SSHIdentityRef = "ssh-config:relay02"
	profile.DesiredState = "disabled"
	return profile
}

func validProxyRouteProfile(p proxyRouteProfile) bool {
	return p.Schema == "gptadmin.proxy-route/v1" &&
		p.ID == "relay02-server44-de-http" && p.Consumer == "relay02" &&
		p.Exit == "node-44-de-http" && p.Listener.BindAddress == "127.0.0.1" &&
		p.Listener.Port == proxyRoutePort && p.SSHIdentityRef == "ssh-config:relay02" &&
		(p.DesiredState == "enabled" || p.DesiredState == "disabled")
}

func (c *proxyRouteController) readProfile() (proxyRouteProfile, bool, error) {
	data, err := os.ReadFile(c.profilePath)
	if errors.Is(err, os.ErrNotExist) {
		return defaultProxyRouteProfile(), false, nil
	}
	if err != nil {
		return proxyRouteProfile{}, false, err
	}
	info, err := os.Stat(c.profilePath)
	if err != nil {
		return proxyRouteProfile{}, false, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return proxyRouteProfile{}, false, errors.New("saved proxy route profile permissions are not private")
	}
	var profile proxyRouteProfile
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&profile); err != nil {
		return proxyRouteProfile{}, false, errors.New("saved proxy route profile is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) || !validProxyRouteProfile(profile) {
		return proxyRouteProfile{}, false, errors.New("saved proxy route profile is invalid")
	}
	return profile, true, nil
}

func writeProxyRoutePrivate(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".proxy-route-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	directory, err := os.Open(dir)
	if err == nil {
		defer directory.Close()
		err = directory.Sync()
	}
	return err
}

func (c *proxyRouteController) saveProfile(profile proxyRouteProfile) error {
	if !validProxyRouteProfile(profile) {
		return errors.New("profile does not match the fixed VUSA to node-44 route")
	}
	return writeProxyRoutePrivate(c.profilePath, profile)
}

func (c *proxyRouteController) command(ctx context.Context, name string, args ...string) ([]byte, error) {
	return c.run(ctx, name, args...)
}

func (c *proxyRouteController) systemctl(ctx context.Context, args ...string) error {
	if len(args) == 0 {
		return errors.New("missing systemctl operation")
	}
	full := append([]string(nil), args...)
	full = append(full, proxyRouteUnit)
	out, err := c.command(ctx, "/usr/bin/systemctl", full...)
	if err != nil {
		return fmt.Errorf("systemctl %s %s failed: %s", strings.Join(args, " "), proxyRouteUnit, strings.TrimSpace(string(out)))
	}
	return nil
}

func (c *proxyRouteController) sshVUSA(ctx context.Context, remoteArgs ...string) ([]byte, error) {
	args := []string{"-u", "admin", "--", "/usr/bin/ssh", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=4", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3", "relay02"}
	args = append(args, remoteArgs...)
	out, err := c.command(ctx, "/usr/sbin/runuser", args...)
	if err != nil {
		return nil, fmt.Errorf("VUSA readback failed: %s", strings.TrimSpace(string(out)))
	}
	return out, nil
}

func (c *proxyRouteController) serviceState(ctx context.Context) string {
	out, err := c.command(ctx, "/usr/bin/systemctl", "show", "--property=ActiveState", "--value", proxyRouteUnit)
	if err != nil {
		return "unknown"
	}
	switch strings.TrimSpace(string(out)) {
	case "active":
		return "active"
	case "inactive":
		return "inactive"
	case "failed":
		return "failed"
	default:
		return "unknown"
	}
}

func (c *proxyRouteController) listenerState(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	out, err := c.sshVUSA(ctx, "ss", "-Hln", "sport", "=", ":24836")
	if err != nil {
		return "unknown"
	}
	return parseProxyRouteListenerState(string(out))
}

func parseProxyRouteListenerState(output string) string {
	if strings.TrimSpace(output) == "" {
		return "closed"
	}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		stateIndex := -1
		for i, field := range fields {
			if field == "LISTEN" {
				stateIndex = i
				break
			}
		}
		if stateIndex < 0 {
			return "unknown"
		}
		localAddressIndex := stateIndex + 3
		if localAddressIndex >= len(fields) {
			return "unknown"
		}
		if fields[localAddressIndex] != "127.0.0.1:24836" {
			return "unknown"
		}
	}
	return "listening"
}

func readProxyRouteProbe(path string) *proxyRouteProbe {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return unknownProxyRouteProbe("Last probe result could not be read: " + err.Error())
	}
	var probe proxyRouteProbe
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&probe); err != nil {
		return unknownProxyRouteProbe("Last probe result is malformed: " + err.Error())
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return unknownProxyRouteProbe("Last probe result contains trailing data.")
	}
	if probe.State != "pass" && probe.State != "fail" && probe.State != "unknown" {
		return unknownProxyRouteProbe("Last probe result has an unsupported state.")
	}
	if probe.CheckedAt == nil {
		return unknownProxyRouteProbe("Last probe result has no check timestamp.")
	}
	if _, err := time.Parse(time.RFC3339, *probe.CheckedAt); err != nil {
		return unknownProxyRouteProbe("Last probe result has an invalid check timestamp.")
	}
	if probe.State == "pass" {
		if probe.Server44PathVerified == nil || !*probe.Server44PathVerified || probe.DirectExitIP == nil || probe.ProxyExitIP == nil || net.ParseIP(*probe.DirectExitIP) == nil || net.ParseIP(*probe.ProxyExitIP) == nil || *probe.DirectExitIP == *probe.ProxyExitIP {
			return unknownProxyRouteProbe("Saved passing probe is missing required live evidence.")
		}
	}
	if probe.Server44PathVerified == nil && probe.Server44PathReason == nil {
		probe.Server44PathReason = strPtr("Server-44 path verification was not available for the saved probe.")
	}
	return &probe
}

func unknownProxyRouteProbe(reason string) *proxyRouteProbe {
	return &proxyRouteProbe{State: "unknown", Server44PathVerified: nil, Server44PathReason: strPtr(reason)}
}

func (c *proxyRouteController) status(ctx context.Context) (proxyRouteState, error) {
	return c.readStatus(ctx, true)
}

func (c *proxyRouteController) mutationStatus(ctx context.Context) (proxyRouteState, error) {
	return c.readStatus(ctx, false)
}

func (c *proxyRouteController) readStatus(ctx context.Context, readListener bool) (proxyRouteState, error) {
	profile, saved, err := c.readProfile()
	if err != nil {
		return proxyRouteState{}, err
	}
	var state proxyRouteState
	state.Profile = profile
	state.Runtime.ProfileSaved = saved
	state.Runtime.ServiceState = c.serviceState(ctx)
	state.Runtime.ListenerState = "unknown"
	if readListener {
		state.Runtime.ListenerState = c.listenerState(ctx)
	}
	state.Runtime.LastProbe = readProxyRouteProbe(c.probePath)
	return state, nil
}

func parseProxyRouteIP(data []byte) (*string, error) {
	value := strings.TrimSpace(string(data))
	if net.ParseIP(value) == nil {
		return nil, errors.New("probe returned an invalid IP address")
	}
	return &value, nil
}

func (c *proxyRouteController) hasEstablished44Socket(ctx context.Context) (bool, error) {
	pidOut, err := c.command(ctx, "/usr/bin/systemctl", "show", "--property=MainPID", "--value", proxyRouteUnit)
	if err != nil {
		return false, fmt.Errorf("systemd service PID readback failed: %w", err)
	}
	pid := strings.TrimSpace(string(pidOut))
	if pid == "" || pid == "0" {
		return false, errors.New("proxy tunnel service has no running main process")
	}
	if c == proxyRouteDefaultController {
		ok, err := established44SocketForPID(ctx, "/proc", pid)
		if err != nil {
			return false, err
		}
		currentPID, err := c.command(ctx, "/usr/bin/systemctl", "show", "--property=MainPID", "--value", proxyRouteUnit)
		if err != nil {
			return false, fmt.Errorf("systemd service PID recheck failed: %w", err)
		}
		if strings.TrimSpace(string(currentPID)) != pid {
			return false, errors.New("proxy service MainPID changed during socket readback")
		}
		return ok, nil
	}
	out, err := c.command(ctx, "/usr/bin/ss", "-Htnp", "state", "established")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		if fields[4] == proxyRouteExitAddress && strings.Contains(strings.Join(fields[5:], " "), "pid="+pid+",") {
			return true, nil
		}
	}
	return false, nil
}

const (
	proxyRouteProcTCPMaxBytes = 16 << 20
	proxyRouteProcFDLimit     = 4096
)

// established44SocketForPID proves the exact connected socket belongs to the
// service MainPID. Any incomplete /proc read is an error, never a negative proof.
func established44SocketForPID(ctx context.Context, procRoot, pid string) (bool, error) {
	if pid == "" || pid == "0" || strings.ContainsAny(pid, "/\\") {
		return false, errors.New("invalid proxy service PID")
	}
	proc := filepath.Join(procRoot, pid)
	startBefore, err := readProcStartTime(proc)
	if err != nil {
		return false, fmt.Errorf("proxy service PID readback failed: %w", err)
	}
	tcpPath := filepath.Join(procRoot, "net", "tcp")
	file, err := os.Open(tcpPath)
	if err != nil {
		return false, fmt.Errorf("TCP socket table readback failed: %w", err)
	}
	data, readErr := io.ReadAll(io.LimitReader(file, proxyRouteProcTCPMaxBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(data) > proxyRouteProcTCPMaxBytes {
		return false, errors.New("TCP socket table readback incomplete")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	inodes := make(map[string]struct{})
	for i, line := range strings.Split(string(data), "\n") {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 10 {
			return false, errors.New("TCP socket table contains a short row")
		}
		if fields[3] != "01" || !procTCPAddressMatches(fields[2], proxyRouteExitAddress) {
			continue
		}
		inode := fields[9]
		if inode == "0" || len(inodes) >= 256 {
			return false, errors.New("TCP socket table has too many matching rows")
		}
		inodes[inode] = struct{}{}
	}
	if len(inodes) == 0 {
		return false, nil
	}
	fds, err := os.ReadDir(filepath.Join(proc, "fd"))
	if err != nil {
		return false, fmt.Errorf("proxy service file descriptors readback failed: %w", err)
	}
	if len(fds) > proxyRouteProcFDLimit {
		return false, errors.New("proxy service has too many file descriptors to verify safely")
	}
	for _, fd := range fds {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		target, err := os.Readlink(filepath.Join(proc, "fd", fd.Name()))
		if errors.Is(err, os.ErrNotExist) {
			return false, errors.New("proxy service file descriptor changed during readback")
		}
		if err != nil {
			return false, fmt.Errorf("proxy service file descriptor readback failed: %w", err)
		}
		if strings.HasPrefix(target, "socket:[") && strings.HasSuffix(target, "]") {
			if _, ok := inodes[strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")]; ok {
				startAfter, err := readProcStartTime(proc)
				if err != nil || startAfter != startBefore {
					return false, errors.New("proxy service PID changed during socket readback")
				}
				if err := ctx.Err(); err != nil {
					return false, err
				}
				return true, nil
			}
		}
	}
	startAfter, err := readProcStartTime(proc)
	if err != nil || startAfter != startBefore {
		return false, errors.New("proxy service PID changed during socket readback")
	}
	return false, nil
}

func procTCPAddressMatches(value, target string) bool {
	parts := strings.Split(target, ":")
	procParts := strings.Split(value, ":")
	if len(parts) != 2 || len(procParts) != 2 {
		return false
	}
	port, err := strconv.Atoi(parts[1])
	if err != nil {
		return false
	}
	address := net.ParseIP(parts[0]).To4()
	if address == nil {
		return false
	}
	decoded, err := hex.DecodeString(procParts[0])
	procPort, portErr := strconv.ParseUint(procParts[1], 16, 16)
	if err != nil || portErr != nil || len(decoded) != 4 {
		return false
	}
	for i, b := range address {
		if decoded[3-i] != b {
			return false
		}
	}
	return procPort == uint64(port)
}

func readProcStartTime(proc string) (string, error) {
	data, err := os.ReadFile(filepath.Join(proc, "stat"))
	if err != nil {
		return "", err
	}
	close := strings.LastIndexByte(string(data), ')')
	if close < 0 {
		return "", errors.New("malformed process stat")
	}
	fields := strings.Fields(string(data)[close+1:])
	if len(fields) <= 19 {
		return "", errors.New("short process stat")
	}
	return fields[19], nil
}

func observeProxyRoutePathBefore(ctx context.Context, deadline time.Time, observe func(context.Context) (bool, error)) (bool, error) {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return false, errors.New("timed out waiting for node-44 socket readback")
	}
	if remaining > 2*time.Second {
		remaining = 2 * time.Second
	}
	checkCtx, cancel := context.WithTimeout(ctx, remaining)
	defer cancel()
	ok, err := observe(checkCtx)
	if !time.Now().Before(deadline) {
		return false, errors.New("timed out waiting for node-44 socket readback")
	}
	return ok, err
}

func (c *proxyRouteController) probe(ctx context.Context) proxyRouteProbe {
	checked := time.Now().UTC().Format(time.RFC3339)
	result := proxyRouteProbe{State: "unknown", CheckedAt: &checked}
	probeCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	if _, _, err := c.readProfile(); err != nil {
		result.Server44PathReason = strPtr("Saved route profile could not be read: " + err.Error())
		return result
	}
	type ipResult struct {
		proxied bool
		ip      *string
		err     error
	}
	requestDone := make(chan ipResult, 2)
	go func() {
		out, err := c.sshVUSA(probeCtx, "curl", "--silent", "--show-error", "--fail", "--max-time", "6", proxyRouteProbeURL)
		var ip *string
		if err == nil {
			ip, err = parseProxyRouteIP(out)
		}
		requestDone <- ipResult{ip: ip, err: err}
	}()
	go func() {
		out, err := c.sshVUSA(probeCtx, "curl", "--silent", "--show-error", "--fail", "--max-time", "7", "--proxy", "http://127.0.0.1:24836", proxyRouteProbeURL)
		var ip *string
		if err == nil {
			ip, err = parseProxyRouteIP(out)
		}
		requestDone <- ipResult{proxied: true, ip: ip, err: err}
	}()

	pathDeadline := time.Now().Add(proxyRoutePathReadbackTimeout)
	deadline := time.NewTimer(time.Until(pathDeadline))
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	var pathConfirmed bool
	var pathReadbackErr error
	pathReadbackDone := false
	var directErr, proxyErr error
	var gotDirect, gotProxy bool
	if ok, err := observeProxyRoutePathBefore(probeCtx, pathDeadline, c.hasEstablished44Socket); err != nil {
		pathReadbackErr = err
		pathReadbackDone = !time.Now().Before(pathDeadline)
	} else if ok {
		pathConfirmed = true
	}
probeLoop:
	for !gotDirect || !gotProxy || (!pathConfirmed && !pathReadbackDone && !gotProxy) {
		select {
		case request := <-requestDone:
			if request.proxied {
				gotProxy = true
				result.ProxyExitIP = request.ip
				proxyErr = request.err
			} else {
				gotDirect = true
				result.DirectExitIP = request.ip
				directErr = request.err
			}
		case <-probeCtx.Done():
			if !pathConfirmed && pathReadbackErr == nil {
				pathReadbackErr = probeCtx.Err()
			}
			break probeLoop
		case <-deadline.C:
			pathReadbackDone = true
			if !pathConfirmed && pathReadbackErr == nil {
				pathReadbackErr = errors.New("timed out waiting for node-44 socket readback")
			}
		case <-ticker.C:
			if gotProxy || pathConfirmed || pathReadbackDone {
				continue
			}
			ok, err := observeProxyRoutePathBefore(probeCtx, pathDeadline, c.hasEstablished44Socket)
			if err != nil {
				if pathReadbackErr == nil {
					pathReadbackErr = err
				}
				pathReadbackDone = !time.Now().Before(pathDeadline)
			} else if ok {
				pathConfirmed = true
			}
		}
	}
	if pathConfirmed {
		result.Server44PathVerified = boolPtr(true)
		appendProxyRouteReason(&result, "Established TCP connection to 203.0.113.10:3128 observed on node-100 during the VUSA proxy request.")
	} else if pathReadbackErr != nil {
		result.Server44PathVerified = nil
		appendProxyRouteReason(&result, "Could not verify the established node-44 socket: "+pathReadbackErr.Error())
	} else {
		result.Server44PathVerified = boolPtr(false)
		appendProxyRouteReason(&result, "No established TCP connection to 203.0.113.10:3128 was observed during the VUSA proxy request.")
	}

	if directErr != nil {
		appendProxyRouteReason(&result, "Direct VUSA request failed: "+directErr.Error())
	}
	if err := probeCtx.Err(); err != nil {
		result.State = "unknown"
		appendProxyRouteReason(&result, "Probe exceeded its overall deadline: "+err.Error())
		return result
	}
	if proxyErr != nil {
		result.State = "fail"
		appendProxyRouteReason(&result, "Proxy request failed: "+proxyErr.Error())
		return result
	}
	if directErr != nil {
		result.State = "unknown"
		return result
	}
	if result.Server44PathVerified == nil {
		result.State = "unknown"
		return result
	}
	if !*result.Server44PathVerified {
		result.State = "fail"
		return result
	}
	if *result.DirectExitIP == *result.ProxyExitIP {
		result.State = "fail"
		appendProxyRouteReason(&result, "Direct and proxied egress IPs matched despite the established node-44 socket.")
		return result
	}
	result.State = "pass"
	return result
}

func boolPtr(value bool) *bool    { return &value }
func strPtr(value string) *string { return &value }

func appendProxyRouteReason(probe *proxyRouteProbe, reason string) {
	if probe.Server44PathReason == nil || strings.TrimSpace(*probe.Server44PathReason) == "" {
		probe.Server44PathReason = strPtr(reason)
		return
	}
	probe.Server44PathReason = strPtr(*probe.Server44PathReason + " " + reason)
}

func (c *proxyRouteController) action(ctx context.Context, operation string) error {
	profile, _, err := c.readProfile()
	if err != nil {
		return err
	}
	switch operation {
	case "enable":
		profile.DesiredState = "enabled"
		if err := c.saveProfile(profile); err != nil {
			return err
		}
		return c.systemctl(ctx, "enable", "--now")
	case "disable":
		profile.DesiredState = "disabled"
		if err := c.saveProfile(profile); err != nil {
			return err
		}
		return c.systemctl(ctx, "disable", "--now")
	case "reconcile":
		if profile.DesiredState == "enabled" {
			return c.systemctl(ctx, "enable", "--now")
		}
		return c.systemctl(ctx, "disable", "--now")
	default:
		return errors.New("unsupported proxy route operation")
	}
}

func decodeProxyRouteProfile(w http.ResponseWriter, r *http.Request) (proxyRouteProfile, error) {
	var request struct {
		Profile proxyRouteProfile `json:"profile"`
	}
	if r.Body == nil {
		return request.Profile, errors.New("request body is required")
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return request.Profile, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return request.Profile, errors.New("request body must contain one JSON object")
	}
	if !validProxyRouteProfile(request.Profile) {
		return request.Profile, errors.New("profile does not match the fixed VUSA to node-44 route")
	}
	return request.Profile, nil
}

func decodeEmptyProxyActionBody(w http.ResponseWriter, r *http.Request) error {
	if r.Body == nil {
		return nil
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	var raw json.RawMessage
	if err := decoder.Decode(&raw); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return errors.New("request body must be an empty JSON object")
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil || body == nil || len(body) != 0 {
		return errors.New("request body must be an empty JSON object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one empty JSON object")
	}
	return nil
}

func (s *Server) adminProxyRoute(w http.ResponseWriter, r *http.Request) {
	s.adminProxyRouteWithController(w, r, defaultProxyRouteController())
}

func (s *Server) adminProxyRouteWithController(w http.ResponseWriter, r *http.Request, controller *proxyRouteController) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet && r.URL.Path == "/admin/api/proxy-route" {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		state, err := controller.status(ctx)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, state)
		return
	}
	if r.Method == http.MethodPut && r.URL.Path == "/admin/api/proxy-route" {
		if cookieAdminSessionPresent(r, s) && !sameOriginAdminMutation(r, adminRequestOrigin(r)) {
			writeJSON(w, http.StatusForbidden, map[string]any{"detail": "same-origin admin request required"})
			return
		}
		profile, err := decodeProxyRouteProfile(w, r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
			return
		}
		controller.mu.Lock()
		err = controller.saveProfile(profile)
		controller.mu.Unlock()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		state, err := controller.mutationStatus(ctx)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, state)
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/admin/api/proxy-route/") {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "not found"})
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	if cookieAdminSessionPresent(r, s) && !sameOriginAdminMutation(r, adminRequestOrigin(r)) {
		writeJSON(w, http.StatusForbidden, map[string]any{"detail": "same-origin admin request required"})
		return
	}
	operation := strings.TrimPrefix(r.URL.Path, "/admin/api/proxy-route/")
	if operation != "enable" && operation != "disable" && operation != "reconcile" && operation != "probe" {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "unknown proxy route operation"})
		return
	}
	if err := decodeEmptyProxyActionBody(w, r); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if operation == "probe" {
		probe := controller.probe(ctx)
		if err := writeProxyRoutePrivate(controller.probePath, probe); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "failed to persist proxy probe result"})
			return
		}
	} else if err := controller.action(ctx, operation); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"detail": err.Error()})
		return
	}
	state, err := controller.mutationStatus(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, state)
}
