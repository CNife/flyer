#!/usr/bin/env bash
# Tests scripts/release-policy.sh against throwaway git repositories.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

REPO="$TMP/repo"
mkdir -p "$REPO/npm/test" "$REPO/internal" "$REPO/.github/workflows" "$REPO/docs" "$REPO/scripts" "$REPO/test/e2e"
cd "$REPO"
git init -q
git config user.email "test@example.com"
git config user.name "release policy test"
git config commit.gpgsign false

printf '# test repo\n' > README.md
printf '{"name":"test","version":"0.3.4"}\n' > npm/package.json
printf 'package app\n' > internal/app.go
printf 'package app\n' > internal/app_test.go
printf 'package e2e\n' > test/e2e/e2e_test.go
printf '{"scripts":{"test":"true"}}\n' > npm/test/install.test.js
printf 'name: ci\n' > .github/workflows/ci.yml
printf '# guide\n' > docs/guide.md
printf '#!/usr/bin/env bash\ntrue\n' > scripts/tool.sh
chmod +x scripts/tool.sh
git add -A
git commit -q -m "base"
BASE="$(git rev-parse HEAD)"

failures=0
policy_output=""
policy_status=0

reset_repo() {
  git reset -q --hard "$BASE"
  git clean -qfd
}

commit() {
  git add -A
  git commit -q -m "$1"
}

run_policy() {
  local base_ref="${1:-$BASE}"
  local labels="${2:-}"
  set +e
  policy_output="$(BASE_REF="$base_ref" PR_LABELS="$labels" bash "$ROOT/scripts/release-policy.sh" 2>&1)"
  policy_status=$?
  set -e
}

expect() {
  local want="$1"
  local name="$2"
  if [ "$policy_status" -ne "$want" ]; then
    printf 'FAIL %s: exit %d, want %d\n%s\n' "$name" "$policy_status" "$want" "$policy_output" >&2
    failures=$((failures + 1))
    return
  fi
  printf 'ok %s\n' "$name"
}

expect_output() {
  local pattern="$1"
  local name="$2"
  if ! grep -Fq "$pattern" <<<"$policy_output"; then
    printf 'FAIL %s: output missing %q\n%s\n' "$name" "$pattern" "$policy_output" >&2
    failures=$((failures + 1))
    return
  fi
}

# Docs/CI/tooling-only changes are allowed without a version bump.
reset_repo
printf '\nmore docs\n' >> README.md
printf '\n# trailing\n' >> .github/workflows/ci.yml
commit "docs only"
run_policy
expect 0 "docs-only"
expect_output "docs/CI/tooling/test-only" "docs-only"

# Test-only changes do not ship and are allowed without a version bump.
reset_repo
printf 'package app\n\nfunc TestChanged() {}\n' > internal/app_test.go
printf 'package e2e\n\nfunc TestChanged() {}\n' > test/e2e/e2e_test.go
printf '{"scripts":{"test":"changed"}}\n' > npm/test/install.test.js
commit "tests only"
run_policy
expect 0 "tests-only"
expect_output "docs/CI/tooling/test-only" "tests-only"

# A shipped code change without a version bump is blocked.
reset_repo
printf 'package app\n\nconst Version = "next"\n' > internal/new.go
commit "code without bump"
run_policy
expect 1 "code without bump"
expect_output "release-related files" "code without bump"

# A shipped code change with a patch bump is allowed.
reset_repo
printf 'package app\n\nconst Version = "next"\n' > internal/new.go
printf '{"name":"test","version":"0.3.5"}\n' > npm/package.json
commit "code with bump"
run_policy
expect 0 "code with bump"
expect_output "version bumped 0.3.4 -> 0.3.5" "code with bump"

# skip-version batches a shipped change without bumping.
reset_repo
printf 'package app\n\nconst Version = "next"\n' > internal/new.go
commit "batched code"
run_policy "" "skip-version"
expect 0 "skip-version without bump"
expect_output "skip-version" "skip-version without bump"

# skip-version and a version bump conflict.
reset_repo
printf 'package app\n\nconst Version = "next"\n' > internal/new.go
printf '{"name":"test","version":"0.3.5"}\n' > npm/package.json
commit "batched code with bump"
run_policy "" "skip-version"
expect 1 "skip-version with bump"
expect_output "skip-version is set" "skip-version with bump"

# Versions must increase.
reset_repo
printf 'package app\n\nconst Version = "old"\n' > internal/new.go
printf '{"name":"test","version":"0.3.3"}\n' > npm/package.json
commit "downgrade"
run_policy
expect 1 "downgrade"
expect_output "must increase" "downgrade"

# Invalid semver is rejected.
reset_repo
printf 'package app\n\nconst Version = "next"\n' > internal/new.go
printf '{"name":"test","version":"next"}\n' > npm/package.json
commit "invalid version"
run_policy
expect 1 "invalid semver"
expect_output "invalid version" "invalid semver"

# A missing base ref is an execution error, not a policy verdict.
reset_repo
printf 'package app\n\nconst Version = "next"\n' > internal/new.go
commit "missing base"
run_policy "refs/heads/does-not-exist"
expect 2 "missing base"
expect_output "base ref" "missing base"

if [ "$failures" -ne 0 ]; then
  printf '%d release policy test(s) failed\n' "$failures" >&2
  exit 1
fi
echo "release policy tests: ok"
