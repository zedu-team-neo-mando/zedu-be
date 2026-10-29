#!/usr/bin/env bash
#
# Fails when a pull request adds organisation lookups sourced from the URL path
# parameter under pkg/controller/. Per AGENTS.md the active organisation must
# come from the signed JWT claims (userClaims["org_id"]), not c.Param("org_id").
#
# Usage: scripts/check-org-id-convention.sh <base-ref> [head-ref]
#
# The check is diff-scoped: only lines added between <base-ref> and <head-ref>
# are inspected, so existing usages do not need to be fixed first. File paths
# listed in .github/org-id-allowlist.txt are skipped.

set -euo pipefail

if [ "$#" -lt 1 ] || [ -z "${1:-}" ]; then
  echo "usage: $0 <base-ref> [head-ref]  (head defaults to HEAD)" >&2
  exit 2
fi

base="$1"
head="${2:-HEAD}"

root="$(cd "$(dirname "$0")/.." && pwd)"
allowlist="$root/.github/org-id-allowlist.txt"
pathspec="pkg/controller"
pattern='Param\("org_id"\)'

is_allowed() {
  [ -f "$allowlist" ] && grep -qxF "$1" "$allowlist"
}

if ! changed="$(git -C "$root" diff --name-only "$base" "$head" -- "$pathspec")"; then
  echo "::error::git diff failed for ${base}..${head}; check that both refs exist" >&2
  exit 2
fi
files="$(grep -E '\.go$' <<<"$changed" || true)"

violations=""
for file in $files; do
  is_allowed "$file" && continue
  if ! diff="$(git -C "$root" diff --unified=0 "$base" "$head" -- "$file")"; then
    echo "::error::git diff failed for ${base}..${head} -- ${file}" >&2
    exit 2
  fi
  hits="$(grep -E '^\+[^+]' <<<"$diff" | grep -nE "$pattern" || true)"
  if [ -n "$hits" ]; then
    violations="${violations}${file}"$'\n'"${hits}"$'\n'
  fi
done

if [ -n "$violations" ]; then
  echo "::error::org_id must come from the JWT claims, not c.Param(\"org_id\")"
  echo "Offending added lines (file, then added-line index within the diff):"
  printf '%s' "$violations"
  exit 1
fi

echo "org_id convention OK: no new c.Param(\"org_id\") under ${pathspec}"
