package hub

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"github.com/megamen32/gptadmin/go-hub/internal/hub/quorum"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTerminalCopyCannotResurrectAfterAck(t *testing.T) {
	s := &Server{cfg: Config{ConfigDir: t.TempDir()}}
	if err := s.initReceiptCopies(); err != nil {
		t.Fatal(err)
	}
	done := float64(time.Now().Unix()) - 2
	id := "ot1.b3duZXI.job"
	c := terminalCopy{Generation: "v1", ID: id, Expires: done + 3600, Job: &shellJob{ID: id, Server: "host", Status: "completed", DoneAt: done, Result: "secret result"}}
	db, err := openTaskDatabase(filepath.Join(s.cfg.ConfigDir, "tasks_state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("CREATE TABLE task_records(kind TEXT,key TEXT,done REAL);"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO task_records VALUES('shell',?,?)", id, done); err != nil {
		t.Fatal(err)
	}
	prepare := func(c *terminalCopy, want bool) {
		t.Helper()
		got, err := s.sourceCopyState(c)
		if err != nil || got != want {
			t.Fatalf("prepare = %v,%v; want %v", got, err, want)
		}
	}
	prepare(&c, true)
	// A replica is read-only data, distinct from the original dispatcher table.
	if err = s.storeTerminalCopy(c); err != nil {
		t.Fatal(err)
	}
	if err = s.confirmCopyState(&c); err != nil {
		t.Fatal(err)
	}
	prepare(&c, false)
	revoke := c
	revoke.Job = nil
	prepare(&revoke, true)
	if err = s.storeTerminalCopy(revoke); err != nil {
		t.Fatal(err)
	}
	if err = s.confirmCopyState(&revoke); err != nil {
		t.Fatal(err)
	}
	prepare(&c, false)
	prepare(&revoke, false)
	if err = s.storeTerminalCopy(c); err != nil {
		t.Fatal(err)
	}
	var null, count int
	if err = db.QueryRow("SELECT body IS NULL FROM task_terminal_copies WHERE generation='v1' AND id=?", id).Scan(&null); err != nil || null != 1 {
		t.Fatalf("late duplicate reexposed result: %v %v", null, err)
	}
	if err = db.QueryRow("SELECT count(*) FROM task_records").Scan(&count); err != nil || count != 1 {
		t.Fatalf("execution rows = %d: %v", count, err)
	}
	// A never-mirrored local result can be consumed with every other node down.
	revoke.ID = "unmirrored"
	prepare(&revoke, false)
	c.ID = revoke.ID
	c.Job.ID = revoke.ID
	prepare(&c, false)
}

func TestDispatchedCopyCannotExecuteOrOutlivePrimaryTombstone(t *testing.T) {
	s := &Server{cfg: Config{ConfigDir: t.TempDir()}}
	if err := s.initReceiptCopies(); err != nil {
		t.Fatal(err)
	}
	now := float64(time.Now().Unix())
	id := "ot1.b3duZXI.active"
	grant := terminalCopy{Generation: "g", ID: id, Expires: now + 3600, Dispatch: &dispatchVoter{ID: "voter", Physical: "physical", Fingerprint: "fingerprint"}, Job: &shellJob{ID: id, Server: "host", ToolName: "shell_exec", Status: "running", StartedAt: now - 1}}
	if err := s.storeTerminalCopy(grant); err != nil {
		t.Fatal(err)
	}
	db, err := openTaskReadDatabase(s.taskDatabasePath())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err = db.QueryRow("SELECT COUNT(*) FROM task_terminal_copies").Scan(&n); err != nil || n != 0 {
		t.Fatalf("running descriptor became an executed result: %d: %v", n, err)
	}
	revoke := terminalCopy{Generation: "g", ID: id, Expires: now + 3600}
	if err = s.storeTerminalCopy(revoke); err != nil {
		t.Fatal(err)
	}
	if err = s.storeTerminalCopy(grant); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow("SELECT COUNT(*) FROM task_terminal_dispatch").Scan(&n); err != nil || n != 0 {
		t.Fatalf("grant survived revoke or delayed copy: %d: %v", n, err)
	}
}

func copyWireFixture(t *testing.T, handler http.HandlerFunc) *Server {
	t.Helper()
	peer := httptest.NewTLSServer(handler)
	t.Cleanup(peer.Close)
	roots := x509.NewCertPool()
	roots.AddCert(peer.Certificate())
	return &Server{quorumPrivateTLS: &quorumPrivateTLS{roots: roots, peers: map[string]quorum.Peer{
		"owner": {ID: "owner"}, "replica": {ID: "replica", InternalHubURL: peer.URL, ServerName: peer.Certificate().DNSNames[0]},
	}}}
}

func TestTerminalCopyRefusedGrantTransfersZeroOutcomeBytes(t *testing.T) {
	received := make(chan []byte, 2)
	s := copyWireFixture(t, func(w http.ResponseWriter, r *http.Request) {
		data, e := io.ReadAll(r.Body)
		if e != nil {
			t.Error(e)
		}
		received <- data
		w.WriteHeader(http.StatusForbidden)
	})
	s.transmitExecutorResult(context.Background(), "replica", forwardedExecutorResult{ID: "ot1.b3duZXI.task", Body: json.RawMessage(`{"id":"ot1.b3duZXI.task","result":"forbidden-raw-output"}`)})
	if len(received) != 1 {
		t.Fatal("an outcome was sent to a receiver without a grant")
	}
	first := <-received
	if strings.Contains(string(first), "forbidden-raw-output") {
		t.Fatal("preflight sent raw bytes")
	}
	var check forwardedExecutorResult
	if err := json.Unmarshal(first, &check); err != nil || len(check.Body) > 0 && string(check.Body) != "null" {
		t.Fatal("authorization preflight included result bytes")
	}
}

func TestTerminalCopyCancellationLeavesHubMutexAvailable(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	s := copyWireFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		w.WriteHeader(http.StatusNoContent)
	})
	done := make(chan error, 1)
	go func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		done <- s.revokeCopiesOutsideLock([]*terminalCopy{{RevokeDispatch: true, Generation: "v1", ID: "ot1.b3duZXI.task", Expires: 9999999999}})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second * 2):
		close(release)
		t.Fatal("private request missing")
	}
	acquired := s.mu.TryLock()
	if acquired {
		s.mu.Unlock()
	}
	close(release)
	if e := <-done; e != nil || !acquired {
		t.Fatalf("cancel held the Hub mutex across network I/O: %v %v", acquired, e)
	}
}

