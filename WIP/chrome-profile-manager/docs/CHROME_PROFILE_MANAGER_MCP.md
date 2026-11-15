# Chrome Extension / Profile Manager MCP

## Separate source installation and GPTAdmin registration

On Ubuntu or macOS, use the same OS user that runs the GPTAdmin node and can
access the intended Chrome profile. From a checkout containing this MCP, with
Python 3, venv/pip, and access to a Python package index:

```sh
python3 install_chrome_profile_manager.py \
  --prefix "$HOME/.local/lib/chrome-profile-manager-1" \
  --state-dir "$HOME/.local/state/chrome-profile-manager" > registration.json
```

The prefix must be a new absolute directory; installation refuses to overwrite
it. A failed install cleans up the prefix it created, but retains the state
directory; an uncatchable termination can leave a partial prefix. Use a new
versioned prefix for an update. The installer creates the state directory as
mode 0700, or requires an existing non-symlink directory owned by the current
user with mode 0700; it does not change an existing directory's permissions.
Keep this directory private after installation. The output is the complete
arguments object for GPTAdmin's `mcp_manage` on the owning node: submit it
as `action: upsert`, then verify with `mcp_tools` and `mcp_call`.
Installation alone does not register or start the child. The venv includes
`websocket-client==1.8.0`, checked against its wheel hash; the bridge and its
extension are copied into the same permanent prefix. Run its provisioner from
that installed directory if separately setting up a profile bridge; it changes
the selected user's native messaging configuration and requires explicit
profile setup. Do not remove a prefix still referenced by a native host.

The controlled-profile backend additionally requires Node and a separate
installed `chrome-devtools-mcp` 1.12.0 package; pass the absolute entry path
with `--devtools-entry`. It is not bundled or installed by this command.
Without it, the child still starts but reports that backend unavailable. The
source install does not configure Chrome, create a profile, or install an
extension. See the platform and capability limitations below.

The ShellMCP source in this branch rejects scoped stdio sessions because its
ownership-safe process lifecycle is unavailable. Unscoped child calls are the
verified route for this test. Scoped child environment construction is designed
to retain only a restricted baseline including
`HOME` and `PATH`, but not `XDG_CONFIG_HOME` or `CHROME_PROFILE_ROOTS`.
For non-default profile or bridge locations, add the required absolute values
to the registration's `config.env` before upsert. Ensure the node's PATH
contains Node when using the controlled backend; set `PATH` explicitly in
`config.env` if the service environment does not provide it. Unscoped
sessions use the parent environment. The source-install test builds a real
ShellMCP and therefore also requires Go 1.26 or newer and its module
dependencies; run it with `python3 -m pytest -q
tests/test_chrome_profile_manager_source_install.py`.

**Development status: limited Linux and macOS controlled-profile candidate.**
The controlled backend has passed isolated Linux Chrome 152 and macOS Chrome
157 tests: an MCP
client staged, installed, backed up, updated, reloaded and rolled back an
unpacked extension while Chrome reported its ID and version. The installed
GPTAdmin children on Ubuntu and macOS were also exercised through install,
backup, update, reload and rollback on separate controlled test profiles.
This is not proof of operation in a normal user profile.

The server launches a temporary derived copy of `chrome-devtools-mcp` 1.12.0
over a pipe, using a manager-owned Chrome user-data directory. The copy removes
Puppeteer's reuse of stale extension metadata: each listing uses fresh CDP
values. The original bundled file must match the pinned SHA-256; mismatches
fail closed, and the original package is not modified. Set
`CHROME_DEVTOOLS_MCP_ENTRY` to the installed package's
`build/src/bin/chrome-devtools-mcp.js` and `CHROME_PROFILE_MANAGER_STATE` to
a private manager-owned directory. Put unpacked source artifacts under that
directory's `artifacts/` subdirectory; stage requests accept relative paths
only. Staging checks content hash, the manifest version and the extension ID
derived from its public `key`. It rejects symbolic links and hard-linked
files. Backup contains the extension artifact, not Chrome profile data or
extension storage.

The manifest must contain a stable public key. An extension without that key
is not currently accepted. Updates require a different version so that a
loaded-version readback can distinguish the two artifacts.

The profile identifier and expected version guard each change; the manager
serializes its own changes per profile. Failed or ambiguous mutations are
not automatically repeated: readback and a pending record retain uncertainty.
An ambiguous reload cannot be proved solely by listing the loaded extension.
There is no background browser polling.

