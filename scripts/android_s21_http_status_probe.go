// Command android_s21_http_status_probe prints only the HTTP status returned by
// the phone-local Android MCP initialize endpoint. It is cross-compiled for the
// S21 and used for secret-safe bootstrap diagnostics without adb forwarding.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

func tokenFrom(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "export "))
		if key, value, ok := strings.Cut(line, "="); ok && key == "ANDROID_S21_MCP_TOKEN" {
			return strings.Trim(strings.TrimSpace(value), "\"'"), nil
		}
	}
	return "", fmt.Errorf("token missing")
}

func main() {
	path := ""
	if len(os.Args) > 1 {
		path = os.Args[1]
	}
	token, err := tokenFrom(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "token_error")
		os.Exit(2)
	}
	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-03-26",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "s21-status-probe", "version": "1"},
		},
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:8080/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "request_error")
		os.Exit(3)
	}
	defer resp.Body.Close()
	fmt.Println(resp.StatusCode)
}
