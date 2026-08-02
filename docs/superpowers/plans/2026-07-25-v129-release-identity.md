# v129 Release Identity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use test-driven-development. Steps use checkbox syntax.

**Goal:** Publish and deploy exact v129 Hub artifact with matching tag, VERSION, manifest, SBOM and runtime identity, restoring /connect.json.

**Architecture:** Tagged builds use committed VERSION without increment and reject tag/version mismatch. Tag CI passes this mode. The deploy uses only the verified v129 Hub artifact and preserves configuration, Tunnel, server-88 and the existing rollback snapshot.

**Tech Stack:** Bash, GitHub Actions YAML, pytest, Go Hub, systemd.

## Global Constraints

- Candidate descends from b00795b14c64703d7b44abb04cf2677e4ae31790 and includes the current /connect.json handler.
- Preserve current dirty .vscode/settings.json, AGENTS.md, CLAUDE.md, docs/BUGS.md, docs/WORKLOG.md and the unrelated secret-ingress plan.
- Do not use mutable latest assets; do not touch Tunnel, HAOS or server-88.
- Never print credentials or environment-file values.

---

### Task 1: Test and implement tagged-build identity

**Files:**

- Modify: tests/test_release_workflow_contract.py
- Modify: tools/build.sh:117-180
- Modify: .github/workflows/build-and-sync.yml:95-99

**Interfaces:**

- Consumes: TAGGED_RELEASE=1, RELEASE_TAG=v<N>, tracked VERSION.
- Produces: a tagged build that preserves VERSION and fails unless RELEASE_TAG equals v$VERSION.

- [ ] **Step 1: Write the failing workflow regression**

```python
def test_tagged_release_build_preserves_the_tagged_version() -> None:
    workflow = yaml.safe_load(WORKFLOW.read_text(encoding="utf-8"))
    steps = workflow["jobs"]["build-and-release"]["steps"]
    build_step = next(step for step in steps if step.get("name") == "Build binaries")
    assert "TAGGED_RELEASE=1" in build_step["run"]
    assert "RELEASE_TAG=\${GITHUB_REF_NAME}" in build_step["run"]
```

- [ ] **Step 2: Prove RED**

Run: `python3 -m pytest tests/test_release_workflow_contract.py::test_tagged_release_build_preserves_the_tagged_version -q`

Expected: FAIL because tag CI currently calls `./tools/build.sh` without a non-bumping mode.

- [ ] **Step 3: Implement the minimal contract**

Refactor the common metadata-writing portion of `build_version()` into `write_build_info()`. Add a tagged branch that reads the numeric VERSION, validates `RELEASE_TAG == v$BUILD_VERSION`, then writes metadata without changing VERSION. Keep current incrementing behavior for developer builds.

- [ ] **Step 4: Wire tag CI**

```yaml
if [[ "${GITHUB_REF}" == refs/tags/v* ]]; then
  TAGGED_RELEASE=1 RELEASE_TAG="${GITHUB_REF_NAME}" ./tools/build.sh
else
  ./tools/build.sh
fi
```

- [ ] **Step 5: Prove GREEN**

Run: `python3 -m pytest tests/test_release_workflow_contract.py::test_tagged_release_build_preserves_the_tagged_version -q`

Expected: PASS.

### Task 2: Exercise tagged-build behavior

**Files:**

- Create: tests/test_tagged_release_build.py
- Modify: tools/build.sh only if Task 1 leaves a behavioral gap.

**Interfaces:**

- Consumes: a numeric VERSION, TAGGED_RELEASE=1 and RELEASE_TAG.
- Produces: unchanged VERSION, generated metadata with the same version, and explicit mismatch failure.

- [ ] **Step 1: Write failing behavior tests**