## Boundaries still under implementation

- Ordinary named profiles are discovered from the `Local State` name map and
  reported as `unmanaged_unavailable`; this component does not restart or copy
  them. An experimental profile extension bridge provides list/get/enable/disable
  in isolated Linux and macOS tests, but initial profile binding, persistence
  in ordinary profiles and installed GPTAdmin use remain unverified. Linux and macOS QA
  installed the bridge via Chrome's own folder chooser and verified its
  automatic reconnection, independent Chrome readback and a call from a newly
  started test GPTAdmin shell after restarting the disposable browser. These
  tests use an isolated profile and test-only pairing adjustment after restart. See
  [bridge details](../chrome_profile_manager_bridge/README.md). An experimental
  `profile_helper_reload` is available when the paired browser already exposes
  a loopback debugging endpoint in its own `DevToolsActivePort` file. It
  validates the bridge, profile name and root, checks the current version,
  invokes Chrome's profile-scoped reload, and reads the loaded version through
  the bridge. List/get/enable/disable also require this binding check. The
  experimental `profile_helper_stage` validates the same profile binding
  before staging a hash-, ID- and version-checked artifact. Stages are shared
  immutable content, not reservations to that profile, and staging does not
  install the extension. `profile_helper_backup` snapshots a verified enabled
  unpacked extension with a stable manifest key. It excludes profile and
  extension storage, rejects paths overlapping discovered browser roots,
  takes two matching copies and checks the loaded version again. The version
  checks and manager lock are not atomic against external changes.
  `profile_helper_prepare` creates a profile-owned directory but does not
  install it. Once installed from that directory, `profile_helper_update`
  creates a backup and reloads the new artifact; `profile_helper_rollback`
  restores a matching backup. `profile_helper_reconcile` inspects an uncertain
  operation without repeating the reload. The update/rollback path was
  exercised through test GPTAdmin and disposable Chrome profiles on Linux
  and macOS; the test used CDP to bootstrap the unpacked extension, which does
  not prove permanent installation. Normal profiles are not yet verified. It requires
  `websocket-client` 1.8.0 and enabled Chrome developer mode. Failed or timed
  out reloads may have taken effect; inspect state before retry. Profile context
  creation may fail when another profile is active. Automatic first installation
  into an ordinary profile is not implemented. The existing native
  consent helper can grant a debugging session but is not an extension backend.
  Policy mode is not implemented. Playwright Extension mode can attach to a selected profile,
  but cannot debug `chrome://` pages and therefore does not provide this
  component with a verified extension-installation route. Chrome enterprise
  force-installation has device-management and policy-scope restrictions.
- Multiple extensions per controlled profile have been exercised in isolated
  Chrome. Ambiguous extension listings fail closed; normal profiles remain
  unavailable.
- Linux and macOS controlled profiles have been tested. Windows lifecycle
  operations fail closed because their file-locking and safe-copy implementation
  is not yet verified. Chromium variants require lifecycle verification.
- The exposed stdio protocol currently uses the legacy MCP handshake for
  `2025-06-18` and `2025-11-25`; it does not claim the modern `2026-07-28`
  protocol or Tasks/subscriptions extensions.

Chrome 152 was observed allowing extension operations over a WebSocket to an
already-running isolated browser. A later Chromium change removed the earlier
pipe and unsafe-flag requirement where WebSockets are permitted. This does not
establish safe named-profile targeting in an ordinary multi-profile browser:
the extension methods do not take a profile or browser-context argument. See
[the Chromium change](https://chromium.googlesource.com/chromium/src/+/dddf05b0e69f88da95c3d6d4698521b775f20c2a)
and [Chrome's extension-tool documentation](https://developer.chrome.com/docs/devtools/agents/extensions).

Source metadata registration is not an installed executable or consumer
acceptance. Never copy a normal profile or expose cookies, tokens, storage or
page contents as a workaround.

To run the optional isolated Chrome integration tests, point the environment
variable above at the installed package and set `CHROME_MANAGER_REAL_TEST=1`
when invoking `python3 -m pytest tests/test_chrome_profile_manager_mcp.py`.
The tests create disposable browser profiles; they do not test an installed
GPTAdmin child or an existing user profile.
