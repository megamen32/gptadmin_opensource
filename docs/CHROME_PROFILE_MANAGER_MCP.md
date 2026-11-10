# Chrome Extension / Profile Manager MCP

**Development status: limited Linux and macOS controlled-profile candidate.**
The controlled backend has passed isolated Linux Chrome 152 and macOS Chrome
157 tests: an MCP
client staged, installed, backed up, updated, reloaded and rolled back an
unpacked extension while Chrome reported its ID and version. The installed
GPTAdmin children on Ubuntu and macOS were also exercised through install,
backup, update, reload and rollback on separate controlled test profiles.
This is not proof of operation in a normal user profile.

The server launches the official `chrome-devtools-mcp` 1.12.0 over a pipe,
using a manager-owned Chrome user-data directory. Set
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
  them. The existing native consent helper can grant a debugging session, but
  is not an extension-management backend. Helper and policy modes are not
  implemented. Playwright Extension mode can attach to a selected profile,
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
