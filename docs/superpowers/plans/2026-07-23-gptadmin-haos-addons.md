# GPTAdmin HAOS Apps Distribution Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use test-driven development for the public distribution contract and verify the resulting repository before publishing.

**Goal:** Publish a clean Home Assistant Apps repository for GPTAdmin Hub Standby while keeping `/home/roomhacker/gptadmin` as the source of truth and keeping all instance credentials out of Git.

**Architecture:** The public repository contains `repository.yaml`, one `gptadmin_hub_standby` app, installation documentation, and a sync marker. The main repository owns runtime code and a packaging/export script; the public repository is refreshed from an explicit source commit. The app accepts instance configuration through Supervisor options, generates its failover bundle on startup, and uses a public ARM64 image reference or local build without embedding live state.

**Tech Stack:** Home Assistant Apps repository format, YAML, Bash, Python, Docker/Buildx, GitHub Actions, pytest, GitHub CLI.

## Global Constraints

- Never publish live tokens, passwords, private URLs, machine IDs, or `/etc` paths.
- Keep existing HAOS deployment working and do not rotate existing credentials.
- The public app targets `aarch64` first and must use a real Linux/ARM64 `frpc`.
- The main repository remains authoritative for runtime and packaging logic.
- Test-first: add a failing public-distribution contract test before implementation.
- Publish only under the authenticated GitHub owner after verifying identity; do not stage unrelated dirty files from the main repository.

---

### Task 1: Define and test the public distribution contract

**Files:**
- Create: `tests/test_haos_public_distribution.py`
- Create: `deploy/homeassistant/gptadmin_hub_standby/gptadmin_failover_config.py`

**Interfaces:**
- `build_failover_config(options: dict[str, Any]) -> dict[str, Any]`
- `build_failover_state(options: dict[str, Any]) -> dict[str, Any]`
- Missing required public failover fields must raise a clear `ValueError`; credentials are read only from runtime options and never returned in the generated state.

- [ ] Write tests asserting the generated config contains the configured primary health/public URLs, rank and endpoint metadata; generated state contains FRP routing metadata but no token or secret; public source files contain no placeholder tokens or machine-specific paths.
- [ ] Run `python3 -m pytest -q tests/test_haos_public_distribution.py` and confirm it fails because the generator and public repository contract do not exist.
- [ ] Implement the minimal generator and source-contract helpers.
- [ ] Rerun the focused test and then the full Python suite.

### Task 2: Make the add-on installable from a clean repository

**Files:**
- Modify: `deploy/homeassistant/gptadmin_hub_standby/run.sh`
- Modify: `deploy/homeassistant/gptadmin_hub_standby/Dockerfile`
- Create: `deploy/homeassistant/gptadmin_hub_standby/config.yaml`
- Create: `deploy/homeassistant/gptadmin_hub_standby/DOCS.md`
- Create: `deploy/homeassistant/gptadmin_hub_standby/CHANGELOG.md`

**Interfaces:**
- `run.sh` writes `/data/config/failover_config.json` and `/data/config/failover_state.json` from nested Supervisor `failover` options before starting Hub/runtime.
- The public config uses a generic image name and `aarch64` schema; all credentials are `password` options with no public defaults.

- [ ] Add tests for the clean config: root repository metadata, valid app version/slug/image/arch, required failover options, and no private defaults.
- [ ] Run the focused test and confirm the new clean files satisfy the contract.
- [ ] Implement the generic app bootstrap while preserving the current generated-instance path used by `scripts/deploy_haos_hub_standby.sh`.
- [ ] Run YAML parsing, shell syntax, Python compilation, and focused tests.

### Task 3: Add source export and release synchronization

**Files:**
- Create: `scripts/export_haos_app_repository.sh`
- Create: `.github/workflows/publish-haos-app.yml`
- Modify: `deploy/homeassistant/gptadmin_hub_standby/README.md`

**Interfaces:**
- `scripts/export_haos_app_repository.sh --output DIR --source-ref REF` creates a sanitized distribution tree from the current source and refuses to copy live state/config files.
- The workflow validates the export, builds/publishes the ARM64 image to GHCR, and updates the distribution repository only from an explicit tag/commit.

- [ ] Add tests for export refusal when protected config/state files are present and for required files in the output.
- [ ] Run the exporter in a temporary directory and inspect the manifest for secrets and instance-specific paths.
- [ ] Implement the exporter and workflow with pinned/explicit FRP version and ARM64 verification.
- [ ] Run `bash -n`, YAML validation, and the packaging dry-run.

### Task 4: Create, publish, and verify the GitHub Apps repository

**Files:**
- Create externally: `megamen32/gptadmin-haos-addons`
- Publish: generated repository tree and README installation button.

- [ ] Verify `gh` identity and target repository does not already exist.
- [ ] Create the public repository, push the sanitized initial tree, and enable the GHCR package workflow.
- [ ] Verify the remote `repository.yaml`, app `config.yaml`, default branch, and public visibility through GitHub API.
- [ ] Record the exact repository URL, commit, and remaining operator step (configure Supervisor password/options and set GHCR package visibility if required).

### Task 5: Close project records and acceptance

**Files:**
- Modify: `docs/WORKLOG.md`
- Modify: `docs/BUGS.md` only if a new publication bug is found.

- [ ] Replace the active worklog entry with factual delivery evidence.
- [ ] Run focused tests, full Python tests, Go package tests, exporter dry-run, and `git diff --check`.
- [ ] Confirm no credentials or private instance paths are present in the published tree.
- [ ] Leave unrelated pre-existing worktree changes untouched.
