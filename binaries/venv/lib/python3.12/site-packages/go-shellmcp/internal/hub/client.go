package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/security"
	"github.com/megamen32/gptadmin/go-shellmcp/internal/system"
)

type Client struct {
	BaseURL  string
	HTTP     *http.Client
	Identity *security.Identity
}

func New(base string, id *security.Identity) *Client {
	return &Client{BaseURL: strings.TrimRight(base, "/"), Identity: id, HTTP: &http.Client{Timeout: 90 * time.Second}}
}

type Beat struct {
	Name          string `json:"name"`
	ServerID      string `json:"server_id"`
	PublicKey     string `json:"public_key"`
	Fingerprint   string `json:"fingerprint"`
	BaseURL       string `json:"base_url"`
	Cores         int    `json:"cores"`
	MemMB         int64  `json:"mem_mb"`
	Time          int64  `json:"time"`
	Mode          string `json:"mode"`
	TransportRole string `json:"transport_role"`
	Backend       string `json:"backend"`
	OS            string `json:"os"`
	BuildVersion  int    `json:"build_version"`
	GitCommit     string `json:"git_commit"`
	DefaultUser   string `json:"default_user,omitempty"`
	DefaultHome   string `json:"default_home,omitempty"`
	DefaultCwd    string `json:"default_cwd,omitempty"`
}

type QueueJob struct {
	ID      string            `json:"id"`
	Cmd     string            `json:"cmd"`
	Cwd     string            `json:"cwd"`
	Timeout int               `json:"timeout"`
	Env     map[string]string `json:"env"`
}

type TaskResult struct {
	ID     string `json:"id"`
	Result any    `json:"result"`
}

func (c *Client) Heartbeat(ctx context.Context, beat Beat) (*http.Response, []byte, error) {
	return c.doJSON(ctx, http.MethodPost, "/heartbeat", beat)
}
func (c *Client) PollQueue(ctx context.Context, name string, timeout int) (QueueJob, bool, error) {
	p := "/queue/" + url.PathEscape(name)
	if timeout > 0 {
		p += fmt.Sprintf("?timeout=%d", timeout)
	}
	resp, body, err := c.do(ctx, http.MethodGet, p, nil)
	if err != nil {
		return QueueJob{}, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return QueueJob{}, false, fmt.Errorf("queue poll HTTP %d: %s", resp.StatusCode, string(body))
	}
	if len(bytes.TrimSpace(body)) == 0 || string(bytes.TrimSpace(body)) == "{}" {
		return QueueJob{}, false, nil
	}
	var job QueueJob
	if err := json.Unmarshal(body, &job); err != nil {
		return QueueJob{}, false, err
	}
	return job, job.ID != "" && job.Cmd != "", nil
}
func (c *Client) PostResult(ctx context.Context, name string, res TaskResult) error {
	p := "/queue/" + url.PathEscape(name) + "/result"
	resp, body, err := c.doJSON(ctx, http.MethodPost, p, res)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("result HTTP %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

func NewBeat(id *security.Identity, baseURL, mode string, build int) Beat {
	info := system.Get()
	name := info.Host
	if id != nil && id.Name != "" {
		name = id.Name
	}
	b := Beat{Name: name, BaseURL: baseURL, Cores: info.Cores, MemMB: info.MemMB, Time: time.Now().Unix(), Mode: mode, TransportRole: "shellmcp_transport_layer", Backend: "local", OS: info.OS, BuildVersion: build, GitCommit: "go-shellmcp"}
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
	u, err := url.Parse(c.BaseURL)
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
	if c.Identity != nil {
		for k, v := range c.Identity.Sign(method, u.EscapedPath(), body) {
			req.Header.Set(k, v)
		}
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, nil, err
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body = io.NopCloser(bytes.NewReader(b))
	return resp, b, nil
}
