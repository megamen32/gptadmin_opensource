package cloudos

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

var directAgentHTTPClient = &http.Client{Timeout: 10 * time.Second}

func callDirectAgent(comp *Computer, method, path string, input any, output any) error {
	base, err := url.Parse(comp.Endpoint)
	if err != nil || base.Scheme != "http" || base.Host == "" {
		return fmt.Errorf("invalid agent endpoint")
	}
	target, err := base.Parse(path)
	if err != nil {
		return fmt.Errorf("invalid agent request")
	}
	var body *bytes.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	} else {
		body = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, target.String(), body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+comp.AgentToken)
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := directAgentHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("agent unavailable: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return fmt.Errorf("agent returned HTTP %d", res.StatusCode)
	}
	if err := json.NewDecoder(res.Body).Decode(output); err != nil {
		return fmt.Errorf("invalid agent response: %w", err)
	}
	return nil
}
