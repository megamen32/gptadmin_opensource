# tests/mac — real-launchd verification harness

This directory hosts harness code that talks to a **real** macOS `launchd`.

Run `launchd_verify.py` on a Mac (local machine or a `macos-latest` GitHub
Actions runner). The harness is fully isolated via `GPTADMIN_SERVICE_SUFFIX`
(`.macverify`) and a fresh `tempfile.mkdtemp(prefix='gptadmin_macverify_')`
for HOME, so it never touches the user's real `~/Library/LaunchAgents`.
It auto-skips on non-Darwin hosts — safe to include in the Linux test suite.