package hub

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func typedPeerRouteRegister(s *Server) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.agents["shell:peer"] == nil {
		s.agents["shell:peer"] = &Agent{AgentID: "shell:peer", Kind: "virtual_shell", Status: "online", Meta: map[string]any{"approved": true}}
	}
	s.agents["shell:peer"].Meta["build_version"] = 1108
}

func TestTypedPeerRoutePreservesPayloadAndOwner(t *testing.T) {
	for _, target := range []string{"file:peer", "control:peer"} {
		for _, enrolled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/enrolled=%t", target, enrolled), func(t *testing.T) {
				body := fmt.Sprintf(` { "target":%q, "tool":"test_tool", "arguments":{"number":9007199254740993}, "background":true } `, target)
				var calls atomic.Int32
				peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					got, _ := io.ReadAll(r.Body)
					if string(got) != body || r.Header.Get("Authorization") != "Bearer token" || r.Header.Get(nodePeerHopHeader) != "1" {
						t.Errorf("typed body/bearer changed: %s", got)
					}
					writeJSON(w, http.StatusOK, map[string]any{"status": "queued", "job_id": "typed-owner-job"})
				}))
				defer peer.Close()
				s := peerTestHub(t, "token", map[string]string{"shell:peer": peer.URL})
				typedPeerRouteRegister(s)
				if enrolled {
					s.mu.Lock()
					delete(s.nodePeers, "shell:peer")
					s.membershipRoutes = map[string]nodePeerRoute{"shell:peer": {URL: peer.URL}}
					s.mu.Unlock()
				}
				r := httptest.NewRequest(http.MethodPost, "/mcp-relay/call", nil)
				r.Header.Set("Authorization", "Bearer token")
				w := httptest.NewRecorder()
				if !s.forwardNodePeerRequest(w, r, target, []byte(body), "/mcp-relay/call") {
					t.Fatal("typed target fell through to legacy queue")
				}
				if calls.Load() != 1 || w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"owner_target":"shell:peer"`) {
					t.Fatalf("wrong owner or duplicate: calls=%d HTTP %d %s", calls.Load(), w.Code, w.Body.String())
				}
				if len(s.shellJobs) != 0 || len(s.relayJobs) != 0 {
					t.Fatal("ingress allocated job")
				}
			})
		}
	}
}

func TestTypedPeerRouteLocalOwnerAndNoSecondHop(t *testing.T) {
	for _, target := range []string{"file:peer", "control:peer"} {
		for _, local := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/local=%t", target, local), func(t *testing.T) {
				var calls atomic.Int32
				peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
				defer peer.Close()
				s := peerTestHub(t, "token", map[string]string{"shell:peer": peer.URL})
				if local {
					if err := s.RegisterLocalExecutor("peer", map[string]string{"server_id": "peer-id", "public_key": "peer-key", "fingerprint": "peer-fingerprint"}, strings.Repeat("t", 32)); err != nil {
						t.Fatal(err)
					}
				}
				typedPeerRouteRegister(s)
				r := httptest.NewRequest(http.MethodPost, "/mcp-relay/call", nil)
				r.Header.Set("Authorization", "Bearer token")
				r.Header.Set(nodePeerHopHeader, "1")
				w := httptest.NewRecorder()
				handled := s.forwardNodePeerRequest(w, r, target, []byte(fmt.Sprintf(`{"target":%q}`, target)), "/mcp-relay/call")
				if local && handled || !local && (!handled || w.Code != http.StatusLoopDetected) || calls.Load() != 0 {
					t.Fatalf("local owner/second hop violated: handled=%t HTTP %d calls=%d", handled, w.Code, calls.Load())
				}
			})
		}
	}
}

func TestTypedPeerRouteControlHubStaysLocal(t *testing.T) {
	var calls atomic.Int32
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer peer.Close()
	s := peerTestHub(t, "token", map[string]string{"shell:hub": peer.URL})
	s.mu.Lock()
	s.agents["shell:hub"] = &Agent{AgentID: "shell:hub", Kind: "virtual_shell", Meta: map[string]any{"approved": true}}
	s.mu.Unlock()
	r := httptest.NewRequest(http.MethodPost, "/mcp-relay/call", nil)
	r.Header.Set("Authorization", "Bearer token")
	w := httptest.NewRecorder()
	if s.forwardNodePeerRequest(w, r, "control:hub", []byte(`{"target":"control:hub","tool":"settings_get"}`), "/mcp-relay/call") || calls.Load() != 0 {
		t.Fatal("control:hub routed to shell:hub")
	}
}

func TestTypedPeerRouteExecRetainsSurfaceACL(t *testing.T) {
	for _, tc := range []struct {
		target, tool string
		args         map[string]any
	}{
		{"file:peer", "system_inspect", map[string]any{"action": "read_file", "path": "/tmp/fixture"}},
		{"control:peer", "mcp_manage", map[string]any{"action": "list"}},
	} {
		t.Run(tc.target, func(t *testing.T) {
			var calls atomic.Int32
			peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if firstString(body, "target") != tc.target || r.Header.Get("Authorization") != "Bearer token" {
					t.Errorf("surface/bearer changed: %#v", body)
				}
				writeJSON(w, http.StatusOK, map[string]any{"status": "completed", "response": map[string]any{"marker": "typed-owner"}})
			}))
			defer peer.Close()
			s := peerTestHub(t, "token", map[string]string{"shell:peer": peer.URL})
			typedPeerRouteRegister(s)
			r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			r.Header.Set("Authorization", "Bearer token")
			r = requestWithAuthClaims(r, map[string]any{"sub": "typed-client", "scope": "gptadmin.read gptadmin.exec", "access_mode": accessModeFull})
			args, _ := json.Marshal(tc.args)
			code := fmt.Sprintf("return await mcp.call(%q,%q,%s);", tc.target, tc.tool, args)
			// A shell grant must not implicitly authorize its file/control surface.
			denied := requestWithAccessProfile(r, AccessProfile{ID: "shell-only", AccessMode: "full", AllowedTargets: []string{"hub", "shell:peer"}, AllowedTools: []string{"exec", tc.tool}})
			got, status := codeModeRun(t, s, denied, map[string]any{"code": code})
			if status == http.StatusOK || calls.Load() != 0 {
				t.Fatalf("surface ACL bypass: %d %#v", status, got)
			}
			allowed := requestWithAccessProfile(r, AccessProfile{ID: "surface-only", AccessMode: "full", AllowedTargets: []string{"hub", tc.target}, AllowedTools: []string{"exec", tc.tool}})
			got, status = codeModeRun(t, s, allowed, map[string]any{"code": code})
			if status != http.StatusOK || calls.Load() != 1 || firstString(mapValue(got["result"]), "marker") != "typed-owner" {
				t.Fatalf("surface route failed: %d %#v calls=%d", status, got, calls.Load())
			}
			if len(s.shellJobs) != 0 || len(s.relayJobs) != 0 {
				t.Fatal("exec allocated legacy job")
			}
		})
	}
}

// Narrow transport/receipt regression: real queued records, no executor run.
func TestTypedPeerQueuedReceiptSurfaceCaller(t *testing.T) {
	for _, target := range []string{"file:peer", "control:peer"} {
		t.Run(target, func(t *testing.T) {
			owner := peerTestHub(t, "token", nil)
			typedPeerRouteRegister(owner)
			profile := AccessProfile{ID: "surface-only", Version: 1, AccessMode: accessModeFull, ApprovalMode: approvalModeUnrestricted, AllowedTargets: []string{"hub", target}, AllowedTools: []string{"system_inspect", "mcp_manage", "job"}}
			owner.accessProfiles[profile.ID] = profile
			if err := saveAccessProfilesState(owner.accessProfilesStatePath(), owner.accessProfiles); err != nil {
				t.Fatal(err)
			}
			request := func(client string) *http.Request {
				r := taskReceiptTestRequest("POST", "/mcp", client, client+"-token", profile)
				r.Header.Set("Authorization", "Bearer token")
				return r
			}
			tool := "system_inspect"
			args := map[string]any{"action": "read_file", "path": "/tmp/fixture"}
			if target == "control:peer" {
				tool = "mcp_manage"
				args = map[string]any{"action": "list"}
			}
			result, status := owner.executeMCPTool(request("caller"), target, tool, args, true, 0, "")
			id := firstString(result, "job_id")
			if status != 200 || id == "" {
				t.Fatalf("queue: %d %#v", status, result)
			}
			peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				var body map[string]any
				_ = json.Unmarshal(raw, &body)
				a := mapValue(mapValue(body["params"])["arguments"])
				// Auth fixture binds the original caller at the receiving transport edge.
				client := "caller"
				if r.Header.Get("Authorization") == "Bearer foreign" {
					client = "foreign"
				}
				rr := request(client)
				rr.Header.Set(nodePeerHopHeader, "1")
				if owner.routeNodePeerJob(w, rr, a, raw) {
					return
				}
				receipt, found := owner.taskReceiptForRequest(rr, firstString(a, "id"), "full")
				if !found {
					writeJSON(w, 403, receipt)
					return
				}
				writeJSON(w, 200, receipt)
			}))
			defer peer.Close()
			ingress := peerTestHub(t, "token", map[string]string{"shell:peer": peer.URL})
			typedPeerRouteRegister(ingress)
			// Use a local transport wrapper only to supply the test identity on the wire.
			for _, client := range []string{"caller", "foreign"} {
				t.Run(client, func(t *testing.T) {
					r := request(client)
					// Destination identity is selected from the bearer in this bounded fixture.
					if client == "foreign" {
						r.Header.Set("Authorization", "Bearer foreign")
					}
					a := map[string]any{"id": id, "owner_target": "shell:peer"}
					raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "job", "arguments": a}})
					w := httptest.NewRecorder()
					if !ingress.routeNodePeerJob(w, r, a, raw) {
						t.Fatal("remote read not routed")
					}
					if client == "caller" && (w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"queued"`)) {
						t.Fatalf("own queued receipt: %d %s", w.Code, w.Body.String())
					}
					if client == "foreign" && w.Code < 400 {
						t.Fatalf("foreign receipt disclosed: %s", w.Body.String())
					}
				})
			}
		})
	}
}

