package hub

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const secretIngressMaxBodyBytes = 64 << 10

func (s *Server) secretIngress(w http.ResponseWriter, r *http.Request) {
	setSecretIngressHeaders(w)
	if s.secretStoreErr != nil || s.secretStore == nil {
		writeSecretIngressMessage(w, http.StatusServiceUnavailable, "Secret input is temporarily unavailable.")
		return
	}
	token := strings.Trim(strings.TrimPrefix(r.URL.Path, "/secret-input/"), "/")
	if token == "" || strings.Contains(token, "/") {
		writeSecretIngressMessage(w, http.StatusNotFound, "Secret input request was not found.")
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.secretIngressForm(w)
	case http.MethodPost:
		contentType := strings.ToLower(strings.TrimSpace(strings.SplitN(r.Header.Get("Content-Type"), ";", 2)[0]))
		if contentType != "application/x-www-form-urlencoded" && contentType != "multipart/form-data" {
			writeSecretIngressMessage(w, http.StatusUnsupportedMediaType, "Secret input was not accepted.")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, secretIngressMaxBodyBytes)
		if err := r.ParseForm(); err != nil {
			writeSecretIngressMessage(w, http.StatusBadRequest, "Secret input was not accepted.")
			return
		}
		value := r.PostFormValue("value")
		if value == "" {
			writeSecretIngressMessage(w, http.StatusBadRequest, "Secret input was not accepted.")
			return
		}
		if _, err := s.secretStore.ConsumeRequest(token, value); err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, ErrSecretRequestConsumed) || errors.Is(err, ErrSecretRequestExpired) || errors.Is(err, ErrSecretNotFound) {
				status = http.StatusGone
			}
			writeSecretIngressMessage(w, status, "Secret input was not accepted.")
			return
		}
		writeSecretIngressMessage(w, http.StatusOK, "Secret accepted. You may close this page.")
	default:
		w.Header().Set("Allow", "GET, POST")
		writeSecretIngressMessage(w, http.StatusMethodNotAllowed, "Method not allowed.")
	}
}

func (s *Server) secretIngressForm(w http.ResponseWriter) {
	page, err := os.ReadFile(filepath.Join(s.cfg.PublicDir, "secret-input", "index.html"))
	if err != nil {
		writeSecretIngressMessage(w, http.StatusNotFound, "Secret input page was not found.")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(page)
}

func setSecretIngressHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; form-action 'self'; style-src 'unsafe-inline'")
}

func writeSecretIngressMessage(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(message + "\n"))
}
