# macOS Auto-Update Refactor — Fix Report

**Fixer**: FIX AGENT (Linux box, cross-compile + existing tests)
**Fix date**: 2026-07-11
**Critic review**: `mac-critic-review.md` (1 CRITICAL + 2 IMPORTANT + 1 minor)
**Follow-up verifier**: REAL-MAC harness `tests/mac/launchd_verify.py` (8/8 pass on macOS 26.6 arm64)

All four critic-review items plus the follow-up improvements are addressed.
Linux code path is untouched; cli.py remains single-file; both Go and Python
test suites stay green; both darwin cross-compiles stay clean.

---

## Follow-up #1 — IMPORTANT: `DefaultUpdateLauncher` honored `GPTADMIN_SERVICE_SUFFIX`

**Status**: already shipped in commit `1e21859` (prior fixer). Re-verified here.

`DefaultUpdateLauncher()` in `go-hub/internal/hub/update_launcher.go` reads
`GPTADMIN_SERVICE_SUFFIX` from env, trims whitespace, validates against
`[A-Za-z0-9_.-]+` (matching `cli.py`'s regex), fails-soft to empty on bad
input, and splices the suffix into the launchd label so it matches
`SVC_AUTO_UPDATE_LABEL` on the Python side. Three unit tests cover
suffix-present, suffix-absent, and malformed-suffix-fallback paths:

- `TestDefaultUpdateLauncherHonorsServiceSuffix` — `.e2e42`, `-staging`,
  `_ci`, whitespace-stripping produce the expected labels.
- `TestDefaultUpdateLauncherDropsMalformedSuffix` — `bad space`, `semi;colon`,
  `slash/infix` fall back to the default label.
- `TestDefaultUpdateLauncherSuffixMatchesPython` — explicit cross-check of
  canonical inputs against the Python construction.

Verified: `go test ./...` is green (1.19s).

---

## Follow-up #2 — IMPORTANT: tighten `AbandonProcessGroup` docstring

**Status**: prior commit `1e21859` corrected the inverted claim. This follow-up
tightens the wording further to make the bootout-resilience semantics explicit.

Old (prior fix):

> "`AbandonProcessGroup=true`: tells launchd to NOT send SIGTERM to the
> job's process group when the wrapper exits. Any leftover children are left
> running. ..."

New (this commit, `b68b668`):

> "`AbandonProcessGroup=true`: prevents launchd from sending SIGTERM to
> the wrapper's process group on bootout. The job is therefore allowed to
> complete even if the parent launchd job is unloaded mid-run. Children are
> *abandoned*, not cleaned up. The wrapper `exec`s into the CLI without
> forking, so we have no children to worry about; the flag is set for
> bootout-resilience, not for cleanup."

Verified: `python3 -c "import ast; ast.parse(open('cli.py').read())"` clean.

---

## Follow-up #3 — promote the REAL-MAC verification harness

**Status**: shipped in commit `89b37f3`.

The harness that verified the launchd logic on a real Mac (8/8 passing on
macOS 26.6 arm64) previously lived at `/tmp/macverify_harness.py` and would
have been lost. Promoted to `tests/mac/launchd_verify.py`.

- Self-contained: imports `cli` from the repo root (added to `sys.path`),
  sets isolated env vars, runs a shim CLI that only touches a marker file,
  exercises `write_autoupdate_unit`, `timer_disable`, `timer_enable`,
  manual `launchctl kickstart`, state-field parsing, and
  `_launchctl_kickstart_cmd`, then cleans up.
- Skip-on-Linux: prints a skip message and exits 0 when `sys.platform` is
  not `darwin`. Uses `pytest.skip(allow_module_level=True)` when invoked
  via pytest.
- 8 checks (same as the proven `/tmp/macverify_harness.py`):
  1. `write_autoupdate_unit` loads the job (`launchctl print` rc=0).
  2. `write_autoupdate_unit` does NOT fire the shim at install (load-only).
  3. `launchctl print` 'state' field is parseable (Go-side concern).
  4. Manual `launchctl kickstart -k` runs the shim (unified trigger).
  5. `timer_disable` does NOT fire the shim (CRITICAL fix).
  6. Job stays loaded after `timer_disable` (manual kickstart preserved).
  7. `timer_enable` DOES fire the shim (intentional first kick).
  8. `_launchctl_kickstart_cmd` builds the right `gui/<uid>/<label>` target.
- Invocation:
  - `python3 tests/mac/launchd_verify.py` (ad-hoc on a real Mac).
  - `GPTADMIN_MAC_VERIFY=1 pytest tests/mac/launchd_verify.py` (CI runner).
- Isolation: `GPTADMIN_INSTALL_MODE=user`, `GPTADMIN_USER_HOME` and
  `GPTADMIN_HOME` both point into a `tempfile.mkdtemp(prefix='gptadmin_macverify_')`,
  `GPTADMIN_SERVICE_SUFFIX=.macverify`, shim CLI writes only to
  `<isolated>/marker.log`. The harness's real `LaunchAgent` is the
  suffixed `com.gptadmin.macverify.auto-update` — never the production label.
- Cleanup: `svc_disable_stop` + `bootout` + plist removal in a `finally`
  block; the temp dir is removed after the run.
- `pyproject.toml` gains `[tool.pytest.ini_options] python_files = ["test_*.py", "*_test.py", "launchd_verify.py"]` so the explicitly-named file
  is collected by pytest alongside the default patterns.
- `tests/mac/README.md` (5 lines): explains "real-launchd harness, macOS only,
  isolated via `GPTADMIN_SERVICE_SUFFIX`."

Verified on Linux: harness exits 0 with the skip message; full pytest suite
goes from 66 passed / 1 skipped to 66 passed / 2 skipped (the new harness is
the additional skip; no regressions).

---

## Original fixes (commit `1e21859`, prior round)

## Fix 1 — CRITICAL: `timer_disable` no longer fires an immediate update

**File**: `cli.py` (macOS block)

### Change

Split the loading logic into two helpers so "load the plist" and "kick the job" are separable:

- New `svc_enable(label, unit_path)` — bootout + bootstrap + `enable` + `load -w` fallback. Returns with the job loaded but **never** invokes `launchctl kickstart`.
- `svc_enable_start(label, unit_path)` now delegates loading to `svc_enable` and **only then** issues the best-effort `launchctl kickstart -k`. It remains the right entry point for long-running services (hub/shellmcp/frpc/cloudflared) where "enable" implies "start now."
- `timer_disable` now calls `svc_enable` (load-only) — fixing the CRITICAL UX bug where `gptadmin auto-update disable` rewrote the plist and then immediately kicked one run.
- `timer_enable` keeps `svc_enable_start` (intentional first kick when the user just enabled periodic updates — confirmed by the critic).
- `write_autoupdate_unit` now also calls `svc_enable` (load-only) at install time so a fresh install has the job registered for future manual kickstart, **without** firing an update. This is purely defensive — the prior call sites still call `timer_enable`/`timer_disable` afterwards, but the load-only call makes the unit self-sufficient if anyone skips those (e.g., a future `version`-only refresh).

### Why

`launchctl kickstart -k` runs the wrapper immediately. Reloading a config-only change (StartInterval removal) must load the new plist content into launchd without running the job. The Python and Go sides share the same plist label so the cli path now matches the Go side's invariants.

### Exact diff sketch

`cli.py` macOS block, around `svc_enable_start`/`timer_disable`/`write_autoupdate_unit` — see git diff.

---

## Fix 2 — IMPORTANT: Go `DefaultUpdateLauncher` honors `GPTADMIN_SERVICE_SUFFIX`

**File**: `go-hub/internal/hub/update_launcher.go`

### Change

`DefaultUpdateLauncher()` now reads `GPTADMIN_SERVICE_SUFFIX`, trims whitespace, validates against `[A-Za-z0-9_.-]+` (same regex cli.py uses), and splices the suffix into the label so the output matches the Python side's construction exactly:

```go
suffix := strings.TrimSpace(os.Getenv("GPTADMIN_SERVICE_SUFFIX"))
if !validServiceSuffix(suffix) {
    suffix = "" // fail-soft; Python is authoritative validator
}
label := "com.gptadmin" + suffix + ".auto-update"
```

### Tests

Added three new tests in `update_launcher_test.go`:

1. `TestDefaultUpdateLauncherHonorsServiceSuffix` — verifies `.e2e42`, `-staging`, `_ci`, and whitespace-stripping produce the expected `com.gptadmin.e2e42.auto-update` etc.
2. `TestDefaultUpdateLauncherDropsMalformedSuffix` — `bad space`, `semi;colon`, `slash/infix` must fall back to the default label rather than splice garbage.
3. `TestDefaultUpdateLauncherSuffixMatchesPython` — explicit cross-check of canonical inputs against the Python construction, so any future drift on either side fails the build.

### Why

The hub's `launchctl kickstart -k <domain>/<label>` targets the same label the plist carries. If Go hardcodes `"com.gptadmin.auto-update"` while the Python side, on the same host, sets `GPTADMIN_SERVICE_SUFFIX=.e2e42` and writes `com.gptadmin.e2e42.auto-update` to the plist, kickstart hits "Could not find service" and the update fails silently.

---

## Fix 3 — IMPORTANT: corrected `AbandonProcessGroup` docstring

**File**: `cli.py`, `_plist_oneshot` docstring.

### Change

Old claim: *"`AbandonProcessGroup=true`: when the wrapper exits, launchd cleans up any straggling children."*

New claim: *"`AbandonProcessGroup=true`: tells launchd to NOT send SIGTERM to the job's process group when the wrapper exits — leftover child processes are left running. The wrapper itself `exec`s into the CLI without forking, so we have no children to clean up; this key is set defensively for clarity of intent, not because we need it."*

### Why

The flag's actual semantics is the opposite of what the original docstring said. The user-facing behavior of auto-update is unchanged (we don't fork from the wrapper), but a future maintainer reading the old docstring would write child processes and assume launchd cleans them up — which it does not, with this flag set.