func TestTypedPeerReceiptStoredSurface(t *testing.T) {
	for _, target := range []string{"file:peer", "control:peer"} {
		t.Run(target, func(t *testing.T) {
			s := peerTestHub(t, "token", nil)
			profile := AccessProfile{ID: "surface", Version: 1, AccessMode: accessModeFull, AllowedTargets: []string{"hub", target}, AllowedTools: []string{"job"}}
			if err := saveAccessProfilesState(s.accessProfilesStatePath(), map[string]AccessProfile{profile.ID: profile}); err != nil {
				t.Fatal(err)
			}
			request := taskReceiptTestRequest("POST", "/mcp", "owner", "owner-token", profile)
			caller := taskCallerFromRequest(request)
			s.mu.Lock()
			s.shellJobs["surface-receipt"] = &shellJob{ID: "surface-receipt", Server: "peer", ReceiptTarget: target, Caller: caller, ToolName: "mcp_manage", Status: "completed", CreatedAt: nowFloat(), DoneAt: nowFloat(), Result: map[string]any{"sentinel": "private-result"}}
			err := s.saveTaskStateLocked("surface-receipt")
			s.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			check := func(t *testing.T, reader *Server) {
				t.Helper()
				if profileAllowsTarget(request, "shell:peer") {
					t.Fatal("fixture grants shell")
				}
				if _, found := reader.taskReceiptForRequest(request, "surface-receipt", "full"); !found {
					t.Error("own surface receipt denied")
				}
				foreign := taskReceiptTestRequest("POST", "/mcp", "foreign", "foreign-token", profile)
				if _, found := reader.taskReceiptForRequest(foreign, "surface-receipt", "full"); found {
					t.Error("foreign receipt disclosed")
				}
				// Same caller and profile version cannot use a shell grant in place of
				// the original surface, even through the generic reader without a hint.
				removed := profile
				removed.AllowedTargets = []string{"hub", "shell:peer"}
				denied := requestWithAccessProfile(request, removed)
				if _, found := reader.taskReceiptForRequest(denied, "surface-receipt", "full"); found {
					t.Error("removed surface still readable with shell grant")
				}
			}
			t.Run("current", func(t *testing.T) { check(t, s) })
			restored := New(s.cfg)
			defer restored.Close()
			t.Run("restart", func(t *testing.T) {
				check(t, restored)
				restored.mu.Lock()
				job := restored.shellJobs["surface-receipt"]
				if job == nil || job.ReceiptTarget != target {
					t.Errorf("stored surface lost after restart: %#v", job)
				}
				restored.mu.Unlock()
			})
			for _, mode := range []string{"eviction", "newer-revision"} {
				t.Run(mode, func(t *testing.T) {
					restored.mu.Lock()
					if mode == "eviction" {
						delete(restored.shellJobs, "surface-receipt")
					} else {
						restored.shellJobs["surface-receipt"].ReceiptTarget = "shell:peer"
						restored.shellJobs["surface-receipt"].Revision = 0
					}
					restored.mu.Unlock()
					check(t, restored)
					restored.mu.Lock()
					if got := restored.shellJobs["surface-receipt"].ReceiptTarget; got != target {
						t.Errorf("lazy refresh surface = %q, want %q", got, target)
					}
					restored.mu.Unlock()
				})
			}
			profile.Version++
			if err := saveAccessProfilesState(s.accessProfilesStatePath(), map[string]AccessProfile{profile.ID: profile}); err != nil {
				t.Fatal(err)
			}
			if _, found := restored.taskReceiptForRequest(request, "surface-receipt", "full"); found {
				t.Error("revoked profile version accepted")
			}
		})
	}
}