func TestStandaloneCopyRequiresOneDistinctApprovedPhysicalCredential(t *testing.T) {
	const credential = "individual-synthetic-credential"
	agent := func(hash string) *Agent {
		return &Agent{Status: "online", Meta: map[string]any{"approved": true, "server_id": "fixture-id", "fingerprint": "fixture-fp", shellTokenHashMetaKey: hash}}
	}
	s := &Server{cfg: Config{ShellToken: "shared-fleet-token"}, agents: map[string]*Agent{"shell:one": agent(shellTokenHash(credential))}}
	if b := s.receiptExecutorBindingLocked("one"); b == nil || b.Kind != "agent" {
		t.Fatal("individual pinned executor rejected")
	}
	s.agents["shell:two"] = agent(shellTokenHash(credential))
	if s.receiptExecutorBindingLocked("one") != nil {
		t.Fatal("ambiguous credential permitted")
	}
	delete(s.agents, "shell:two")
	s.agents["shell:one"] = agent(shellTokenHash(s.cfg.ShellToken))
	if s.receiptExecutorBindingLocked("one") != nil {
		t.Fatal("global credential permitted")
	}
	s.agents["shell:one"] = agent(shellTokenHash(credential))
	s.agents["shell:one"].Meta["approved"] = false
	if s.receiptExecutorBindingLocked("one") != nil {
		t.Fatal("unapproved executor permitted")
	}
}

func TestOlderExecutorHasNoSecretReceiptCopyPolicy(t *testing.T) {
	t.Setenv("GPTADMIN_RECEIPT_COPY_GENERATION", "test-generation")
	s := &Server{quorumMode: quorum.OwnerLocal, quorumNode: &quorum.Node{}, quorumPrivateTLS: &quorumPrivateTLS{}}
	j := &shellJob{ToolName: "shell_exec", Env: map[string]any{"SYNTHETIC": "private fixture"}, SecretValues: []string{"private fixture"}}
	for _, url := range []string{"/queue/worker", "/queue/worker?receipt_redaction=2"} {
		if policy := s.queueReceiptRedaction(j, httptest.NewRequest("GET", url, nil)); policy != nil {
			t.Fatal("older or unknown worker permitted secret grant")
		}
	}
	req := httptest.NewRequest("GET", "/queue/worker?receipt_redaction=1", nil)
	p := s.queueReceiptRedaction(j, req)
	if p == nil || len(p.EnvNames) != 1 || p.EnvNames[0] != "SYNTHETIC" {
		t.Fatal("exact capability should contain only the original env name")
	}
	j.SecretValues = append(j.SecretValues, "unbound-private-fixture")
	if s.queueReceiptRedaction(j, req) != nil {
		t.Fatal("incomplete mapping permitted disclosure")
	}
}

func TestRecoveredReceiptSecretsAreBoundToOneTaskAndNeverCopied(t *testing.T) {
	root := t.TempDir()
	vault, err := NewSecretStoreWithStateFile(root, filepath.Join(root, "vault"), filepath.Join(root, "key"), filepath.Join(root, "state.json"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{secretStore: vault, quorumMode: quorum.OwnerLocal, quorumNode: &quorum.Node{}, quorumPrivateTLS: &quorumPrivateTLS{}}
	t.Setenv("GPTADMIN_RECEIPT_COPY_GENERATION", "test-generation")
	const private = "synthetic-unprinted-dictionary"
	j := &shellJob{ID: "ot1.b3duZXI.job", Status: "running", SecretValues: []string{private}}
	envelope, e := s.sealOriginalReceiptSecrets(j)
	if e != nil || len(envelope) == 0 || strings.Contains(string(envelope), private) {
		t.Fatal("invalid sealed original dictionary")
	}
	j.SecretValues = nil
	values, e := s.originalReceiptSecrets(j)
	if e != nil || len(values) != 1 || values[0] != private {
		t.Fatal("issuer lost redaction after restart")
	}
	j.ID = "ot1.b3duZXI.different"
	if values, e = s.originalReceiptSecrets(j); e == nil || len(values) != 0 {
		t.Fatal("cipher accepted another job's dictionary")
	}
	j.Status = "completed"
	envelope, e = s.sealOriginalReceiptSecrets(j)
	if e != nil || len(envelope) != 0 {
		t.Fatal("terminal row retained secret dictionary")
	}
	copy, e := json.Marshal(readbackShell(j))
	if e != nil || strings.Contains(string(copy), private) || strings.Contains(string(copy), "redaction_sealed") {
		t.Fatal("private dictionary sent to backup")
	}
}
