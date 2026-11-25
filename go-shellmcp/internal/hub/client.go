package hub

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/megamen32/gptadmin/go-shellmcp/hubroute"
	"github.com/megamen32/gptadmin/go-shellmcp/internal/security"
	"github.com/megamen32/gptadmin/go-shellmcp/internal/system"
	"github.com/megamen32/gptadmin/go-shellmcp/receiptredaction"
)

type Client struct {
	BaseURL  string
	HTTP     *http.Client
	Identity *security.Identity
	Token    string
	routes   hubroute.Origins
	unpinned *http.Client
	routeMu  sync.Mutex
	cache    string
}

// Queue arguments may use the Hub's existing 64 MiB request envelope.
// Advertise this bound so older, 1 MiB readers are never leased larger jobs.
const queueResponseMaxBytes = 64 << 20

func New(base string, id *security.Identity, token, dnsServer, resolveTo string) *Client {
	return &Client{BaseURL: strings.TrimRight(base, "/"), Identity: id, Token: token, HTTP: newHTTPClient(dnsServer, resolveTo), unpinned: newHTTPClient(dnsServer, "")}
}

// Cache only HTTPS origins learned on an authenticated queue connection. A
// different configured seed starts a new trust relationship. The embedded
// executor always polls loopback and gets its route from its own Node.
func (c *Client) RestoreQuorumRoutes(dir string) {
	if dir == "" || len(hubroute.Parse(c.BaseURL)) != 1 {
		return
	}
	c.cache = filepath.Join(dir, "shellmcp-quorum-hubs.json")
	data, err := os.ReadFile(c.cache)
	var cached struct{ Seed, Hubs string }
	if err == nil && json.Unmarshal(data, &cached) == nil && cached.Seed == c.BaseURL {
		c.routes.Set(hubroute.Parse(c.BaseURL + "," + cached.Hubs))
	}
}

