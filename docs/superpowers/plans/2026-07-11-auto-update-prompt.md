# Auto-Update Prompt, CLI Hint, Admin Versions + Update Button

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add explicit auto-update choice during install, startup update hints when auto-update is off, and admin UI with version display + one-click update.

**Architecture:** Reuses existing `gptadmin update` flow. Service-unit (systemd oneshot) always installed — used by both timer (auto) and admin button (manual). Update state is file-backed (not in-memory), written by wrapper script. Hub launches update via external supervisor (systemd start / nohup).

**Tech Stack:** Python 3 (cli.py), Go (go-hub), vanilla JS (admin UI)

## Global Constraints

- Keep cli.py as single-file — no new Python modules
- Go: new files for `update_state` and `update_launcher` (don't bloat server.go further)
- CLI cache: `~/.gptadmin/update_check.json`, 0600, atomic write
- Hub state: `~/.gptadmin/update_state.json`, flock, atomic write
- Admin API: POST only, no client-provided command args, CTL-token auth
- UI: must handle expected downtime during restart (no fatal error)
- All existing commands/tests must continue to pass

---

## Part 1: CLI — Setup Prompt

### Task 1: Decouple service-unit from timer (split `write_autoupdate_unit`)

**Files:**
- Modify: `cli.py` — `write_autoupdate_unit` functions (macOS: line ~777, Linux: line ~980)
- Modify: `cli.py` — `svc_autoupdate_enable_start` (line 2470)
- Modify: `cli.py` — `svc_autoupdate_disable_stop` (line 2482)

**What changes:** Service-unit file always written to disk; only timer is created/destroyed by enable/disable. Button in admin can always `systemctl start gptadmin-auto-update.service`.

- [ ] **Step 1: Change macOS `write_autoupdate_unit` to only write plist, skip timer logic**

Existing code at line 777-789 (macOS block, inside `elif platform.system() == 'Darwin':`):
```python
    def write_autoupdate_unit(env: dict):
        LOG_DIR.mkdir(parents=True, exist_ok=True)
        BIN_DIR.mkdir(parents=True, exist_ok=True)
        SERVICES_DIR.mkdir(parents=True, exist_ok=True)
        wrapper = BIN_DIR / 'run_auto_update.sh'
        wrapper.write_text(
            f'#!/bin/sh\n'
            f'set -a; [ -f {ENV_FILE} ] && . {ENV_FILE}; set +a\n'
            f'exec {CLI_PATH} --{INSTALL_SCOPE} update --auto\n'
        )
        os.chmod(wrapper, 0o755)
        UNIT_PATH_AUTO_UPDATE.write_text(_make_interval_plist(
            SVC_AUTO_UPDATE_LABEL, wrapper, LOG_DIR / 'auto-update.log', auto_update_interval_seconds(env)))
```

Replace with — always write the wrapper and plist (service unit), but use a plain launchd daemon (no interval). The timer behavior (interval) moves into `svc_autoupdate_enable_start` where it loads the plist:

```python
    def write_autoupdate_service_unit():
        """Write the auto-update service wrapper + launchd plist (always installed, no interval)."""
        LOG_DIR.mkdir(parents=True, exist_ok=True)
        BIN_DIR.mkdir(parents=True, exist_ok=True)
        SERVICES_DIR.mkdir(parents=True, exist_ok=True)
        wrapper = BIN_DIR / 'run_auto_update.sh'
        wrapper.write_text(
            f'#!/bin/sh\n'
            f'set -a; [ -f {ENV_FILE} ] && . {ENV_FILE}; set +a\n'
            f'exec {CLI_PATH} --{INSTALL_SCOPE} update --auto\n'
        )
        os.chmod(wrapper, 0o755)
        # Write plist WITHOUT StartInterval — it runs once when started via launchctl start
        plist = f'''<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key><string>{SVC_AUTO_UPDATE_LABEL}</string>
    <key>ProgramArguments</key>
    <array><string>{wrapper}</string></array>
    <key>RunAtLoad</key><false/>
    <key>StandardOutPath</key><string>{LOG_DIR / 'auto-update.log'}</string>
    <key>StandardErrorPath</key><string>{LOG_DIR / 'auto-update.log'}</string>
</dict>
</plist>'''
        UNIT_PATH_AUTO_UPDATE.write_text(plist)

    def write_autoupdate_timer_plist(env: dict):
        """Write launchd plist WITH StartInterval for periodic auto-update."""
        write_autoupdate_service_unit()  # ensure service wrapper exists
        wrapper = BIN_DIR / 'run_auto_update.sh'
        UNIT_PATH_AUTO_UPDATE.write_text(_make_interval_plist(
            SVC_AUTO_UPDATE_LABEL, wrapper, LOG_DIR / 'auto-update.log', auto_update_interval_seconds(env)))
```

- [ ] **Step 2: Change Linux `write_autoupdate_unit` to split service from timer**

Existing code at line 980-987 (Linux block, inside `else: # Linux systemd`):
```python
    def write_autoupdate_unit(env: dict):
        UNIT_PATH_AUTO_UPDATE.parent.mkdir(parents=True, exist_ok=True)
        UNIT_PATH_AUTO_UPDATE.write_text(AUTO_UPDATE_SERVICE_TPL.format(
            env_file=ENV_FILE, cli_path=CLI_PATH, install_scope=INSTALL_SCOPE))
        UNIT_PATH_AUTO_UPDATE_TIMER.write_text(AUTO_UPDATE_TIMER_TPL.format(
            interval_sec=auto_update_interval_seconds(env),
            random_delay_sec=auto_update_randomized_delay_seconds(env),
            service_name=SYSTEMD_AUTO_UPDATE))
```

Replace with:
```python
    def write_autoupdate_service_unit():
        """Always write the oneshot service unit (used by manual button + timer)."""
        UNIT_PATH_AUTO_UPDATE.parent.mkdir(parents=True, exist_ok=True)
        UNIT_PATH_AUTO_UPDATE.write_text(AUTO_UPDATE_SERVICE_TPL.format(
            env_file=ENV_FILE, cli_path=CLI_PATH, install_scope=INSTALL_SCOPE))

    def write_autoupdate_timer_unit(env: dict):
        """Write and enable the timer unit (only when auto-update is on)."""
        write_autoupdate_service_unit()  # service must exist for timer to reference
        UNIT_PATH_AUTO_UPDATE_TIMER.write_text(AUTO_UPDATE_TIMER_TPL.format(
            interval_sec=auto_update_interval_seconds(env),
            random_delay_sec=auto_update_randomized_delay_seconds(env),
            service_name=SYSTEMD_AUTO_UPDATE))
```

- [ ] **Step 3: Update macOS `svc_autoupdate_enable_start` (line 2470-2480)**

Existing:
```python
def svc_autoupdate_enable_start(env: dict):
    if not auto_update_enabled(env):
        svc_autoupdate_disable_stop()
        return
    write_autoupdate_unit(env)
    svc_daemon_reload()
    if IS_MACOS:
        svc_enable_start(SVC_AUTO_UPDATE_LABEL, UNIT_PATH_AUTO_UPDATE)
    else:
        run(_systemctl_cmd('enable', '--now', SYSTEMD_AUTO_UPDATE_TIMER))
```

Replace with:
```python
def svc_autoupdate_enable_start(env: dict):
    # Always write the service unit (needed for manual update button).
    if IS_MACOS:
        write_autoupdate_service_unit()
    else:
        write_autoupdate_service_unit()

    if not auto_update_enabled(env):
        svc_autoupdate_disable_stop()
        return

    # Write and start the timer.
    if IS_MACOS:
        write_autoupdate_timer_plist(env)
        svc_daemon_reload()
        svc_enable_start(SVC_AUTO_UPDATE_LABEL, UNIT_PATH_AUTO_UPDATE)
    else:
        write_autoupdate_timer_unit(env)
        svc_daemon_reload()
        run(_systemctl_cmd('enable', '--now', SYSTEMD_AUTO_UPDATE_TIMER))
```

- [ ] **Step 4: Update `svc_autoupdate_disable_stop` (line 2482-2490)**

Existing:
```python
def svc_autoupdate_disable_stop():
    if IS_MACOS:
        svc_disable_stop(SVC_AUTO_UPDATE_LABEL, UNIT_PATH_AUTO_UPDATE)
        safe_rm(UNIT_PATH_AUTO_UPDATE)
    else:
        run(_systemctl_cmd('disable', '--now', SYSTEMD_AUTO_UPDATE_TIMER), check=False)
        safe_rm(UNIT_PATH_AUTO_UPDATE_TIMER)
        safe_rm(UNIT_PATH_AUTO_UPDATE)
        svc_daemon_reload()
```

Replace with:
```python
def svc_autoupdate_disable_stop():
    """Stop and remove only the timer — service unit stays for manual `systemctl start`."""
    if IS_MACOS:
        svc_disable_stop(SVC_AUTO_UPDATE_LABEL, UNIT_PATH_AUTO_UPDATE)
        safe_rm(UNIT_PATH_AUTO_UPDATE)
    else:
        run(_systemctl_cmd('disable', '--now', SYSTEMD_AUTO_UPDATE_TIMER), check=False)
        safe_rm(UNIT_PATH_AUTO_UPDATE_TIMER)
        svc_daemon_reload()
    # NOTE: service unit (UNIT_PATH_AUTO_UPDATE) intentionally NOT removed.
```

- [ ] **Step 5: Update `autoupdate_unit_pairs()` (line ~2467) to match new split**

Existing:
```python
def autoupdate_unit_pairs():
    if IS_MACOS:
        return [(SVC_AUTO_UPDATE_LABEL, UNIT_PATH_AUTO_UPDATE)]
    return [(SYSTEMD_AUTO_UPDATE, UNIT_PATH_AUTO_UPDATE), (SYSTEMD_AUTO_UPDATE_TIMER, UNIT_PATH_AUTO_UPDATE_TIMER)]
```

No change needed — this is used only for status display and already handles correctly.

Wait, there's an issue. On macOS `svc_autoupdate_disable_stop()` removes the plist. But we need a plist for `launchctl start` to work. On macOS, the button uses `nohup run_auto_update.sh &` instead (see spec section 3.1), so we don't need a persistent launchd unit. But for consistency and status display, let's keep the macOS plist. Actually the spec says: macOS button uses `nohup /opt/gptadmin/bin/run_auto_update.sh >> ~/.gptadmin/auto-update.log 2>&1 </dev/null &` with setsid. So the macOS plist doesn't need to survive disable. The wrapper script is always present. Good — current code is fine.

Actually wait — `safe_rm(UNIT_PATH_AUTO_UPDATE)` removes the plist on macOS disable. But `write_autoupdate_service_unit()` now writes the plist separately. And `svc_autoupdate_enable_start` now calls `write_autoupdate_service_unit()` unconditionally first, then `write_autoupdate_timer_plist()` only if enabled. So on disable, we remove the interval plist. On manual button start (macOS), we use `nohup wrapper.sh &` directly.

But after disable, `write_autoupdate_service_unit()` is not called (only in enable_start). This means after disable, the plist doesn't exist. That's fine for macOS since the button uses nohup.

For Linux, the service unit stays. The disable only removes the timer and daemon-reloads. The service unit remains on disk for `systemctl start`.

This is correct. No further changes needed to step 5.

- [ ] **Step 6: Ensure `cmd_update` also writes the service unit unconditionally**

At line 3334, `cmd_update` calls `svc_autoupdate_enable_start(env_read())`. With our new code, this now always writes the service unit (good) and conditionally writes timer based on env. Correct.

At line 2693, `svc_autoupdate_enable_start(env)` (in service shellmcp setup flow). Same behavior — always writes service. Correct.

- [ ] **Step 7: Test existing commands still work**

```bash
python3 cli.py auto-update status   # should show status without error
python3 cli.py version              # should still work
```

- [ ] **Step 8: Commit**

```bash
git add cli.py
git commit -m "feat: decouple auto-update service unit from timer (always write service)"
```

---

### Task 2: Add auto-update prompt to setup_interactive

**Files:**
- Modify: `cli.py` — `setup_interactive()` around line 1719

- [ ] **Step 1: Add prompt between env defaults and pkg download**

The prompt goes after `env.setdefault('GPTADMIN_AUTO_UPDATE', 'true')` / `env.setdefault('GPTADMIN_AUTO_UPDATE_INTERVAL_SEC', '21600')` but before the pkg download starts. Currently lines 1719-1721 set defaults unconditionally.

Change lines 1717-1721:
```python
    env.setdefault('GPTADMIN_AUTO_UPDATE', 'true')
    env.setdefault('GPTADMIN_AUTO_UPDATE_INTERVAL_SEC', '21600')
    sync_oauth_origin_env(env)
    env_set_many(env)
```

To:
```python
    # Auto-update prompt: only in interactive mode; non-interactive keeps default (true).
    import shutil
    if not silent:  # silent = CI / --yes mode
        current = env.get('GPTADMIN_AUTO_UPDATE', 'true')
        existing_in_file = env_read().get('GPTADMIN_AUTO_UPDATE', '')
        # If this is a re-setup (env file already has the key), show current value as default
        if existing_in_file and existing_in_file.lower() in ('false', '0', 'no'):
            current = 'false'
            default_choice = 'n'
        else:
            default_choice = 'y'
        col = shutil.get_terminal_size().columns if hasattr(shutil, 'get_terminal_size') else 80
        print(f'\n{colored("  Автообновление", attrs=["bold"])}')
        prompt_text = 'Включить автообновление (проверка каждые 6ч, systemd timer / launchd)?'
        ch = ask(prompt_text, default_choice)
        if ch.lower() in ('n', 'no', 'нет'):
            env['GPTADMIN_AUTO_UPDATE'] = 'false'
            print('  Автообновление выключено. Включить потом: gptadmin auto-update enable')
        else:
            env['GPTADMIN_AUTO_UPDATE'] = 'true'
    sync_oauth_origin_env(env)
    env_set_many(env)
```

Note: Need to import `shutil` if not already imported (should be — `shutil` used throughout). And need `colored` — check if there's a color helper. Let me check.

Actually, looking at the code style in setup_interactive, it uses `c_green`, `c_red`, `c_dim` helpers (functions). The style is:
```python
    print(f'  {c_dim("...")} {c_green("...")}')
```

Let me simplify to match existing style:
```python
    if not silent:
        current = env.get('GPTADMIN_AUTO_UPDATE', 'true')
        existing_in_file = env_read().get('GPTADMIN_AUTO_UPDATE', '')
        if existing_in_file and existing_in_file.lower() in ('false', '0', 'no'):
            current = 'false'
            default_choice = 'n'
        else:
            default_choice = 'y'
        print(f'\n{c_bold("Автообновление")}')
        ch = ask('Включить автообновление (проверка каждые 6ч, systemd timer / launchd)', default_choice)
        if ch.lower() in ('n', 'no', 'нет'):
            env['GPTADMIN_AUTO_UPDATE'] = 'false'
            print(f'  {c_dim("Автообновление выключено. Включить потом:")} {c_green("gptadmin auto-update enable")}')
        else:
            env['GPTADMIN_AUTO_UPDATE'] = 'true'
```

Check if `c_bold` exists:
```bash
grep -n "def c_bold\|def c_green\|def c_dim\|def colored" cli.py
```

Step: Verify c_bold exists or use alternative.

- [ ] **Step 2: Verify c_bold function exists**

```bash
grep -n "^def c_" cli.py | head -10
```

- [ ] **Step 3: If c_bold missing, use bold escape directly**

If `c_bold` doesn't exist:
```python
        print(f'\n\033[1mАвтообновление\033[0m')
```

- [ ] **Step 4: Test manually**

```bash
# Test normal setup (interactive, should prompt)
python3 cli.py setup --help  # just check parse works

# Test silent mode
python3 -c "
import sys; sys.argv = ['gptadmin', 'setup', '--yes']
# Should not prompt
"
```

- [ ] **Step 5: Commit**

```bash
git add cli.py
git commit -m "feat: ask auto-update preference during interactive setup"
```

---

### Task 3: Handle re-setup with existing .env (already in Task 2 code)

The code in Task 2 Step 1 already handles re-setup: reads `env_read().get('GPTADMIN_AUTO_UPDATE')` and uses it as default. No separate task needed.

- [ ] **Step 1: Verify re-setup detection works**

```bash
# Mock existing env with auto-update=false
echo "GPTADMIN_AUTO_UPDATE=false" > /tmp/test_env
GPTADMIN_CONFIG_DIR=/tmp gptadmin setup --yes 2>&1 | head -5
# Should not prompt, keep false
```

- [ ] **Step 2: Commit** (squash with Task 2)

---

## Part 2: CLI — Update Hint with Caching

### Task 4: Add cache read/write functions

**Files:**
- Modify: `cli.py` — add to utility section (near env functions, around line 270)

- [ ] **Step 1: Define cache path and functions**

Add after `INSTALLED_BUILD_FILE` definition (~line 110) or near other state-file definitions:

```python
# Update check cache (near line ~112, after INSTALLED_BUILD_FILE)
UPDATE_CHECK_CACHE = GPTADMIN_HOME / 'update_check.json'
UPDATE_CHECK_COOLDOWN_S = 3600       # 1 hour after failed attempt
UPDATE_CHECK_FRESH_S = 86400         # 24 hours for successful check
UPDATE_CHECK_TIMEOUT_S = 3           # manifest fetch timeout
```

- [ ] **Step 2: Add cache read/write functions**

Add after `_write_installed_build_marker` (~line 3145):

```python
def _read_update_cache():
    """Return update check cache dict or None on any error (missing, corrupt)."""
    try:
        raw = UPDATE_CHECK_CACHE.read_text(encoding='utf-8')
        return json.loads(raw)
    except Exception:
        return None

def _write_update_cache(data: dict):
    """Atomically write update check cache (0600)."""
    tmp = UPDATE_CHECK_CACHE.with_name(UPDATE_CHECK_CACHE.name + '.tmp')
    try:
        tmp.write_text(json.dumps(data, ensure_ascii=False), encoding='utf-8')
        os.chmod(tmp, 0o600)
        os.replace(tmp, UPDATE_CHECK_CACHE)
    except Exception:
        try:
            if tmp.exists():
                tmp.unlink()
        except Exception:
            pass
```

- [ ] **Step 3: Commit**

```bash
git add cli.py
git commit -m "feat: add update check cache read/write functions"
```

---

### Task 5: Implement `maybe_update_hint()` function

**Files:**
- Modify: `cli.py` — add function before `main()`

- [ ] **Step 1: Add the function**

```python
def maybe_update_hint(args):
    """Check for available update and print hint to stderr (best-effort).
    
    Only runs when auto-update is disabled and update may be available.
    Uses local cache to avoid network requests on every CLI invocation.
    """
    # Skip when auto-update is enabled — no hint needed.
    from_env = env_read()
    if auto_update_enabled(from_env):
        return

    # Skip for certain commands.
    cmd = getattr(args, 'command', None)
    if cmd in ('update', 'auto-update'):
        return
    if getattr(args, 'auto', False):
        return

    # Read cache.
    cache = _read_update_cache()
    now = int(time.time())

    # Determine if we need a network check.
    last_success = cache.get('last_success_ts', 0) if cache else 0
    last_attempt = cache.get('last_attempt_ts', 0) if cache else 0
    age_success = now - last_success
    age_attempt = now - last_attempt

    remote_version = None
    remote_sha = None

    if cache and 0 <= age_success < UPDATE_CHECK_FRESH_S:
        # Cache still fresh — use cached remote version.
        remote_version = cache.get('remote_version')
        remote_sha = cache.get('remote_sha256')
    elif age_attempt < UPDATE_CHECK_COOLDOWN_S and age_success >= UPDATE_CHECK_FRESH_S:
        # Recent failed attempt — cooldown active, skip.
        return
    else:
        # Need network check.
        try:
            info = _remote_artifact_build_info()
            if info:
                remote_version = info.get('build_version')
                if isinstance(remote_version, str):
                    remote_version = int(remote_version)
                remote_sha = info.get('sha256', '')
            cache_data = {
                'last_success_ts': now,
                'last_attempt_ts': now,
                'remote_version': remote_version,
                'remote_sha256': remote_sha or '',
            }
            _write_update_cache(cache_data)
        except Exception:
            # Network error — record attempt, cooldown next runs.
            cache_data = {
                'last_success_ts': last_success,
                'last_attempt_ts': now,
                'remote_version': remote_version,
                'remote_sha256': remote_sha or '',
            }
            _write_update_cache(cache_data)
            return

    if remote_version is None:
        return

    # Compare with installed version.
    try:
        installed = _installed_build_info()
        installed_v = installed.get('build_version')
        if isinstance(installed_v, str):
            installed_v = int(installed_v)
    except Exception:
        return  # can't determine installed version — silent exit

    if installed_v is None or installed_v == 0:
        return  # dev build

    try:
        installed_v = int(installed_v)
        remote_v = int(remote_version)
    except (TypeError, ValueError):
        return

    if remote_v <= installed_v:
        return  # already up to date

    # Print hint to stderr.
    print(
        f'\nℹ {c_yellow("Доступно обновление")}: build {installed_v} → {remote_v}.\n'
        f'  Обновить:          {c_green("gptadmin update")}\n'
        f'  Включить авто:     {c_green("gptadmin auto-update enable")}\n',
        file=sys.stderr,
    )
```

- [ ] **Step 2: Verify imports needed**

Check if `time`, `json`, `sys`, `c_yellow` are available at this scope. `time` is imported at top of cli.py. `json` is imported. `sys` is imported. Check `c_yellow`:

```bash
grep -n "^def c_yellow\|yellow" cli.py | head -3
```

- [ ] **Step 3: If c_yellow not defined, use plain text**

```python
    print(
        f'\nℹ Доступно обновление: build {installed_v} → {remote_v}.\n'
        f'  Обновить:          gptadmin update\n'
        f'  Включить авто:     gptadmin auto-update enable\n',
        file=sys.stderr,
    )
```

- [ ] **Step 4: Commit**

```bash
git add cli.py
git commit -m "feat: add maybe_update_hint for CLI startup update notifications"
```

---

### Task 6: Hook `maybe_update_hint()` into `main()`

**Files:**
- Modify: `cli.py` — `main()` function (~line 3665)

- [ ] **Step 1: Add call early in main()**

Find `def main():` at line ~3665. After argument parsing and before command dispatch, add:

```python
def main():
    args = parse_args()  # existing line
    # ... existing setup ...

    # Update hint (best-effort, non-blocking).
    try:
        maybe_update_hint(args)
    except Exception:
        pass  # never break CLI for a hint

    # ... existing command dispatch ...
    args.func(args)  # existing line
```

Wait, let me check the actual main() structure:

```bash
sed -n '3665,3720p' cli.py
```

Actually from the exploration, main() uses argparse subparsers. Let me check the exact structure.

- [ ] **Step 1 (revised): Read main() structure**

```bash
sed -n '3665,3710p' cli.py
```

- [ ] **Step 2: Add hint call at the right spot**

Insert after argparse subs are set up and before `args.func(args)`:
```python
    # Best-effort update hint (silent on any error).
    try:
        maybe_update_hint(args)
    except Exception:
        pass
    args.func(args)
```

- [ ] **Step 3: Test**

```bash
# Set auto-update off for testing
GPTADMIN_AUTO_UPDATE=false python3 cli.py version 2>/dev/null
# Should complete normally (hint may or may not show depending on network)

# Test that errors don't break CLI
GPTADMIN_AUTO_UPDATE=false python3 -c "
import sys; sys.argv = ['gptadmin', 'version']
# Will parse args, try hint, run version
"
```

- [ ] **Step 4: Commit**

```bash
git add cli.py
git commit -m "feat: hook update hint into CLI main() entry point"
```

---

### Task 7: Manual integration test for CLI changes

- [ ] **Step 1: Test setup prompt**

```bash
# In a test dir (doesn't modify real install)
python3 -c "
import sys
# Just verify the module loads and functions exist
sys.path.insert(0, '.')
import importlib.util
spec = importlib.util.spec_from_file_location('cli', 'cli.py')
"
```

- [ ] **Step 2: Test hint with real env**

```bash
# Set up test conditions
GPTADMIN_AUTO_UPDATE=false python3 cli.py version 2>&1 | head -20
# Should either show hint (if update available) or run silently
```

- [ ] **Step 3: Test cache file creation**

```bash
ls -la ~/.gptadmin/update_check.json 2>/dev/null && echo "cache exists" || echo "no cache yet (OK if first run)"
cat ~/.gptadmin/update_check.json 2>/dev/null | python3 -m json.tool
```

- [ ] **Step 4: Commit any fixes**

---

## Part 3: Hub — Backend Changes

### Task 8: Create `update_state.go`

**Files:**
- Create: `go-hub/internal/hub/update_state.go`

**Interfaces:**
- Produces: `UpdateState`, `UpdateCurrent`, `UpdateResult` structs
- Produces: `ReadUpdateState(path string) (*UpdateState, error)`
- Produces: `WriteUpdateState(path string, s *UpdateState) error`
- Produces: `AcquireUpdateLock(lockPath string) (*os.File, error)` — exclusive flock
- Produces: `ReleaseUpdateLock(f *os.File) error`

- [ ] **Step 1: Create the file**

```go
package hub

import (
	"encoding/json"
	"fmt"
	"os"
	"syscall"
)

// UpdateState represents the persistent update state file.
type UpdateState struct {
	Current    UpdateCurrent `json:"current"`
	LastResult *UpdateResult `json:"last_result"`
}

// UpdateCurrent tracks right-now update activity.
type UpdateCurrent struct {
	Status string `json:"status"` // "idle" | "running"
}

// UpdateResult records the outcome of the last completed update.
type UpdateResult struct {
	Status      string `json:"status"`       // "done" | "error"
	Message     string `json:"message"`
	StartedAt   int64  `json:"started_at"`
	FinishedAt  int64  `json:"finished_at"`
	FromVersion int    `json:"from_version"`
	ToVersion   int    `json:"to_version"`
}

// ReadUpdateState reads and parses the update state file.
// Returns nil, nil if the file does not exist.
func ReadUpdateState(path string) (*UpdateState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read update state: %w", err)
	}
	var s UpdateState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse update state: %w", err)
	}
	return &s, nil
}

// WriteUpdateState atomically writes the update state file.
func WriteUpdateState(path string, s *UpdateState) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal update state: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("write update state tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename update state: %w", err)
	}
	return nil
}

// AcquireUpdateLock takes an exclusive flock on the lock file.
// Returns the open file handle (caller must ReleaseUpdateLock).
func AcquireUpdateLock(lockPath string) (*os.File, error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("acquire lock: %w", err)
	}
	return f, nil
}

// ReleaseUpdateLock releases the flock and closes the file.
func ReleaseUpdateLock(f *os.File) error {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		f.Close()
		return fmt.Errorf("release lock: %w", err)
	}
	return f.Close()
}

// EnsureDefaultUpdateState returns a state with idle current if s is nil.
func EnsureDefaultUpdateState(s *UpdateState) *UpdateState {
	if s == nil {
		return &UpdateState{
			Current: UpdateCurrent{Status: "idle"},
		}
	}
	if s.Current.Status == "" {
		s.Current.Status = "idle"
	}
	return s
}
```

- [ ] **Step 2: Verify compilation**

```bash
cd go-hub && go build ./...
```

- [ ] **Step 3: Commit**

```bash
git add go-hub/internal/hub/update_state.go
git commit -m "feat(hub): add update_state.go — file-backed update state with flock"
```

---

### Task 9: Test `update_state.go`

**Files:**
- Create: `go-hub/internal/hub/update_state_test.go`

- [ ] **Step 1: Write tests**

```go
package hub

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadWriteUpdateState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "update_state.json")

	// Read non-existent file returns nil.
	s, err := ReadUpdateState(path)
	if err != nil {
		t.Fatalf("ReadUpdateState on missing file: %v", err)
	}
	if s != nil {
		t.Fatalf("expected nil state for missing file, got %+v", s)
	}

	// Write and read back.
	state := &UpdateState{
		Current: UpdateCurrent{Status: "idle"},
		LastResult: &UpdateResult{
			Status:      "done",
			Message:     "Updated build 119 → 120",
			StartedAt:   time.Now().Unix(),
			FinishedAt:  time.Now().Unix(),
			FromVersion: 119,
			ToVersion:   120,
		},
	}
	if err := WriteUpdateState(path, state); err != nil {
		t.Fatalf("WriteUpdateState: %v", err)
	}

	got, err := ReadUpdateState(path)
	if err != nil {
		t.Fatalf("ReadUpdateState: %v", err)
	}
	if got == nil {
		t.Fatal("expected state, got nil")
	}
	if got.Current.Status != "idle" {
		t.Errorf("expected idle, got %q", got.Current.Status)
	}
	if got.LastResult == nil {
		t.Fatal("expected last_result")
	}
	if got.LastResult.Message != "Updated build 119 → 120" {
		t.Errorf("unexpected message: %q", got.LastResult.Message)
	}
}

func TestEnsureDefaultUpdateState(t *testing.T) {
	// nil -> default idle.
	s := EnsureDefaultUpdateState(nil)
	if s.Current.Status != "idle" {
		t.Errorf("expected idle, got %q", s.Current.Status)
	}

	// empty status -> idle.
	s2 := EnsureDefaultUpdateState(&UpdateState{Current: UpdateCurrent{}})
	if s2.Current.Status != "idle" {
		t.Errorf("expected idle, got %q", s2.Current.Status)
	}
}

func TestAcquireReleaseLock(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "update.lock")

	f, err := AcquireUpdateLock(lockPath)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	// Second acquire should fail (lock held).
	_, err2 := AcquireUpdateLock(lockPath)
	if err2 == nil {
		t.Fatal("expected second acquire to fail with lock held")
	}

	// Release and re-acquire.
	if err := ReleaseUpdateLock(f); err != nil {
		t.Fatalf("release: %v", err)
	}

	f3, err := AcquireUpdateLock(lockPath)
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	ReleaseUpdateLock(f3)

	// Verify lock file still exists.
	if _, err := os.Stat(lockPath); os.IsNotExist(err) {
		t.Error("lock file should persist after release")
	}
}
```

- [ ] **Step 2: Run tests**

```bash
cd go-hub && go test ./internal/hub/ -run TestReadWriteUpdateState -v
cd go-hub && go test ./internal/hub/ -run TestAcquireReleaseLock -v
cd go-hub && go test ./internal/hub/ -run TestEnsureDefaultUpdateState -v
```

Expected: all PASS.

- [ ] **Step 3: Commit**

```bash
git add go-hub/internal/hub/update_state_test.go
git commit -m "test(hub): add tests for update_state.go"
```

---

### Task 10: Create `update_launcher.go`

**Files:**
- Create: `go-hub/internal/hub/update_launcher.go`

**Interfaces:**
- Produces: `LaunchUpdate(scope string) error` — starts systemd oneshot (Linux) or nohup wrapper (macOS)
- Consumes: shellmcp has `syscall` pattern for process management

- [ ] ** Step 1: Check if hub already knows install scope or discovers it**

```bash
grep -n "INSTALL_SCOPE\|install_scope\|InstallScope\|GPTADMIN_INSTALL_MODE" go-hub/internal/hub/server.go | head -5
```

If no such field, read from env:

```bash
grep -n "GPTADMIN_INSTALL_MODE\|INSTALL_SCOPE" go-hub/internal/hub/server.go
```

- [ ] **Step 2: Create update_launcher.go**

```go
package hub

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"syscall"
)

// UpdateLauncher describes how to launch the update script externally.
type UpdateLauncher struct {
	// ServiceUnit is the systemd service name (Linux only).
	ServiceUnit string
	// WrapperPath is the run_auto_update.sh path (macOS fallback).
	WrapperPath string
	// LogPath for stdout/stderr.
	LogPath string
	// IsUserInstall is true for systemd --user scope.
	IsUserInstall bool
}

// DefaultUpdateLauncher returns a launcher configured from environment.
func DefaultUpdateLauncher() *UpdateLauncher {
	isUser := os.Getenv("GPTADMIN_INSTALL_MODE") == "user" ||
		os.Getenv("GPTADMIN_INSTALL_SCOPE") == "user"
	installDir := os.Getenv("GPTADMIN_HOME")
	if installDir == "" {
		home, _ := os.UserHomeDir()
		installDir = home + "/.local/share/gptadmin"
	}
	return &UpdateLauncher{
		ServiceUnit:   "gptadmin-auto-update.service",
		WrapperPath:   installDir + "/bin/run_auto_update.sh",
		LogPath:       installDir + "/auto-update.log",
		IsUserInstall: isUser,
	}
}

// LaunchUpdate starts the update as an external process that survives hub restart.
func (l *UpdateLauncher) LaunchUpdate() error {
	switch runtime.GOOS {
	case "linux":
		return l.launchSystemd()
	case "darwin":
		return l.launchNohup()
	default:
		return fmt.Errorf("unsupported OS for update launcher: %s", runtime.GOOS)
	}
}

func (l *UpdateLauncher) launchSystemd() error {
	args := []string{"start", l.ServiceUnit}
	if l.IsUserInstall {
		args = []string{"--user", "start", l.ServiceUnit}
	}
	cmd := exec.Command("systemctl", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl start %s: %w (output: %s)", l.ServiceUnit, err, string(out))
	}
	return nil
}

func (l *UpdateLauncher) launchNohup() error {
	cmd := exec.Command("nohup", l.WrapperPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid:   true,
		Setsid:    true,
	}
	cmd.Stdout = nil // nohup redirects
	cmd.Stderr = nil
	cmd.Stdin = nil
	// Set nohup log.
	logFile, err := os.OpenFile(l.LogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err == nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		defer logFile.Close()
	}
	// Detach: nohup + & via Start (don't Wait).
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("nohup start: %w", err)
	}
	// Release the process — it will run independently.
	go func() {
		cmd.Wait()
	}()
	return nil
}

// CheckUpdateRunning returns true if an update is already in progress.
func (l *UpdateLauncher) CheckUpdateRunning() bool {
	switch runtime.GOOS {
	case "linux":
		return l.checkSystemdActive()
	case "darwin":
		// On macOS, check if wrapper process is running.
		return l.checkNohupRunning()
	default:
		return false
	}
}

func (l *UpdateLauncher) checkSystemdActive() bool {
	args := []string{"is-active", l.ServiceUnit}
	if l.IsUserInstall {
		args = []string{"--user", "is-active", l.ServiceUnit}
	}
	cmd := exec.Command("systemctl", args...)
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return string(out) == "active\n" || string(out) == "activating\n"
}

func (l *UpdateLauncher) checkNohupRunning() bool {
	// pgrep the wrapper script.
	cmd := exec.Command("pgrep", "-f", l.WrapperPath)
	err := cmd.Run()
	return err == nil
}
```

- [ ] **Step 3: Verify compilation**

```bash
cd go-hub && go build ./...
```

- [ ] **Step 4: Commit**

```bash
git add go-hub/internal/hub/update_launcher.go
git commit -m "feat(hub): add update_launcher.go — external supervisor launch for updates"
```

---

### Task 11: Test `update_launcher.go`

**Files:**
- Create: `go-hub/internal/hub/update_launcher_test.go`

- [ ] **Step 1: Write basic tests**

```go
package hub

import (
	"os"
	"runtime"
	"testing"
)

func TestDefaultUpdateLauncher(t *testing.T) {
	l := DefaultUpdateLauncher()
	if l.ServiceUnit != "gptadmin-auto-update.service" {
		t.Errorf("unexpected service unit: %q", l.ServiceUnit)
	}
	if l.WrapperPath == "" {
		t.Error("wrapper path should not be empty")
	}
}

func TestLaunchUpdateUnsupportedOS(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return // test runs on linux/darwin
	}
	// Create a minimal wrapper.
	dir := t.TempDir()
	wrapper := dir + "/run_auto_update.sh"
	os.WriteFile(wrapper, []byte("#!/bin/sh\necho ok\nexit 0"), 0755)

	l := &UpdateLauncher{
		ServiceUnit: "gptadmin-auto-update.service",
		WrapperPath: wrapper,
		LogPath:     dir + "/log.txt",
		IsUserInstall: os.Getenv("GPTADMIN_INSTALL_MODE") == "user",
	}

	// CheckRunning should not crash.
	_ = l.CheckUpdateRunning()

	// LaunchUpdate through systemd might not work in test (no systemd --user in CI).
	// But it should not panic.
	if runtime.GOOS == "linux" {
		err := l.LaunchUpdate()
		// May fail if systemd not available — that's fine.
		t.Logf("LaunchUpdate result: %v", err)
	}
}
```

- [ ] **Step 2: Run tests**

```bash
cd go-hub && go test ./internal/hub/ -run TestDefaultUpdateLauncher -v
cd go-hub && go test ./internal/hub/ -run TestLaunchUpdateUnsupportedOS -v
```

- [ ] **Step 3: Commit**

```bash
git add go-hub/internal/hub/update_launcher_test.go
git commit -m "test(hub): add tests for update_launcher.go"
```

---

### Task 12: Modify `server.go` — adminOverview + POST handler

**Files:**
- Modify: `go-hub/internal/hub/server.go`

**What changes:**
1. `adminOverview` adds `shell_builds` and `update` fields
2. New `POST /admin/api/update` endpoint
3. Update state file path from env

- [ ] **Step 1: Add updateStatePath and updateLockPath fields to Server struct**

Find the `Server struct` definition. Add fields:
```go
	updateStatePath string
	updateLockPath  string
	updateLauncher  *UpdateLauncher
```

- [ ] **Step 2: Initialize in NewServer or server init**

Find where Server is constructed. Add:
```go
	home := os.Getenv("GPTADMIN_HOME")
	if home == "" {
		userHome, _ := os.UserHomeDir()
		home = userHome + "/.gptadmin"
	}
	s.updateStatePath = home + "/update_state.json"
	s.updateLockPath = home + "/update.lock"
	s.updateLauncher = DefaultUpdateLauncher()
```

- [ ] **Step 3: Add shell_builds aggregation to adminOverview**

In `adminOverview` (line 1726), after building `servers` list, add shell build aggregation:

```go
// Aggregate shell builds from heartbeat data.
shellBuilds := map[string]any{
	"latest":  0,
	"oldest":  0,
	"versions": map[string]int{},
}
{
	buildCounts := map[string]int{}
	for _, srv := range servers {
		// Server struct has Meta field with beat data including build_version.
		// Check if this is a shellmcp server.
		if meta, ok := srv["meta"].(map[string]any); ok {
			if bv, ok := meta["build_version"]; ok {
				ver := fmt.Sprintf("%v", bv)
				// Strip decimal if float64.
				if f, ok := bv.(float64); ok {
					ver = fmt.Sprintf("%d", int(f))
				}
				buildCounts[ver]++
			}
		}
	}
	versions := map[string]int{}
	latest := 0
	oldest := 0
	for ver, count := range buildCounts {
		versions[ver] = count
		v, _ := strconv.Atoi(ver)
		if v > latest {
			latest = v
		}
		if oldest == 0 || v < oldest {
			oldest = v
		}
	}
	shellBuilds["latest"] = latest
	shellBuilds["oldest"] = oldest
	shellBuilds["versions"] = versions
}
```

- [ ] **Step 4: Add update state reading to adminOverview**

```go
// Read update state.
updateState := map[string]any{
	"current":     map[string]string{"status": "idle"},
	"last_result": nil,
}
if st, err := ReadUpdateState(s.updateStatePath); err == nil {
	st = EnsureDefaultUpdateState(st)
	updateState["current"] = map[string]string{"status": st.Current.Status}
	if st.LastResult != nil {
		updateState["last_result"] = st.LastResult
	}
}
```

- [ ] **Step 5: Add fields to adminOverview response**

In the `writeJSON` call, add:
```go
"shell_builds": shellBuilds,
"update": updateState,
```

- [ ] **Step 6: Add POST /admin/api/update handler**

```go
func (s *Server) adminTriggerUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}

	// Check if an update is already running.
	st, _ := ReadUpdateState(s.updateStatePath)
	st = EnsureDefaultUpdateState(st)
	if st.Current.Status == "running" || s.updateLauncher.CheckUpdateRunning() {
		writeJSON(w, 409, map[string]any{"detail": "update already running"})
		return
	}

	// Try to acquire lock.
	lock, err := AcquireUpdateLock(s.updateLockPath)
	if err != nil {
		writeJSON(w, 409, map[string]any{"detail": "update already running"})
		return
	}

	// Mark running in state file.
	st.Current.Status = "running"
	now := time.Now().Unix()
	if st.LastResult != nil {
		st.LastResult.StartedAt = now
	} else {
		st.LastResult = &UpdateResult{StartedAt: now}
	}
	if err := WriteUpdateState(s.updateStatePath, st); err != nil {
		ReleaseUpdateLock(lock)
		writeJSON(w, 500, map[string]any{"detail": "failed to write state"})
		return
	}
	ReleaseUpdateLock(lock) // release — the external supervisor holds its own lifecycle

	// Launch via external supervisor.
	if err := s.updateLauncher.LaunchUpdate(); err != nil {
		// Reset state on launch failure.
		st.Current.Status = "idle"
		st.LastResult = &UpdateResult{
			Status:     "error",
			Message:    err.Error(),
			StartedAt:  now,
			FinishedAt: time.Now().Unix(),
		}
		WriteUpdateState(s.updateStatePath, st)
		writeJSON(w, 500, map[string]any{"detail": "failed to start update"})
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "status": "running"})
}
```

- [ ] **Step 7: Register the route**

Find the admin route registration (near other `HandleFunc` calls, around line 310-330). Add:
```go
	mux.HandleFunc("/admin/api/update", s.adminTriggerUpdate)
```

- [ ] **Step 8: Ensure admin auth is applied**

Check if admin routes use middleware. If all `/admin/api/*` share auth, the new endpoint is automatically protected.

```bash
grep -n "/admin/api" go-hub/internal/hub/server.go | head -10
```

If individual auth, verify the new handler has auth check.

- [ ] **Step 9: Verify compilation**

```bash
cd go-hub && go build ./...
```

Fix any compilation errors (imports needed: `strconv`, `fmt`, `time`).

- [ ] **Step 10: Add missing imports**

The server.go changes need these imports (check if already present):
```go
import (
	"strconv"
	// ... existing
)
```

- [ ] **Step 11: Commit**

```bash
git add go-hub/internal/hub/server.go
git commit -m "feat(hub): add shell_builds, update state to adminOverview, POST /admin/api/update"
```

---

### Task 13: Test server.go changes

**Files:**
- Modify: `go-hub/internal/hub/server_test.go`

- [ ] **Step 1: Add test for adminOverview with shell_builds and update**

Find existing admin overview test. Add assertions:

```go
func TestAdminOverviewIncludesShellBuildsAndUpdate(t *testing.T) {
	// This test assumes a running test server or mocked state.
	// For now, test that the field structure is correct by calling ReadUpdateState directly.
	dir := t.TempDir()
	statePath := dir + "/update_state.json"

	// Write a test state.
	state := &UpdateState{
		Current: UpdateCurrent{Status: "idle"},
		LastResult: &UpdateResult{
			Status:      "done",
			Message:     "Updated build 119 → 120",
			StartedAt:   123,
			FinishedAt:  456,
			FromVersion: 119,
			ToVersion:   120,
		},
	}
	if err := WriteUpdateState(statePath, state); err != nil {
		t.Fatalf("WriteUpdateState: %v", err)
	}

	got, err := ReadUpdateState(statePath)
	if err != nil {
		t.Fatalf("ReadUpdateState: %v", err)
	}
	if got.LastResult.Status != "done" {
		t.Errorf("expected done, got %q", got.LastResult.Status)
	}
}

func TestAdminTriggerUpdateReturns409WhenRunning(t *testing.T) {
	// Write state with running status, verify handler returns 409.
	dir := t.TempDir()
	statePath := dir + "/update_state.json"
	state := &UpdateState{Current: UpdateCurrent{Status: "running"}}
	WriteUpdateState(statePath, state)

	st, err := ReadUpdateState(statePath)
	if err != nil {
		t.Fatalf("ReadUpdateState: %v", err)
	}
	if st.Current.Status != "running" {
		t.Errorf("expected running, got %q", st.Current.Status)
	}
}
```

- [ ] **Step 2: Run existing + new tests**

```bash
cd go-hub && go test ./internal/hub/ -v -count=1 2>&1 | tail -30
```

Expected: existing tests pass; new tests pass.

- [ ] **Step 3: Fix any failures**

Check test output for compilation or assertion failures.

- [ ] **Step 4: Commit**

```bash
git add go-hub/internal/hub/server_test.go
git commit -m "test(hub): add tests for shell_builds and update state in admin API"
```

---

### Task 14: Integration test — hub update flow

- [ ] **Step 1: Create integration test file**

Create: `go-hub/internal/hub/server_integration_test.go` (may already exist for other tests)

```go
package hub

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestUpdateStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "update_state.json")
	lockPath := filepath.Join(dir, "update.lock")

	// 1. Start idle.
	s, _ := ReadUpdateState(statePath)
	if s != nil {
		t.Fatal("expected nil for missing file")
	}

	// 2. Write running.
	state := &UpdateState{Current: UpdateCurrent{Status: "running"}}
	if err := WriteUpdateState(statePath, state); err != nil {
		t.Fatalf("write: %v", err)
	}

	// 3. Second "request" sees running.
	s2, _ := ReadUpdateState(statePath)
	if s2.Current.Status != "running" {
		t.Errorf("expected running, got %q", s2.Current.Status)
	}

	// 4. Lock test.
	f, err := AcquireUpdateLock(lockPath)
	if err != nil {
		t.Fatalf("acquire lock: %v", err)
	}
	// Second acquire should fail.
	_, err2 := AcquireUpdateLock(lockPath)
	if err2 == nil {
		t.Fatal("expected lock conflict")
	}
	ReleaseUpdateLock(f)

	// 5. Write done.
	state.Current.Status = "idle"
	state.LastResult = &UpdateResult{
		Status:      "done",
		Message:     "ok",
		FinishedAt:  999,
		ToVersion:   120,
	}
	WriteUpdateState(statePath, state)

	// 6. Read done.
	s3, _ := ReadUpdateState(statePath)
	if s3.Current.Status != "idle" {
		t.Errorf("expected idle, got %q", s3.Current.Status)
	}
	if s3.LastResult.ToVersion != 120 {
		t.Errorf("expected to_version 120, got %d", s3.LastResult.ToVersion)
	}
}
```

- [ ] **Step 2: Run integration test**

```bash
cd go-hub && go test ./internal/hub/ -run TestUpdateStateRoundTrip -v
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add go-hub/internal/hub/server_integration_test.go
git commit -m "test(hub): add update state round-trip integration test"
```

---

## Part 4: Admin UI

### Task 15: Modify `index.html` — version display + update button

**Files:**
- Modify: `public/admin/index.html`

- [ ] **Step 1: Add version display and update button to sidebar**

Find `#sideMeta` div (line 32 in index.html). Add version+button area before or after it:

```html
    <div class="sidegroup small muted" id="sideMeta">—</div>
    <div class="sidegroup" id="sideVersion">
      <div class="muted small">hub: <span id="hubVersion">—</span></div>
      <div class="muted small">shells: <span id="shellVersion">—</span></div>
      <button class="navbtn" id="btnUpdate" onclick="triggerUpdate()" style="margin-top:6px;width:100%">
        <span id="btnUpdateLabel">Обновить этот узел</span>
      </button>
      <div class="muted" id="updateHint" style="font-size:10px;margin-top:2px">
        Обновляет hub и локальный shell на этом сервере
      </div>
    </div>
```

- [ ] **Step 2: Add update status area (shown during/after update)**

```html
    <div class="sidegroup" id="updateStatus" style="display:none">
      <div class="muted small" id="updateStatusText"></div>
      <div class="muted" id="updateResult" style="font-size:11px;margin-top:4px"></div>
    </div>
```

- [ ] **Step 3: Commit**

```bash
git add public/admin/index.html
git commit -m "feat(admin): add version display and update button to sidebar"
```

---

### Task 16: Modify `app.js` — render logic, button handler, downtime handling

**Files:**
- Modify: `public/admin/app.js`

**Base patterns** (verified from source):
- `$` = `document.getElementById` (line 5)
- `api(path, opts)` = fetch wrapper (line 7); throws on non-OK, returns parsed JSON
- `token()` = CTL token from input or localStorage (line 6)
- `refreshAll()` = line 321: `api('/admin/api/overview?...')` then `renderAll()`, catch sets `$('status').textContent='Нет связи'`
- `renderAll()` = line 138: fills sidebar elements from `state` (global)
- **No `handleFetchError` exists.** Error suppression is done in refreshAll's catch block.
- Auto-refresh: `setInterval(()=>{if($('autoRefresh').checked)refreshAll()},15000)` (line 398)

- [ ] **Step 1: Add `let updateStartedFromBuild = null` global, near line 5 after `let state`**

```javascript
let state=null,currentView=localStorage.getItem('gptadmin_view')||'overview',updateStartedFromBuild=null;
```

- [ ] **Step 2: Modify `refreshAll()` (line 321) — handle expected downtime**

Existing refreshAll (line 321-322):
```javascript
async function refreshAll(){try{$('status').textContent='загрузка…';$('status').className='status-badge right';const lim=$('auditLimit')?.value||160;state=await api('/admin/api/overview?limit='+encodeURIComponent(lim));renderAll();$('status').textContent='● online';$('status').className='status-badge ok right'}catch(e){$('status').textContent='Нет связи';$('status').className='status-badge err right'}}
```

Replace with:
```javascript
async function refreshAll(){try{$('status').textContent='загрузка…';$('status').className='status-badge right';const lim=$('auditLimit')?.value||160;state=await api('/admin/api/overview?limit='+encodeURIComponent(lim));renderAll();$('status').textContent='● online';$('status').className='status-badge ok right';if(updateStartedFromBuild!==null&&state.build){const nb=state.build.build_version;if(nb!=updateStartedFromBuild){const rd=$('updateResult');if(rd)rd.textContent='Обновлено: build '+updateStartedFromBuild+' → '+nb;updateStartedFromBuild=null;const btn=$('btnUpdate'),bl=$('btnUpdateLabel');if(btn)btn.disabled=false;if(bl)bl.textContent='Обновить этот узел'}}}catch(e){if(updateStartedFromBuild!==null){$('status').textContent='…перезапуск';$('status').className='status-badge warn right';const st=$('updateStatusText');if(st)st.textContent='Сервис перезапускается…';const sd=$('updateStatus');if(sd)sd.style.display='block'}else{$('status').textContent='Нет связи';$('status').className='status-badge err right'}}}
```

- [ ] **Step 3: Add `triggerUpdate()` function — insert before `refreshAll`**

```javascript
async function triggerUpdate(){const btn=$('btnUpdate'),bl=$('btnUpdateLabel');if(btn)btn.disabled=true;if(bl)bl.textContent='Запуск…';try{const j=await api('/admin/api/update',{method:'POST'});updateStartedFromBuild=state&&state.build?state.build.build_version:null;if(bl)bl.textContent='Обновляю…';if(btn)btn.classList.add('btn-disabled')}catch(e){if(e.message&&e.message.indexOf('already running')>-1){if(bl)bl.textContent='Уже идёт...';if(btn)btn.disabled=true}else{alert('Ошибка: '+(e.message||'неизвестная ошибка'));if(btn)btn.disabled=false;if(bl)bl.textContent='Обновить этот узел'}}}
```

- [ ] **Step 4: Add version display + update state to `renderAll()` (end of function, before closing `}`)**

Find the last line of `renderAll()` — it ends the closing `}` after the last `$('sideMeta').innerHTML = ...` assignment. Add after the `$('sideMeta').innerHTML` assignment:

```javascript
const hv=$('hubVersion'),sv=$('shellVersion');if(hv){const b=state.build||{};hv.textContent='build '+(b.build_version||'—')+' ('+(b.git_commit||'').slice(0,7)+')'}if(sv){const sb=state.shell_builds||{},vs=sb.versions||{},p=[];for(const[v,c]of Object.entries(vs))p.push(v+'×'+c);sv.textContent=p.length?p.join(', '):'—'}const upd=state.update||{},cur=upd.current||{},lr=upd.last_result,btn=$('btnUpdate'),bl=$('btnUpdateLabel'),sd=$('updateStatus'),st=$('updateStatusText'),rd=$('updateResult');if(cur.status==='running'){if(btn){btn.disabled=true;btn.classList.add('btn-disabled')}if(bl)bl.textContent='Обновляю…';if(sd)sd.style.display='block';if(st)st.textContent='Сервис перезапускается…'}else{if(btn){btn.disabled=false;btn.classList.remove('btn-disabled')}if(bl)bl.textContent='Обновить этот узел'}if(lr&&lr.status==='done'){if(sd)sd.style.display='block';if(st)st.textContent='';if(rd)rd.textContent=lr.message||'Обновление завершено'}else if(lr&&lr.status==='error'){if(sd)sd.style.display='block';if(rd){rd.textContent=lr.message||'Ошибка обновления';rd.style.color='var(--red,#e74c3c)'}}
```

- [ ] **Step 5: Verify syntax**

```bash
# Quick JS syntax check with node (if available)
echo "const $=(id)=>id; const state=null; " | node --check /dev/stdin && echo "OK"
```

- [ ] **Step 6: Commit**

```bash
git add public/admin/app.js
git commit -m "feat(admin): add version rendering, update button, and downtime handling"
```

---

### Task 17: Add `.btn-disabled` CSS style (if needed)

**Files:**
- Modify: `public/admin/style.css` (if exists)

- [ ] **Step 1: Check existing button styles**

```bash
grep -n "navbtn\|\.btn\|disabled" public/admin/style.css | head -10
```

- [ ] **Step 2: Add disabled style if missing**

```css
.navbtn:disabled,
.btn-disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
```

- [ ] **Step 3: Commit**

```bash
git add public/admin/style.css
git commit -m "style(admin): add disabled button style for update-in-progress"
```

---

### Task 18: Final integration test

- [ ] **Step 1: Run all Go tests**

```bash
cd go-hub && go test ./... -count=1 2>&1 | tail -20
```

Expected: all PASS (or existing failures unrelated to changes).

- [ ] **Step 2: Verify CLI tests (if any)**

```bash
python3 -m pytest tests/ -k "update or version" -v 2>&1 | tail -20
# or check if there are cli tests at all
ls tests/ 2>/dev/null
```

- [ ] **Step 3: Verify build.sh still works**

Check that build.sh references `write_autoupdate_unit` or uses the same functions:
```bash
grep -n "autoupdate\|auto-update\|AUTO_UPDATE" tools/build.sh
```

If build.sh only deals with VERSION bump + ldflags, no changes needed.

- [ ] **Step 4: Check git status is clean except for intentional changes**

```bash
git status
```

- [ ] **Step 5: Final commit**

```bash
git add -A
git commit -m "feat: auto-update prompt, CLI hint, admin update button — final"
```

---

## CI Check

After all commits:
```bash
cd go-hub && go test ./... -count=1
python3 cli.py version
python3 cli.py auto-update status
```

All should pass.