```python
def test_tagged_build_keeps_version_and_generates_matching_metadata(tmp_path: Path) -> None:
    repo = make_minimal_build_repo(tmp_path, version="129")
    completed = run_build(repo, tagged=True, tag="v129")
    assert completed.returncode == 0, completed.stderr
    assert (repo / "VERSION").read_text(encoding="utf-8") == "129\n"
    assert "BUILD_VERSION = 129" in (repo / "client/gptadmin_build_info.py").read_text(encoding="utf-8")

def test_tagged_build_rejects_tag_version_mismatch(tmp_path: Path) -> None:
    completed = run_build(make_minimal_build_repo(tmp_path, version="129"), tagged=True, tag="v130")
    assert completed.returncode != 0
    assert "RELEASE_TAG must equal v129" in completed.stderr
```

- [ ] **Step 2: Prove RED**

Run: `python3 -m pytest tests/test_tagged_release_build.py -q`

Expected: FAIL before the tagged mode exists.

- [ ] **Step 3: Implement only minimal fixture and command helpers**

The fixture must run the real copied build script through its metadata-only path. It must not mock the version decision or access a network.

- [ ] **Step 4: Prove GREEN**

Run: `python3 -m pytest tests/test_tagged_release_build.py tests/test_release_workflow_contract.py -q`

Expected: PASS.

### Task 3: Cut and verify v129

**Files:**

- Modify: VERSION
- Modify: docs/WORKLOG.md
- Modify: docs/BUGS.md

**Interfaces:**

- Consumes: green Tasks 1-2 and a clean release candidate.
- Produces: one Release build 129 commit and immutable tag v129 whose artifacts all identify 129 and the release commit.

- [ ] **Step 1: Set VERSION to 129 and commit only the release slice**

```bash
git add VERSION tools/build.sh .github/workflows/build-and-sync.yml tests/test_release_workflow_contract.py tests/test_tagged_release_build.py docs/BUGS.md docs/WORKLOG.md
git commit -m "Release build 129"
```

- [ ] **Step 2: Run the release-boundary ladder once**

```bash
cd go-hub && go test ./... && go test -race ./... && go vet ./...
cd ../go-shellmcp && go test ./...
cd ../go-proxyrelay && go test ./...
cd .. && python3 -m pytest tests/ --ignore=tests/e2e
python3 -m pytest tests/test_completion_matrix.py -q
cd admin-ui && npm ci && npm test && npm run lint && npm run build -- --base=/admin/
cd .. && TAGGED_RELEASE=1 RELEASE_TAG=v129 CLEAN=1 FORCE=1 ./tools/build.sh
python3 tools/verify_release_manifest.py verify --root . --manifest build/manifest.json
python3 tools/verify_installer_links.py --installer deploy/install.sh --target linux/amd64 --target darwin/arm64 --android --json
```

- [ ] **Step 3: Push only after identity is exact**

Push the release commit to main. Require auto-tag to create only v129, and tag CI to use TAGGED_RELEASE=1 RELEASE_TAG=v129.

### Task 4: Canary deploy the exact v129 Hub artifact

**Files:**

- Modify: docs/WORKLOG.md
- Modify: docs/BUGS.md
- Create: trash/logs/v129-hub-canary-20260725.md

**Interfaces:**

- Consumes: exact-tag checksum and /var/backups/gptadmin/primary-recovery-20260725T151500Z.
- Produces: Hub-only v129 runtime or an atomic rollback to fdca78d.

- [ ] **Step 1: Verify artifact identity and remote baseline**

Require tag, VERSION, manifest, SBOM and Hub /version to agree on 129; verify the rollback manifest and no active deployment process.

- [ ] **Step 2: Canary exact artifact**

Run the Hub artifact with disposable port/state. Require health, version, /connect.json fields, OAuth discovery and safe demo MCP before replacement.

- [ ] **Step 3: Atomic Hub-only swap**

Validate checksum in a temporary remote path, atomically replace only the Hub binary, and start gptadmin-hub.service once. Do not edit config, unit, nginx, Tunnel, HAOS or server-88.

- [ ] **Step 4: Live acceptance and rollback**

Require loopback then public health/version/connect/OAuth, browser login-refresh-overview and harmless MCP. On any regression restore the backed-up binary atomically and prove prior health/admin.

- [ ] **Step 5: Record completion only after delayed stability**

Observe through the real delayed-stop window with cheap health/discovery/auth probes. Keep Tunnel and server-88 open unless separately proven.
