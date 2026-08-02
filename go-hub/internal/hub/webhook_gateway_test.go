package hub

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func webhookTestSignature(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp + "."))
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func webhookRequest(method, path string, body []byte) *http.Request {
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	return request
}

func TestWebhookGatewayRejectsMissingOrInvalidHMAC(t *testing.T) {
	s := New(Config{WebhookRoutes: []WebhookRoute{{
		ID:         "build",
		HMACSecret: "webhook-secret",
		Action:     WebhookAction{Kind: "mcp", Target: "hub", Tool: "status"},
	}}})
	body := []byte(`{"message":"hello"}`)

	for name, signature := range map[string]string{
		"missing": "",
		"invalid": "sha256=00",
	} {
		t.Run(name, func(t *testing.T) {
			record := httptest.NewRecorder()
			request := webhookRequest(http.MethodPost, "/webhooks/v1/build", body)
			request.Header.Set("X-Webhook-Timestamp", strconv.FormatInt(time.Now().Unix(), 10))
			if signature != "" {
				request.Header.Set("X-Webhook-Signature", signature)
			}
			s.Handler().ServeHTTP(record, request)
			if record.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, body = %s", record.Code, record.Body.String())
			}
		})
	}
}

func TestWebhookGatewayRendersJSONAndDispatchesConfiguredShell(t *testing.T) {
	s := New(Config{WebhookRoutes: []WebhookRoute{{
		ID:    "build",
		Token: "webhook-token",
		Action: WebhookAction{
			Kind:         "shell",
			Target:       "shell:runner",
			ApprovalMode: approvalModeBoundedAutonomous,
			Command:      `printf '%s:%s' '{{event.repository.name}}' '{{event.number}}'`,
			Cwd:          "/srv/project",
		},
	}}})
	s.mu.Lock()
	s.agents["shell:runner"] = &Agent{AgentID: "shell:runner", Status: "online"}
	s.mu.Unlock()
	body := []byte(`{"repository":{"name":"gptadmin"},"number":42}`)
	request := webhookRequest(http.MethodPost, "/webhooks/v1/build", body)
	request.Header.Set("Authorization", "Bearer webhook-token")
	record := httptest.NewRecorder()
	s.Handler().ServeHTTP(record, request)
	if record.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", record.Code, record.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(record.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	jobID, ok := response["job_id"].(string)
	if !ok || jobID == "" {
		t.Fatalf("response missing job_id: %s", record.Body.String())
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		var rendered *shellJob
		for _, job := range s.shellJobs {
			if job.Cmd == `printf '%s:%s' "$GPTADMIN_WEBHOOK_VALUE_0" "$GPTADMIN_WEBHOOK_VALUE_1"` && job.Env["GPTADMIN_WEBHOOK_VALUE_0"] == "gptadmin" && job.Env["GPTADMIN_WEBHOOK_VALUE_1"] == "42" && job.Cwd == "/srv/project" {
				copy := *job
				rendered = &copy
				break
			}
		}
		s.mu.Unlock()
		if rendered != nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("shell job %q was not dispatched", jobID)
}

func TestWebhookShellTemplateValuesCannotBecomeShellSource(t *testing.T) {
	payload := `'; touch /tmp/webhook-should-not-execute; echo '`
	command, environment, err := renderWebhookShellCommand(`printf '%s' '{{event.value}}'`, map[string]any{"value": payload})
	if err != nil {
		t.Fatalf("safe webhook rendering rejected event: %v", err)
	}
	if strings.Contains(command, payload) {
		t.Fatalf("webhook event value was interpolated into shell source: %q", command)
	}
	if len(environment) == 0 || environment["GPTADMIN_WEBHOOK_VALUE_0"] != payload {
		t.Fatalf("webhook event value was not isolated as data: cmd=%q env=%#v", command, environment)
	}
}

func TestWebhookGatewayIdempotencyReturnsOriginalJob(t *testing.T) {
	s := New(Config{WebhookRoutes: []WebhookRoute{{
		ID:     "build",
		Token:  "webhook-token",
		Action: WebhookAction{Kind: "mcp", Target: "hub", Tool: "status"},
	}}})
	body := []byte(`{"event":"push"}`)
	call := func() map[string]any {
		request := webhookRequest(http.MethodPost, "/webhooks/v1/build", body)
		request.Header.Set("Authorization", "Bearer webhook-token")
		request.Header.Set("Idempotency-Key", "provider-event-1")
		record := httptest.NewRecorder()
		s.Handler().ServeHTTP(record, request)
		if record.Code != http.StatusAccepted {
			t.Fatalf("status = %d, body = %s", record.Code, record.Body.String())
		}
		var response map[string]any
		if err := json.Unmarshal(record.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	first := call()
	second := call()
	if first["job_id"] != second["job_id"] {
		t.Fatalf("duplicate created a new job: first=%v second=%v", first, second)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.webhookJobs) != 1 {
		t.Fatalf("webhook jobs = %d, want 1", len(s.webhookJobs))
	}
}

func TestWebhookGatewayDeliversConfiguredCallback(t *testing.T) {
	callback := make(chan []byte, 1)
	callbackServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		callback <- body
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer callbackServer.Close()

	s := New(Config{WebhookRoutes: []WebhookRoute{{
		ID:       "status",
		Token:    "webhook-token",
		Action:   WebhookAction{Kind: "mcp", Target: "hub", Tool: "status"},
		Callback: &WebhookCallback{URL: callbackServer.URL},
	}}})
	request := webhookRequest(http.MethodPost, "/webhooks/v1/status", []byte(`{"event":"push"}`))
	request.Header.Set("Authorization", "Bearer webhook-token")
	record := httptest.NewRecorder()
	s.Handler().ServeHTTP(record, request)
	if record.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", record.Code, record.Body.String())
	}

	select {
	case body := <-callback:
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatal(err)
		}
		if payload["status"] != "completed" || payload["route_id"] != "status" {
			t.Fatalf("callback payload = %s", string(body))
		}
	case <-time.After(time.Second):
		t.Fatal("callback was not delivered")
	}
}

func TestWebhookSignatureFormatUsesRawBody(t *testing.T) {
	body := []byte(`{"a":1}`)
	if strings.Contains(webhookTestSignature("secret", "1", body), " ") {
		t.Fatal("signature contains whitespace")
	}
}

func TestWebhookV2SignatureBindsMethodPathAndIdempotencyKey(t *testing.T) {
	secret := "webhook-v2-secret"
	route := WebhookRoute{
		ID: "notify-repair-100", HMACSecret: secret, SignatureVersion: "v2",
		Action: WebhookAction{Kind: "mcp", Target: "hub", Tool: "status"},
	}
	s := New(Config{WebhookRoutes: []WebhookRoute{route}})
	body := []byte(`{"schema":"notify.agent-job.v1"}`)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	path := "/webhooks/v1/notify-repair-100"
	key := "notify-delivery-1"
	signature := webhookSignatureV2(secret, http.MethodPost, path, timestamp, key, body)

	valid := webhookRequest(http.MethodPost, path, body)
	valid.Header.Set("X-Webhook-Timestamp", timestamp)
	valid.Header.Set("X-Webhook-Signature", signature)
	valid.Header.Set("Idempotency-Key", key)
	validRecord := httptest.NewRecorder()
	s.Handler().ServeHTTP(validRecord, valid)
	if validRecord.Code != http.StatusAccepted {
		t.Fatalf("valid v2 signature status=%d body=%s", validRecord.Code, validRecord.Body.String())
	}

	changedKey := webhookRequest(http.MethodPost, path, body)
	changedKey.Header.Set("X-Webhook-Timestamp", timestamp)
	changedKey.Header.Set("X-Webhook-Signature", signature)
	changedKey.Header.Set("Idempotency-Key", "notify-delivery-2")
	changedRecord := httptest.NewRecorder()
	s.Handler().ServeHTTP(changedRecord, changedKey)
	if changedRecord.Code != http.StatusUnauthorized {
		t.Fatalf("changed idempotency key status=%d, want 401", changedRecord.Code)
	}

	var accepted map[string]any
	if err := json.Unmarshal(validRecord.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	jobPath := "/webhook-jobs/" + fmt.Sprint(accepted["job_id"])
	getSignature := webhookSignatureV2(secret, http.MethodGet, jobPath, timestamp, "", nil)
	get := webhookRequest(http.MethodGet, jobPath, nil)
	get.Header.Set("X-Webhook-Timestamp", timestamp)
	get.Header.Set("X-Webhook-Signature", getSignature)
	getRecord := httptest.NewRecorder()
	s.Handler().ServeHTTP(getRecord, get)
	if getRecord.Code != http.StatusOK {
		t.Fatalf("valid signed GET status=%d body=%s", getRecord.Code, getRecord.Body.String())
	}

	wrongMethod := webhookRequest(http.MethodGet, jobPath, nil)
	wrongMethod.Header.Set("X-Webhook-Timestamp", timestamp)
	wrongMethod.Header.Set("X-Webhook-Signature", signature)
	wrongRecord := httptest.NewRecorder()
	s.Handler().ServeHTTP(wrongRecord, wrongMethod)
	if wrongRecord.Code != http.StatusUnauthorized {
		t.Fatalf("POST signature reused for GET status=%d, want 401", wrongRecord.Code)
	}
}

func TestWebhookShellResultRequiresSuccessfulExit(t *testing.T) {
	completed := func(returnCode any) map[string]any {
		return map[string]any{
			"status": "completed",
			"response": map[string]any{
				"structuredContent": map[string]any{
					"result": map[string]any{"returncode": returnCode, "stderr": "must not enter the error"},
				},
			},
		}
	}
	if err := validateWebhookShellResult(completed(float64(0))); err != nil {
		t.Fatalf("zero exit rejected: %v", err)
	}
	if err := validateWebhookShellResult(completed(float64(1))); err == nil || strings.Contains(err.Error(), "must not enter") {
		t.Fatalf("nonzero exit was not safely rejected: %v", err)
	}
	if err := validateWebhookShellResult(map[string]any{"status": "completed"}); err == nil {
		t.Fatal("missing returncode was accepted")
	}
	if err := validateWebhookShellResult(completed("invalid")); err == nil {
		t.Fatal("invalid returncode was accepted")
	}
	if err := validateWebhookShellResult(map[string]any{"status": "running"}); err == nil {
		t.Fatal("non-terminal shell result was accepted")
	}
}

func TestWebhookTemplatesPreserveTypedJSONValues(t *testing.T) {
	event := map[string]any{
		"repository": map[string]any{"name": "gptadmin"},
		"items":      []any{"first", "second"},
		"number":     json.Number("42"),
	}
	rendered, err := renderWebhookValue(map[string]any{
		"name":  "{{event.repository.name}}",
		"count": "{{event.number}}",
		"item":  "{{event.items.1}}",
	}, event)
	if err != nil {
		t.Fatal(err)
	}
	values := mapValue(rendered)
	if values["name"] != "gptadmin" || values["count"] != json.Number("42") || values["item"] != "second" {
		t.Fatalf("rendered values = %#v", values)
	}
	if _, err := decodeWebhookEvent([]byte(`{"a":1}{"b":2}`)); err == nil {
		t.Fatal("trailing JSON value was accepted")
	}
}

func TestWebhookRoutesCRUDPersistsWithoutExposingSecrets(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "webhooks.json")
	s := New(Config{CtlToken: "ctl", WebhookConfigFile: configPath})
	route := WebhookRoute{
		ID:    "build",
		Token: "route-secret",
		Action: WebhookAction{
			Kind:   "mcp",
			Target: "hub",
			Tool:   "status",
		},
	}
	body, err := json.Marshal(route)
	if err != nil {
		t.Fatal(err)
	}
	create := webhookRequest(http.MethodPost, "/webhook-routes", body)
	create.Header.Set("Authorization", "Bearer ctl")
	created := httptest.NewRecorder()
	s.Handler().ServeHTTP(created, create)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", created.Code, created.Body.String())
	}
	if strings.Contains(created.Body.String(), "route-secret") {
		t.Fatalf("create response leaked route secret: %s", created.Body.String())
	}

	list := webhookRequest(http.MethodGet, "/webhook-routes", nil)
	list.Header.Set("Authorization", "Bearer ctl")
	listed := httptest.NewRecorder()
	s.Handler().ServeHTTP(listed, list)
	if listed.Code != http.StatusOK || strings.Contains(listed.Body.String(), "route-secret") {
		t.Fatalf("list status/body = %d/%s", listed.Code, listed.Body.String())
	}

	stat, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if stat.Mode().Perm() != 0o600 {
		t.Fatalf("webhook config mode = %o, want 600", stat.Mode().Perm())
	}
	restarted := New(Config{WebhookConfigFile: configPath})
	if _, ok := restarted.webhookRoutes["build"]; !ok {
		t.Fatal("route was not loaded after restart")
	}

	remove := webhookRequest(http.MethodDelete, "/webhook-routes/build", nil)
	remove.Header.Set("Authorization", "Bearer ctl")
	removed := httptest.NewRecorder()
	s.Handler().ServeHTTP(removed, remove)
	if removed.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, body = %s", removed.Code, removed.Body.String())
	}
	if restarted := New(Config{WebhookConfigFile: configPath}); len(restarted.webhookRoutes) != 0 {
		t.Fatalf("deleted route survived restart: %#v", restarted.webhookRoutes)
	}
}

