package hub

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	webhookBodyLimit        = 1 << 20
	webhookDefaultMaxSkew   = 5 * time.Minute
	webhookCallbackTimeout  = 10 * time.Second
	webhookCallbackAttempts = 3
)

// WebhookRoute is a named, authenticated ingress rule. Secrets are supplied
// by operator-owned configuration and are never accepted from the request.
type WebhookRoute struct {
	ID               string           `json:"id"`
	Token            string           `json:"token,omitempty"`
	HMACSecret       string           `json:"hmac_secret,omitempty"`
	SignatureVersion string           `json:"signature_version,omitempty"`
	MaxSkewSeconds   int              `json:"max_skew_seconds,omitempty"`
	Action           WebhookAction    `json:"action,omitempty"` // legacy single-action route
	Actions          []WebhookAction  `json:"actions,omitempty"`
	Callback         *WebhookCallback `json:"callback,omitempty"`
}

// WebhookAction describes one explicitly configured target operation.
// Agent Herder, ShellMCP, or any other registered MCP target can be selected
// by configuration without becoming a special dependency of the gateway.
type WebhookAction struct {
	Kind           string         `json:"kind"`
	Target         string         `json:"target"`
	ApprovalMode   string         `json:"approval_mode,omitempty"`
	TimeoutSeconds float64        `json:"timeout_seconds,omitempty"`
	Tool           string         `json:"tool,omitempty"`
	Arguments      map[string]any `json:"arguments,omitempty"`
	Prompt         string         `json:"prompt,omitempty"`
	PromptArg      string         `json:"prompt_arg,omitempty"`
	Command        string         `json:"command,omitempty"`
	Cwd            string         `json:"cwd,omitempty"`
	DelaySeconds   float64        `json:"delay_seconds,omitempty"`
}

type WebhookCallback struct {
	URL        string `json:"url"`
	Token      string `json:"token,omitempty"`
	HMACSecret string `json:"hmac_secret,omitempty"`
}

type webhookConfigFile struct {
	Routes []WebhookRoute `json:"routes"`
}

type webhookDelivery struct {
	Fingerprint string
	JobID       string
	CreatedAt   time.Time
}

type webhookJob struct {
	ID                  string            `json:"job_id"`
	RouteID             string            `json:"route_id"`
	Status              string            `json:"status"`
	CreatedAt           time.Time         `json:"created_at"`
	StartedAt           time.Time         `json:"started_at,omitempty"`
	CompletedAt         time.Time         `json:"completed_at,omitempty"`
	Result              map[string]any    `json:"result,omitempty"`
	Error               string            `json:"error,omitempty"`
	CallbackStatus      string            `json:"callback_status,omitempty"`
	CorrelationID       string            `json:"correlation_id,omitempty"`
	TraceRefs           []string          `json:"trace_refs,omitempty"`
	Progress            []webhookProgress `json:"progress,omitempty"`
	LastProgressAt      time.Time         `json:"last_progress_at,omitempty"`
	ProgressFingerprint string            `json:"progress_fingerprint,omitempty"`
	UsefulProgress      bool              `json:"useful_progress,omitempty"`
}

type webhookProgress struct {
	ReceivedAt     time.Time `json:"received_at"`
	CorrelationID  string    `json:"correlation_id,omitempty"`
	Step           string    `json:"step,omitempty"`
	EvidenceRefs   []string  `json:"evidence_refs,omitempty"`
	TraceRefs      []string  `json:"trace_refs,omitempty"`
	Fingerprint    string    `json:"fingerprint,omitempty"`
	UsefulProgress bool      `json:"useful_progress"`
}

type webhookProgressUpdate struct {
	CorrelationID string   `json:"correlation_id,omitempty"`
	Step          string   `json:"step"`
	EvidenceRefs  []string `json:"evidence_refs"`
	TraceRefs     []string `json:"trace_refs,omitempty"`
	Fingerprint   string   `json:"fingerprint"`
}

const (
	webhookProgressMaxStepLen        = 256
	webhookProgressMaxRefLen         = 256
	webhookProgressMaxEvidenceRefs   = 16
	webhookProgressMaxTraceRefs      = 16
	webhookProgressMaxEntries        = 64
	webhookProgressMaxCorrelationLen = 128
	webhookProgressMaxFingerprintLen = 128
)

