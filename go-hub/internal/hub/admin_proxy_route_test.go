package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProxyRouteProfileAtomicPrivatePersistenceAndAllowlist(t *testing.T) {
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "nested", "profile.json")
	controller := newProxyRouteController(profilePath, filepath.Join(dir, "probe.json"), nil)
	profile := defaultProxyRouteProfile()
	profile.DesiredState = "enabled"
	if err := controller.saveProfile(profile); err != nil {
		t.Fatal(err)
	}
	got, saved, err := controller.readProfile()
	if err != nil || !saved || got != profile {
		t.Fatalf("readProfile() = %#v, %v, %v; want saved profile", got, saved, err)
	}
	fileInfo, err := os.Stat(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	if fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("profile mode = %o, want 600", fileInfo.Mode().Perm())
	}
	dirInfo, err := os.Stat(filepath.Dir(profilePath))
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("profile directory mode = %o, want 700", dirInfo.Mode().Perm())
	}

	profile.Listener.Port = 22
	if err := controller.saveProfile(profile); err == nil {
		t.Fatal("saveProfile accepted a non-allowlisted listener")
	}
}

func TestProxyRoutePUTRequiresFrozenProfileEnvelopeAndRejectsUnknownFields(t *testing.T) {
	profile := defaultProxyRouteProfile()
	profile.DesiredState = "enabled"
	encoded, err := json.Marshal(map[string]any{"profile": profile})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("PUT", "/admin/api/proxy-route", bytes.NewReader(encoded))
	got, err := decodeProxyRouteProfile(httptest.NewRecorder(), req)
	if err != nil || got != profile {
		t.Fatalf("valid profile envelope decoded as %#v, %v", got, err)
	}

	bad := []byte(`{"profile":{"schema":"gptadmin.proxy-route/v1","id":"relay02-server44-de-http","consumer":"relay02","exit":"node-44-de-http","listener":{"bind_address":"127.0.0.1","port":24836},"ssh_identity_ref":"ssh-config:relay02","desired_state":"enabled","host":"attacker"}}`)
	req = httptest.NewRequest("PUT", "/admin/api/proxy-route", bytes.NewReader(bad))
	if _, err := decodeProxyRouteProfile(httptest.NewRecorder(), req); err == nil {
		t.Fatal("profile decoder accepted an unknown arbitrary-host field")
	}
}

func TestProxyRouteCookieMutationRequiresSameOrigin(t *testing.T) {
	dir := t.TempDir()
	controller := newProxyRouteController(filepath.Join(dir, "profile.json"), filepath.Join(dir, "probe.json"), nil)
	s := &Server{}
	s.cfg.AdminPassword = "test-only-session-key"
	s.cfg.PublicOrigin = "https://admin.example.test"
	profile := defaultProxyRouteProfile()
	encoded, err := json.Marshal(map[string]any{"profile": profile})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("PUT", "https://admin.example.test/admin/api/proxy-route", bytes.NewReader(encoded))
	req.AddCookie(&http.Cookie{Name: adminSessionCookieName, Value: s.signAdminSession(time.Now().Add(time.Hour))})
	req.Header.Set("Origin", "https://attacker.example.test")
	response := httptest.NewRecorder()
	s.adminProxyRouteWithController(response, req, controller)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-origin cookie mutation status = %d, want 403", response.Code)
	}
	if _, err := os.Stat(filepath.Join(dir, "profile.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cross-origin mutation wrote profile: stat error = %v", err)
	}
}

