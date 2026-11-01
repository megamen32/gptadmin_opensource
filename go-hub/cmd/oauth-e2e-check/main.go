// oauth-e2e-check verifies the public authorization-code flow using the same
// HTTP Basic client authentication that Codex sends to the token endpoint.
// The admin password is read once from stdin; it is never accepted as a flag
// or environment variable and is never printed.
package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const defaultOrigin = "https://gptadmin.bezrabotnyi.com"

func main() {
	originFlag := flag.String("origin", defaultOrigin, "public GPTAdmin origin")
	flag.Parse()
	origin := strings.TrimRight(*originFlag, "/")
	password, err := io.ReadAll(io.LimitReader(os.Stdin, 4097))
	if err != nil || len(bytes.TrimSpace(password)) == 0 || len(password) > 4096 {
		fail("password stdin is required")
	}
	password = bytes.TrimSpace(password)

	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	callback := "http://127.0.0.1:49173/callback/gptadmin-e2e"
	registeredResponse := postJSON(client, origin+"/register", map[string]any{"redirect_uris": []string{callback}, "client_name": "gptadmin-oauth-e2e-check"})
	if registeredResponse.StatusCode != http.StatusCreated {
		fail("registration failed")
	}
	var registration struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	decode(registeredResponse.Body, &registration)
	if registration.ClientID == "" || registration.ClientSecret == "" {
		fail("registration returned incomplete credentials")
	}

	verifier := randomURL(48)
	challengeBytes := sha256.Sum256([]byte(verifier))
	form := url.Values{"client_id": {registration.ClientID}, "redirect_uri": {callback}, "resource": {origin + "/mcp"}, "scope": {"gptadmin.read gptadmin.inspect gptadmin.exec offline_access"}, "password": {string(password)}, "code_challenge": {base64.RawURLEncoding.EncodeToString(challengeBytes[:])}, "code_challenge_method": {"S256"}}
	authorized := postForm(client, origin+"/oauth/authorize", form)
	if authorized.StatusCode != http.StatusFound {
		fail("authorization failed")
	}
	location, err := url.Parse(authorized.Header.Get("Location"))
	if err != nil {
		fail("authorization redirect missing")
	}
	code := location.Query().Get("code")
	if code == "" {
		fail("authorization code missing")
	}

	tokenForm := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {callback}, "resource": {origin + "/mcp"}, "code_verifier": {verifier}}
	tokenRequest, err := http.NewRequest(http.MethodPost, origin+"/oauth/token", strings.NewReader(tokenForm.Encode()))
	if err != nil {
		fail("token request creation failed")
	}
	tokenRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tokenRequest.SetBasicAuth(registration.ClientID, registration.ClientSecret)
	tokenResponse, err := client.Do(tokenRequest)
	if err != nil {
		fail("token request failed")
	}
	defer tokenResponse.Body.Close()
	if tokenResponse.StatusCode != http.StatusOK {
		fail("token exchange failed")
	}
	var token struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	decode(tokenResponse.Body, &token)
	if token.AccessToken == "" || !strings.EqualFold(token.TokenType, "Bearer") {
		fail("token response invalid")
	}
	fmt.Println("OAuth E2E: OK (authorization-code flow and HTTP Basic client authentication)")
}

func postJSON(c *http.Client, endpoint string, body any) *http.Response {
	raw, _ := json.Marshal(body)
	request, _ := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(raw))
	request.Header.Set("Content-Type", "application/json")
	response, err := c.Do(request)
	if err != nil {
		fail("registration request failed")
	}
	return response
}
func postForm(c *http.Client, endpoint string, body url.Values) *http.Response {
	request, _ := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(body.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := c.Do(request)
	if err != nil {
		fail("authorization request failed")
	}
	return response
}
func decode(r io.Reader, target any) {
	if json.NewDecoder(r).Decode(target) != nil {
		fail("invalid JSON response")
	}
}
func randomURL(size int) string {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		fail("random generation failed")
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func fail(message string) { fmt.Fprintln(os.Stderr, "OAuth E2E:", message); os.Exit(1) }