func loadWebhookRoutes(path string) ([]WebhookRoute, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var config webhookConfigFile
	if err := json.Unmarshal(b, &config); err != nil {
		return nil, err
	}
	return config.Routes, nil
}

func validateWebhookRoutes(routes []WebhookRoute) error {
	seen := map[string]bool{}
	for _, route := range routes {
		if route.ID == "" || strings.Contains(route.ID, "/") || strings.Contains(route.ID, "\\") {
			return fmt.Errorf("webhook route id must be a single non-empty path segment")
		}
		if seen[route.ID] {
			return fmt.Errorf("duplicate webhook route %q", route.ID)
		}
		seen[route.ID] = true
		if (route.Token == "") == (route.HMACSecret == "") {
			return fmt.Errorf("webhook route %q must configure exactly one token or hmac_secret", route.ID)
		}
		if route.SignatureVersion != "" && route.SignatureVersion != "v1" && route.SignatureVersion != "v2" {
			return fmt.Errorf("webhook route %q has unsupported signature_version", route.ID)
		}
		if route.SignatureVersion != "" && route.HMACSecret == "" {
			return fmt.Errorf("webhook route %q signature_version requires hmac_secret", route.ID)
		}
		if len(route.Actions) > 0 {
			if route.Action.Kind != "" || route.Action.Target != "" {
				return fmt.Errorf("webhook route %q must configure action or actions, not both", route.ID)
			}
			for index, action := range route.Actions {
				if err := validateWebhookAction(route.ID, action); err != nil {
					return fmt.Errorf("webhook route %q action %d: %w", route.ID, index+1, err)
				}
			}
		} else if err := validateWebhookAction(route.ID, route.Action); err != nil {
			return err
		}
		if route.Callback != nil {
			parsed, err := url.Parse(route.Callback.URL)
			if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
				return fmt.Errorf("webhook route %q callback url must be http(s)", route.ID)
			}
			if route.Callback.Token != "" && route.Callback.HMACSecret != "" {
				return fmt.Errorf("webhook route %q callback must configure at most one token or hmac_secret", route.ID)
			}
		}
	}
	return nil
}

func validateWebhookAction(routeID string, action WebhookAction) error {
	if action.Target == "" {
		return fmt.Errorf("webhook route %q action target is required", routeID)
	}
	if action.ApprovalMode != "" && action.ApprovalMode != approvalModeAskBeforeWrite && action.ApprovalMode != approvalModeBoundedAutonomous {
		return fmt.Errorf("webhook route %q has invalid approval_mode", routeID)
	}
	if action.DelaySeconds < 0 || action.DelaySeconds > 24*60*60 {
		return fmt.Errorf("webhook route %q action delay_seconds must be between 0 and 86400", routeID)
	}
	switch action.Kind {
	case "mcp":
		if action.Tool == "" {
			return fmt.Errorf("webhook route %q mcp action tool is required", routeID)
		}
	case "prompt":
		if action.Tool == "" || action.Prompt == "" {
			return fmt.Errorf("webhook route %q prompt action requires tool and prompt", routeID)
		}
	case "shell":
		if !strings.HasPrefix(action.Target, "shell:") || action.Command == "" {
			return fmt.Errorf("webhook route %q shell action requires shell target and command", routeID)
		}
	default:
		return fmt.Errorf("webhook route %q has unsupported action kind %q", routeID, action.Kind)
	}
	return nil
}

func webhookRouteMap(routes []WebhookRoute) map[string]WebhookRoute {
	result := make(map[string]WebhookRoute, len(routes))
	for _, route := range routes {
		route.Action.Arguments = cloneMap(route.Action.Arguments)
		for index := range route.Actions {
			route.Actions[index].Arguments = cloneMap(route.Actions[index].Arguments)
		}
		if route.Callback != nil {
			callback := *route.Callback
			route.Callback = &callback
		}
		result[route.ID] = route
	}
	return result
}

