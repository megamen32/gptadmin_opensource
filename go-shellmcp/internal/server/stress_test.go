//go:build stress

package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestStressExecHTTP(t *testing.T) {
	s := New(Config{Token: "t", LogLimit: 4096, ExecTimeout: 10, SpillDir: t.TempDir()})
	const requests = 120
	const workers = 20
	jobs := make(chan int, requests)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				cmd := fmt.Sprintf("printf job%d; >&2 printf err%d", i, i)
				body, _ := json.Marshal(map[string]string{"cmd": cmd})
				req := httptest.NewRequest(http.MethodPost, "/exec", bytes.NewReader(body))
				req.Header.Set("Authorization", "Bearer t")
				rec := httptest.NewRecorder()
				s.Handler().ServeHTTP(rec, req)
				if rec.Code != 200 {
					t.Errorf("request %d status=%d body=%s", i, rec.Code, rec.Body.String())
					continue
				}
				var got map[string]any
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Errorf("request %d bad json: %v", i, err)
					continue
				}
				if got["stdout"] != fmt.Sprintf("job%d", i) || got["stderr"] != fmt.Sprintf("err%d", i) {
					t.Errorf("request %d bad streams: %#v", i, got)
				}
			}
		}()
	}
	for i := 0; i < requests; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
}
