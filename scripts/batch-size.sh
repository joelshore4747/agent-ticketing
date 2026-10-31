#!/usr/bin/env bash
# Reports how big a batch's change was, for the digest's Measures section (goal 9, built in TP-160).
#
#   ./scripts/batch-size.sh <batch-id> [--base <commit>] [--dry-run] [--off-plan-ok]
#
# The API never runs git and never opens a project's repo_path (rule 22), so the numbers the digest
# reads have to be measured out here and posted in, the way scripts/usage-report.sh posts a session's
# tokens. This counts `git diff --numstat <base>..<the batch's git_ref>` split into code and tests —
# the ceiling counts code only (TP-212) — and posts both pairs to POST /api/batches/{id}/size, which
# records the whole diff and the test half of it. The base is the branch's merge-base with main unless --base says
# otherwise. Run it before the batch is finished: finish_batch computes the digest from what is
# recorded then, and a batch nobody measured reads "size unknown" rather than failing.
# API and REPO can point it elsewhere (scripts/test_batch_size.py does); LOOP_PROJECT names a connected
# project's batch, which the API otherwise looks for in the default one.
#
# A branch carries whatever was committed onto it, which is not always the batch's own plan, so a
# diff of the branch is not by itself a measure of the batch (TP-180). Batch 20c65c62 recorded -5
# where its six planned tickets came to -40: TP-176 and TP-178, neither of them in the plan, were
# merged onto the same branch, and that -5 is what its eval read as passing the cleanup check and
# what goal 9 remembers. So before posting, this names every ticket merged onto the branch that the
# batch does not list, and refuses. A fix found while gating a batch belongs in the batch:
# add_to_batch sequences it, and the Narrator can then account for the lines. --off-plan-ok records
# the mixed figure anyway, for when that really is the right answer.
set -euo pipefail

API="${API:-http://localhost:8080}"
REPO="${REPO:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
BATCH="${1:?usage: batch-size.sh <batch-id> [--base <commit>] [--dry-run] [--off-plan-ok]}"
shift
BASE="" DRY="" OFF_PLAN_OK=""
while [ $# -gt 0 ]; do
  case "$1" in
    --base) BASE=$2; shift 2 ;;
    --dry-run) DRY=1; shift ;;
    --off-plan-ok) OFF_PLAN_OK=1; shift ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done
g() { git -C "$REPO" "$@"; }