func (c *Client) learnQuorumRoutes(raw string) {
	if raw == "" || len(hubroute.Parse(c.BaseURL)) != 1 {
		return
	}
	c.routeMu.Lock()
	defer c.routeMu.Unlock()
	if !c.routes.Set(hubroute.Parse(c.BaseURL+","+raw)) || c.cache == "" {
		return
	}
	data, _ := json.Marshal(struct{ Seed, Hubs string }{c.BaseURL, raw})
	f, err := os.CreateTemp(filepath.Dir(c.cache), ".quorum-hubs-*")
	if err != nil {
		return
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if closeErr := f.Close(); err == nil && closeErr == nil {
		_ = os.Rename(f.Name(), c.cache)
	}
}

func newHTTPClient(dnsServer, resolveTo string) *http.Client {
	client := &http.Client{Timeout: 90 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	dnsServer = strings.TrimSpace(dnsServer)
	resolveTo = strings.TrimSpace(resolveTo)
	if resolveTo != "" && net.ParseIP(resolveTo) == nil {
		resolveTo = ""
	}
	roots := configuredRootCAs()
	if dnsServer == "" && resolveTo == "" && roots == nil {
		return client
	}

	resolver := net.DefaultResolver
	if dnsServer != "" {
		host, _, err := net.SplitHostPort(dnsServer)
		if err != nil {
			host = dnsServer
			dnsServer = net.JoinHostPort(dnsServer, "53")
		}
		if net.ParseIP(host) == nil {
			return client
		}
		resolver = &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "udp", dnsServer)
			},
		}
	}

	dialer := &net.Dialer{}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if roots != nil {
		transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	}
	if dnsServer == "" && resolveTo == "" {
		client.Transport = transport
		return client
	}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		var lastErr error
		if resolveTo != "" {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(resolveTo, port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		resolved, err := resolver.LookupHost(ctx, host)
		if err != nil {
			if lastErr == nil {
				return nil, err
			}
			return nil, fmt.Errorf("preferred Hub route %s failed: %v; DNS lookup for %q: %w", resolveTo, lastErr, host, err)
		}
		for _, ip := range resolved {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip, port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, fmt.Errorf("configured DNS server returned no addresses for %q", host)
	}
	client.Transport = transport
	return client
}

// configuredRootCAs keeps user-installed CA bundles effective for the Go
// client on macOS, where crypto/x509 may otherwise rely only on the system
// keychain. This is needed for locally reissued Hub certificates and remains
// additive to the platform roots.
func configuredRootCAs() *x509.CertPool {
	path := strings.TrimSpace(os.Getenv("SSL_CERT_FILE"))
	if path == "" {
		return nil
	}
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	pemBytes, err := os.ReadFile(path)
	if err != nil || !roots.AppendCertsFromPEM(pemBytes) {
		return roots
	}
	return roots
}

type Beat struct {
	Hostname             string           `json:"hostname,omitempty"`
	Name                 string           `json:"name"`
	ServerID             string           `json:"server_id"`
	PublicKey            string           `json:"public_key"`
	Fingerprint          string           `json:"fingerprint"`
	BaseURL              string           `json:"base_url"`
	Cores                int              `json:"cores"`
	MemMB                int64            `json:"mem_mb"`
	Time                 int64            `json:"time"`
	Mode                 string           `json:"mode"`
	TransportRole        string           `json:"transport_role"`
	Backend              string           `json:"backend"`
	OS                   string           `json:"os"`
	BuildVersion         int              `json:"build_version"`
	FileTransferVersion  int              `json:"file_transfer_version,omitempty"`
	FilesystemACLVersion int              `json:"filesystem_acl_version,omitempty"`
	GitCommit            string           `json:"git_commit"`
	DefaultUser          string           `json:"default_user,omitempty"`
	DefaultHome          string           `json:"default_home,omitempty"`
	DefaultCwd           string           `json:"default_cwd,omitempty"`
	MCPAgents            []map[string]any `json:"mcp_agents,omitempty"`
	SpoolBytes           int64            `json:"spool_bytes,omitempty"`
	StorageBytes         int64            `json:"storage_bytes,omitempty"`
	OutboxDepth          int              `json:"outbox_depth,omitempty"`
	OutboxRetryAttempts  int              `json:"outbox_retry_attempts,omitempty"`
	QueuePollCount       int64            `json:"queue_poll_count,omitempty"`
	QueuePollErrors      int64            `json:"queue_poll_errors,omitempty"`
	QueuePollLatencyMS   int64            `json:"queue_poll_latency_ms,omitempty"`
	OutboxRetryFailures  int64            `json:"outbox_retry_failures,omitempty"`
	OutboxDelivered      int64            `json:"outbox_delivered,omitempty"`
	SelfRepairDesired    int              `json:"self_repair_desired,omitempty"`
	SelfRepairState      string           `json:"self_repair_state,omitempty"`
}

type QueueJob struct {
	ReceiptRedaction *receiptredaction.Policy `json:"receipt_redaction,omitempty"`
	ExecutionOrigin  map[string]any           `json:"execution_origin,omitempty"`
	ID               string                   `json:"id"`
	TraceID          string                   `json:"trace_id,omitempty"`
	TraceParent      string                   `json:"traceparent,omitempty"`
	ToolName         string                   `json:"tool_name,omitempty"`
	Arguments        map[string]any           `json:"arguments,omitempty"`
	Cmd              string                   `json:"cmd,omitempty"`
	Cwd              string                   `json:"cwd,omitempty"`
	Timeout          int                      `json:"timeout,omitempty"`
	Env              map[string]string        `json:"env,omitempty"`
	RuntimeSettings  map[string]any           `json:"runtime_settings,omitempty"`
	FilesystemACL    *FilesystemACL           `json:"filesystem_acl,omitempty"`
}

// FilesystemACL is trusted only when received in an authenticated Hub queue
// response. MCP clients must never be able to supply this value in tool args.
type FilesystemACL struct {
	Version         int       `json:"version"`
	ProfileID       string    `json:"profile_id"`
	ProfileVersion  int64     `json:"profile_version"`
	RootsDigest     string    `json:"roots_digest"`
	JobID           string    `json:"job_id"`
	Target          string    `json:"target"`
	Tool            string    `json:"tool"`
	ArgumentsSHA256 string    `json:"args_sha256"`
	ReadRoots       *[]string `json:"read_roots,omitempty"`
	WriteRoots      *[]string `json:"write_roots,omitempty"`
}

type FilesystemACLCheck struct {
	ID              string `json:"id"`
	ToolName        string `json:"tool_name"`
	ArgumentsSHA256 string `json:"args_sha256"`
}

type TaskResult struct {
	RedactionVersion int    `json:"receipt_redaction_version,omitempty"`
	ID               string `json:"id"`
	TraceID          string `json:"trace_id,omitempty"`
	TraceParent      string `json:"traceparent,omitempty"`
	Result           any    `json:"result"`
	Error            any    `json:"error,omitempty"`
}

func (c *Client) Heartbeat(ctx context.Context, beat Beat) (*http.Response, []byte, error) {
	return c.doJSON(ctx, http.MethodPost, "/heartbeat", beat)
}
func (c *Client) PollQueue(ctx context.Context, beat Beat, timeout int) (QueueJob, bool, error) {
	name := beat.Name
	p := "/queue/" + url.PathEscape(name)
	q := url.Values{}
	q.Set("max_response_bytes", fmt.Sprint(queueResponseMaxBytes))
	q.Set("receipt_redaction", fmt.Sprint(receiptredaction.Version))
	if timeout > 0 {
		q.Set("timeout", fmt.Sprintf("%d", timeout))
	}
	if beat.ServerID != "" {
		q.Set("server_id", beat.ServerID)
		q.Set("hostname", beat.Hostname)
	}
	if beat.PublicKey != "" {
		q.Set("public_key", beat.PublicKey)
	}
	if beat.Fingerprint != "" {
		q.Set("fingerprint", beat.Fingerprint)
	}
	if beat.BaseURL != "" {
		q.Set("base_url", beat.BaseURL)
	}
	if beat.Mode != "" {
		q.Set("mode", beat.Mode)
	}
	if beat.TransportRole != "" {
		q.Set("transport_role", beat.TransportRole)
	}
	if beat.Backend != "" {
		q.Set("backend", beat.Backend)
	}
	if beat.OS != "" {
		q.Set("os", beat.OS)
	}
	if beat.Cores > 0 {
		q.Set("cores", fmt.Sprintf("%d", beat.Cores))
	}
	if beat.MemMB > 0 {
		q.Set("mem_mb", fmt.Sprintf("%d", beat.MemMB))
	}
	if beat.BuildVersion > 0 {
		q.Set("build_version", fmt.Sprintf("%d", beat.BuildVersion))
	}
	if beat.FileTransferVersion > 0 {
		q.Set("file_transfer_version", fmt.Sprintf("%d", beat.FileTransferVersion))
	}
	if beat.FilesystemACLVersion > 0 {
		q.Set("filesystem_acl_version", fmt.Sprintf("%d", beat.FilesystemACLVersion))
	}
	if beat.GitCommit != "" {
		q.Set("git_commit", beat.GitCommit)
	}
	if beat.DefaultUser != "" {
		q.Set("default_user", beat.DefaultUser)
	}
	if beat.DefaultHome != "" {
		q.Set("default_home", beat.DefaultHome)
	}
	if beat.DefaultCwd != "" {
		q.Set("default_cwd", beat.DefaultCwd)
	}
	if beat.SpoolBytes > 0 {
		q.Set("spool_bytes", fmt.Sprintf("%d", beat.SpoolBytes))
	}
	if beat.StorageBytes > 0 {
		q.Set("storage_bytes", fmt.Sprintf("%d", beat.StorageBytes))
	}
	if beat.OutboxDepth > 0 {
		q.Set("outbox_depth", fmt.Sprintf("%d", beat.OutboxDepth))
	}
	if beat.OutboxRetryAttempts > 0 {
		q.Set("outbox_retry_attempts", fmt.Sprintf("%d", beat.OutboxRetryAttempts))
	}
	if beat.QueuePollCount > 0 {
		q.Set("queue_poll_count", fmt.Sprintf("%d", beat.QueuePollCount))
	}
	if beat.QueuePollErrors > 0 {
		q.Set("queue_poll_errors", fmt.Sprintf("%d", beat.QueuePollErrors))
	}
	if beat.QueuePollLatencyMS > 0 {
		q.Set("queue_poll_latency_ms", fmt.Sprintf("%d", beat.QueuePollLatencyMS))
	}
	if beat.OutboxRetryFailures > 0 {
		q.Set("outbox_retry_failures", fmt.Sprintf("%d", beat.OutboxRetryFailures))
	}
	if beat.OutboxDelivered > 0 {
		q.Set("outbox_delivered", fmt.Sprintf("%d", beat.OutboxDelivered))
	}
	if beat.SelfRepairDesired > 0 {
		q.Set("self_repair_desired", fmt.Sprintf("%d", beat.SelfRepairDesired))
	}
	if beat.SelfRepairState != "" {
		q.Set("self_repair_state", beat.SelfRepairState)
	}
	if enc := q.Encode(); enc != "" {
		p += "?" + enc
	}
	resp, body, err := c.doWithResponseLimit(ctx, http.MethodGet, p, nil, queueResponseMaxBytes)
	if err != nil {
		return QueueJob{}, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return QueueJob{}, false, fmt.Errorf("queue poll HTTP %d: %s", resp.StatusCode, string(body))
	}
	if resp.TLS != nil && len(resp.TLS.VerifiedChains) > 0 {
		c.learnQuorumRoutes(resp.Header.Get(hubroute.Header))
	}
	if len(bytes.TrimSpace(body)) == 0 || string(bytes.TrimSpace(body)) == "{}" {
		return QueueJob{}, false, nil
	}
	var job QueueJob
	if err := json.Unmarshal(body, &job); err != nil {
		return QueueJob{}, false, err
	}
	return job, job.ID != "" && (job.Cmd != "" || job.ToolName != ""), nil
}

func (c *Client) CheckFilesystemACL(ctx context.Context, name string, check FilesystemACLCheck) (FilesystemACL, error) {
	path := "/queue/" + url.PathEscape(name) + "/acl-check"
	response, body, err := c.doJSON(ctx, http.MethodPost, path, check)
	if err != nil {
		return FilesystemACL{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return FilesystemACL{}, fmt.Errorf("filesystem ACL check HTTP %d", response.StatusCode)
	}
	var result struct {
		Policy FilesystemACL `json:"filesystem_acl"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return FilesystemACL{}, err
	}
	return result.Policy, nil
}

type HTTPError struct {
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string { return fmt.Sprintf("result HTTP %d: %s", e.StatusCode, e.Body) }

func (c *Client) PostResult(ctx context.Context, name string, res TaskResult) error {
	body, err := json.Marshal(res)
	if err != nil {
		return err
	}
	return c.PostResultJSON(ctx, name, res.ID, body)
}

// PostResultJSON preserves exact JSON numbers and opaque result bytes from a
// previous process. The full job ID lets ingress select the owner before reading
// the body; the owner independently checks the body against that route.
func (c *Client) PostResultJSON(ctx context.Context, name, id string, body []byte) error {
	p := "/queue/" + url.PathEscape(name) + "/result"
	scoped := strings.HasPrefix(id, "ot1.")
	if scoped {
		p += "?job_id=" + url.QueryEscape(id)
		if receiptredaction.PayloadVersion(body) == receiptredaction.Version {
			p += "&receipt_redaction=1"
		}
	}
	resp, ackBody, err := c.do(ctx, http.MethodPost, p, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &HTTPError{StatusCode: resp.StatusCode, Body: string(ackBody)}
	}
	var ack struct {
		OK      bool   `json:"ok"`
		ID      string `json:"id"`
		OwnerID string `json:"owner_id"`
	}
	if json.Unmarshal(ackBody, &ack) != nil || !ack.OK {
		return fmt.Errorf("Hub did not acknowledge result %s", id)
	}
	if scoped {
		owner, _, _ := strings.Cut(strings.TrimPrefix(id, "ot1."), ".")
		if ack.ID != id || ack.OwnerID == "" || base64.RawURLEncoding.EncodeToString([]byte(ack.OwnerID)) != owner {
			return fmt.Errorf("owner did not acknowledge result %s", id)
		}
	}
	return nil
}

func NewBeat(id *security.Identity, baseURL, mode string, build int) Beat {
	info := system.Get()
	name := info.Host
	if id != nil && id.Name != "" {
		name = id.Name
	}
	b := Beat{Name: name, BaseURL: baseURL, Cores: info.Cores, MemMB: info.MemMB, Time: time.Now().Unix(), Mode: mode, TransportRole: "shellmcp_transport_layer", Backend: "local", OS: info.OS, BuildVersion: build, FilesystemACLVersion: 1, FileTransferVersion: 3, GitCommit: "go-shellmcp"}
	if id != nil {
		b.ServerID = id.ServerID
		b.PublicKey = id.PublicKey
		b.Fingerprint = id.Fingerprint
	}
	return b
}

func (c *Client) doJSON(ctx context.Context, method, p string, payload any) (*http.Response, []byte, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, err
	}
	return c.do(ctx, method, p, b)
}
func (c *Client) do(ctx context.Context, method, p string, body []byte) (*http.Response, []byte, error) {
	return c.doWithResponseLimit(ctx, method, p, body, 1<<20)
}

func (c *Client) doWithResponseLimit(ctx context.Context, method, p string, body []byte, maxBytes int64) (*http.Response, []byte, error) {
	origin := c.routes.Current()
	client := c.HTTP
	if origin == "" {
		origin = c.BaseURL
	} else if origin != c.BaseURL {
		client = c.unpinned
	}
	u, err := url.Parse(origin)
	if err != nil {
		return nil, nil, err
	}
	u.Path = path.Clean("/" + strings.TrimLeft(p, "/"))
	if strings.Contains(p, "?") {
		parts := strings.SplitN(p, "?", 2)
		u.Path = path.Clean("/" + strings.TrimLeft(parts[0], "/"))
		u.RawQuery = parts[1]
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if c.Identity != nil {
		for k, v := range c.Identity.Sign(method, u.EscapedPath(), body) {
			req.Header.Set(k, v)
		}
	}
	req.GetBody = nil
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() == nil {
			c.routes.Failed(origin)
		}
		return nil, nil, err
	}
	if resp.StatusCode >= 500 || resp.StatusCode >= 300 && resp.StatusCode < 400 {
		c.routes.Failed(origin)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		c.routes.Failed(origin)
		return nil, nil, fmt.Errorf("Hub response read failed: %w", err)
	}
	if int64(len(b)) > maxBytes {
		return nil, nil, fmt.Errorf("Hub response exceeds %d bytes", maxBytes)
	}
	resp.Body = io.NopCloser(bytes.NewReader(b))
	return resp, b, nil
}