func TestProxyRouteCookieMutationUsesIngressOriginAndFailsClosed(t *testing.T) {
	const ingressOrigin = "https://your-subdomain.t.became.bezrabotnyi.com"
	tests := []struct {
		name       string
		method     string
		path       string
		origin     string
		referer    string
		forwarded  []string
		wantStatus int
	}{
		{name: "same-origin referer fallback", referer: ingressOrigin + "/admin/", forwarded: []string{"https"}, wantStatus: http.StatusBadRequest},
		{name: "same-origin POST to unknown action", method: http.MethodPost, path: "/admin/api/proxy-route/not-a-real-action", referer: ingressOrigin + "/admin/", forwarded: []string{"https"}, wantStatus: http.StatusNotFound},
		{name: "foreign-origin POST", method: http.MethodPost, path: "/admin/api/proxy-route/not-a-real-action", origin: "https://attacker.example", forwarded: []string{"https"}, wantStatus: http.StatusForbidden},
		{name: "foreign origin", origin: "https://attacker.example", forwarded: []string{"https"}, wantStatus: http.StatusForbidden},
		{name: "missing origin and referer", forwarded: []string{"https"}, wantStatus: http.StatusForbidden},
		{name: "conflicting forwarded schemes", referer: ingressOrigin + "/admin/", forwarded: []string{"https", "http"}, wantStatus: http.StatusForbidden},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			controller := newProxyRouteController(filepath.Join(dir, "profile.json"), filepath.Join(dir, "probe.json"), nil)
			s := &Server{}
			s.cfg.AdminPassword = "test-only-session-key"
			s.cfg.PublicOrigin = "https://admin.example.test"

			method := test.method
			if method == "" {
				method = http.MethodPut
			}
			requestPath := test.path
			if requestPath == "" {
				requestPath = "/admin/api/proxy-route"
			}
			body := `{"profile":{}}`
			if method == http.MethodPost {
				body = `{}`
			}
			req := httptest.NewRequest(method, "http://your-subdomain.t.became.bezrabotnyi.com"+requestPath, strings.NewReader(body))
			req.AddCookie(&http.Cookie{Name: adminSessionCookieName, Value: s.signAdminSession(time.Now().Add(time.Hour))})
			req.Header.Set("X-Forwarded-Proto", test.forwarded[0])
			if len(test.forwarded) > 1 {
				req.Header.Add("X-Forwarded-Proto", test.forwarded[1])
			}
			if test.origin != "" {
				req.Header.Set("Origin", test.origin)
			}
			if test.referer != "" {
				req.Header.Set("Referer", test.referer)
			}
			response := httptest.NewRecorder()
			s.adminProxyRouteWithController(response, req, controller)
			if response.Code != test.wantStatus {
				t.Fatalf("status=%d body=%s; want %d", response.Code, response.Body.String(), test.wantStatus)
			}
			if _, err := os.Stat(filepath.Join(dir, "profile.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("request saved a profile: stat error=%v", err)
			}
		})
	}
}

func TestProxyRoutePUTPersistsWithoutStartingService(t *testing.T) {
	dir := t.TempDir()
	var mu sync.Mutex
	var calls []string
	runner := func(_ context.Context, name string, args ...string) ([]byte, error) {
		mu.Lock()
		calls = append(calls, name+" "+strings.Join(args, " "))
		mu.Unlock()
		if name == "/usr/bin/systemctl" && strings.Contains(strings.Join(args, " "), "ActiveState") {
			return []byte("inactive\n"), nil
		}
		if name == "/usr/sbin/runuser" {
			return []byte("LISTEN 0 128 127.0.0.1:24836 0.0.0.0:*\n"), nil
		}
		return nil, errors.New("unexpected command")
	}
	controller := newProxyRouteController(filepath.Join(dir, "profile.json"), filepath.Join(dir, "probe.json"), runner)
	s := &Server{}
	profile := defaultProxyRouteProfile()
	profile.DesiredState = "enabled"
	encoded, err := json.Marshal(map[string]any{"profile": profile})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("PUT", "/admin/api/proxy-route", bytes.NewReader(encoded))
	response := httptest.NewRecorder()
	s.adminProxyRouteWithController(response, req, controller)
	if response.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body=%s", response.Code, response.Body.String())
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	var state proxyRouteState
	if err := json.Unmarshal(body, &state); err != nil {
		t.Fatal(err)
	}
	if !state.Runtime.ProfileSaved || state.Profile.DesiredState != "enabled" || state.Runtime.ServiceState != "inactive" {
		t.Fatalf("unexpected persisted state: %#v", state)
	}
	for _, call := range calls {
		if strings.Contains(call, " enable ") || strings.Contains(call, " start ") {
			t.Fatalf("PUT unexpectedly started/enabled service: %q", call)
		}
	}
}