func (s *Server) webhookEndpoint(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	routeID := strings.TrimPrefix(r.URL.Path, "/webhooks/v1/")
	if routeID == "" || strings.Contains(routeID, "/") {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "unknown webhook route"})
		return
	}
	s.mu.Lock()
	route, ok := s.webhookRoutes[routeID]
	s.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "unknown webhook route"})
		return
	}
	body, err := readWebhookBody(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	if err := verifyWebhookRequest(r, route, body, s.now()); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"detail": err.Error()})
		return
	}
	event, err := decodeWebhookEvent(body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}

	deliveryKey := strings.TrimSpace(firstNonEmpty(
		r.Header.Get("Idempotency-Key"),
		r.Header.Get("X-Event-ID"),
		r.Header.Get("X-GitHub-Delivery"),
	))
	if deliveryKey == "" {
		deliveryKey = sha256Hex(body)
	}
	fingerprint := sha256Hex(append([]byte(routeID+"\x00"), body...))
	s.mu.Lock()
	now := s.now()
	for key, delivery := range s.webhookDeliveries {
		if now.Sub(delivery.CreatedAt) > idempotencyTTL {
			delete(s.webhookDeliveries, key)
		}
	}
	if existing := s.webhookDeliveries[routeID+"\x00"+deliveryKey]; existing != nil {
		if existing.Fingerprint != fingerprint {
			s.mu.Unlock()
			writeJSON(w, http.StatusConflict, map[string]any{"detail": "idempotency key was already used for a different event"})
			return
		}
		jobID := existing.JobID
		s.mu.Unlock()
		writeJSON(w, http.StatusAccepted, map[string]any{"route_id": routeID, "job_id": jobID, "status": "accepted", "duplicate": true})
		return
	}
	jobID := newID()
	s.webhookDeliveries[routeID+"\x00"+deliveryKey] = &webhookDelivery{Fingerprint: fingerprint, JobID: jobID, CreatedAt: now}
	s.webhookJobs[jobID] = &webhookJob{ID: jobID, RouteID: routeID, Status: "accepted", CreatedAt: now}
	if err := s.saveWebhookStateLocked(); err != nil {
		log.Printf("webhook state save failed path=%s err=%v", s.webhookStatePath(), err)
	}
	s.mu.Unlock()

	go s.runWebhookJob(jobID, route, event)
	writeJSON(w, http.StatusAccepted, map[string]any{"route_id": routeID, "job_id": jobID, "status": "accepted"})
}

