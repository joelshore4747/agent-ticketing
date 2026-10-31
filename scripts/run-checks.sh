#!/usr/bin/env bash
# Runs a ticket's command checks and records the held-out gate (TP-44/TP-73, built in TP-96).
#
#   ./scripts/run-checks.sh TP-42          # against the live tracker
#   DB=ticketing_demo_123 ./scripts/run-checks.sh TP-1
#   REPO=<path> ./scripts/run-checks.sh FS-1    # that checkout, over the project's own repo_path
#
# It reads every command check for the ticket, public and held-out alike, runs each in the checkout
# of the ticket's own project — its repo_path, or this one when it has none — and compares the exit
# code with the one the check expects. Then it writes `gate_passed` and `gate_checked_at` straight to
# Postgres. No transport carries those two columns, which is the point: the agent being judged cannot
# set its own verdict (rule 21), and a held-out check's command never passes through the API, where
# the agent could read it.
#
# Manual checks are listed, not run: a person decides those. The gate is the command checks alone.
# Prints PASS/FAIL per check and exits non-zero if any fails (or the ticket has no command checks).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PGURL="${PGURL:-postgres://ticketing:ticketing@127.0.0.1:5434}"
DB="${DB:-ticketing}"
KEY="${1:?usage: run-checks.sh TICKET-KEY}"
[[ "$KEY" =~ ^[A-Z][A-Z0-9]{1,9}-[0-9]+$ ]] || { echo "not a ticket key: $KEY" >&2; exit 2; }
command -v psql >/dev/null || { echo "psql not on PATH" >&2; exit 2; }
q() { psql "$PGURL/$DB" -qtAX -v ON_ERROR_STOP=1 "$@"; }

# TP-295 (lesson TP-280): stale guidance fails the gate of the ticket whose own diff moved the document, instead of surfacing a batch later at the eval, where loop.sh hands it to an agent who no longer has the change (slots 5, 7, 8 and 11 to 13).
# A STALE line on paths this branch left alone is not this gate's business, so no ticket ever waits on another's drift. Kept clear of the check's side effects, so scripts/test_gate.py can table-test it.
guidance_moved() { # <base> <guidance-check report>: one message per STALE document this branch moved
  local base=$1 slug rest paths pin
  # shellcheck disable=SC2086 -- paths are deliberately word-split into git's pathspec list.
  while read -r _ slug rest; do slug=${slug%:} paths=${rest%% changed since *} pin=${rest##* changed since }
    [ -z "$(git -C "$ROOT" log --format=%H "$base..HEAD" -- $paths)" ] || echo "$slug is stale and this branch moved it: read 'git diff ${pin%% *}..HEAD -- $paths', correct docs/guidance/$slug.md, PUT it, then './scripts/guidance-check.sh --accept-only $slug'"
  done < <(grep '^STALE' <<<"$2" || true)
}

# TP-346: a check belongs to the ticket's own project, so it runs where that project's code is. This
# script lives in the ticketing checkout and used to run every check in it, which for a connected
# project meant a green ticketing gate marking that project's work passed. The order is loop.py's
# pick_repo, for the same reasons: REPO wins, so the loop's own call runs the gate in the checkout it
# just gave the agents and a demo can point this at a scratch clone; then the project's repo_path,
# the only thing that says where a project's code is (rule 22); then this directory, which is what
# the project this script lives in has, since migration 0001 seeds TP with no repo_path.
# `|| true`: an unknown key leaves read non-zero, and the message below says more than `set -e` would.
IFS=$'\t' read -r ticket_id project repo_path \
  < <(q -F $'\t' -c "SELECT t.id, p.key, COALESCE(p.repo_path, '') FROM tickets t
                     JOIN projects p ON p.id = t.project_id WHERE t.key = '$KEY'") || true
[ -n "${ticket_id:-}" ] || { echo "no ticket $KEY in $DB" >&2; exit 2; }
WORK="${REPO:-$repo_path}"
if [ -z "$WORK" ]; then
  WORK="$ROOT"
else
  [ -d "$WORK" ] || { echo "$project's checkout is not a directory on this machine: $WORK" >&2; exit 2; }
  WORK=$(cd "$WORK" && pwd)
fi
[ "$WORK" = "$ROOT" ] || echo "$KEY belongs to $project, checked out at $WORK: its checks run there"

# One check per line: ordinal, expected exit, kind, visibility, then the command (base64, because a
# command is free text and may hold any separator) and the description.
checks=$(q -F $'\t' -c "SELECT ordinal, expect_exit, kind, visibility, encode(convert_to(COALESCE(command, ''), 'UTF8'), 'base64'), description
                        FROM ticket_checks WHERE ticket_id = '$ticket_id' ORDER BY ordinal" | tr -d '\r')

ran=0 failed=0
while IFS=$'\t' read -r ordinal expect kind visibility command64 description; do
  [ -z "$ordinal" ] && continue
  if [ "$kind" != command ]; then
    echo "SKIP  $ordinal. $description (manual: a person decides this one)"
    continue
  fi
  command=$(printf '%s' "$command64" | tr -d '\n' | base64 -d)
  ran=$((ran + 1))
  set +e
  (cd "$WORK" && bash -c "$command") >/dev/null 2>&1
  code=$?
  set -e
  # A held-out command is never printed: the output of this script can end up in a ticket.
  shown=$([ "$visibility" = public ] && printf '%s' "$command" || printf '(held out)')
  if [ "$code" -eq "$expect" ]; then
    echo "PASS  $ordinal. $description — $shown exited $code"
  else
    failed=$((failed + 1))
    echo "FAIL  $ordinal. $description — $shown exited $code, expected $expect"
  fi
done <<< "$checks"

if [ "$ran" -eq 0 ]; then
  echo "$KEY has no command checks; nothing to gate on" >&2
  exit 1
fi
if [ "$WORK" = "$ROOT" ]; then
  while read -r stale; do ran=$((ran + 1)); failed=$((failed + 1)); echo "FAIL  guidance: $stale"; done \
    < <(guidance_moved "$(git -C "$ROOT" merge-base "${GATE_BASE:-main}" HEAD)" "$(cd "$ROOT" && ./scripts/guidance-check.sh 2>&1 || true)")
else
  # guidance-check.sh reads this corpus against this checkout's git history, which says nothing about
  # another project's code: it would fail $project's gate for drift in a repository it has never seen.
  # A connected project's own documents are its own to check (TP-346).
  echo "SKIP  guidance: $project's code is not in this checkout, so this checkout's guidance is not its gate"
fi
passed=$([ "$failed" -eq 0 ] && echo true || echo false)
q -c "UPDATE tickets SET gate_passed = $passed, gate_checked_at = now() WHERE id = '$ticket_id'"
echo
echo "$KEY: gate_passed = $passed ($((ran - failed)) of $ran checks)"
[ "$failed" -eq 0 ]