func TestProxyRouteMutationResponseDoesNotDependOnListenerSSHReadback(t *testing.T) {
	for _, operation := range []string{"save", "enable"} {
		t.Run(operation, func(t *testing.T) {
			dir := t.TempDir()
			var listenerReads int
			runner := func(_ context.Context, name string, args ...string) ([]byte, error) {
				joined := strings.Join(args, " ")
				if name == "/usr/bin/systemctl" && strings.Contains(joined, "ActiveState") {
					return []byte("active\n"), nil
				}
				if name == "/usr/sbin/runuser" {
					listenerReads++
					return nil, errors.New("listener readback is unavailable")
				}
				if name == "/usr/bin/systemctl" {
					return nil, nil
				}
				return nil, errors.New("unexpected command")
			}
			controller := newProxyRouteController(filepath.Join(dir, "profile.json"), filepath.Join(dir, "probe.json"), runner)
			profile := defaultProxyRouteProfile()
			profile.DesiredState = "enabled"
			body, err := json.Marshal(map[string]any{"profile": profile})
			if err != nil {
				t.Fatal(err)
			}
			method, path, requestBody := http.MethodPut, "/admin/api/proxy-route", body
			if operation == "enable" {
				if err := controller.saveProfile(profile); err != nil {
					t.Fatal(err)
				}
				method, path, requestBody = http.MethodPost, "/admin/api/proxy-route/enable", []byte("{}")
			}
			req := httptest.NewRequest(method, path, bytes.NewReader(requestBody))
			response := httptest.NewRecorder()
			(&Server{}).adminProxyRouteWithController(response, req, controller)
			if response.Code != http.StatusOK {
				t.Fatalf("mutation status=%d body=%s", response.Code, response.Body.String())
			}
			if listenerReads != 0 {
				t.Fatalf("mutation response made %d optional remote listener readbacks", listenerReads)
			}
			var state proxyRouteState
			if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
				t.Fatal(err)
			}
			if state.Runtime.ListenerState != "unknown" || !state.Runtime.ProfileSaved || state.Runtime.ServiceState != "active" {
				t.Fatalf("mutation response did not report saved state with honest unknown listener state: %#v", state)
			}
		})
	}
}

func TestProxyRouteActionAcceptsOnlyAnEmptyObject(t *testing.T) {
	for _, body := range []string{"{}", ""} {
		req := httptest.NewRequest("POST", "/admin/api/proxy-route/probe", strings.NewReader(body))
		if err := decodeEmptyProxyActionBody(httptest.NewRecorder(), req); err != nil {
			t.Fatalf("accepted action body %q was rejected: %v", body, err)
		}
	}
	for _, body := range []string{"null", `{"host":"attacker"}`, "{} {}"} {
		req := httptest.NewRequest("POST", "/admin/api/proxy-route/probe", strings.NewReader(body))
		if err := decodeEmptyProxyActionBody(httptest.NewRecorder(), req); err == nil {
			t.Fatalf("invalid action body %q was accepted", body)
		}
	}
}

func TestProxyRouteSaveDoesNotStartTunnelAndLifecycleIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	var mu sync.Mutex
	var calls [][]string
	runner := func(_ context.Context, name string, args ...string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		call := append([]string{name}, args...)
		calls = append(calls, call)
		return nil, nil
	}
	controller := newProxyRouteController(filepath.Join(dir, "profile.json"), filepath.Join(dir, "probe.json"), runner)
	profile := defaultProxyRouteProfile()
	profile.DesiredState = "enabled"
	if err := controller.saveProfile(profile); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Fatalf("saving profile invoked external commands: %#v", calls)
	}
	for i := 0; i < 2; i++ {
		if err := controller.action(context.Background(), "enable"); err != nil {
			t.Fatal(err)
		}
	}
	if len(calls) != 2 {
		t.Fatalf("enable command count = %d, want two idempotent systemctl requests", len(calls))
	}
	for _, call := range calls {
		if strings.Join(call, " ") != "/usr/bin/systemctl enable --now gptadmin-proxy-route-relay02.service" {
			t.Fatalf("unexpected lifecycle command: %#v", call)
		}
	}
}

