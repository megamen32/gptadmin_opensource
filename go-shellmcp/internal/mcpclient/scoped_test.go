package mcpclient

// These tests prove local partition bookkeeping only, below integration and
// release acceptance. They do not establish any working upstream transport.
import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/supervisor"
)

func scopedTestAgent() supervisor.Agent {
	return supervisor.Agent{Ref: "child", Enabled: true, Transport: "streamable-http", URL: "https://example.invalid/mcp"}
}

func TestScopedPartitionsSeparateConsumersAndReuseSameScope(t *testing.T) {
	c := New()
	defer c.CloseAll()
	a := scopedTestAgent()
	p, _, err := c.acquireScope(a, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	c.releaseScope(p)
	same, _, err := c.acquireScope(a, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	c.releaseScope(same)
	other, _, err := c.acquireScope(a, strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	c.releaseScope(other)
	if same != p || other == p || other.client == p.client {
		t.Fatal("scopes failed to partition protocol clients")
	}
	if len(c.remote) != 0 || len(c.stdio) != 0 {
		t.Fatal("scoped bookkeeping contaminated legacy pools")
	}
}

func TestScopedCapacityNeverEvictsActiveCalls(t *testing.T) {
	c := New()
	a := scopedTestAgent()
	var held []*scopedPartition
	for i := 0; i < maxScopedPartitions; i++ {
		p, _, err := c.acquireScope(a, fmt.Sprintf("%064x", i))
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, p)
	}
	if _, _, err := c.acquireScope(a, strings.Repeat("f", 64)); err == nil {
		t.Fatal("capacity did not fail closed")
	}
	for _, p := range held {
		if p.ctx.Err() != nil {
			t.Fatal("active partition was evicted")
		}
		c.releaseScope(p)
	}
	c.CloseAll()
}

func TestScopedCredentialChangeInvalidatesIdlePartition(t *testing.T) {
	t.Setenv("SCOPED_TEST_CREDENTIAL", "first$literal")
	a := scopedTestAgent()
	a.ConnectionMode = "stateless"
	a.Headers = map[string]string{"Authorization": "Bearer ${SCOPED_TEST_CREDENTIAL}"}
	c := New()
	defer c.CloseAll()
	scope := strings.Repeat("a", 64)
	p, effective, err := c.acquireScope(a, scope)
	if err != nil {
		t.Fatal(err)
	}
	c.releaseScope(p)
	if effective.Headers["Authorization"] != "Bearer first$literal" {
		t.Fatal("credential snapshot was not resolved")
	}
	t.Setenv("SCOPED_TEST_CREDENTIAL", "second")
	next, _, err := c.acquireScope(a, scope)
	if err != nil {
		t.Fatal(err)
	}
	c.releaseScope(next)
	if p == next || p.fingerprint == next.fingerprint || p.ctx.Err() == nil {
		t.Fatal("changed credential retained old partition")
	}
}

func TestCloseScopeCancelsActivePartitionAndWaitsForRelease(t *testing.T) {
	c := New()
	a := scopedTestAgent()
	scope := strings.Repeat("a", 64)
	p, _, err := c.acquireScope(a, scope)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- c.CloseScope(a.Ref, scope) }()
	select {
	case <-p.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("active call was not cancelled")
	}
	select {
	case <-done:
		t.Fatal("close returned before active partition released")
	default:
	}
	c.releaseScope(p)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("close did not complete")
	}
	if len(c.scoped) != 0 {
		t.Fatal("closed partition retained")
	}
	if err := c.CloseScope(a.Ref, scope); err != nil {
		t.Fatal(err)
	}
}