func (s *Server) webhookJobEndpoint(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	jobID := strings.TrimPrefix(r.URL.Path, "/webhook-jobs/")
	isProgress := strings.HasSuffix(jobID, "/progress")
	if isProgress {
		jobID = strings.TrimSuffix(jobID, "/progress")
		jobID = strings.TrimSuffix(jobID, "/")
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "progress updates require POST"})
			return
		}
	}
	if jobID == "" || strings.Contains(jobID, "/") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "missing job_id"})
		return
	}
	s.mu.Lock()
	job := s.webhookJobs[jobID]
	if job == nil {
		s.mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "unknown webhook job"})
		return
	}
	route := s.webhookRoutes[job.RouteID]
	response := cloneWebhookJob(job)
	s.mu.Unlock()
	body, err := readWebhookBody(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	if err := verifyWebhookRequest(r, route, body, s.now()); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"detail": err.Error()})
		return
	}
	if isProgress {
		s.handleWebhookJobProgress(w, r, jobID, route, body)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleWebhookJobProgress(w http.ResponseWriter, r *http.Request, jobID string, route WebhookRoute, body []byte) {
	var update webhookProgressUpdate
	if err := json.Unmarshal(body, &update); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "webhook progress body must be valid JSON"})
		return
	}
	if err := validateWebhookProgressUpdate(update); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	deliveryKey := strings.TrimSpace(firstNonEmpty(r.Header.Get("Idempotency-Key"), r.Header.Get("X-Event-ID"), r.Header.Get("X-GitHub-Delivery")))
	if deliveryKey == "" {
		deliveryKey = sha256Hex(body)
	}
	progressKey := jobID + "\x00progress\x00" + deliveryKey
	fingerprint := sha256Hex(append([]byte(jobID+"\x00"), body...))

	s.mu.Lock()
	defer s.mu.Unlock()
	job := s.webhookJobs[jobID]
	if job == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "unknown webhook job"})
		return
	}
	if job.Status == "completed" || job.Status == "failed" {
		writeJSON(w, http.StatusConflict, map[string]any{"detail": "webhook job is terminal"})
		return
	}
	if existing := s.webhookDeliveries[progressKey]; existing != nil {
		if existing.Fingerprint != fingerprint {
			writeJSON(w, http.StatusConflict, map[string]any{"detail": "idempotency key was already used for a different progress update"})
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"job_id": jobID, "status": job.Status, "duplicate": true, "useful_progress": false})
		return
	}
	progress := webhookProgress{
		ReceivedAt:    s.now(),
		CorrelationID: boundedString(update.CorrelationID, webhookProgressMaxCorrelationLen),
		Step:          boundedString(update.Step, webhookProgressMaxStepLen),
		EvidenceRefs:  boundedStrings(update.EvidenceRefs, webhookProgressMaxEvidenceRefs, webhookProgressMaxRefLen),
		TraceRefs:     boundedStrings(update.TraceRefs, webhookProgressMaxTraceRefs, webhookProgressMaxRefLen),
		Fingerprint:   boundedString(update.Fingerprint, webhookProgressMaxFingerprintLen),
	}
	useful := isUsefulWebhookProgress(job, progress)
	progress.UsefulProgress = useful
	job.Progress = append(job.Progress, progress)
	if len(job.Progress) > webhookProgressMaxEntries {
		job.Progress = job.Progress[len(job.Progress)-webhookProgressMaxEntries:]
	}
	job.LastProgressAt = progress.ReceivedAt
	if progress.CorrelationID != "" {
		job.CorrelationID = progress.CorrelationID
	}
	if len(progress.TraceRefs) > 0 {
		job.TraceRefs = append([]string(nil), progress.TraceRefs...)
	}
	job.ProgressFingerprint = progress.Fingerprint
	job.UsefulProgress = useful
	s.webhookDeliveries[progressKey] = &webhookDelivery{Fingerprint: fingerprint, JobID: jobID, CreatedAt: progress.ReceivedAt}
	if err := s.saveWebhookStateLocked(); err != nil {
		log.Printf("webhook state save failed path=%s err=%v", s.webhookStatePath(), err)
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job_id": jobID, "status": job.Status, "useful_progress": useful})
}

func validateWebhookProgressUpdate(update webhookProgressUpdate) error {
	if strings.TrimSpace(update.Step) == "" {
		return errors.New("webhook progress step is required")
	}
	if len(update.Step) > webhookProgressMaxStepLen {
		return fmt.Errorf("webhook progress step exceeds %d bytes", webhookProgressMaxStepLen)
	}
	if len(update.Fingerprint) == 0 {
		return errors.New("webhook progress fingerprint is required")
	}
	if len(update.Fingerprint) > webhookProgressMaxFingerprintLen {
		return fmt.Errorf("webhook progress fingerprint exceeds %d bytes", webhookProgressMaxFingerprintLen)
	}
	if len(update.CorrelationID) > webhookProgressMaxCorrelationLen {
		return fmt.Errorf("webhook progress correlation_id exceeds %d bytes", webhookProgressMaxCorrelationLen)
	}
	if len(update.EvidenceRefs) > webhookProgressMaxEvidenceRefs {
		return fmt.Errorf("webhook progress evidence_refs exceeds %d entries", webhookProgressMaxEvidenceRefs)
	}
	if len(update.TraceRefs) > webhookProgressMaxTraceRefs {
		return fmt.Errorf("webhook progress trace_refs exceeds %d entries", webhookProgressMaxTraceRefs)
	}
	return nil
}

func boundedString(value string, maxLen int) string {
	value = strings.TrimSpace(value)
	if len(value) > maxLen {
		return value[:maxLen]
	}
	return value
}

