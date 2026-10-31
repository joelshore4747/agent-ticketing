#!/usr/bin/env bash
# Loads docs/guidance/*.md into the tracker as guidance documents.
#
# This is a BOOTSTRAP, not a sync. The database is the served truth: once a document exists there it
# can be edited through the API, and re-running this script would quietly append a revision putting
# the repo's copy back. So an existing slug is skipped unless you pass --replace, which is the
# deliberate "the file is newer than the database" case.
#
# Even then the PUT replaces the whole body, so a file that is behind deletes what the server has and
# the file lacks. --replace therefore counts those lines first and refuses the file (TP-192), naming
# how many would go; --force says you meant it. At 08ab5ec that refusal would have saved three rules.
#
# Conventions the agent needs live here rather than in CLAUDE.md so that there is one source of
# truth, and so that an agent working without guidance is genuinely working without guidance.
set -euo pipefail
API="${API:-http://localhost:8080}"
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/docs/guidance"
# Both readings of a guidance file live in one module, here and in guidance-check.sh (TP-235).
LIB="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/guidance_file.py"

REPLACE=0
FORCE=0
for arg in "$@"; do
  case "$arg" in
    --replace) REPLACE=1 ;;
    --force) FORCE=1 ;;   # only means anything with --replace: nothing else here overwrites
    *) echo "usage: guidance-seed.sh [--replace [--force]]" >&2; exit 2 ;;
  esac
done
refused=0; put=()

existing=$(curl -sf ${TICKETING_TOKEN:+-H "Authorization: Bearer $TICKETING_TOKEN"} "$API/api/guidance" | python3 "$LIB" list | cut -d'|' -f1 | tr '\n' ' ')

for file in "$DIR"/*.md; do
  slug=$(basename "$file" .md)
  if [[ " $existing " == *" $slug "* ]]; then
    if [ "$REPLACE" != 1 ]; then
      echo "skip    $slug (already in the database; --replace to overwrite)"
      continue
    fi
    # How much of the served document this file does not contain. `|| true` because an unreachable
    # document is not a reason to stop the run; it is a reason not to claim anything about drift.
    served=$(curl -sf ${TICKETING_TOKEN:+-H "Authorization: Bearer $TICKETING_TOKEN"} "$API/api/guidance/$slug" || true)
    if [ -n "$served" ]; then
      read -r _ served_only <<<"$(python3 "$LIB" drift "$file" <<<"$served")"
      if [ "$served_only" -gt 0 ] && [ "$FORCE" != 1 ]; then
        # A reworded line counts too, because the PUT deletes it either way: it replaces the whole
        # body, and nothing keeps the old text but the revision history.
        echo "refuse  $slug: the file lacks $served_only served lines (reworded or deleted); this PUT" \
             "would replace the whole body — --force to do it anyway" >&2
        refused=$((refused + 1))
        continue
      fi
    fi
  fi

  payload=$(python3 "$LIB" payload "$file")
  curl -sf -X PUT "$API/api/guidance/$slug" \
    -H 'content-type: application/json' -H 'X-Actor: guidance-seed' \
    ${TICKETING_TOKEN:+-H "Authorization: Bearer $TICKETING_TOKEN"} \
    -d "$payload" >/dev/null
  echo "loaded  $slug"
  put+=("$slug")
done

if [ "$refused" != 0 ]; then
  echo "refused $refused file(s) that would have deleted served lines; --force overwrites them" >&2
fi

# Stamp the documents this run actually wrote with the commit their source_paths are at, so
# guidance-check has a baseline instead of reporting them as unreviewed on the first run. Only
# those: one this run skipped or refused is one nobody re-read, and pinning it would delete the
# staleness that says so (TP-273). The rest are still reported, hence before the non-zero exit.
checked=0
"$(dirname "${BASH_SOURCE[0]}")/guidance-check.sh" --accept-only ${put[@]+"${put[@]}"} || checked=1

[ "$refused" = 0 ] && [ "$checked" = 0 ] || exit 1
