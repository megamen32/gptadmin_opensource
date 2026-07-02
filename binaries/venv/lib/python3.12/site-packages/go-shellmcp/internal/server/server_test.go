package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestExecEndpoint(t *testing.T) {
	s := New(Config{Token: "t", LogLimit: 8192, ExecTimeout: 5, SpillDir: t.TempDir()})
	req := httptest.NewRequest(http.MethodPost, "/exec", bytes.NewBufferString(`{"cmd":"printf ok"}`))
	req.Header.Set("Authorization", "Bearer t")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["stdout"] != "ok" {
		t.Fatalf("bad body: %#v", got)
	}
}

func TestExecLiveEndpoint(t *testing.T) {
	s := New(Config{Token: "t", LogLimit: 8192, ExecTimeout: 5, SpillDir: t.TempDir()})
	req := httptest.NewRequest(http.MethodPost, "/exec/live", bytes.NewBufferString(`{"cmd":"echo ok"}`))
	req.Header.Set("Authorization", "Bearer t")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `"type":"chunk"`) || !strings.Contains(body, `"type":"exit"`) {
		t.Fatalf("bad live response code=%d body=%s", rec.Code, body)
	}
}

func TestBackgroundJob(t *testing.T) {
	s := New(Config{Token: "t", LogLimit: 8192, ExecTimeout: 5, SpillDir: t.TempDir()})
	req := httptest.NewRequest(http.MethodPost, "/exec", bytes.NewBufferString(`{"cmd":"printf bg","background":true}`))
	req.Header.Set("Authorization", "Bearer t")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 202 {
		t.Fatalf("want 202 got %d %s", rec.Code, rec.Body.String())
	}
	var start map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &start)
	id := start["job_id"].(string)
	for i := 0; i < 30; i++ {
		get := httptest.NewRequest(http.MethodGet, "/jobs/"+id, nil)
		get.Header.Set("Authorization", "Bearer t")
		gr := httptest.NewRecorder()
		s.Handler().ServeHTTP(gr, get)
		if strings.Contains(gr.Body.String(), `"state":"done"`) && strings.Contains(gr.Body.String(), `"stdout":"bg"`) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job did not finish")
}

func TestUnauthorized(t *testing.T) {
	s := New(Config{Token: "t"})
	req := httptest.NewRequest(http.MethodGet, "/system/health", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("want 401 got %d", rec.Code)
	}
}

func TestFileEndpoint(t *testing.T) {
	dir := t.TempDir()
	s := New(Config{Token: "t", LogLimit: 4, ExecTimeout: 5, SpillDir: dir})
	req := httptest.NewRequest(http.MethodPost, "/exec", bytes.NewBufferString(`{"cmd":"printf 123456789"}`))
	req.Header.Set("Authorization", "Bearer t")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	p, _ := got["stdout_path"].(string)
	if p == "" {
		t.Fatalf("missing stdout_path: %s", rec.Body.String())
	}
	r2 := httptest.NewRequest(http.MethodGet, "/file?path="+p, nil)
	r2.Header.Set("Authorization", "Bearer t")
	rec2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec2, r2)
	if rec2.Code != 200 || rec2.Body.String() != "123456789" {
		t.Fatalf("file code=%d body=%q", rec2.Code, rec2.Body.String())
	}
}