func boundedStrings(values []string, maxItems, maxLen int) []string {
	if len(values) == 0 {
		return nil
	}
	if len(values) > maxItems {
		values = values[:maxItems]
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = boundedString(value, maxLen)
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}

func isUsefulWebhookProgress(job *webhookJob, progress webhookProgress) bool {
	if job == nil {
		return false
	}
	last := webhookProgress{}
	if n := len(job.Progress); n > 0 {
		last = job.Progress[n-1]
	}
	if last.Step == progress.Step && strings.Join(last.EvidenceRefs, ",") == strings.Join(progress.EvidenceRefs, ",") && last.Fingerprint == progress.Fingerprint {
		return false
	}
	return progress.Step != "" && (len(progress.EvidenceRefs) > 0 || progress.Fingerprint != "")
}

func readWebhookBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return []byte{}, nil
	}
	limited := io.LimitReader(r.Body, webhookBodyLimit+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(body) > webhookBodyLimit {
		return nil, fmt.Errorf("webhook body exceeds %d bytes", webhookBodyLimit)
	}
	return body, nil
}

func decodeWebhookEvent(body []byte) (any, error) {
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	var event any
	if err := decoder.Decode(&event); err != nil {
		return nil, fmt.Errorf("webhook body must be valid JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("webhook body must contain one JSON value")
		}
		return nil, fmt.Errorf("webhook body has trailing data: %w", err)
	}
	return event, nil
}

func extractWebhookHealthMetadata(event any) (string, []string) {
	root := mapValue(event)
	if root == nil {
		return "", nil
	}
	correlationID := boundedString(firstString(root, "correlation_id"), webhookProgressMaxCorrelationLen)
	traceRefs := boundedStrings(stringSlice(root["trace_refs"]), webhookProgressMaxTraceRefs, webhookProgressMaxRefLen)
	if correlationID == "" || len(traceRefs) == 0 {
		health := mapValue(root["health"])
		if correlationID == "" {
			correlationID = boundedString(firstString(health, "correlation_id"), webhookProgressMaxCorrelationLen)
		}
		if len(traceRefs) == 0 {
			traceRefs = boundedStrings(stringSlice(health["trace_refs"]), webhookProgressMaxTraceRefs, webhookProgressMaxRefLen)
		}
	}
	return correlationID, traceRefs
}

func verifyWebhookRequest(r *http.Request, route WebhookRoute, body []byte, now time.Time) error {
	if route.Token != "" {
		for _, candidate := range []string{
			r.Header.Get("X-Webhook-Token"),
			r.Header.Get("X-GPTAdmin-Webhook-Token"),
			r.Header.Get("Authorization"),
		} {
			candidate = strings.TrimSpace(strings.TrimPrefix(candidate, "Bearer "))
			if candidate != "" && hmac.Equal([]byte(candidate), []byte(route.Token)) {
				return nil
			}
		}
		return errors.New("invalid webhook token")
	}
	timestampText := strings.TrimSpace(r.Header.Get("X-Webhook-Timestamp"))
	timestamp, err := strconv.ParseInt(timestampText, 10, 64)
	if err != nil {
		return errors.New("missing or invalid webhook timestamp")
	}
	maxSkew := webhookDefaultMaxSkew
	if route.MaxSkewSeconds > 0 {
		maxSkew = time.Duration(route.MaxSkewSeconds) * time.Second
	}
	if delta := now.Sub(time.Unix(timestamp, 0)); delta > maxSkew || delta < -maxSkew {
		return errors.New("webhook timestamp is outside the allowed replay window")
	}
	expected := webhookSignature(route.HMACSecret, timestampText, body)
	if route.SignatureVersion == "v2" {
		expected = webhookSignatureV2(
			route.HMACSecret,
			r.Method,
			r.URL.EscapedPath(),
			timestampText,
			strings.TrimSpace(r.Header.Get("Idempotency-Key")),
			body,
		)
	}
	if !hmac.Equal([]byte(expected), []byte(strings.TrimSpace(r.Header.Get("X-Webhook-Signature")))) {
		return errors.New("invalid webhook signature")
	}
	return nil
}