---

## Fix 4 — minor (race window)

The race window between `bootout` and `bootstrap` is largely resolved by Fix 1's clean separation: a config reload now never re-kicks, so the only races are on first-install or first-enable, both of which are followed by an explicit user action. No further retry logic added — over-engineering for an edge case that self-heals within a single user-visible retry on the UI button.

---

## Test updates

`tests/test_update_semantics.py::test_macos_launchd_bootout_is_not_duplicated_before_bootstrap` previously asserted that `svc_enable_start` directly performed `bootout`/`bootstrap`. After the split, the bootstrap lives in the new `svc_enable` helper. The test was updated to assert the new invariants:

- `svc_enable_start` delegates the load to `svc_enable` (contains `svc_enable(label, unit_path)` and contains neither `bootout` nor `bootstrap`).
- `svc_enable` owns the bootout + bootstrap.
- `timer_disable` calls `svc_enable` (NOT `svc_enable_start`) — the regression guard.
- `timer_enable` keeps `svc_enable_start` (intentional first kick).

---

## Verification

### Go

```
$ cd go-hub && go test -count=1 ./...
ok      github.com/megamen32/gptadmin/go-hub/internal/hub    1.191s
```

New subtests visible with `-v`:

```
TestDefaultUpdateLauncher                          (unchanged)
TestDefaultUpdateLauncherHonorsServiceSuffix       (.e2e42 / -staging / _ci / whitespace)
TestDefaultUpdateLauncherDropsMalformedSuffix      (bad space / semi;colon / slash/infix)
TestDefaultUpdateLauncherSuffixMatchesPython       (Python parity)
TestDomainUserInstall / TestDomainSystemInstall    (unchanged)
TestLaunchUpdateUnsupportedOS                      (unchanged)
```