func TestProxyRouteProbeRequiresIndependentEstablishedServer44Socket(t *testing.T) {
	dir := t.TempDir()
	controller := newProxyRouteController(filepath.Join(dir, "profile.json"), filepath.Join(dir, "probe.json"), nil)
	if err := controller.saveProfile(defaultProxyRouteProfile()); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	proxyStarted := make(chan struct{})
	ssChecks := 0
	controller.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "/usr/bin/systemctl" && strings.Contains(strings.Join(args, " "), "MainPID") {
			return []byte("4321\n"), nil
		}
		if name == "/usr/sbin/runuser" {
			joined := strings.Join(args, " ")
			if strings.Contains(joined, "--proxy http://127.0.0.1:24836") {
				select {
				case <-proxyStarted:
				default:
					close(proxyStarted)
				}
				return []byte("212.192.31.128"), nil
			}
			return []byte("185.240.120.152"), nil
		}
		if name == "/usr/bin/ss" {
			mu.Lock()
			ssChecks++
			current := ssChecks
			mu.Unlock()
			select {
			case <-proxyStarted:
				return []byte("ESTAB 0 0 203.0.113.10:42000 203.0.113.10:3128 users:((\"ssh\",pid=4321,fd=10))\n"), nil
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(10 * time.Millisecond):
				if current > 1 {
					return []byte(""), nil
				}
				return []byte(""), nil
			}
		}
		return nil, errors.New("unexpected probe command")
	}

	probe := controller.probe(context.Background())
	if probe.State != "pass" || probe.Server44PathVerified == nil || !*probe.Server44PathVerified {
		t.Fatalf("probe did not require and observe established server44 socket: %#v", probe)
	}
	if probe.DirectExitIP == nil || *probe.DirectExitIP != "185.240.120.152" || probe.ProxyExitIP == nil || *probe.ProxyExitIP != "212.192.31.128" {
		t.Fatalf("probe did not preserve live egress measurements: %#v", probe)
	}
}

func TestProxyRouteProbeStartsDirectAndProxyReadsConcurrently(t *testing.T) {
	dir := t.TempDir()
	controller := newProxyRouteController(filepath.Join(dir, "profile.json"), filepath.Join(dir, "probe.json"), nil)
	if err := controller.saveProfile(defaultProxyRouteProfile()); err != nil {
		t.Fatal(err)
	}
	directStarted := make(chan struct{})
	proxyStarted := make(chan struct{})
	controller.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		if name == "/usr/bin/systemctl" && strings.Contains(joined, "MainPID") {
			return []byte("4321\n"), nil
		}
		if name == "/usr/sbin/runuser" {
			if strings.Contains(joined, "--proxy http://127.0.0.1:24836") {
				close(proxyStarted)
				return []byte("212.192.31.128"), nil
			}
			close(directStarted)
			select {
			case <-proxyStarted:
				return []byte("185.240.120.152"), nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if name == "/usr/bin/ss" {
			return []byte("ESTAB 0 0 203.0.113.10:42000 203.0.113.10:3128 users:((\"ssh\",pid=4321,fd=10))\n"), nil
		}
		return nil, errors.New("unexpected probe command")
	}

	probeDone := make(chan proxyRouteProbe, 1)
	probeCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { probeDone <- controller.probe(probeCtx) }()
	select {
	case <-directStarted:
	case <-time.After(time.Second):
		t.Fatal("direct request did not start")
	}
	select {
	case <-proxyStarted:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("proxy request did not start while the direct request was in flight")
	}
	select {
	case probe := <-probeDone:
		if probe.State != "pass" || probe.DirectExitIP == nil || probe.ProxyExitIP == nil {
			t.Fatalf("concurrent probe did not retain both egress results: %#v", probe)
		}
	case <-time.After(time.Second):
		t.Fatal("probe did not finish after both requests returned")
	}
}

func TestProxyRouteProbePreservesLaterSocketReadbackFailure(t *testing.T) {
	dir := t.TempDir()
	controller := newProxyRouteController(filepath.Join(dir, "profile.json"), filepath.Join(dir, "probe.json"), nil)
	if err := controller.saveProfile(defaultProxyRouteProfile()); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	ssChecks := 0
	pollStarted := make(chan struct{})
	var pollOnce sync.Once
	controller.run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		if name == "/usr/bin/systemctl" && strings.Contains(joined, "MainPID") {
			return []byte("4321\n"), nil
		}
		if name == "/usr/bin/ss" {
			mu.Lock()
			ssChecks++
			current := ssChecks
			mu.Unlock()
			if current == 1 {
				return nil, nil
			}
			pollOnce.Do(func() { close(pollStarted) })
			return nil, errors.New("later socket readback failed")
		}
		if name == "/usr/sbin/runuser" {
			<-pollStarted
			if strings.Contains(joined, "--proxy http://127.0.0.1:24836") {
				return []byte("212.192.31.128"), nil
			}
			return []byte("185.240.120.152"), nil
		}
		return nil, errors.New("unexpected probe command")
	}

	probe := controller.probe(context.Background())
	if probe.State != "unknown" || probe.Server44PathVerified != nil || probe.Server44PathReason == nil ||
		!strings.Contains(*probe.Server44PathReason, "Could not verify") ||
		!strings.Contains(*probe.Server44PathReason, "later socket readback failed") {
		t.Fatalf("later socket readback error was lost or treated as no socket: %#v", probe)
	}
}