func webhookSignature(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp + "."))
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func webhookSignatureV2(secret, method, path, timestamp, idempotencyKey string, body []byte) string {
	canonical := strings.Join([]string{
		strings.ToUpper(method),
		path,
		timestamp,
		idempotencyKey,
		sha256Hex(body),
	}, "\n")
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(canonical))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func (s *Server) runWebhookJob(jobID string, route WebhookRoute, event any) {
	s.mu.Lock()
	job := s.webhookJobs[jobID]
	if job == nil {
		s.mu.Unlock()
		return
	}
	if correlationID, traceRefs := extractWebhookHealthMetadata(event); correlationID != "" || len(traceRefs) > 0 {
		job.CorrelationID = correlationID
		job.TraceRefs = append([]string(nil), traceRefs...)
	}
	job.Status = "running"
	job.StartedAt = s.now()
	if err := s.saveWebhookStateLocked(); err != nil {
		log.Printf("webhook state save failed path=%s err=%v", s.webhookStatePath(), err)
	}
	s.mu.Unlock()

	result, err := s.dispatchWebhookActions(route, event)
	s.mu.Lock()
	job = s.webhookJobs[jobID]
	if job != nil {
		job.CompletedAt = s.now()
		if err != nil {
			job.Status = "failed"
			job.Error = err.Error()
		} else {
			job.Status = "completed"
			job.Result = result
		}
		if err := s.saveWebhookStateLocked(); err != nil {
			log.Printf("webhook state save failed path=%s err=%v", s.webhookStatePath(), err)
		}
	}
	callback := route.Callback
	callbackJob := cloneWebhookJob(job)
	s.mu.Unlock()
	if callback != nil && callbackJob != nil {
		if err := deliverWebhookCallback(*callback, *callbackJob); err != nil {
			s.mu.Lock()
			if current := s.webhookJobs[jobID]; current != nil {
				current.CallbackStatus = "failed"
				if saveErr := s.saveWebhookStateLocked(); saveErr != nil {
					log.Printf("webhook state save failed path=%s err=%v", s.webhookStatePath(), saveErr)
				}
			}
			s.mu.Unlock()
			log.Printf("webhook callback delivery failed route=%s job=%s err=%v", route.ID, jobID, err)
			return
		}
		s.mu.Lock()
		if current := s.webhookJobs[jobID]; current != nil {
			current.CallbackStatus = "delivered"
			if saveErr := s.saveWebhookStateLocked(); saveErr != nil {
				log.Printf("webhook state save failed path=%s err=%v", s.webhookStatePath(), saveErr)
			}
		}
		s.mu.Unlock()
	}
}

func webhookActions(route WebhookRoute) []WebhookAction {
	if len(route.Actions) > 0 {
		return route.Actions
	}
	return []WebhookAction{route.Action}
}

func (s *Server) dispatchWebhookActions(route WebhookRoute, event any) (map[string]any, error) {
	actions := webhookActions(route)
	if len(actions) == 1 && len(route.Actions) == 0 {
		return s.dispatchWebhookAction(actions[0], event)
	}
	results := make([]map[string]any, 0, len(actions))
	for index, action := range actions {
		if index > 0 && action.DelaySeconds > 0 {
			time.Sleep(time.Duration(action.DelaySeconds * float64(time.Second)))
		}
		result, err := s.dispatchWebhookAction(action, event)
		if err != nil {
			return nil, fmt.Errorf("webhook action %d failed: %w", index+1, err)
		}
		results = append(results, result)
	}
	return map[string]any{"action_count": len(results), "actions": results}, nil
}

