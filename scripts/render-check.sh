#!/usr/bin/env bash
# Screenshots the built web app's main pages in headless Chromium against fixed seed data, and
# compares them with committed baselines under scripts/render-baselines/. Nothing else checks how
# the stylesheet actually renders: the web tests run in jsdom and never load global.css, so a CSS
# refactor like TP-120 had no guard (rule 21).
#
#   ./scripts/render-check.sh            # compare against the committed baselines
#   ./scripts/render-check.sh --update   # rewrite the baselines instead, and exit 0
#
# Builds web/dist, serves it against a throwaway API with fixed data (a fresh database per run, so
# the same seed produces the same ticket keys every time), and drives the four main pages — board, a
# ticket, a batch, needs-you — at desktop and 360px widths. Exits non-zero naming any page whose
# pixels moved more than a small threshold; --update accepts the change instead.
#
# This is the standing guard for CSS, and `just gate` chains it after lint and test so a stylesheet
# change cannot reach a commit unchecked (TP-141). Run `just gate`, not this by hand.
#
# Needs a locally cached Playwright + Chromium: this machine has one at
# ~/.cache/ms-playwright/chromium-1228, reached through an npx-cached `playwright` package (neither
# is a project dependency, deliberately — see web/scripts/render-check.mjs). On a machine without
# one, `cd web && npx playwright install chromium`; until then this exits non-zero rather than
# passing quietly. Override RENDER_CHECK_CHROMIUM / RENDER_CHECK_PLAYWRIGHT if yours live elsewhere,
# or RENDER_CHECK_THRESHOLD to change how many pixels may differ before a page fails (a fraction of
# the image, default 0.001).
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/lib/throwaway.sh

UPDATE=false
if [ "${1:-}" = --update ]; then UPDATE=true; fi

echo "Building the web app..."
(cd web && npm run build >/dev/null)

start_throwaway

H='content-type: application/json'
post() {
  local resp code
  resp=$(curl -s -w '\n%{http_code}' -X POST "$api$1" -H "$H" -H "X-Actor: ${3:-render-check}" -d "$2")
  code=${resp##*$'\n'}
  resp=${resp%$'\n'*}
  if [ "$code" -ge 400 ]; then echo "POST $1 -> $code: $resp" >&2; exit 22; fi
  printf '%s' "$resp"
}
patch() {
  local resp code
  resp=$(curl -s -w '\n%{http_code}' -X PATCH "$api$1" -H "$H" -H "X-Actor: ${3:-render-check}" -d "$2")
  code=${resp##*$'\n'}
  resp=${resp%$'\n'*}
  if [ "$code" -ge 400 ]; then echo "PATCH $1 -> $code: $resp" >&2; exit 22; fi
}
jf() { python3 -c "import sys,json;d=json.load(sys.stdin);print(d$1)"; }
version_of() { curl -sf "$api/api/tickets/$1" | jf "['version']"; }
done_body() { python3 -c 'import json,sys;print(json.dumps({"version":int(sys.argv[1]),"status":"done","resolution":sys.argv[2],"verified_by":sys.argv[3]}))' "$1" "$2" "$3"; }

echo "Seeding fixed data into $api..."

# Two AI findings, left in triage (the web area has no auto_accept_ai): fills the inbox column and
# the "findings to accept or dismiss" group on Needs you.
post /api/tickets '{"title":"Card border colour drifts between columns","description":"The to-do and doing columns use slightly different border tones for the same card state.","type":"bug","priority":"medium","source":"ai","area":"web","locations":[{"file_path":"web/src/components/BoardCard.tsx","line_start":12}]}' >/dev/null
post /api/tickets '{"title":"Empty-state copy repeats itself across columns","description":"Three of the four columns say almost the same thing when empty.","type":"improvement","priority":"low","source":"ai","area":"web"}' >/dev/null

# Three human tickets, one per remaining column — human tickets start open, never triage.
t_open=$(post /api/tickets '{"title":"Session banner never dismisses","description":"Clicking away does not close it, and there is no close button.","type":"bug","priority":"high","source":"human","area":"web"}')
TICKET_KEY=$(echo "$t_open" | jf "['key']")

t_doing=$(post /api/tickets '{"title":"Group counts by tag on the batch page","description":"The batch page lists tickets flat; grouping by tag would match the board.","type":"feature","priority":"medium","source":"human","area":"web"}')
doing_key=$(echo "$t_doing" | jf "['key']")
patch "/api/tickets/$doing_key" '{"version":1,"status":"in_progress"}'

t_done=$(post /api/tickets '{"title":"Trim dead CSS from the old table view","description":"The table view was replaced by the board and its rules were never removed.","type":"chore","priority":"low","source":"human","area":"web"}')
done_key=$(echo "$t_done" | jf "['key']")
patch "/api/tickets/$done_key" '{"version":1,"status":"in_progress"}'
patch "/api/tickets/$done_key" "$(done_body "$(version_of "$done_key")" "Removed the unused .table-view rules." "grep -r table-view web/src — no matches")"

# A comment and an uncited decision, so the ticket page and Needs you both have something to show.
post "/api/tickets/$TICKET_KEY/comments" '{"body":"Reproduced on the AI board too, so it is not source-specific."}' >/dev/null
post "/api/tickets/$TICKET_KEY/decisions" '{"kind":"accepted","rationale":"Matches how the board already handles a stuck dialog elsewhere."}' >/dev/null

# A finished plan batch, so Batches and the batch page have a digest, reviews and tickets to show.
plan=$(post /api/batches/plan '{"title":"Small board polish","what":"Tidy two rough edges on the board.","how":"One ticket, then the eval.","why":"So the board reads clean before the next goal review.","demo":"just lint && just test","tickets":[{"title":"Polish batch: tidy card spacing","type":"chore","area":"web","goals":[3]}]}')
BATCH_ID=$(echo "$plan" | jf "['batch']['id']")
post "/api/batches/$BATCH_ID/reviews" '{"phase":"before","commit":"0000000","body":"Nothing outstanding before starting."}' >/dev/null
post /graphql "$(python3 -c 'import json,sys;print(json.dumps({"query":"mutation($id: ID!) { acceptBatch(id: $id) { id } }","variables":{"id":sys.argv[1]}}))' "$BATCH_ID")" joel >/dev/null

for key in $(echo "$plan" | python3 -c 'import sys,json;print("\n".join(t["key"] for t in json.load(sys.stdin)["tickets"]))'); do
  patch "/api/tickets/$key" "{\"version\":$(version_of "$key"),\"status\":\"in_progress\"}"
  patch "/api/tickets/$key" "$(done_body "$(version_of "$key")" "Fixture ticket for render-check." "just lint && just test — ok")"
done
post "/api/batches/$BATCH_ID/reviews" '{"phase":"after","commit":"0000000","body":"Both tickets landed; nothing else changed."}' >/dev/null
patch "/api/batches/$BATCH_ID" "$(python3 -c 'import json,sys;print(json.dumps({"finish":True,"summary":"## What was added\nTidied two board rough edges.\n## Why\nGoal 3: a codebase that stays tidy.\n## What it achieved\nThe board reads clean again.\n## Where in the code\nweb/src/components/BoardCard.tsx\n## How it moves the goals\nSmall, deliberate diffs stay tidy."}))')"

echo "Rendering pages in headless Chromium..."
render_args=(--api "$api" --ticket "$TICKET_KEY" --batch "$BATCH_ID")
if $UPDATE; then render_args+=(--update); fi
(cd web && npm run render-check -- "${render_args[@]}")
