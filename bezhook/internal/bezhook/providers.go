package bezhook

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ProviderState is reported by the external tunnel providers (cloudflared,
// ngrok). Online=true once the public URL is known.
type ProviderState struct {
	Name   string
	URL    string
	Online bool
	Err    string
}

// KnownProviders are the supported tunnel backends.
var KnownProviders = []string{"frp", "cloudflared", "ngrok"}

// ParseProviders parses and validates a comma-separated provider list
// ("cloudflared,ngrok"), preserving order and dropping duplicates.
func ParseProviders(list string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, part := range strings.Split(list, ",") {
		part = strings.TrimSpace(strings.ToLower(part))
		if part == "" {
			continue
		}
		known := false
		for _, k := range KnownProviders {
			if part == k {
				known = true
				break
			}
		}
		if !known {
			return nil, fmt.Errorf("unknown provider %q (known: %s)", part, strings.Join(KnownProviders, ", "))
		}
		if !seen[part] {
			seen[part] = true
			out = append(out, part)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no providers given")
	}
	return out, nil
}

// ExtraBinDirs is where bezhook looks for provider binaries besides PATH.
var ExtraBinDirs = func() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{filepath.Join(home, ".bezhook", "bin"), filepath.Join(home, ".local", "bin")}
}

// FindBin returns the path of a provider binary or "" with an install hint.
func FindBin(name string, extraDirs []string) (path, hint string) {
	if p, err := exec.LookPath(name); err == nil {
		return p, ""
	}
	for _, dir := range extraDirs {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p, ""
		}
	}
	switch name {
	case "cloudflared":
		return "", "install cloudflared: https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/ (или winget install cloudflare.cloudflared / brew install cloudflared)"
	case "ngrok":
		return "", "install ngrok: https://ngrok.com/download (нужен бесплатный аккаунт и NGROK_AUTHTOKEN)"
	}
	return "", "binary not found: " + name
}

var tryCloudflareRe = regexp.MustCompile(`https://[a-z0-9]([a-z0-9-]*[a-z0-9])?\.trycloudflare\.com`)

// ParseTryCloudflareURL extracts the quick-tunnel URL from cloudflared output.
func ParseTryCloudflareURL(chunk string) string {
	return tryCloudflareRe.FindString(chunk)
}

// extractJSONMessage pulls "message" out of a cloudflared JSON log line.
func extractJSONMessage(line string) string {
	var entry struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		return ""
	}
	return entry.Message
}

// runProviderLoop supervises one provider: (re)start with backoff, report
// transitions through cb, return when ctx is done.
func runProviderLoop(ctx context.Context, name string, start func(ctx context.Context, report func(ProviderState)) error, cb func(ProviderState)) {
	for {
		err := start(ctx, cb)
		if ctx.Err() != nil {
			cb(ProviderState{Name: name, Online: false, Err: "stopped"})
			return
		}
		cb(ProviderState{Name: name, Online: false, Err: fmt.Sprintf("process exited (%v), restarting in 5s", err)})
		if !sleepCtx(ctx, 5*time.Second) {
			return
		}
	}
}

// RunCloudflared runs `cloudflared tunnel --url http://host:port` (free quick
// tunnel, random https://*.trycloudflare.com URL) until ctx is done.
func RunCloudflared(ctx context.Context, opts Options, cb func(ProviderState)) error {
	bin, hint := FindBin("cloudflared", ExtraBinDirs())
	if bin == "" {
		return fmt.Errorf("cloudflared not found: %s", hint)
	}
	run := func(ctx context.Context, report func(ProviderState)) error {
		cmd := exec.CommandContext(ctx, bin,
			"tunnel", "--no-autoupdate", "--protocol", "http2",
			"--url", fmt.Sprintf("http://%s:%d", opts.LocalHost, opts.LocalPort),
		)
		cmd.WaitDelay = 3 * time.Second
		setPgid(cmd)
		stderr, err := cmd.StderrPipe()
		if err != nil {
			return err
		}
		if err := cmd.Start(); err != nil {
			return err
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		reported := false
		go func() {
			sc := bufio.NewScanner(stderr)
			sc.Buffer(make([]byte, 64*1024), 64*1024)
			for sc.Scan() {
				line := sc.Text()
				if !reported {
					if u := ParseTryCloudflareURL(line); u != "" {
						reported = true
						report(ProviderState{Name: "cloudflared", URL: u, Online: true})
					}
				}
				// Surface cloudflared's own error lines (JSON logs) so 530-style
				// edge problems are visible in bezhook output.
				if strings.Contains(line, `"level":"error"`) {
					if msg := extractJSONMessage(line); msg != "" {
						report(ProviderState{Name: "cloudflared", Online: false, Err: msg})
					}
				}
			}
		}()
		select {
		case <-ctx.Done():
			killTree(cmd.Process)
			<-done
			return ctx.Err()
		case err := <-done:
			return err
		}
	}
	runProviderLoop(ctx, "cloudflared", run, cb)
	return nil
}

// ngrokAPIResponse is the subset of ngrok's local agent API (default
// http://127.0.0.1:4040/api/tunnels) that bezhook needs.
type ngrokAPIResponse struct {
	Tunnels []struct {
		Name      string `json:"name"`
		PublicURL string `json:"public_url"`
	} `json:"tunnels"`
}

// ParseNgrokURLs extracts https public URLs from the agent API body.
func ParseNgrokURLs(body []byte) []string {
	var r ngrokAPIResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil
	}
	var urls []string
	for _, t := range r.Tunnels {
		if strings.HasPrefix(t.PublicURL, "https://") {
			urls = append(urls, t.PublicURL)
		}
	}
	return urls
}

// RunNgrok runs `ngrok http <port>` (free personal tier; needs
// NGROK_AUTHTOKEN in the environment or a configured account) until ctx is
// done, discovering the public URL through the local agent API on :4040.
func RunNgrok(ctx context.Context, opts Options, cb func(ProviderState)) error {
	bin, hint := FindBin("ngrok", ExtraBinDirs())
	if bin == "" {
		return fmt.Errorf("ngrok not found: %s", hint)
	}
	run := func(ctx context.Context, report func(ProviderState)) error {
		cmd := exec.CommandContext(ctx, bin,
			"http", fmt.Sprintf("%s:%d", opts.LocalHost, opts.LocalPort),
			"--log", "stdout", "--log-format", "term",
		)
		cmd.WaitDelay = 3 * time.Second
		setPgid(cmd)
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		if err := cmd.Start(); err != nil {
			return err
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()

		tick := time.NewTicker(500 * time.Millisecond)
		defer tick.Stop()
		client := &http.Client{Timeout: 2 * time.Second}
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				killTree(cmd.Process)
				<-done
				return ctx.Err()
			case err := <-done:
				return fmt.Errorf("ngrok exited early (нет authtoken? задай NGROK_AUTHTOKEN): %v", err)
			case <-tick.C:
				resp, err := client.Get("http://127.0.0.1:4040/api/tunnels")
				if err != nil {
					continue
				}
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
				resp.Body.Close()
				if urls := ParseNgrokURLs(body); len(urls) > 0 {
					report(ProviderState{Name: "ngrok", URL: urls[0], Online: true})
					select {
					case <-ctx.Done():
						killTree(cmd.Process)
						<-done
						return ctx.Err()
					case err := <-done:
						return err
					}
				}
			}
		}
	}
	runProviderLoop(ctx, "ngrok", run, cb)
	return nil
}