func (s *Server) dispatchWebhookAction(action WebhookAction, event any) (map[string]any, error) {
	timeout := s.cfg.DefaultTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if action.TimeoutSeconds > 0 {
		bounded := action.TimeoutSeconds
		if bounded < 1 {
			bounded = 1
		}
		if bounded > 600 {
			bounded = 600
		}
		timeout = time.Duration(bounded * float64(time.Second))
	}
	switch action.Kind {
	case "shell":
		command, commandEnv, err := renderWebhookShellCommand(action.Command, event)
		if err != nil {
			return nil, err
		}
		cwd, err := renderWebhookString(action.Cwd, event)
		if err != nil {
			return nil, err
		}
		policyRequest := requestWithAutomationProfile(nil, "webhook", action.Target, "shell_exec", action.ApprovalMode)
		result, status := s.executeMCPTool(policyRequest, action.Target, "shell_exec", map[string]any{
			"cmd":     command,
			"cwd":     cwd,
			"env":     commandEnv,
			"timeout": int(timeout / time.Second),
		}, false, timeout, "")
		if status >= http.StatusBadRequest {
			return nil, fmt.Errorf("webhook shell action failed policy with status %d: %v", status, result)
		}
		if err := validateWebhookShellResult(result); err != nil {
			return nil, err
		}
		return result, nil
	case "mcp", "prompt":
		args, err := renderWebhookValue(action.Arguments, event)
		if err != nil {
			return nil, err
		}
		arguments := mapValue(args)
		if action.Kind == "prompt" {
			prompt, err := renderWebhookString(action.Prompt, event)
			if err != nil {
				return nil, err
			}
			argumentName := action.PromptArg
			if argumentName == "" {
				argumentName = "message"
			}
			arguments[argumentName] = prompt
		}
		selectedTarget, status, detail := s.selectMCPRelayTarget(action.Target)
		if status != http.StatusOK {
			return nil, fmt.Errorf("webhook target rejected: %s", detail)
		}
		policyRequest := requestWithAutomationProfile(nil, "webhook", selectedTarget, action.Tool, action.ApprovalMode)
		result, resultStatus := s.executeMCPTool(policyRequest, selectedTarget, action.Tool, arguments, false, timeout, "")
		if resultStatus < http.StatusOK || resultStatus >= http.StatusMultipleChoices {
			return nil, fmt.Errorf("webhook MCP action failed with status %d: %v", resultStatus, result)
		}
		return result, nil
	default:
		return nil, fmt.Errorf("unsupported webhook action kind %q", action.Kind)
	}
}

func validateWebhookShellResult(result map[string]any) error {
	if firstString(result, "status") != "completed" {
		return errors.New("webhook shell action did not complete")
	}
	response := mapValue(result["response"])
	structured := mapValue(response["structuredContent"])
	execution := mapValue(structured["result"])
	rawReturnCode, ok := execution["returncode"]
	if !ok {
		return errors.New("webhook shell action did not return an exit code")
	}
	returnCode, err := strconv.Atoi(fmt.Sprint(rawReturnCode))
	if err != nil {
		return errors.New("webhook shell action returned an invalid exit code")
	}
	if returnCode != 0 {
		return fmt.Errorf("webhook shell action exited with code %d", returnCode)
	}
	return nil
}

func renderWebhookValue(value any, event any) (any, error) {
	switch typed := value.(type) {
	case string:
		if strings.HasPrefix(typed, "{{") && strings.HasSuffix(typed, "}}") && strings.Count(typed, "{{") == 1 {
			path := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(typed, "{{"), "}}"))
			resolved, ok := lookupWebhookValue(event, path)
			if !ok {
				return nil, fmt.Errorf("webhook template path %q was not found", path)
			}
			return resolved, nil
		}
		return renderWebhookString(typed, event)
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, child := range typed {
			rendered, err := renderWebhookValue(child, event)
			if err != nil {
				return nil, err
			}
			result[key] = rendered
		}
		return result, nil
	case []any:
		result := make([]any, len(typed))
		for i, child := range typed {
			rendered, err := renderWebhookValue(child, event)
			if err != nil {
				return nil, err
			}
			result[i] = rendered
		}
		return result, nil
	default:
		return value, nil
	}
}

func renderWebhookString(template string, event any) (string, error) {
	if template == "" {
		return "", nil
	}
	var out strings.Builder
	for len(template) > 0 {
		start := strings.Index(template, "{{")
		if start < 0 {
			out.WriteString(template)
			break
		}
		out.WriteString(template[:start])
		template = template[start+2:]
		end := strings.Index(template, "}}")
		if end < 0 {
			return "", errors.New("webhook template has an unclosed placeholder")
		}
		path := strings.TrimSpace(template[:end])
		value, ok := lookupWebhookValue(event, path)
		if !ok {
			return "", fmt.Errorf("webhook template path %q was not found", path)
		}
		if object, ok := value.(map[string]any); ok {
			encoded, err := json.Marshal(object)
			if err != nil {
				return "", err
			}
			out.Write(encoded)
		} else {
			out.WriteString(fmt.Sprint(value))
		}
		template = template[end+2:]
	}
	return out.String(), nil
}

