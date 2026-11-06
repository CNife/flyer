#!/usr/bin/env bash
# Release policy: shipped changes must bump npm/package.json.
#
# Inputs:
#   BASE_REF   base git ref (default: origin/main)
#   PR_LABELS  comma-separated PR labels (default: empty)
#
# A PR may batch a release by adding the `skip-version` label; in that case
# npm/package.json must stay unchanged. Docs/CI/tooling/test-only changes are
# allowed without a version bump.
set -euo pipefail

ROOT="$(git rev-parse --show-toplevel)"
cd "$ROOT"

BASE_REF="${BASE_REF:-origin/main}"
PR_LABELS="${PR_LABELS:-}"

if ! git rev-parse --verify --quiet "${BASE_REF}^{commit}" >/dev/null; then
  echo "::error::base ref ${BASE_REF} not found; fetch it before running the release policy" >&2
  exit 2
fi

base_version="$(git show "${BASE_REF}:npm/package.json" | node -p 'JSON.parse(require("fs").readFileSync(0, "utf8")).version')"
head_version="$(node -p 'require("./npm/package.json").version')"

labels=",${PR_LABELS},"
labels="${labels// /,}"
skip_version=0
case "$labels" in
  *,skip-version,*) skip_version=1 ;;
esac

if [ "$skip_version" -eq 1 ]; then
  if [ "$head_version" != "$base_version" ]; then
    echo "::error::skip-version is set, but npm/package.json changed: ${base_version} -> ${head_version}" >&2
    exit 1
  fi
  echo "release policy: skip-version, npm/package.json stays at ${base_version}"
  exit 0
fi

if [ "$head_version" != "$base_version" ]; then
  if ! cmp="$(node - "$head_version" "$base_version" <<'NODE'
const [a, b] = process.argv.slice(2);
const re = /^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$/;

function parse(v) {
  const m = re.exec(v);
  if (!m) return null;
  return {
    nums: [Number(m[1]), Number(m[2]), Number(m[3])],
    pre: m[4] ? m[4].split(".") : [],
  };
}

const pa = parse(a);
const pb = parse(b);
if (!pa || !pb) {
  console.error(`invalid semver: ${!pa ? a : b}`);
  process.exit(2);
}

const cmpNum = (x, y) => (x === y ? 0 : x < y ? -1 : 1);
let cmp = 0;
for (let i = 0; i < 3 && cmp === 0; i++) {
  cmp = cmpNum(pa.nums[i], pb.nums[i]);
}

if (cmp === 0) {
  if (pa.pre.length === 0 && pb.pre.length > 0) {
    cmp = 1;
  } else if (pa.pre.length > 0 && pb.pre.length === 0) {
    cmp = -1;
  } else {
    const len = Math.max(pa.pre.length, pb.pre.length);
    for (let i = 0; i < len && cmp === 0; i++) {
      if (i >= pa.pre.length) {
        cmp = -1;
      } else if (i >= pb.pre.length) {
        cmp = 1;
      } else {
        const x = pa.pre[i];
        const y = pb.pre[i];
        const xn = /^[0-9]+$/.test(x);
        const yn = /^[0-9]+$/.test(y);
        if (xn && yn) {
          cmp = cmpNum(Number(x), Number(y));
        } else if (xn) {
          cmp = -1;
        } else if (yn) {
          cmp = 1;
        } else {
          cmp = x === y ? 0 : x < y ? -1 : 1;
        }
      }
    }
  }
}

console.log(cmp);
NODE
  )"; then
    echo "::error::npm/package.json has an invalid version: ${base_version} -> ${head_version}" >&2
    exit 1
  fi

  if [ "$cmp" != "1" ]; then
    echo "::error::npm/package.json must increase: ${base_version} -> ${head_version}" >&2
    exit 1
  fi

  echo "release policy: version bumped ${base_version} -> ${head_version}"
  exit 0
fi

if git merge-base "$BASE_REF" HEAD >/dev/null 2>&1; then
  changed="$(git diff --name-only "${BASE_REF}...HEAD")"
else
  # Shallow CI checkouts may not have a merge base; HEAD is the PR merge commit.
  changed="$(git diff --name-only "$BASE_REF" HEAD)"
fi

shipped=()
while IFS= read -r file; do
  [ -n "$file" ] || continue
  case "$file" in
    .github/* | docs/* | scripts/* | test/* | *_test.go | npm/test/* | *.md | LICENSE | .gitignore | .gitattributes | .githooks/* | justfile | .goreleaser.yaml | .no-mistakes.yaml)
      ;;
    *)
      shipped+=("$file")
      ;;
  esac
done <<<"$changed"

if [ "${#shipped[@]}" -gt 0 ]; then
  echo "::error::this PR changes release-related files but does not bump npm/package.json:" >&2
  printf '  %s\n' "${shipped[@]}" >&2
  echo "Bump npm/package.json, or add the skip-version label to batch this release." >&2
  exit 1
fi

echo "release policy: docs/CI/tooling/test-only change, version stays at ${base_version}"