func TestProxyRouteProbePreservesDirectReadFailureReason(t *testing.T) {
	for _, test := range []struct {
		name       string
		directBody string
		directErr  error
		wantReason string
	}{
		{name: "curl failure", directBody: "direct VUSA transport failed", directErr: errors.New("remote curl failed"), wantReason: "direct VUSA transport failed"},
		{name: "invalid IP", directBody: "not-an-ip", wantReason: "probe returned an invalid IP address"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			controller := newProxyRouteController(filepath.Join(dir, "profile.json"), filepath.Join(dir, "probe.json"), nil)
			if err := controller.saveProfile(defaultProxyRouteProfile()); err != nil {
				t.Fatal(err)
			}
			controller.run = func(_ context.Context, name string, args ...string) ([]byte, error) {
				joined := strings.Join(args, " ")
				if name == "/usr/bin/systemctl" && strings.Contains(joined, "MainPID") {
					return []byte("4321\n"), nil
				}
				if name == "/usr/bin/ss" {
					return []byte("ESTAB 0 0 203.0.113.10:42000 203.0.113.10:3128 users:((\"ssh\",pid=4321,fd=10))\n"), nil
				}
				if name == "/usr/sbin/runuser" {
					if strings.Contains(joined, "--proxy http://127.0.0.1:24836") {
						return []byte("212.192.31.128"), nil
					}
					return []byte(test.directBody), test.directErr
				}
				return nil, errors.New("unexpected probe command")
			}

			probe := controller.probe(context.Background())
			if probe.State != "unknown" || probe.Server44PathReason == nil ||
				!strings.Contains(*probe.Server44PathReason, "Direct VUSA request failed:") ||
				!strings.Contains(*probe.Server44PathReason, test.wantReason) {
				var reason string
				if probe.Server44PathReason != nil {
					reason = *probe.Server44PathReason
				}
				t.Fatalf("direct read failure reason was lost or misclassified: state=%s reason=%q", probe.State, reason)
			}
		})
	}
}