func renderWebhookShellCommand(template string, event any) (string, map[string]any, error) {
	// Render event values through environment data, never as shell source text.
	env := map[string]any{}
	var out strings.Builder
	valueIndex := 0
	for len(template) > 0 {
		start := strings.Index(template, "{{")
		if start < 0 {
			out.WriteString(template)
			break
		}
		prefix := template[:start]
		rest := template[start+2:]
		end := strings.Index(rest, "}}")
		if end < 0 {
			return "", nil, errors.New("webhook template has an unclosed placeholder")
		}
		path := strings.TrimSpace(rest[:end])
		value, ok := lookupWebhookValue(event, path)
		if !ok {
			return "", nil, fmt.Errorf("webhook template path %q was not found", path)
		}
		variable := fmt.Sprintf("GPTADMIN_WEBHOOK_VALUE_%d", valueIndex)
		valueIndex++
		if object, ok := value.(map[string]any); ok {
			encoded, err := json.Marshal(object)
			if err != nil {
				return "", nil, err
			}
			env[variable] = string(encoded)
		} else {
			env[variable] = fmt.Sprint(value)
		}
		suffix := rest[end+2:]
		if (strings.HasSuffix(prefix, "'") && strings.HasPrefix(suffix, "'")) || (strings.HasSuffix(prefix, `"`) && strings.HasPrefix(suffix, `"`)) {
			prefix = prefix[:len(prefix)-1]
			suffix = suffix[1:]
		}
		out.WriteString(prefix)
		out.WriteString(`"$` + variable + `"`)
		template = suffix
	}
	return out.String(), env, nil
}

func lookupWebhookValue(event any, path string) (any, bool) {
	path = strings.TrimSpace(path)
	if path == "json" || path == "event" {
		return event, true
	}
	path = strings.TrimPrefix(path, "event.")
	current := event
	for _, part := range strings.Split(path, ".") {
		if part == "" {
			return nil, false
		}
		switch typed := current.(type) {
		case map[string]any:
			var ok bool
			current, ok = typed[part]
			if !ok {
				return nil, false
			}
		case []any:
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= len(typed) {
				return nil, false
			}
			current = typed[index]
		default:
			return nil, false
		}
	}
	return current, true
}

func deliverWebhookCallback(callback WebhookCallback, job webhookJob) error {
	body, err := json.Marshal(map[string]any{
		"job_id":   job.ID,
		"route_id": job.RouteID,
		"status":   job.Status,
		"result":   job.Result,
		"error":    job.Error,
	})
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: webhookCallbackTimeout}
	var lastErr error
	for attempt := 1; attempt <= webhookCallbackAttempts; attempt++ {
		request, err := http.NewRequest(http.MethodPost, callback.URL, strings.NewReader(string(body)))
		if err != nil {
			return err
		}
		request.Header.Set("Content-Type", "application/json")
		if callback.Token != "" {
			request.Header.Set("Authorization", "Bearer "+callback.Token)
		}
		if callback.HMACSecret != "" {
			timestamp := strconv.FormatInt(time.Now().Unix(), 10)
			request.Header.Set("X-Webhook-Timestamp", timestamp)
			request.Header.Set("X-Webhook-Signature", webhookSignature(callback.HMACSecret, timestamp, body))
		}
		response, err := client.Do(request)
		if err == nil {
			response.Body.Close()
			if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
				return nil
			}
			lastErr = fmt.Errorf("callback returned HTTP %d", response.StatusCode)
			if response.StatusCode >= http.StatusBadRequest && response.StatusCode < http.StatusInternalServerError {
				return lastErr
			}
		} else {
			lastErr = err
		}
		if attempt < webhookCallbackAttempts {
			time.Sleep(time.Duration(attempt*25) * time.Millisecond)
		}
	}
	return lastErr
}

func cloneWebhookJob(job *webhookJob) *webhookJob {
	if job == nil {
		return nil
	}
	clone := *job
	clone.Result = cloneMap(job.Result)
	clone.TraceRefs = append([]string(nil), job.TraceRefs...)
	clone.Progress = append([]webhookProgress(nil), job.Progress...)
	return &clone
}
