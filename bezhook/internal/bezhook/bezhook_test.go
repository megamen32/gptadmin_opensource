package bezhook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/fatedier/frp/pkg/config/v1"
)

func TestValidateSubdomain(t *testing.T) {
	ok := []string{"myapp", "your-subdomain", "a", "abc-123", strings.Repeat("a", 63)}
	for _, s := range ok {
		if err := ValidateSubdomain(s); err != nil {
			t.Errorf("expected %q valid, got %v", s, err)
		}
	}
	bad := []string{"", "-abc", "abc-", "UPPER", "a b", "a_b", strings.Repeat("a", 64), " точка"}
	for _, s := range bad {
		if err := ValidateSubdomain(s); err == nil {
			t.Errorf("expected %q invalid", s)
		}
	}
}

func TestRandomSubdomain(t *testing.T) {
	for i := 0; i < 50; i++ {
		s, err := RandomSubdomain()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(s, "u-f") || len(s) != 10 {
			t.Fatalf("bad random subdomain %q", s)
		}
		if err := ValidateSubdomain(s); err != nil {
			t.Fatalf("random subdomain invalid: %v", err)
		}
	}
}

func TestParseTTL(t *testing.T) {
	cases := map[string]time.Duration{
		"72h":  72 * time.Hour,
		"30m":  30 * time.Minute,
		"72":   72 * time.Hour,
		"0":    0,
		"1h":   time.Hour,
		"120s": 120 * time.Second,
	}
	for in, want := range cases {
		got, err := ParseTTL(in)
		if err != nil || got != want {
			t.Errorf("ParseTTL(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "abc", "-1h", "1.5x"} {
		if _, err := ParseTTL(in); err == nil {
			t.Errorf("ParseTTL(%q) expected error", in)
		}
	}
}

func TestParseEndpoints(t *testing.T) {
	gws, err := ParseEndpoints("a.example:7000, b.example:27001, c.example")
	if err != nil {
		t.Fatal(err)
	}
	want := []Gateway{{"a.example", 7000}, {"b.example", 27001}, {"c.example", 7000}}
	if len(gws) != len(want) {
		t.Fatalf("got %d endpoints, want %d", len(gws), len(want))
	}
	for i := range want {
		if gws[i] != want[i] {
			t.Errorf("endpoint %d = %+v, want %+v", i, gws[i], want[i])
		}
	}
	if _, err := ParseEndpoints("a:0"); err == nil {
		t.Error("port 0 must fail")
	}
	if _, err := ParseEndpoints("a:99999"); err == nil {
		t.Error("port 99999 must fail")
	}
	if _, err := ParseEndpoints("  "); err == nil {
		t.Error("empty list must fail")
	}
}

func TestFleetSettingsLayering(t *testing.T) {
	// Defaults only.
	tok, dom, tlsName, gws, err := FleetSettings(fileConfig{}, Env{})
	if err != nil {
		t.Fatal(err)
	}
	if tok != DefaultToken || dom != DefaultDomain || tlsName != DefaultTLSServerName {
		t.Fatalf("defaults not applied: %q %q %q", tok, dom, tlsName)
	}
	if len(gws) != 3 || gws[1].Addr != "relay02.bezrabotnyi.com" || gws[1].Port != 27000 {
		t.Fatalf("default endpoints wrong: %+v", gws)
	}

	// File layer.
	fc := fileConfig{
		Token:     "filetok",
		Domain:    "file.example",
		Endpoints: []string{"h1:1", "h2:2"},
	}
	tok, dom, _, gws, err = FleetSettings(fc, Env{})
	if err != nil || tok != "filetok" || dom != "file.example" || len(gws) != 2 || gws[0].Port != 1 {
		t.Fatalf("file layer not applied: %q %q %+v %v", tok, dom, gws, err)
	}

	// Env layer wins over file.
	env := Env{"BEZHOOK_TOKEN": "envtok", "BEZHOOK_ENDPOINTS": "h3:3"}
	tok, _, _, gws, err = FleetSettings(fc, env)
	if err != nil || tok != "envtok" || len(gws) != 1 || gws[0] != (Gateway{"h3", 3}) {
		t.Fatalf("env layer not applied: %q %+v %v", tok, gws, err)
	}

	// Empty tls name falls back to domain.
	_, dom, tlsName, _, _ = mustFleet(fileConfig{Domain: "d.example"}, Env{})
	if tlsName != "d.example" {
		t.Fatalf("tls fallback wrong: %q", tlsName)
	}
}

func mustFleet(fc fileConfig, env Env) (string, string, string, []Gateway, error) {
	return FleetSettings(fc, env)
}

func TestLoadFileConfigOptional(t *testing.T) {
	dir := t.TempDir()
	fc, err := LoadFileConfig(filepath.Join(dir, "missing.toml"))
	if err != nil || fc.Token != "" {
		t.Fatalf("missing config must be ok, got %+v %v", fc, err)
	}
	path := filepath.Join(dir, "c.toml")
	if err := os.WriteFile(path, []byte("token = \"x\"\nendpoints = [\"a:1\"]\ndefault_ttl = \"24h\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fc, err = LoadFileConfig(path)
	if err != nil || fc.Token != "x" || len(fc.Endpoints) != 1 || fc.DefaultTTL != "24h" {
		t.Fatalf("config parse wrong: %+v %v", fc, err)
	}
	bad := filepath.Join(dir, "bad.toml")
	os.WriteFile(bad, []byte("token = [not toml"), 0o600)
	if _, err := LoadFileConfig(bad); err == nil {
		t.Fatal("broken toml must fail")
	}
}

func TestFileDefaultTTL(t *testing.T) {
	if got := FileDefaultTTL(fileConfig{}); got != DefaultTTL {
		t.Fatalf("default ttl wrong: %v", got)
	}
	if got := FileDefaultTTL(fileConfig{DefaultTTL: "24h"}); got != 24*time.Hour {
		t.Fatalf("file ttl wrong: %v", got)
	}
	if got := FileDefaultTTL(fileConfig{DefaultTTL: "garbage"}); got != DefaultTTL {
		t.Fatalf("garbage ttl should fall back to default, got %v", got)
	}
}

// TestBuildClientConfig pins the exact frp wire shape against the fleet:
// token auth, forced TLS with fleet SNI, http vhost proxy with subdomain.
// This is a unit test over local config construction only; it makes no claim
// about server behavior (that is covered by live integration).
func TestBuildClientConfig(t *testing.T) {
	opts := Options{
		LocalHost:     "127.0.0.1",
		LocalPort:     3000,
		Subdomain:     "myapp",
		Domain:        DefaultDomain,
		Token:         "tok",
		TLSServerName: DefaultTLSServerName,
	}
	cfg := BuildClientConfig(Gateway{Addr: "gw.example", Port: 27000}, opts)
	c := &cfg.ClientCommonConfig
	if c.ServerAddr != "gw.example" || c.ServerPort != 27000 {
		t.Fatalf("server endpoint wrong: %+v", c)
	}
	if c.Auth.Method != "token" || c.Auth.Token != "tok" {
		t.Fatalf("auth wrong: %+v", c.Auth)
	}
	if c.Transport.TLS.Enable == nil || !*c.Transport.TLS.Enable {
		t.Fatal("tls must be enabled (frps enforces it)")
	}
	if c.Transport.TLS.ServerName != DefaultTLSServerName {
		t.Fatalf("tls server name wrong: %q", c.Transport.TLS.ServerName)
	}
	if c.LoginFailExit == nil || *c.LoginFailExit {
		t.Fatal("LoginFailExit must be false: bezhook retries forever")
	}
	if len(cfg.Proxies) != 1 {
		t.Fatalf("want exactly one proxy, got %d", len(cfg.Proxies))
	}
	http, ok := cfg.Proxies[0].ProxyConfigurer.(*v1.HTTPProxyConfig)
	if !ok {
		t.Fatalf("proxy configurer type = %T, want *v1.HTTPProxyConfig", cfg.Proxies[0].ProxyConfigurer)
	}
	if cfg.Proxies[0].Type != "http" {
		t.Fatalf("proxy type wrong: %q", cfg.Proxies[0].Type)
	}
	if http.Name != "bezhook-myapp" {
		t.Fatalf("proxy name wrong: %q", http.Name)
	}
	if http.SubDomain != "myapp" {
		t.Fatalf("subdomain wrong: %q", http.SubDomain)
	}
	if http.LocalIP != "127.0.0.1" || http.LocalPort != 3000 {
		t.Fatalf("local backend wrong: %s:%d", http.LocalIP, http.LocalPort)
	}
	if !http.Transport.UseEncryption || !http.Transport.UseCompression {
		t.Fatal("proxy transport should use encryption+compression over WAN")
	}
}



func TestParseProviders(t *testing.T) {
	got, err := ParseProviders("cloudflared, ngrok,cloudflared")
	if err != nil || len(got) != 2 || got[0] != "cloudflared" || got[1] != "ngrok" {
		t.Fatalf("ParseProviders = %v, %v", got, err)
	}
	if _, err := ParseProviders("frp,teleport"); err == nil {
		t.Fatal("unknown provider must fail")
	}
	if _, err := ParseProviders("  "); err == nil {
		t.Fatal("empty provider list must fail")
	}
}

func TestParseTryCloudflareURL(t *testing.T) {
	cases := map[string]string{
		"2026-01-01 INF |  https://my-tunnel-x7.trycloudflare.com  |": "https://my-tunnel-x7.trycloudflare.com",
		"no url here": "",
	}
	for in, want := range cases {
		if got := ParseTryCloudflareURL(in); got != want {
			t.Errorf("ParseTryCloudflareURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseNgrokURLs(t *testing.T) {
	body := []byte(`{"tunnels":[{"name":"command_line","public_url":"https://8a2b-94-25-1-10.ngrok-free.app","proto":"https"},{"name":"command_line (http)","public_url":"http://localhost:3000","proto":"http"}]}`)
	urls := ParseNgrokURLs(body)
	if len(urls) != 1 || !strings.HasPrefix(urls[0], "https://") || !strings.HasSuffix(urls[0], ".ngrok-free.app") {
		t.Fatalf("ParseNgrokURLs = %v", urls)
	}
	if ParseNgrokURLs([]byte("not json")) != nil {
		t.Fatal("broken json must yield nil")
	}
}

func TestFindBinExtraDirs(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "fakebin")
	os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755)
	if got, hint := FindBin("fakebin", []string{dir}); got != p || hint != "" {
		t.Fatalf("FindBin extra dir failed: %q %q", got, hint)
	}
	if _, hint := FindBin("sh", []string{}); hint != "" {
		t.Fatal("PATH lookup should succeed for sh without hint")
	}
	nonExec := filepath.Join(dir, "noexec")
	os.WriteFile(nonExec, []byte("x"), 0o644)
	if got, _ := FindBin("noexec", []string{dir}); got != "" {
		t.Fatalf("non-executable file must not match: %q", got)
	}
}