func TestProxyRouteProbeWaitsForEstablishedSocketAfterSSHSetup(t *testing.T) {
	dir := t.TempDir()
	controller := newProxyRouteController(filepath.Join(dir, "profile.json"), filepath.Join(dir, "probe.json"), nil)
	profile := defaultProxyRouteProfile()
	profile.DesiredState = "enabled"
	if err := controller.saveProfile(profile); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	const sshSetupDelay = 9 * time.Second
	const httpRequestDelay = 1 * time.Second
	const targetPID = "4321"
	controller.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case name == "/usr/bin/systemctl" && strings.Contains(joined, "MainPID"):
			return []byte(targetPID + "\n"), nil
		case name == "/usr/bin/ss":
			if time.Since(started) < sshSetupDelay {
				return nil, nil
			}
			return []byte("ESTAB 0 0 203.0.113.10:42000 203.0.113.10:3128 users:((\"ssh\",pid=4321,fd=10))\n"), nil
		case name == "/usr/sbin/runuser" && strings.Contains(joined, "--proxy http://127.0.0.1:24836"):
			timer := time.NewTimer(sshSetupDelay + httpRequestDelay)
			defer timer.Stop()
			select {
			case <-timer.C:
				return []byte("212.192.31.128"), nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		case name == "/usr/sbin/runuser":
			timer := time.NewTimer(sshSetupDelay)
			defer timer.Stop()
			select {
			case <-timer.C:
				return []byte("185.240.120.152"), nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		default:
			return nil, errors.New("unexpected command")
		}
	}

	probe := controller.probe(context.Background())
	if probe.State != "pass" || probe.DirectExitIP == nil || *probe.DirectExitIP != "185.240.120.152" || probe.ProxyExitIP == nil || *probe.ProxyExitIP != "212.192.31.128" || probe.Server44PathVerified == nil || !*probe.Server44PathVerified {
		direct, proxied, path, reason := "<nil>", "<nil>", "<nil>", "<nil>"
		if probe.DirectExitIP != nil {
			direct = *probe.DirectExitIP
		}
		if probe.ProxyExitIP != nil {
			proxied = *probe.ProxyExitIP
		}
		if probe.Server44PathVerified != nil {
			path = fmt.Sprint(*probe.Server44PathVerified)
		}
		if probe.Server44PathReason != nil {
			reason = *probe.Server44PathReason
		}
		t.Fatalf("probe did not preserve both IPs and the delayed MainPID-bound node-44 proof: state=%q direct=%s proxy=%s path=%s reason=%s", probe.State, direct, proxied, path, reason)
	}
}

func TestProxyRouteProbeUsesUnknownWhenSocketReadbackFails(t *testing.T) {
	dir := t.TempDir()
	controller := newProxyRouteController(filepath.Join(dir, "profile.json"), filepath.Join(dir, "probe.json"), nil)
	if err := controller.saveProfile(defaultProxyRouteProfile()); err != nil {
		t.Fatal(err)
	}
	controller.run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "/usr/bin/systemctl" && strings.Contains(strings.Join(args, " "), "MainPID") {
			return []byte("4321\n"), nil
		}
		if name == "/usr/sbin/runuser" {
			joined := strings.Join(args, " ")
			if strings.Contains(joined, "--proxy http://127.0.0.1:24836") {
				return []byte("212.192.31.128"), nil
			}
			return []byte("185.240.120.152"), nil
		}
		if name == "/usr/bin/ss" {
			return nil, errors.New("ss permission denied")
		}
		return nil, errors.New("unexpected probe command")
	}

	probe := controller.probe(context.Background())
	if probe.State != "unknown" || probe.Server44PathVerified != nil || probe.Server44PathReason == nil || !strings.Contains(*probe.Server44PathReason, "Could not verify") {
		t.Fatalf("socket readback failure was not represented as unknown with reason: %#v", probe)
	}
}

func TestProxyRouteCorruptPersistedProbeCannotRestoreFakePass(t *testing.T) {
	path := filepath.Join(t.TempDir(), "last-probe.json")
	if err := os.WriteFile(path, []byte(`{"state":"pass","server44_path_verified":false,"direct_exit_ip":"185.240.120.152","proxy_exit_ip":"212.192.31.128"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	probe := readProxyRouteProbe(path)
	if probe == nil || probe.State != "unknown" || probe.Server44PathVerified != nil || probe.Server44PathReason == nil {
		t.Fatalf("invalid persisted pass was not downgraded to unknown: %#v", probe)
	}
}
