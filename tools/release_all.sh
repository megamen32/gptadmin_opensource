#!/usr/bin/env bash
# One-command release: tests -> sanitized export -> full 16-bundle build ->
# public source snapshot -> public tag + GitHub release.
#
# Usage:
#   tools/release_all.sh                # release VERSION (repo file) with tests
#   tools/release_all.sh --skip-tests   # skip the pytest gate
#   GITHUB_PUBLIC_REPO=owner/repo tools/release_all.sh
#
# Everything runs on this machine: Go cross-compilation covers linux/windows/
# android; the Android x64 compiler comes from the local NDK (see
# docs below). No GitHub-hosted runners, no Actions billing.
#
# Requirements:
#   go, node/npm, python3+uv, gh (authed), git-filter-repo via
#   git-private2public, ANDROID_HOME with ndk;27.0.12077973 for android x64.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

TAG_NAME=""
SKIP_TESTS=0
PUBLIC_REPO="${GITHUB_PUBLIC_REPO:-megamen32/gptadmin_opensource}"
while [[ $# -gt 0 ]]; do
	case "$1" in
	--skip-tests) SKIP_TESTS=1 ;;
	v[0-9]*) TAG_NAME="$1" ;;
	*) echo "unknown arg: $1" >&2; exit 64 ;;
	esac
	shift
done

# --- 0. Preflight -----------------------------------------------------------
command -v gh >/dev/null || { echo "ERROR: gh CLI required" >&2; exit 1; }
command -v git-private2public >/dev/null || { echo "ERROR: git-private2public required" >&2; exit 1; }
# Product/build version remains the plain integer from VERSION. Public tags
# add a timestamp suffix only to make publication events collision-free.
BASE_VERSION="$(tr -d '[:space:]' < VERSION)"
[[ "$BASE_VERSION" =~ ^[0-9]+$ ]] || { echo "ERROR: VERSION must be a plain integer" >&2; exit 1; }
if [[ -z "$TAG_NAME" ]]; then
  TAG_NAME="v${BASE_VERSION}.$(date +%Y%m%d%H%M%S)"
fi
BUILD_TAG="v$BASE_VERSION"

# --- 1. Go tests on the raw tree --------------------------------------------
echo "=== Go tests (raw tree) ==="
( cd go-hub && go test ./... )
( cd go-shellmcp && go test ./... )


# --- 2. Sanitized export clone (uncommitted local WIP never ships) ----------
EXPORT_DIR="$ROOT/.tmp/release-$TAG_NAME"
rm -rf "$EXPORT_DIR"
git clone -q --no-hardlinks "$ROOT" "$EXPORT_DIR"
( cd "$EXPORT_DIR" && bash scripts/publicize_sanitize.sh )

# --- 2b. Test gate: sanitized tree may only fail where the raw tree fails ---
# Peers land environment-bound reds on main all the time, so the gate is a
# diff: run the suite before and after sanitization; any test that starts
# failing because of sanitization blocks the release. Tests that encode
# private literals by design are ignored on the sanitized pass.
SAN_IGNORES=(
	--ignore=tests/test_completion_matrix.py
	--ignore=tests/test_real_mcp_clients.py
	--ignore=tests/test_public_mirror.py
	--ignore=tests/test_haos_public_distribution.py
	--ignore=tests/test_frp_watchdog.py
)
SAN_DESELECTS=(
	--deselect tests/test_site_docs.py::test_site_docs_mirror_root_source_and_public_tree
	--deselect tests/test_docs_product_contract.py::test_custom_gpt_prompt_selects_actions_not_native_mcp
	--deselect tests/test_docs_product_contract.py::test_public_custom_gpt_instructions_stay_in_sync_with_prompt_source
)
collect_failures() {
	( cd "$1" && shift && uv run pytest tests/ -q --tb=no "$@" 2>&1 || true ) \
		| grep -E "^(FAILED|ERROR) " | sed 's/ - .*//' | sort || true
}
if [[ "$SKIP_TESTS" != "1" ]]; then
	echo "=== Test gate: raw baseline ==="
	raw_failures="$(collect_failures "$EXPORT_DIR")"
	echo "=== Test gate: sanitized tree ==="
	san_failures="$(collect_failures "$EXPORT_DIR" "${SAN_IGNORES[@]}" "${SAN_DESELECTS[@]}")"
	new_failures="$(comm -13 <(printf '%s\n' "$raw_failures" | grep -v '^$' | sort) <(printf '%s\n' "$san_failures" | grep -v '^$' | sort))"
	if [[ -n "$new_failures" ]]; then
		echo "$new_failures" >&2
		echo "ERROR: sanitization introduced test failures" >&2
		exit 1
	fi
	echo "✓ Sanitized tree matches the raw-tree test baseline"
else
	echo "=== Tests skipped (--skip-tests) ==="
fi

