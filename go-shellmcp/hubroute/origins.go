// Package hubroute selects an ingress for the next independent request to a
// quorum. All origins come from the operator or the authenticated Hub.
package hubroute

import (
	"net/url"
	"slices"
	"strings"
	"sync"
)

const Header = "X-GPTAdmin-Quorum-Hubs"

type Origins struct {
	mu    sync.Mutex
	urls  []string
	index int
}

// Parse accepts only complete HTTPS origins. Never send a bearer to an
// advertised path, a URL containing credentials, or a cleartext alternative.
func Parse(raw string) []string {
	var urls []string
	if len(raw) > 4096 {
		return nil
	}
	for _, part := range strings.Split(raw, ",") {
		u, err := url.Parse(strings.TrimSpace(part))
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
			(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
			return nil
		}
		origin := "https://" + u.Host
		if !slices.Contains(urls, origin) {
			urls = append(urls, origin)
		}
	}
	return urls
}

func (o *Origins) Set(urls []string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(urls) == 0 || slices.Equal(o.urls, urls) {
		return false
	}
	o.urls, o.index = slices.Clone(urls), 0
	return true
}

func (o *Origins) Current() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.urls) == 0 {
		return ""
	}
	return o.urls[o.index]
}

// Failed never retries the request. A late failure from a concurrent poll
// also cannot move a healthy connection off its current origin.
func (o *Origins) Failed(origin string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.urls) > 0 && o.urls[o.index] == origin {
		o.index = (o.index + 1) % len(o.urls)
	}
}
