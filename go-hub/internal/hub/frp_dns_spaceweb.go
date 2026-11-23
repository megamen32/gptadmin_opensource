package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

var errFRPSpaceWebUnresolved = errors.New("FRP SpaceWeb publication unresolved; readback required")

type frpSpaceWeb struct {
	helper string
	name   string
	env    []string
}

func frpSpaceWebFromEnv() (frpSpaceWeb, error) {
	const (
		helperKey    = "GPTADMIN_FRP_DNS_SPACEWEB_HELPER"
		nameKey      = "GPTADMIN_FRP_DNS_SPACEWEB_NAME"
		zoneKey      = "GPTADMIN_FRP_DNS_ZONE"
		credsKey     = "GPTADMIN_FRP_DNS_SPACEWEB_CREDENTIALS_FILE"
		transportKey = "GPTADMIN_FRP_DNS_SPACEWEB_TRANSPORT"
	)
	helper, creds, transport := os.Getenv(helperKey), os.Getenv(credsKey), os.Getenv(transportKey)
	name := strings.ToLower(strings.TrimSuffix(os.Getenv(nameKey), "."))
	zone := strings.ToLower(strings.TrimSuffix(os.Getenv(zoneKey), "."))
	if !filepath.IsAbs(helper) || !filepath.IsAbs(creds) || (transport != "" && !filepath.IsAbs(transport)) ||
		!validFRPDNSZone(name) || !validFRPDNSZone(zone) || !strings.HasSuffix(name, "."+zone) {
		return frpSpaceWeb{}, errors.New("FRP SpaceWeb settings are missing or invalid")
	}
	info, err := os.Stat(helper)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 || info.Mode().Perm()&0022 != 0 {
		return frpSpaceWeb{}, errors.New("FRP SpaceWeb helper is not a trusted executable")
	}
	if info, err := os.Stat(creds); err != nil || !info.Mode().IsRegular() {
		return frpSpaceWeb{}, errors.New("FRP SpaceWeb credential file is unavailable")
	}
	return frpSpaceWeb{helper: helper, name: name, env: []string{
		"PATH=/usr/bin:/bin", nameKey + "=" + name, zoneKey + "=" + zone,
		credsKey + "=" + creds, transportKey + "=" + transport,
	}}, nil
}

type frpSpaceWebResponse struct {
	OK     bool     `json:"ok"`
	Value  string   `json:"value"`
	Values []string `json:"values"`
}

type frpSpaceWebOutput struct{ buffer bytes.Buffer }

func (b *frpSpaceWebOutput) Write(data []byte) (int, error) {
	if len(data) > (64<<10)-b.buffer.Len() {
		return 0, errors.New("FRP SpaceWeb response exceeds limit")
	}
	return b.buffer.Write(data)
}

func (p frpSpaceWeb) call(ctx context.Context, action, address string) (frpSpaceWebResponse, error) {
	select {
	case frpDNSPublication <- struct{}{}:
		defer func() { <-frpDNSPublication }()
	case <-ctx.Done():
		return frpSpaceWebResponse{}, errFRPSpaceWebUnresolved
	}
	request := struct {
		Action string `json:"action"`
		Name   string `json:"name"`
		Value  string `json:"value,omitempty"`
	}{Action: action, Name: p.name, Value: address}
	data, err := json.Marshal(request)
	if err != nil {
		return frpSpaceWebResponse{}, errFRPSpaceWebUnresolved
	}
	cmd := exec.CommandContext(ctx, p.helper, "--allow-append-a")
	cmd.Env = p.env
	cmd.Stdin = bytes.NewReader(data)
	cmd.WaitDelay = time.Second
	var output frpSpaceWebOutput
	cmd.Stdout = &output
	var result frpSpaceWebResponse
	// Stderr and invalid stdout are deliberately kept out of join responses and logs.
	if cmd.Run() != nil || json.Unmarshal(output.buffer.Bytes(), &result) != nil {
		return frpSpaceWebResponse{}, errFRPSpaceWebUnresolved
	}
	return result, nil
}

func (p frpSpaceWeb) publish(ctx context.Context, address string) error {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	result, err := p.call(ctx, "append_a", address)
	if err == nil {
		if result.OK && result.Value == address {
			return nil
		}
		return errFRPSpaceWebUnresolved
	}
	// The helper may have exited after submitting the write. Never launch another add.
	if ctx.Err() == nil {
		return p.reconcile(ctx, address)
	}
	return errFRPSpaceWebUnresolved
}

func (p frpSpaceWeb) reconcile(ctx context.Context, address string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result, err := p.call(ctx, "read_a_pool", "")
	if err != nil || !result.OK || !slices.Contains(result.Values, address) {
		return errFRPSpaceWebUnresolved
	}
	return nil
}