# --- 3. Tagged full build inside the sanitized export -----------------------
echo "=== Building 16-bundle matrix for $TAG_NAME (build $BASE_VERSION) ==="
printf '%s\n' "$BASE_VERSION" > "$EXPORT_DIR/VERSION"
release_commit="$(git -C "$EXPORT_DIR" rev-parse HEAD)"
# build.sh requires v<integer> to resolve to HEAD for a pinned release. Move
# that tag only inside this disposable sanitized export; private tags stay put.
git -C "$EXPORT_DIR" tag -f "$BUILD_TAG" "$release_commit" >/dev/null
( cd "$EXPORT_DIR" && \
  export ANDROID_HOME="${ANDROID_HOME:-/opt/android-sdk}" && \
  export ANDROID_X86_64_CC="$ANDROID_HOME/ndk/27.0.12077973/toolchains/llvm/prebuilt/linux-x86_64/bin/x86_64-linux-android24-clang" && \
  TAGGED_RELEASE=1 RELEASE_TAG="$BUILD_TAG" RELEASE_COMMIT="$release_commit" ./tools/build.sh )
ASSET_DIR="$EXPORT_DIR/build"
[[ -f "$ASSET_DIR/gptadmin-checksums.txt" ]] || { echo "ERROR: build produced no checksums" >&2; exit 1; }

# --- 4. Public source snapshot (git-private2public) -------------------------
echo "=== Publishing sanitized source snapshot ==="
RUNTIME_GITPUBLIC="$ROOT/.tmp/gitpublic-$TAG_NAME"
rm -rf "$RUNTIME_GITPUBLIC"
mkdir -p "$RUNTIME_GITPUBLIC"
cp -a "$ROOT/.gitpublic/." "$RUNTIME_GITPUBLIC/"
python3 - "$RUNTIME_GITPUBLIC/config" "$ROOT" <<'PYCFG'
from pathlib import Path
import sys
p = Path(sys.argv[1])
root = sys.argv[2]
lines = []
seen = False
for raw in p.read_text().splitlines():
    if raw.strip().startswith("source") and "=" in raw:
        lines.append(f"source = {root}")
        seen = True
    else:
        lines.append(raw)
if not seen:
    lines.append(f"source = {root}")
p.write_text("\n".join(lines) + "\n")
PYCFG
( cd "$ROOT" && git-private2public publish -c "$RUNTIME_GITPUBLIC" )
public_sha="$(git ls-remote "git@github.com:$PUBLIC_REPO.git" refs/heads/main | awk '{print $1}')"
[[ -n "$public_sha" ]] || { echo "ERROR: public main ref missing after publish" >&2; exit 1; }

# The private/local VERSION lane (1000+) is intentionally independent from the
# timestamped public release id. Stamp the public snapshot with the actual
# release id and install the one public self-hosted workflow that needs a
# short-lived GITHUB_TOKEN with packages:write for GHCR. Private Actions stay
# disabled and no GitHub-hosted runner is used.
PUBLIC_DIR="$ROOT/.tmp/public-$TAG_NAME"
rm -rf "$PUBLIC_DIR"
git clone -q --no-hardlinks "git@github.com:$PUBLIC_REPO.git" "$PUBLIC_DIR"
(
  cd "$PUBLIC_DIR"
  rm -rf .github/workflows
  mkdir -p .github/workflows
  cp "$ROOT/.github/workflows/publish-haos-addon.yml" .github/workflows/publish-haos-addon.yml
  printf '%s\n' "$BASE_VERSION" > VERSION
  git config user.name "GPTAdmin local release"
  git config user.email "gptadmin@bezrabotnyi.com"
  git add VERSION .github/workflows/publish-haos-addon.yml
  if ! git diff --cached --quiet; then
    git commit -m "release: prepare public $TAG_NAME"
    git push origin HEAD:main
  fi
)
public_sha="$(git -C "$PUBLIC_DIR" rev-parse HEAD)"
remote_public_sha="$(git ls-remote "git@github.com:$PUBLIC_REPO.git" refs/heads/main | awk '{print $1}')"
[[ "$remote_public_sha" == "$public_sha" ]] || { echo "ERROR: public main verification failed" >&2; exit 1; }
public_version="$(git -C "$PUBLIC_DIR" show "$public_sha:VERSION" | tr -d '[:space:]')"
[[ "$public_version" == "$BASE_VERSION" ]] || { echo "ERROR: public VERSION mismatch: $public_version != $BASE_VERSION" >&2; exit 1; }

# --- 5. Public tag + GitHub release with all assets -------------------------
echo "=== Publishing $TAG_NAME to $PUBLIC_REPO ==="
(
  cd "$PUBLIC_DIR"
  git tag "$TAG_NAME" "$public_sha"
  git push origin "$TAG_NAME"
)
release_assets=()
while IFS= read -r asset_name; do
	asset="$ASSET_DIR/$asset_name"
	[[ -f "$asset" ]] && release_assets+=("$asset")
done < <(python3 -c 'import json, sys; print(*[a["file"] for a in json.load(open(sys.argv[1]))["artifacts"]], sep="\n")' "$ASSET_DIR/gptadmin-release-matrix.json")
release_assets+=("$ASSET_DIR/gptadmin-checksums.txt" "$ASSET_DIR/gptadmin-release-matrix.json")
gh release create "$TAG_NAME" --repo "$PUBLIC_REPO" --latest \
	--title "$TAG_NAME" \
	--notes "Sanitized local release of build ${BASE_VERSION} from commit ${release_commit}." \
	"${release_assets[@]}"

echo "✓ Released $TAG_NAME: https://github.com/$PUBLIC_REPO/releases/tag/$TAG_NAME"
