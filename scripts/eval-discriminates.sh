#!/usr/bin/env bash
# Does the test a run added actually fail without the change it tests?
#
# This is the strongest quality signal in the harness and it needs no model at all. "A test was
# added" is nearly free to satisfy and says almost nothing; a test that passes with or without the
# fix documents nothing and will be deleted by whoever discovers it. So: apply only the test hunks
# to a clean tree and run the suite. It must go red. Then apply the whole patch. It must go green.
#
# Three outcomes, and the middle one is the interesting one:
#   discriminates  the tests fail alone and pass with the change — the test pins the behaviour
#   passive        the tests pass even without the change — the test is decoration
#   none           the patch touches no test file
#
#   ./scripts/eval-discriminates.sh --patch <diff.patch> --base <sha>
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="${EVAL_WORK:-${TMPDIR:-/tmp}/ticketing-eval}"
# What counts as a test in this repo. git-apply patterns, not shell globs.
TEST_GLOBS=('*_test.go' 'web/src/test/*' '*.test.ts' '*.test.tsx')
PATCH= BASE=

while [ $# -gt 0 ]; do
  case $1 in
    --patch) PATCH=$2; shift ;;
    --base)  BASE=$2; shift ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
  shift
done
[ -s "${PATCH:-}" ] || { echo "--patch must name a non-empty diff" >&2; exit 2; }
[ -n "$BASE" ] || { echo "--base must name the commit the patch was taken against" >&2; exit 2; }

includes=(); for g in "${TEST_GLOBS[@]}"; do includes+=("--include=$g"); done

repo=$(mktemp -d "$WORK/discriminate.XXXXXX")
trap 'rm -rf "$repo"' EXIT
git clone --quiet --single-branch --no-tags "$ROOT" "$repo"
git -C "$repo" -c advice.detachedHead=false checkout --quiet "$BASE"

# Tests alone. --check first so a patch that cannot apply is reported as such rather than as a
# passing suite, which is the failure mode that would quietly mark every run "passive".
if ! git -C "$repo" apply --check "${includes[@]}" "$PATCH" 2>/dev/null; then
  echo "none"; exit 0
fi
git -C "$repo" apply "${includes[@]}" "$PATCH"
if git -C "$repo" diff --quiet; then
  echo "none"; exit 0
fi

alone=0; ( cd "$repo" && just test >"$repo/.alone.log" 2>&1 ) || alone=$?
if [ "$alone" = 0 ]; then
  echo "passive"; exit 0
fi

# The whole change. If this does not go green the run is broken, not discriminating.
git -C "$repo" checkout --quiet -- .
git -C "$repo" apply "$PATCH" 2>/dev/null || { echo "none"; exit 0; }
together=0; ( cd "$repo" && just test >"$repo/.together.log" 2>&1 ) || together=$?
[ "$together" = 0 ] && echo "discriminates" || echo "passive"
