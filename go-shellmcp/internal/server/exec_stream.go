package server

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/audit"
	"github.com/megamen32/gptadmin/go-shellmcp/internal/shell"
)

// execStream is the SSE (text/event-stream) variant of /exec/live. It
// reuses shell.RunLive and writes one event per shell.Event as
// `data: <json>\n\n`. The endpoint is POST only and accepts the same
// shell.Request body as /exec and /exec/live.
func (s *Server) execStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	req, ok := s.decodeExec(w, r)
	if !ok {
		return
	}
	cmdField := firstToken(req.Cmd)
	s.auditLog.Event(audit.ExecStart, map[string]any{
		"cmd":        cmdField,
		"user":       req.RunAsUser,
		"background": req.Background,
		"transport":  "sse",
	})

	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if flusher != nil {
		flusher.Flush()
	}

	startTime := time.Now()
	emit := func(e shell.Event) {
		b, err := json.Marshal(e)
		if err != nil {
			return
		}
		_, _ = w.Write([]byte("data: "))
		_, _ = w.Write(b)
		_, _ = w.Write([]byte("\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}
	res := s.runShellStream(r.Context(), req, emit)
	elapsedMS := time.Since(startTime).Milliseconds()
	endFields := map[string]any{
		"cmd":         cmdField,
		"user":        req.RunAsUser,
		"background":  req.Background,
		"return_code": res.ReturnCode,
		"elapsed_ms":  elapsedMS,
		"transport":   "sse",
	}
	if res.Error != "" {
		endFields["error"] = res.Error
	}
	s.auditLog.Event(audit.ExecEnd, endFields)
}