func TestScopedLazyExpiryAndOriginalValidatorIdentity(t *testing.T) {
	c := New()
	defer c.CloseAll()
	a := scopedTestAgent()
	a.ConnectionMode = "stateless"
	c.SetAgentValidator(func(candidate supervisor.Agent) bool { return candidate.Ref == "child" })
	p, _, err := c.acquireScope(a, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	c.releaseScope(p)
	p.lastUsed = time.Now().Add(-scopedIdleTTL - time.Second)
	next, _, err := c.acquireScope(a, strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	c.releaseScope(next)
	if p.ctx.Err() == nil || len(c.scoped) != 1 {
		t.Fatal("idle partition not expired")
	}
	if !next.client.validator(a) {
		t.Fatal("validator lost original child identity")
	}
}

func TestScopeValidationRejectsMalformedValues(t *testing.T) {
	for _, scope := range []string{"", "a", strings.Repeat("z", 64), strings.Repeat("a", 65)} {
		if ValidateScope(scope) == nil {
			t.Fatal("malformed scope accepted")
		}
	}
}

func TestStatefulScopeDoesNotExpireIdleMutableSession(t *testing.T) {
	c := New()
	defer c.CloseAll()
	a := scopedTestAgent()
	scope := strings.Repeat("a", 64)
	p, _, err := c.acquireScope(a, scope)
	if err != nil {
		t.Fatal(err)
	}
	c.releaseScope(p)
	p.lastUsed = time.Now().Add(-scopedIdleTTL - time.Hour)
	same, _, err := c.acquireScope(a, scope)
	if err != nil {
		t.Fatal(err)
	}
	c.releaseScope(same)
	if same != p || p.ctx.Err() != nil {
		t.Fatal("stateful mutable session expired")
	}
}

func TestStatefulScopeRejectsCredentialDriftWithoutResettingState(t *testing.T) {
	t.Setenv("SCOPED_TEST_CREDENTIAL", "first")
	a := scopedTestAgent()
	a.Headers = map[string]string{"Authorization": "Bearer ${SCOPED_TEST_CREDENTIAL}"}
	c := New()
	defer c.CloseAll()
	scope := strings.Repeat("a", 64)
	p, _, err := c.acquireScope(a, scope)
	if err != nil {
		t.Fatal(err)
	}
	c.releaseScope(p)
	t.Setenv("SCOPED_TEST_CREDENTIAL", "second")
	if _, _, err := c.acquireScope(a, scope); err == nil {
		t.Fatal("stateful credential drift silently reset upstream")
	}
	if p.ctx.Err() != nil || len(c.scoped) != 1 {
		t.Fatal("stateful partition was disposed by drift")
	}
}

func TestClosedScopeCannotBeRecreatedByDelayedAdmission(t *testing.T) {
	c := New()
	a := scopedTestAgent()
	scope := strings.Repeat("a", 64)
	if err := c.CloseScope(a.Ref, scope); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.acquireScope(a, scope); err == nil {
		t.Fatal("closed scope was recreated")
	}
}

func TestClosedScopeTombstoneCapacityFailsClosed(t *testing.T) {
	c := New()
	a := scopedTestAgent()
	for i := 0; i < maxClosedScopes; i++ {
		if err := c.CloseScope(a.Ref, fmt.Sprintf("%064x", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.CloseScope(a.Ref, strings.Repeat("f", 64)); err == nil {
		t.Fatal("tombstone overflow silently accepted")
	}
	if len(c.closedScopes) > maxClosedScopes {
		t.Fatal("unbounded tombstones")
	}
	if _, _, err := c.acquireScope(a, strings.Repeat("e", 64)); err == nil {
		t.Fatal("overflow allowed scoped admission")
	}
}

// This double exercises local teardown disposition only. It does not prove
// that any real upstream supports or completes protocol DELETE.
type dispositionOnlyRemote struct {
	failures int
	closes   int
}

func (r *dispositionOnlyRemote) call(context.Context, string, any) (map[string]any, error) {
	panic("not an integration transport")
}
func (r *dispositionOnlyRemote) close() error {
	r.closes++
	if r.failures > 0 {
		r.failures--
		return errors.New("local disposition failure")
	}
	return nil
}

func TestFailedCloseRetainsPartitionAndRetriesRemoteDisposition(t *testing.T) {
	c := New()
	a := scopedTestAgent()
	scope := strings.Repeat("a", 64)
	p, _, err := c.acquireScope(a, scope)
	if err != nil {
		t.Fatal(err)
	}
	c.releaseScope(p)
	remote := &dispositionOnlyRemote{failures: 1}
	p.client.remote[a.Ref] = remote
	if err := c.CloseScope(a.Ref, scope); err == nil {
		t.Fatal("failed remote disposition reported success")
	}
	if len(c.scoped) != 1 || p.client.remote[a.Ref] != remote {
		t.Fatal("failed cleanup discarded retry state or capacity reservation")
	}
	if _, _, err := c.acquireScope(a, scope); err == nil {
		t.Fatal("failed close scope became callable")
	}
	if err := c.CloseScope(a.Ref, scope); err != nil {
		t.Fatal(err)
	}
	if len(c.scoped) != 0 || remote.closes != 2 {
		t.Fatal("remote disposition did not retry")
	}
	if err := c.CloseScope(a.Ref, scope); err != nil {
		t.Fatal(err)
	}
}

func TestScopedStdioEnvironmentExcludesUnrelatedSecretsAndTracksDeclaredCredentials(t *testing.T) {
	t.Setenv("SCOPED_OTHER_CLIENT_TOKEN", "unrelated-first")
	t.Setenv("SCOPED_DECLARED_TOKEN", "declared-first$literal")
	t.Setenv("PATH", "/deterministic/runtime/path")
	t.Setenv("HOME", "/operator/home")
	a := supervisor.Agent{Ref: "stdio", Enabled: true, Transport: "stdio", Command: "unused", Env: map[string]string{"BOUND_TOKEN": "${SCOPED_DECLARED_TOKEN}"}}
	_, env, first := effectiveDefinition(a)
	merged := map[string]string{}
	for _, pair := range env {
		key, value, _ := strings.Cut(pair, "=")
		merged[key] = value
	}
	if merged["SCOPED_OTHER_CLIENT_TOKEN"] != "" || merged["SCOPED_DECLARED_TOKEN"] != "" {
		t.Fatal("executor credentials inherited implicitly")
	}
	if merged["BOUND_TOKEN"] != "declared-first$literal" || merged["HOME"] != "/operator/home" || merged["PATH"] != "/deterministic/runtime/path" {
		t.Fatal("declared credential or runtime baseline lost")
	}
	t.Setenv("SCOPED_OTHER_CLIENT_TOKEN", "unrelated-second")
	_, _, same := effectiveDefinition(a)
	if same != first {
		t.Fatal("unrelated secret affected scoped definition fingerprint")
	}
	t.Setenv("SCOPED_DECLARED_TOKEN", "declared-second")
	_, _, changed := effectiveDefinition(a)
	if changed == first {
		t.Fatal("declared credential change did not affect fingerprint")
	}
}

func TestScopedRuntimeBaselineKeys(t *testing.T) {
	for _, key := range []string{"PATH", "HOME", "USER", "LOGNAME", "SHELL", "LANG", "LANGUAGE", "LC_ALL", "LC_CTYPE", "TZ", "TMPDIR", "TMP", "TEMP", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
		if !scopedRuntimeEnvironmentKey(key) {
			t.Fatal("runtime baseline key omitted")
		}
	}
	for _, key := range []string{"CTL_TOKEN", "OAUTH_SECRET", "FRP_TOKEN", "OPENAI_API_KEY", "OTHER_CLIENT_BEARER", "NODE_OPTIONS", "LD_PRELOAD", "PYTHONPATH"} {
		if scopedRuntimeEnvironmentKey(key) {
			t.Fatal("unrelated or executable-injection environment inherited")
		}
	}
}

func TestScopedStdioSnapshotRemainsNonNilWithEmptyRuntimeBaseline(t *testing.T) {
	original := os.Environ()
	os.Clearenv()
	defer func() {
		for _, pair := range original {
			key, value, ok := strings.Cut(pair, "=")
			if ok {
				_ = os.Setenv(key, value)
			}
		}
	}()
	_, environment, _ := effectiveDefinition(supervisor.Agent{Ref: "stdio", Enabled: true, Transport: "stdio", Command: "unused"})
	if environment == nil || len(environment) != 0 {
		t.Fatal("empty scoped snapshot would fall back to executor environment")
	}
}

func TestExpectedGenerationRejectsValidationAdmissionRace(t *testing.T) {
	c := New()
	a := scopedTestAgent()
	scope := strings.Repeat("a", 64)
	validated := WithExpectedGeneration(context.Background(), c.Generation(a.Ref))
	if err := c.Close(a.Ref); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.acquireScope(a, scope, validated); err == nil {
		t.Fatal("old validated lifecycle admitted after close")
	}
	current := WithExpectedGeneration(context.Background(), c.Generation(a.Ref))
	p, _, err := c.acquireScope(a, scope, current)
	if err != nil {
		t.Fatal(err)
	}
	c.releaseScope(p)
	c.CloseAll()
}

func TestSSEHandshakeTimeoutHasBoundedDefault(t *testing.T) {
	for _, configured := range []time.Duration{0, -time.Second} {
		if sseHandshakeTimeout(configured) != 30*time.Second {
			t.Fatal("SSE handshake lacks bounded fallback")
		}
	}
	if sseHandshakeTimeout(2*time.Second) != 2*time.Second {
		t.Fatal("configured handshake budget ignored")
	}
}

func TestOldEpochCloseRetryRequiresRetainedOrConfirmedDisposition(t *testing.T) {
	c := New()
	a := scopedTestAgent()
	scope := strings.Repeat("a", 64)
	if c.CanRetryScopeClose(a.Ref, scope) {
		t.Fatal("unknown old scope treated as owned")
	}
	c.mu.Lock()
	err := c.rememberClosedScopeLocked(a.Ref, scope, time.Now())
	c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if c.CanRetryScopeClose(a.Ref, scope) {
		t.Fatal("admission tombstone treated as disposal proof")
	}
	if err := c.CloseScope(a.Ref, scope); err != nil {
		t.Fatal(err)
	}
	if !c.CanRetryScopeClose(a.Ref, scope) {
		t.Fatal("confirmed local disposition was not retained")
	}
}
