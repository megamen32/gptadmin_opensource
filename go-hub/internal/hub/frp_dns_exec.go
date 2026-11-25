package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// The executable and its credentials are selected by the local operator.
// Neither is accepted from a join grant or an incoming relay.
type frpDNSHelper struct {
	helper     string
	name       string
	args       []string
	env        []string
	unresolved error
}

func validFRPDNSPool(zone, name string) bool {
	return validFRPDNSZone(name) && validFRPDNSZone(zone) && strings.HasSuffix(name, "."+zone)
}

func frpDNSNameFromEnv(key string) string {
	return strings.ToLower(strings.TrimSuffix(os.Getenv(key), "."))
}

func frpDNSExecFromEnv() (frpDNSHelper, error) {
	const (
		zoneKey  = "GPTADMIN_FRP_DNS_ZONE"
		nameKey  = "GPTADMIN_FRP_DNS_NAME"
		credsKey = "GPTADMIN_FRP_DNS_CREDENTIALS_FILE"
	)
	zone, name := frpDNSNameFromEnv(zoneKey), frpDNSNameFromEnv(nameKey)
	if !validFRPDNSPool(zone, name) {
		return frpDNSHelper{}, errors.New("FRP DNS adapter requires an exact name in the configured zone")
	}
	env := []string{"PATH=/usr/bin:/bin", zoneKey + "=" + zone, nameKey + "=" + name}
	creds := os.Getenv(credsKey)
	if creds != "" {
		env = append(env, credsKey+"="+creds)
	}
	return newFRPDNSHelper("DNS adapter", os.Getenv("GPTADMIN_FRP_DNS_HELPER"), name, creds, nil, env)
}

func newFRPDNSHelper(provider, helper, name, creds string, args, env []string) (frpDNSHelper, error) {
	info, err := os.Stat(helper)
	if !filepath.IsAbs(helper) || err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 || info.Mode().Perm()&0022 != 0 {
		return frpDNSHelper{}, fmt.Errorf("FRP %s helper is not a trusted executable", provider)
	}
	if creds != "" {
		if info, err := os.Stat(creds); !filepath.IsAbs(creds) || err != nil || !info.Mode().IsRegular() {
			return frpDNSHelper{}, fmt.Errorf("FRP %s credential file is unavailable", provider)
		}
	}
	return frpDNSHelper{helper: helper, name: name, args: args, env: env,
		unresolved: fmt.Errorf("FRP %s publication unresolved; readback required", provider)}, nil
}

type frpDNSHelperResponse struct {
	OK     bool     `json:"ok"`
	Value  string   `json:"value"`
	Values []string `json:"values"`
}

type frpDNSHelperOutput struct{ buffer bytes.Buffer }

func (b *frpDNSHelperOutput) Write(data []byte) (int, error) {
	if len(data) > (64<<10)-b.buffer.Len() {
		return 0, errors.New("FRP DNS adapter response exceeds limit")
	}
	return b.buffer.Write(data)
}

func (p frpDNSHelper) call(ctx context.Context, action, address string) (frpDNSHelperResponse, error) {
	select {
	case frpDNSPublication <- struct{}{}:
		defer func() { <-frpDNSPublication }()
	case <-ctx.Done():
		return frpDNSHelperResponse{}, p.unresolved
	}
	request := struct {
		Action string `json:"action"`
		Name   string `json:"name"`
		Value  string `json:"value,omitempty"`
	}{Action: action, Name: p.name, Value: address}
	data, err := json.Marshal(request)
	if err != nil {
		return frpDNSHelperResponse{}, p.unresolved
	}
	cmd := exec.CommandContext(ctx, p.helper, p.args...)
	cmd.Env = p.env
	cmd.Stdin = bytes.NewReader(data)
	cmd.WaitDelay = time.Second
	var output frpDNSHelperOutput
	cmd.Stdout = &output
	var result frpDNSHelperResponse
	// Stderr and invalid stdout are deliberately kept out of join responses and logs.
	if cmd.Run() != nil || json.Unmarshal(output.buffer.Bytes(), &result) != nil {
		return frpDNSHelperResponse{}, p.unresolved
	}
	return result, nil
}

func (p frpDNSHelper) publish(ctx context.Context, address string) error {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	result, err := p.call(ctx, "append_a", address)
	if err == nil {
		if result.OK && result.Value == address {
			return nil
		}
		return p.unresolved
	}
	// The helper may have exited after submitting the write. Never launch another add.
	if ctx.Err() == nil {
		return p.reconcile(ctx, address)
	}
	return p.unresolved
}

func (p frpDNSHelper) reconcile(ctx context.Context, address string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result, err := p.call(ctx, "read_a_pool", "")
	if err != nil || !result.OK || !slices.Contains(result.Values, address) {
		return p.unresolved
	}
	return nil
}