func TestWebhookJobsSurviveRestart(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "webhook-state.json")
	route := WebhookRoute{ID: "status", Token: "route-secret", Action: WebhookAction{Kind: "mcp", Target: "hub", Tool: "status"}}
	cfg := Config{WebhookRoutes: []WebhookRoute{route}, WebhookStateFile: statePath}
	s := New(cfg)
	request := webhookRequest(http.MethodPost, "/webhooks/v1/status", []byte(`{"event":"push"}`))
	request.Header.Set("Authorization", "Bearer route-secret")
	accepted := httptest.NewRecorder()
	s.Handler().ServeHTTP(accepted, request)
	if accepted.Code != http.StatusAccepted {
		t.Fatalf("submit status = %d, body = %s", accepted.Code, accepted.Body.String())
	}
	var submission map[string]any
	if err := json.Unmarshal(accepted.Body.Bytes(), &submission); err != nil {
		t.Fatal(err)
	}
	jobID := submission["job_id"].(string)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		job := cloneWebhookJob(s.webhookJobs[jobID])
		s.mu.Unlock()
		if job != nil && (job.Status == "completed" || job.Status == "failed") {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("webhook state was not persisted: %v", err)
	}

	restarted := New(cfg)
	status := webhookRequest(http.MethodGet, "/webhook-jobs/"+jobID, nil)
	status.Header.Set("Authorization", "Bearer route-secret")
	result := httptest.NewRecorder()
	restarted.Handler().ServeHTTP(result, status)
	if result.Code != http.StatusOK {
		t.Fatalf("status after restart = %d, body = %s", result.Code, result.Body.String())
	}
	var persisted webhookJob
	if err := json.Unmarshal(result.Body.Bytes(), &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.ID != jobID || persisted.Status == "accepted" {
		t.Fatalf("persisted job = %#v", persisted)
	}

	duplicate := webhookRequest(http.MethodPost, "/webhooks/v1/status", []byte(`{"event":"push"}`))
	duplicate.Header.Set("Authorization", "Bearer route-secret")
	replayed := httptest.NewRecorder()
	restarted.Handler().ServeHTTP(replayed, duplicate)
	if replayed.Code != http.StatusAccepted || !strings.Contains(replayed.Body.String(), `"duplicate":true`) || !strings.Contains(replayed.Body.String(), jobID) {
		t.Fatalf("replayed delivery = %d/%s", replayed.Code, replayed.Body.String())
	}
}