batch_json=$(curl -sf ${TICKETING_TOKEN:+-H "Authorization: Bearer $TICKETING_TOKEN"} "$API/api/batches/$BATCH${LOOP_PROJECT:+?project=$LOOP_PROJECT}") || { echo "no batch $BATCH at $API" >&2; exit 2; }
read -r ref size_base title <<< "$(python3 -c '
import json, sys
b = json.load(sys.stdin)["batch"]
print(b["git_ref"] or "-", b.get("size_base_commit") or "-", b["title"])' <<< "$batch_json")"
# The plan the branch is supposed to hold: every ticket the batch lists, whatever its status.
keys=$(python3 -c '
import json, sys
print(" ".join(t["key"] for t in json.load(sys.stdin).get("tickets") or []))' <<< "$batch_json")

[ "$ref" != - ] || { echo "refused: batch \"$title\" has no branch (git_ref), so there is nothing to measure" >&2; exit 1; }
g rev-parse --verify --quiet "refs/heads/$ref" >/dev/null || { echo "refused: branch $ref does not exist in $REPO" >&2; exit 1; }

if [ -z "$BASE" ]; then
  # Once the batch has landed, merge-base(main, ref) is the branch tip itself and the diff is empty.
  # A silent 0 is the worst answer a measurement can give, so say what happened and what to pass.
  if g merge-base --is-ancestor "$ref" main; then
    # Unless it was measured once already, in which case where it started is on the record and
    # re-measuring a landed batch (to check it, or to answer the off-plan question below) needs no
    # argument from the caller.
    [ "$size_base" != - ] || {
      echo "refused: $ref is already merged into main, so it has no diff against it. Measure a landed batch" >&2
      echo "         against where it started: --base <its before-review commit>." >&2
      exit 1
    }
    BASE=$size_base
    echo "note: $ref is already in main; measuring against the base it was first measured from"
  else
    BASE=$(g merge-base main "$ref") || { echo "refused: $ref has no merge-base with main" >&2; exit 1; }
  fi
fi
BASE=$(g rev-parse --verify "$BASE^{commit}")

# Code and tests are counted apart, because the ceiling counts code only (TP-212). A test line used to
# cost a builder exactly what a code line did, so the cheapest way under the ceiling was to thin the
# test that proved the change: 63% of batch 3's growth was tests (+314/-3 of its +603/-113), and TP-181
# was closed unmerged at net +90 of which +41 was its own two test files. TEST_PATH below is the only
# definition of "a test", and it reads the path and nothing else, so code moved into a test file to duck
# the ceiling shows up in review rather than being absorbed by the figure.
read -r added deleted test_added test_deleted <<< "$(g diff --numstat "$BASE..$ref" | python3 -c '
import re, sys

TEST_PATH = re.compile(r"""
      _test\.go$           # Go: tickets_test.go
    | \.test\.[jt]sx?$      # web: Batches.test.tsx, api.test.ts
    | (^|/)test_[^/]+\.py$  # python unittest: scripts/test_loop.py
    | (^|/)tests?/          # anything under a test/ or tests/ directory, at any depth
""", re.VERBOSE)
# Written by a package manager, not by anyone: the first FS batch measured +6117 code lines, 5655 of them
# package-lock.json, and a ceiling charged for a lockfile measures the dependency tree, not the work.
LOCKFILE = re.compile(r"(^|/)(package-lock\.json|pnpm-lock\.yaml|yarn\.lock|go\.sum|Cargo\.lock|poetry\.lock)$")


def post_image(path):
    """A rename reads "src/{old => new}.go", or "old => new" with no shared part. Either way what the
    pattern has to judge is the file as it now is; the brace form ends in "}" and matches nothing."""
    if " => " not in path:
        return path
    before, brace, after = path.partition("{")
    if not brace:
        return path.split(" => ")[-1]
    inner, _, rest = after.partition("}")
    return before + inner.split(" => ")[-1] + rest


a = d = ta = td = 0
for line in sys.stdin:
    parts = line.rstrip("\n").split("\t")
    if len(parts) < 3 or parts[0] == "-":  # "-" is a binary file: no lines to count
        continue
    if LOCKFILE.search(post_image(parts[2])):
        continue
    plus, minus = int(parts[0]), int(parts[1])
    a += plus
    d += minus
    if TEST_PATH.search(post_image(parts[2])):
        ta += plus
        td += minus
print(a, d, ta, td)')"
code_added=$((added - test_added))
code_deleted=$((deleted - test_deleted))

printf '%s vs %s: code +%d / -%d, net %+d; tests +%d / -%d, net %+d\n' \
  "$ref" "${BASE:0:12}" "$code_added" "$code_deleted" "$((code_added - code_deleted))" \
  "$test_added" "$test_deleted" "$((test_added - test_deleted))"

# Whose lines are those? Walk the merges the branch itself carries (--first-parent: a ticket branch
# merged in is one of them, a commit inside that branch is not) back to where the branch was cut,
# which is the previous batch landing — batch-merge.sh writes "Merge <ref>: <title> (batch <uuid>)".
# Stopping there keeps an earlier batch of tickets, which this base already accounts for, out of it.
off_plan=$(g log --merges --first-parent --format='%h%x09%s' "$BASE..$ref" | python3 -c '
import re, sys

plan = {k.upper() for k in sys.argv[1:]}
# The two subjects this repo produces for a ticket: "Merge TP-177: ..." by hand, and the git default
# "Merge branch ticket/tp-170-... into batch/2-...". Both anchored, so the hex of a batch uuid or the
# digits of a branch name can never be read as a ticket key.
TICKET = re.compile(r"^Merge ([A-Za-z][A-Za-z0-9]{1,9}-\d+)\b|\bticket/([A-Za-z][A-Za-z0-9]{1,9}-\d+)\b")
LANDED_BATCH = re.compile(r"^Merge \S+: .* \(batch [0-9a-f-]{36}\)$")
seen, rows = set(), []
for line in sys.stdin:
    sha, _, subject = line.rstrip("\n").partition("\t")
    if LANDED_BATCH.match(subject):
        break
    found = TICKET.search(subject)
    if not found:
        continue
    key = (found.group(1) or found.group(2)).upper()
    if key not in plan and key not in seen:
        seen.add(key)
        rows.append(key + "\t" + sha + "\t" + subject)
# git hands them newest first; the order they landed in reads better.
print("\n".join(reversed(rows)))
' $keys)

if [ -n "$off_plan" ]; then
  echo "merged onto $ref but not in this batch (it plans ${keys:-nothing}):" >&2
  while IFS=$'\t' read -r key sha subject; do
    [ -n "$key" ] && echo "  $key  $sha  $subject" >&2
  done <<< "$off_plan"
  if [ -z "$OFF_PLAN_OK" ]; then
    echo "refused: their lines are inside the figures above, so those are not this batch's own," >&2
    echo "         and the digest would credit this batch for work no ticket of its own did. A fix found while" >&2
    echo "         gating goes into the batch with add_to_batch; --off-plan-ok records the mixed figure anyway." >&2
    exit 1
  fi
fi

[ -n "$DRY" ] && exit 0

body=$(python3 -c '
import json, sys
print(json.dumps({"lines_added": int(sys.argv[1]), "lines_deleted": int(sys.argv[2]),
                  "test_lines_added": int(sys.argv[3]), "test_lines_deleted": int(sys.argv[4]),
                  "base_commit": sys.argv[5], **({"project": sys.argv[6]} if sys.argv[6] else {})}))' \
  "$added" "$deleted" "$test_added" "$test_deleted" "$BASE" "${LOOP_PROJECT:-}")
curl -sf -X POST "$API/api/batches/$BATCH/size" -H 'Content-Type: application/json' \
  ${TICKETING_TOKEN:+-H "Authorization: Bearer $TICKETING_TOKEN"} -d "$body" > /dev/null \
  || { echo "POST /api/batches/$BATCH/size failed" >&2; exit 1; }
echo "reported to $API"
