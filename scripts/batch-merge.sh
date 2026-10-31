#!/usr/bin/env bash
# Merges a finished batch's integration branch into main (TP-37, built in TP-99).
#
#   ./scripts/batch-merge.sh <batch-id> [--dry-run]
#
# The API never runs git, so this is the step finish_batch hands you. Before touching main it checks
# what the tracker and the repository both say:
#   - the batch is finished (rule 16: its tickets and its eval are done), and has a branch (git_ref);
#   - every done ticket's branch, ticket/<key>-*, is already merged into that branch. A done ticket
#     with no branch at all is reported, not refused: its work may have gone in some other way.
# Then it merges with --no-ff, so main keeps one merge per batch and each ticket's commits under it.
# --dry-run prints the plan and changes nothing. API and REPO can point it elsewhere (scripts/test_batch_merge.py does),
# and LOOP_PROJECT names a connected project's batch, which the API otherwise looks for in the default one.
set -euo pipefail

API="${API:-http://localhost:8080}"
REPO="${REPO:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
BATCH="${1:?usage: batch-merge.sh <batch-id> [--dry-run]}"
DRY=${2:-}
g() { git -C "$REPO" "$@"; }

batch_json=$(curl -sf ${TICKETING_TOKEN:+-H "Authorization: Bearer $TICKETING_TOKEN"} "$API/api/batches/$BATCH${LOOP_PROJECT:+?project=$LOOP_PROJECT}") || { echo "no batch $BATCH at $API" >&2; exit 2; }
read -r finished ref title <<< "$(python3 -c '
import json, sys
b = json.load(sys.stdin)["batch"]
print(b["finished_at"] or "-", b["git_ref"] or "-", b["title"])' <<< "$batch_json")"

[ "$finished" != - ] || { echo "refused: batch \"$title\" has not finished; finish_batch comes first (rule 16)" >&2; exit 1; }
[ "$ref" != - ] || { echo "refused: batch \"$title\" has no branch (git_ref); its tickets went to main one by one" >&2; exit 1; }
g rev-parse --verify --quiet "refs/heads/$ref" >/dev/null || { echo "refused: branch $ref does not exist in $REPO" >&2; exit 1; }

unmerged=() missing=()
while read -r key; do
  [ -z "$key" ] && continue
  lower=$(tr '[:upper:]' '[:lower:]' <<< "$key")
  branches=$(g for-each-ref --format='%(refname:short)' "refs/heads/ticket/$lower" "refs/heads/ticket/$lower-*")
  if [ -z "$branches" ]; then
    missing+=("$key")
    continue
  fi
  while read -r b; do
    g merge-base --is-ancestor "$b" "$ref" || unmerged+=("$b")
  done <<< "$branches"
done <<< "$(python3 -c '
import json, sys
for t in json.load(sys.stdin)["tickets"]:
    if t["status"] == "done":
        print(t["key"])' <<< "$batch_json")"

for key in "${missing[@]}"; do echo "note: $key is done but has no ticket/${key,,}-* branch here"; done
if [ ${#unmerged[@]} -gt 0 ]; then
  echo "refused: not yet merged into $ref: ${unmerged[*]}" >&2
  echo "  merge each with: git switch $ref && git merge --no-ff <branch>" >&2
  exit 1
fi

message="Merge $ref: $title (batch $BATCH)"
if [ "$DRY" = --dry-run ]; then
  echo "would run: git switch main && git merge --no-ff $ref -m \"$message\""
  exit 0
fi
g diff --quiet && g diff --cached --quiet || { echo "refused: $REPO has uncommitted changes" >&2; exit 1; }
g switch --quiet main
g merge --no-ff --quiet "$ref" -m "$message"
echo "merged $ref into main: $(g log -1 --format='%h %s')"