func TestWebhookJobEndpointRequiresRouteAuth(t *testing.T) {
	route := WebhookRoute{ID: "status", Token: "route-secret", Action: WebhookAction{Kind: "mcp", Target: "hub", Tool: "status"}}
	s := New(Config{WebhookRoutes: []WebhookRoute{route}})
	s.mu.Lock()
	s.webhookJobs["job-1"] = &webhookJob{ID: "job-1", RouteID: "status", Status: "completed", CreatedAt: time.Now()}
	s.mu.Unlock()

	unauthorized := webhookRequest(http.MethodGet, "/webhook-jobs/job-1", nil)
	denied := httptest.NewRecorder()
	s.Handler().ServeHTTP(denied, unauthorized)
	if denied.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, body = %s", denied.Code, denied.Body.String())
	}

	authorized := webhookRequest(http.MethodGet, "/webhook-jobs/job-1", nil)
	authorized.Header.Set("Authorization", "Bearer route-secret")
	allowed := httptest.NewRecorder()
	s.Handler().ServeHTTP(allowed, authorized)
	if allowed.Code != http.StatusOK {
		t.Fatalf("authorized status = %d, body = %s", allowed.Code, allowed.Body.String())
	}
}

func TestWebhookCallbackRetriesWithBoundedAttempts(t *testing.T) {
	var attempts atomic.Int32
	callbackServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if attempts.Add(1) < 3 {
			writer.WriteHeader(http.StatusBadGateway)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer callbackServer.Close()

	s := New(Config{WebhookRoutes: []WebhookRoute{{
		ID:       "status",
		Token:    "route-secret",
		Action:   WebhookAction{Kind: "mcp", Target: "hub", Tool: "status"},
		Callback: &WebhookCallback{URL: callbackServer.URL},
	}}})
	request := webhookRequest(http.MethodPost, "/webhooks/v1/status", []byte(`{"event":"push"}`))
	request.Header.Set("Authorization", "Bearer route-secret")
	accepted := httptest.NewRecorder()
	s.Handler().ServeHTTP(accepted, request)
	if accepted.Code != http.StatusAccepted {
		t.Fatalf("submit status = %d, body = %s", accepted.Code, accepted.Body.String())
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && attempts.Load() < 3 {
		time.Sleep(10 * time.Millisecond)
	}
	if got := attempts.Load(); got != 3 {
		t.Fatalf("callback attempts = %d, want bounded retry count 3", got)
	}
}