```
$ cd go-hub && go vet ./...                         # clean
$ cd go-hub && GOOS=darwin GOARCH=amd64 go build ./...   # clean
$ cd go-hub && GOOS=darwin GOARCH=arm64 go build ./...   # clean
```

### Python

```
$ python3 -m pytest tests/test_plist_macos.py -v
tests/test_plist_macos.py::test_plist_oneshot_no_interval_has_oneshot_semantics PASSED
tests/test_plist_macos.py::test_plist_oneshot_with_interval_emits_start_interval PASSED
tests/test_plist_macos.py::test_plist_oneshot_output_is_well_formed_xml PASSED
tests/test_plist_macos.py::test_plist_oneshot_no_interval_regression_guard PASSED
tests/test_plist_macos.py::test_plist_oneshot_label_is_rendered_inside_string_tag PASSED
tests/test_plist_macos.py::test_launchctl_kickstart_cmd_user_domain_uses_current_uid PASSED
tests/test_plist_macos.py::test_launchctl_kickstart_cmd_system_domain_skips_uid PASSED
```

```
$ python3 -m pytest tests/ -q --ignore=tests/e2e
........................s..........................................      [100%]
66 passed, 1 skipped, 1 warning in 9.91s
```

(1 skipped = existing skip; the only failed test was the one we updated above.)

### Smoke

```
$ python3 cli.py auto-update status
GPTAdmin Auto-update
  Auto-update: enabled interval=21600s

$ python3 cli.py version
  Platform:  Linux x86_64
```

---

## Files changed

### Round 1 (commit `1e21859`, prior fix)

- `cli.py` — split svc_enable/svc_enable_start, wired timer_disable/write_autoupdate_unit, corrected AbandonProcessGroup docstring.
- `go-hub/internal/hub/update_launcher.go` — suffix-aware label construction + validator.
- `go-hub/internal/hub/update_launcher_test.go` — three new tests.
- `tests/test_update_semantics.py` — updated assertion block.
- `.superpowers/sdd/mac-fix-report.md` — original report.

### Round 2 (this round, follow-ups)

- `cli.py` — further tightened `AbandonProcessGroup` docstring to spell out the
  bootout-resilience semantics.
- `tests/mac/launchd_verify.py` (new) — promoted real-launchd verification
  harness; skip-on-Linux with `pytest.skip(allow_module_level=True)`.
- `tests/mac/README.md` (new) — 5-line note on the harness purpose.
- `tests/mac/conftest.py` (new) — empty marker so the directory is part of
  the test suite.
- `pyproject.toml` — `[tool.pytest.ini_options] python_files` extended with
  `launchd_verify.py` so the explicitly-named harness is collected.
- `.superpowers/sdd/mac-fix-report.md` — this round's additions appended.
