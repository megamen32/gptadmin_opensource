#!/usr/bin/env bash
# Sanitize the working tree so release bundles never carry personal
# infrastructure. Rules mirror .gitpublic/replace + .gitpublic/ignore (the
# same policy git-private2public applies to the public source mirror).
#
# Run AFTER all test steps and BEFORE tools/build.sh: tests must validate the
# private semantics on the raw tree, bundles must ship the sanitized one.
set -euo pipefail
cd "$(dirname "$0")/.."

echo "▸ Removing personal-only files"
rm -f GPTADMIN_PROMPT.md docs/old_prompt.txt \
	public/gptadmin_infrastructure_mcp_instructions.md \
	public/gptadmin_instructions_under_8000.md
rm -rf .gitpublic tests/fixtures/private .playwright-mcp

echo "▸ Applying text replacements"
while IFS= read -r file; do
	sed -i -E \
		-e 's/95\.[0-9]+\.[0-9]+\.[0-9]+/203.0.113.10/g' \
		-e 's/10\.[0-9]+\.[0-9]+\.[0-9]+/203.0.113.10/g' \
		-e 's/192\.168\.[0-9]+\.[0-9A-Za-z]+/203.0.113.10/g' \
		-e 's/172\.(1[6-9]|2[0-9]|3[01])\.[0-9]+\.[0-9]+/203.0.113.10/g' \
		-e 's/admin-server-100|admin-server-88|server01|server01|server01|server01|server01/server01/g' \
		-e 's/admin/admin/g' \
		-e 's/uplink|uplink/uplink/g' \
		-e 's/[0-9]{8,12}:[A-Za-z0-9_-]{30,}/<TELEGRAM_TOKEN>/g' \
		-e 's/(ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9_]{20,}|github_pat_[A-Za-z0-9_]{20,}/<GITHUB_TOKEN>/g' \
		-e 's/sk-(proj-)?[A-Za-z0-9_-]{20,}/<OPENAI_KEY>/g' \
		-e 's/AKIA[0-9A-Z]{16}/<AWS_EXAMPLE_KEY>/g' \
		-e 's/your-subdomain\.t\.gptadmin\.bezrabotnyi\.com/your-subdomain.t.became.bezrabotnyi.com/g' \
		-e 's/your-subdomain/your-subdomain/g' \
		-e 's|https://gptadmin\.bezrabotnyi\.com|https://became.bezrabotnyi.com|g' \
		-e 's/gptadmin\.bezrabotnyi\.com/became.bezrabotnyi.com/g' \
		-e 's/gptadminmcp\.bezrabotnyi\.com/became.bezrabotnyi.com/g' \
		-e 's/eyJhbGciOiJIUzI1NiJ9\.eyJzdWIiOiJhZG1pbiJ9\.[A-Za-z0-9_-]+/<TEST_JWT>/g' \
		"$file"
done < <(grep -rIlE 'your-subdomain|bezrabotnyi\.com|admin|95\.(16[0-9]|31)\.[0-9]+\.[0-9]+|192\.168\.|AKIA[0-9A-Z]{16}|eyJhbGciOiJIUzI1NiJ9' \
	--exclude-dir=.git --exclude-dir=.tmp --exclude-dir=node_modules \
	--exclude-dir=dist --exclude='*.webp' --exclude='*.png' --exclude='*.jpg' \
	--exclude='*.zip' --exclude='*.tar.gz' . 2>/dev/null || true)

echo "▸ Verifying no personal data survives"
leaks="$(grep -rInE 'your-subdomain|admin-server|95\.165\.165\.65|95\.31\.7\.115' \
	--exclude-dir=.git --exclude-dir=.tmp --exclude-dir=node_modules \
	--exclude=publicize_sanitize.sh \
	--exclude='*.webp' --exclude='*.png' --exclude='*.jpg' \
	--exclude='*.zip' --exclude='*.tar.gz' . 2>/dev/null || true)"
if [ -n "$leaks" ]; then
	echo "$leaks" >&2
	echo "ERROR: personal data survived sanitization" >&2
	exit 1
fi
echo "✓ Tree sanitized for public bundles"
