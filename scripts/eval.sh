#!/usr/bin/env bash
# A/B runner: does guidance change what an agent does?
#
# One run = one ticket, one arm, one throwaway repo clone, one throwaway database, one `claude -p`.
# Results are one JSON object per line in eval-results.jsonl. There is deliberately no experiments
# table: JSONL is greppable, diffable, needs no migration, and survives this prototype being dropped.
#
# The arm is decided HERE and delivered through request headers, so the agent has no say in — and no
# knowledge of — which arm it is in. A "guided" run that resolved no documents is a bug in this
# script, and is recorded as such rather than folded silently into the control numbers.
#
#   ./scripts/eval.sh --prepare                     # build the template database (dev API must be stopped)
#   ./scripts/eval.sh --tickets TP-12,TP-13 --replicates 3
#
# Reading the numbers: at the N this can realistically run, the honest claims are a MANIPULATION
# CHECK (did guidance change behaviour at all?) and a SAFETY SCREEN (did it make things worse?).
# "Guidance improves quality by X%" is not answerable by any automated metric here.
set -euo pipefail
# Go lives under ~/.local/go rather than system-wide, and this script is not a Justfile recipe, so it
# does not inherit the Justfile's PATH. Without this, `go run` fails loudly at startup and, worse,
# `go test` fails silently while scoring and records tests=0 for both arms (TP-53).
export PATH="$HOME/.local/go/bin:$PATH"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PGURL="${EVAL_PGURL:-postgres://ticketing:ticketing@127.0.0.1:5434}"
TEMPLATE=eval_template
SOURCE_DB="${EVAL_SOURCE_DB:-ticketing}"
WORK="${EVAL_WORK:-${TMPDIR:-/tmp}/ticketing-eval}"
OUT="$ROOT/eval-results.jsonl"
BUNDLES="${EVAL_BUNDLES:-$WORK/bundles}"
# Kept out of the measured diff and out of every review payload. The first is the runner's own file;
# the other two are where an agent could write something that tells a blind reviewer which arm it is.
DIFF_PATHSPEC=(':(exclude).mcp.json' ':(exclude).claude' ':(exclude)CLAUDE.md' ':(exclude)eval'
               ':(exclude)eval-results.jsonl' ':(exclude)eval-reviews.jsonl')
RUN="eval-$(date +%Y%m%d-%H%M%S)"
REPLICATES=3
TICKETS=
BASE=$(git -C "$ROOT" rev-parse HEAD)
ARMS=(control guided)
# Every document in the corpus, unless you name a subset. See the note on process instructions below.
GUIDANCE=

psql_() { psql "$PGURL/postgres" -qtAX -c "$1"; }

while [ $# -gt 0 ]; do
  case $1 in
    --prepare)    PREPARE=1 ;;
    --tickets)    TICKETS=$2; shift ;;
    --replicates) REPLICATES=$2; shift ;;
    --run)        RUN=$2; shift ;;
    --base)       BASE=$2; shift ;;
    --guidance)   GUIDANCE=$2; shift ;;
    --out)        OUT=$2; shift ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
  shift
done

# --- the template -----------------------------------------------------------
# Cloning a database needs no other session connected to the source, so the dev API must be stopped.
# The clone carries the seeded areas, the guidance corpus and the candidate tickets in one step,
# which is the whole reason this is a template clone and not a fixture script that would drift.
if [ "${PREPARE:-}" = 1 ]; then
  psql_ "DROP DATABASE IF EXISTS $TEMPLATE WITH (FORCE)"
  psql_ "CREATE DATABASE $TEMPLATE TEMPLATE $SOURCE_DB"
  echo "template $TEMPLATE built from $SOURCE_DB at $BASE"
  exit 0
fi

[ -n "$TICKETS" ] || { echo "name the tickets under test: --tickets TP-12,TP-13" >&2; exit 2; }
command -v claude >/dev/null || { echo "claude CLI not on PATH" >&2; exit 2; }
command -v go >/dev/null || { echo "go not on PATH: the throwaway API and the scoring both need it" >&2; exit 2; }

# The agent works in a clone checked out at $BASE, which is a COMMIT. Anything uncommitted is not in
# it. With the Go port still untracked, every run would hand the agent the old Rust tree, fail lint
# against a Justfile that does not match it, and produce numbers about nothing.
git -C "$ROOT" diff --quiet && git -C "$ROOT" diff --cached --quiet || {
  echo "working tree is dirty: \$BASE is a commit, and the commit is what the agent gets" >&2; exit 2; }
[ -z "$(git -C "$ROOT" ls-files --others --exclude-standard)" ] || {
  echo "untracked files: the clone will not contain them" >&2
  git -C "$ROOT" ls-files --others --exclude-standard | sed 's/^/  /' >&2; exit 2; }
git -C "$ROOT" cat-file -e "$BASE:api/internal/service/service.go" 2>/dev/null || {
  echo "\$BASE ($BASE) does not contain api/internal/: it is not the code under test" >&2; exit 2; }

MIRROR="$WORK/mirror.git"
mkdir -p "$WORK" "$BUNDLES"
# A bare mirror, not a worktree: worktrees share an object database, so `git log --all` in one arm
# would show the other arm's commits. The clone is --single-branch for the same reason.
rm -rf "$MIRROR"
git clone --quiet --mirror "$ROOT" "$MIRROR"

# --- one run ----------------------------------------------------------------
run_once() {
  local ticket=$1 arm=$2 rep=$3
  local slug="${RUN//[^a-z0-9]/_}_${ticket,,}_${arm}_${rep}"
  local db="eval_${slug//-/_}" repo="$WORK/$slug" port started claim_rows guidance_sha docs
  local bundle="$BUNDLES/$slug"
  local lint=0 tests=0 checks_passed=0 checks_total=0 probes_passed=0 probes_total=0 rc=0
  local cost_usd="null" in_tok=0 out_tok=0 cache_w=0 cache_r=0 discriminates=""
  local status='' resolution='' numstat='0 0'

  # Anything the tracker gained after this instant was filed by the agent, which is how the review
  # bundle knows which follow-up tickets are its doing.
  started=$(date -u +%FT%TZ)

  psql_ "DROP DATABASE IF EXISTS $db WITH (FORCE)" >/dev/null
  psql_ "CREATE DATABASE $db TEMPLATE $TEMPLATE" >/dev/null

  # Attach or detach guidance from OUTSIDE the agent's reach. Control is not "no batch": it is the
  # same batch with nothing attached, so the two arms differ in exactly one thing.
  local batch
  batch=$(psql "$PGURL/$db" -qtAX -c "
    WITH b AS (
      INSERT INTO batches (project_id, kind, actor, title, brief)
      SELECT project_id, 'plan', '$RUN', 'evaluation', ''
      FROM tickets WHERE key = '$ticket' RETURNING id
    ), t AS (
      UPDATE tickets SET batch_id = (SELECT id FROM b) WHERE key = '$ticket'
    ) SELECT id FROM b" | tr -d '[:space:]')
  if [ "$arm" = guided ]; then
    psql "$PGURL/$db" -qtAX -c "
      INSERT INTO batch_guidance (batch_id, document_id, revision_id, position)
      SELECT '$batch', id, current_revision_id, weight FROM guidance_documents
      WHERE archived_at IS NULL $( [ -n "$GUIDANCE" ] && echo "AND slug = ANY('{${GUIDANCE//,/,}}')" )" >/dev/null
  fi

  port=$(python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()')
  # env -u: its own eval database holds no credentials, so it runs in local mode and reads the actor from X-Actor whatever Joel has exported for the live API (TP-316).
  ( cd "$ROOT/api" && env -u TICKETING_TOKENS DATABASE_URL="$PGURL/$db" PORT="$port" go run . >"$WORK/$slug.api.log" 2>&1 ) &
  local api=$!
  # Anything that ends the script from here on — a failed assertion, Ctrl-C, the guided-run abort
  # below — would otherwise leave an API process holding a port and an eval database behind.
  #
  # Global mirrors, not the locals above, because the trap reads them. Confirmed the hard way: when
  # `set -e` aborts a function on a failing command, its `local` variables are ALREADY torn down by
  # the time the EXIT trap fires, even though the trap was registered inside that same function —
  # referencing them there throws "unbound variable" and the cleanup commands after it never run.
  # Reproduced in isolation before this fix; it is exactly what let one real run leak both an API
  # process and an eval database on its first crash.
  CLEANUP_API=$api
  CLEANUP_DB=$db
  # WITH (FORCE) (Postgres 13+, confirmed 16.14 here) rather than a plain DROP: a second real run
  # left an idle connection to its own throwaway database even after `kill "$api"`, most likely a
  # pgx pool connection outliving the killed process across the docker-forwarded port, and a plain
  # DROP DATABASE refuses while any connection remains. FORCE terminates and drops regardless of
  # why the connection is still there, rather than depending on a diagnosis of that race.
  trap 'kill "$CLEANUP_API" 2>/dev/null || true; psql_ "DROP DATABASE IF EXISTS $CLEANUP_DB WITH (FORCE)" >/dev/null 2>&1 || true' EXIT

  # A silent timeout here is not "proceed and hope" — the agent still has a live MCP config
  # pointing at this port whether or not anything answers, and will simply work around a dead
  # endpoint by finding another way in (confirmed: it started its own dev stack against the REAL
  # database and claimed a real ticket for real, entirely outside this sandbox). Refusing loudly
  # here is what stops that ticket's data from ever being a "result" in the first place.
  healthy=0
  for _ in $(seq 40); do
    curl -sf "http://127.0.0.1:$port/healthz" >/dev/null 2>&1 && { healthy=1; break; }
    sleep 0.5
  done
  if [ "$healthy" != 1 ]; then
    echo "  FATAL: the throwaway API on :$port never became healthy for $ticket $arm #$rep" >&2
    echo "  --- $WORK/$slug.api.log ---" >&2
    tail -20 "$WORK/$slug.api.log" >&2 2>/dev/null || true
    exit 4
  fi

  git clone --quiet --single-branch --no-tags "$MIRROR" "$repo"
  git -C "$repo" -c advice.detachedHead=false checkout --quiet "$BASE"
  # The reviewer rubrics are committed to this repo, so the clone carries them. They contain no
  # answers — the answer key lives outside the repository entirely — but an agent reading how its
  # output will be judged is not doing the ticket, it is doing the rubric.
  rm -rf "$repo/eval"

  # The headers are the experiment's only channel for the arm. They are read from the transport, so
  # the agent cannot forget them, misreport them, or discover them by reading its own tool arguments.
  # Written OUTSIDE the clone. Inside it, `cat .mcp.json` is one tool call and the agent reads its
  # own arm out of the header block — the comment above is true of tool arguments and was false of
  # the filesystem. Claude Code's file tools are confined to cwd plus --add-dir, and $WORK is
  # neither, so the config is reachable by the CLI and not by the agent.
  cat > "$WORK/$slug.mcp.json" <<JSON
{ "mcpServers": { "ticketing": { "type": "http", "url": "http://127.0.0.1:$port/mcp",
    "headers": { "X-Eval-Run": "$RUN", "X-Eval-Arm": "$arm", "X-Eval-Model": "${EVAL_MODEL:-default}" } } } }
JSON

  # Deliberately bare: naming the ticket and nothing else. Any instruction added here is an
  # instruction BOTH arms get, which is how an experiment silently stops measuring anything.
  #
  # stream-json, not the default text format. Verified live: default `claude -p` stdout carries NO
  # trace of tool activity at all — only the final response text — so the ran_lint/ran_tests greps
  # below only ever matched an agent's closing SUMMARY happening to mention "just lint" in prose,
  # never whether it actually ran. stream-json's JSONL retains the real tool_use commands (the grep
  # still matches a JSON string like "command":"just lint" the same way it matched plain text) and
  # its final `type:"result"` line carries total_cost_usd and the full usage object, which nothing
  # here has ever captured. --verbose is not optional: the CLI refuses stream-json in print mode
  # without it. </dev/null: unpiped print-mode stdin costs a verified ~3s stall per call.
  ( cd "$repo" && timeout "${EVAL_TIMEOUT:-1800}" claude -p \
      "Claim the ticket $ticket from the ticketing MCP server and do the work it describes." \
      --mcp-config "$WORK/$slug.mcp.json" --strict-mcp-config --dangerously-skip-permissions \
      --output-format stream-json --verbose < /dev/null \
      >"$WORK/$slug.agent.log" 2>&1 ) || true

  # --- cost and usage, from the stream-json log's final `type:"result"` line -----------------
  # The only record of what this call actually cost. Scanned from the tail rather than assumed to
  # be the literal last line, since a truncated or empty log (the process was killed by `timeout`)
  # must not crash the run — it should just report as no data, same as a run that never claimed.
  read -r cost_usd in_tok out_tok cache_w cache_r < <(python3 "$ROOT/scripts/lib/stream_usage.py" "$WORK/$slug.agent.log" 2>/dev/null || echo "null 0 0 0 0")

  # --- assertions before scoring --------------------------------------------
  # An agent that never claimed never received guidance and never wrote a claim row, so a "guided"
  # run without one is a control wearing the wrong label. Score it as invalid, never as a result.
  claim_rows=$(psql "$PGURL/$db" -qtAX -c "
    SELECT count(*) FROM ticket_claims c JOIN tickets t ON t.id = c.ticket_id
    WHERE t.key = '$ticket' AND c.run_tag = '$RUN'" | tr -d '[:space:]')
  # c.claimed_at, not bare claimed_at: both ticket_claims and tickets have a column by that name,
  # and Postgres refuses to guess which one an unqualified ORDER BY means. Found on the first real
  # run this ever executed against a ticket with a real claim row — self-tests never exercise this
  # query at all, since they bypass eval.sh's own database queries entirely.
  guidance_sha=$(psql "$PGURL/$db" -qtAX -c "
    SELECT coalesce(guidance_sha256, '') FROM ticket_claims c JOIN tickets t ON t.id = c.ticket_id
    WHERE t.key = '$ticket' AND c.run_tag = '$RUN' ORDER BY c.claimed_at LIMIT 1" | tr -d '[:space:]')
  docs=$(psql "$PGURL/$db" -qtAX -c "
    SELECT count(*) FROM ticket_claim_documents d JOIN ticket_claims c ON c.id = d.claim_id
    JOIN tickets t ON t.id = c.ticket_id WHERE t.key = '$ticket' AND c.run_tag = '$RUN'" | tr -d '[:space:]')

  # One guided run that resolved nothing means the attach step is broken, and every guided run
  # after it is a control wearing the wrong label. Recording it as invalid and carrying on burns the
  # budget before anyone notices.
  if [ "$arm" = guided ] && [ "$docs" = 0 ]; then
    echo "  FATAL: guided run for $ticket resolved no documents; the attach step is broken" >&2
    exit 3
  fi

  read -r status resolution < <(psql "$PGURL/$db" -qtAX -F$'\t' -c "
    SELECT status, coalesce(length(resolution), 0) FROM tickets WHERE key = '$ticket'")

  # --- scoring ---------------------------------------------------------------
  ( cd "$repo" && just lint >"$WORK/$slug.lint.log" 2>&1 ) && lint=1
  # `just test` directly, not the `test: db` recipe: `db` runs `docker compose up -d --wait db`,
  # and compose.yml hardcodes `container_name: ticketing-db` — every throwaway clone's compose
  # tries to create a container under that exact same name and collides with the real, persistent
  # dev container, which is normally already running. Confirmed: this was silently failing BOTH
  # arms of the first real run this harness ever completed, identically, which would have read as
  # "guidance made no difference to test outcomes" when it was actually testing nothing at all.
  # Postgres is already guaranteed reachable by eval.sh's own setup, so `db` is not needed here.
  ( ( cd "$repo/api" && go test ./... ) && ( cd "$repo/web" && npm test -- --run ) \
    ) >"$WORK/$slug.test.log" 2>&1 && tests=1

  # Did the agent run the checker itself? Only the guided arm is told to, so "guided passes lint
  # more often" may be measuring the instruction rather than the code. The runs that never invoked
  # it are the only clean comparison, and this is what lets the report separate the two.
  local ran_lint=0 ran_tests=0
  grep -qE '(^|[^a-z-])just lint([^a-z-]|$)' "$WORK/$slug.agent.log" 2>/dev/null && ran_lint=1
  grep -qE '(^|[^a-z-])just test([^a-z-]|$)' "$WORK/$slug.agent.log" 2>/dev/null && ran_tests=1

  # Acceptance checks are hidden from MCP on purpose: an agent that can read the test it will be
  # scored against is not being measured.
  while IFS=$'\t' read -r command expect; do
    [ -z "$command" ] && continue
    checks_total=$((checks_total + 1))
    # rc captured explicitly: under `set -e` a bare subshell that exits non-zero ends the script,
    # so the first check with expect_exit=1 — the shape of "assert this is NOT in the diff", the
    # most useful kind — would abort the run and leak the API process and the database.
    rc=0; ( cd "$repo" && eval "$command" >/dev/null 2>&1 ) || rc=$?
    [ "$rc" = "$expect" ] && checks_passed=$((checks_passed + 1))
  done < <(psql "$PGURL/$db" -qtAX -F$'\t' -c "
    SELECT command, expect_exit FROM ticket_checks c JOIN tickets t ON t.id = c.ticket_id
    WHERE t.key = '$ticket' AND c.kind = 'command' ORDER BY ordinal")

  while IFS=$'\t' read -r command expect; do
    [ -z "$command" ] && continue
    probes_total=$((probes_total + 1))
    rc=0; ( cd "$repo" && eval "$command" >/dev/null 2>&1 ) || rc=$?
    [ "$rc" = "$expect" ] && probes_passed=$((probes_passed + 1))
  done < <(psql "$PGURL/$db" -qtAX -F$'\t' -c "
    -- Selected from the documents that WOULD have been attached, identically in both arms. Joining
    -- through ticket_claim_documents made probes_total structurally 0 for control, and 7/9 against
    -- 0/0 is not a comparison: conformance needs the counterfactual to mean anything.
    SELECT p.command, p.expect_exit FROM guidance_probes p
    JOIN guidance_documents d ON d.current_revision_id = p.revision_id
    WHERE d.archived_at IS NULL
      AND d.project_id = (SELECT project_id FROM tickets WHERE key = '$ticket')
      $( [ -n "$GUIDANCE" ] && echo "AND d.slug = ANY('{${GUIDANCE//,/,}}')" )")

  # Measured from the working tree, not from HEAD. `working-a-ticket` says "one ticket, one branch,
  # one push" and is served ONLY to the guided arm, so scoring committed work alone would credit a
  # guided agent for committing and score an uncommitted control arm 0 0 for work it actually did.
  # The arm must not be able to change what the measurement can see.
  git -C "$repo" add -A >/dev/null 2>&1 || true
  numstat=$(git -C "$repo" diff --numstat --cached "$BASE" -- "${DIFF_PATHSPEC[@]}" \
    | awk '{a+=$1;d+=$2} END {print (a+0), (d+0)}')

  # --- the review bundle -----------------------------------------------------
  # Everything a blind reviewer is allowed to see, plus meta.json which it never is. The clone is
  # reused by later runs' cleanup and the database is dropped below, so this is the only moment the
  # work still exists.
  mkdir -p "$bundle"
  git -C "$repo" diff --cached "$BASE" -- "${DIFF_PATHSPEC[@]}" > "$bundle/diff.patch"

  # Opt-in: eval-discriminates.sh runs the whole Postgres-backed suite TWICE more (tests alone, then
  # the whole patch) to prove a test actually pins the behaviour rather than passing either way.
  # That's real wall-clock cost on top of the `just test` already run above, so it stays off unless
  # asked for, rather than silently 3x-ing every run's runtime.
  if [ "${EVAL_DISCRIMINATES:-}" = 1 ] && [ -s "$bundle/diff.patch" ]; then
    discriminates=$("$ROOT/scripts/eval-discriminates.sh" \
      --patch "$bundle/diff.patch" --base "$BASE" 2>"$WORK/$slug.discriminates.log" || echo none)
  fi

  psql "$PGURL/$db" -qtAX -c "
    SELECT json_build_object(
      'key', t.key, 'title', t.title, 'description', t.description,
      'type', t.type, 'priority', t.priority,
      'area', (SELECT a.name FROM areas a WHERE a.id = t.area_id),
      'locations', (SELECT coalesce(json_agg(json_build_object(
                      'file_path', l.file_path, 'line_start', l.line_start) ORDER BY l.file_path), '[]'::json)
                    FROM ticket_locations l WHERE l.ticket_id = t.id))
    FROM tickets t WHERE t.key = '$ticket'" > "$bundle/ticket.json"

  # pr_url is not a ticket column — PRs are their own table (link_pull_request writes there, not
  # onto tickets), found the same way as the pr_url column itself: this query had never run for
  # real until tonight. The most recent one is "the" PR for the bundle's purposes.
  psql "$PGURL/$db" -qtAX -c "
    SELECT json_build_object(
      'status', t.status, 'resolution', t.resolution, 'branch', t.branch,
      'pr_url', (SELECT p.url FROM pull_requests p WHERE p.ticket_id = t.id
                 ORDER BY p.created_at DESC LIMIT 1),
      'comments', (SELECT coalesce(json_agg(json_build_object(
                     'author', c.author, 'kind', c.kind, 'body', c.body) ORDER BY c.created_at), '[]'::json)
                   FROM comments c WHERE c.ticket_id = t.id),
      'filed', (SELECT coalesce(json_agg(json_build_object(
                  'key', x.key, 'title', x.title, 'type', x.type, 'priority', x.priority,
                  'area', (SELECT a.name FROM areas a WHERE a.id = x.area_id)) ORDER BY x.created_at), '[]'::json)
                FROM tickets x WHERE x.created_at > '$started' AND x.key <> t.key))
    FROM tickets t WHERE t.key = '$ticket'" > "$bundle/trail.json"

  python3 -c '
import json, sys
print(json.dumps({"run": sys.argv[1], "ticket": sys.argv[2], "arm": sys.argv[3],
                  "replicate": int(sys.argv[4]), "base": sys.argv[5], "slug": sys.argv[6]}))
' "$RUN" "$ticket" "$arm" "$rep" "$BASE" "$slug" > "$bundle/meta.json"

  kill "$api" 2>/dev/null || true
  wait "$api" 2>/dev/null || true

  python3 -c '
import json, sys
added, deleted = sys.argv[13].split()
cost = None if sys.argv[21] == "null" else float(sys.argv[21])
row = {
  "run": sys.argv[1], "ticket": sys.argv[2], "arm": sys.argv[3], "replicate": int(sys.argv[4]),
  "base": sys.argv[5],
  # valid=false means the run tells us nothing about guidance, whatever its other numbers say.
  "valid": int(sys.argv[6]) > 0 and (sys.argv[3] != "guided" or int(sys.argv[8]) > 0),
  "claims": int(sys.argv[6]), "guidance_sha256": sys.argv[7] or None, "documents_served": int(sys.argv[8]),
  "completed": sys.argv[9] == "done" and int(sys.argv[10]) > 0,
  "status": sys.argv[9], "lint": sys.argv[11] == "1", "tests": sys.argv[12] == "1",
  "lines_added": int(added), "lines_deleted": int(deleted),
  "checks": [int(sys.argv[14]), int(sys.argv[15])], "probes": [int(sys.argv[16]), int(sys.argv[17])],
  "bundle": sys.argv[18],
  "ran_lint": sys.argv[19] == "1", "ran_tests": sys.argv[20] == "1",
  "cost_usd": cost,
  "input_tokens": int(sys.argv[22]), "output_tokens": int(sys.argv[23]),
  "cache_creation_input_tokens": int(sys.argv[24]), "cache_read_input_tokens": int(sys.argv[25]),
}
if sys.argv[26]:
    row["test_discriminates"] = sys.argv[26]
print(json.dumps(row))' "$RUN" "$ticket" "$arm" "$rep" "$BASE" "$claim_rows" "$guidance_sha" "$docs" \
    "$status" "$resolution" "$lint" "$tests" "$numstat" \
    "$checks_passed" "$checks_total" "$probes_passed" "$probes_total" "$bundle" \
    "$ran_lint" "$ran_tests" "$cost_usd" "$in_tok" "$out_tok" "$cache_w" "$cache_r" "$discriminates" \
    >> "$OUT"

  echo "  $ticket $arm #$rep: claims=$claim_rows docs=$docs status=$status lint=$lint tests=$tests"
  psql_ "DROP DATABASE IF EXISTS $db WITH (FORCE)" >/dev/null
  trap - EXIT
}

for rep in $(seq 1 "$REPLICATES"); do
  IFS=, read -ra keys <<< "$TICKETS"
  for ticket in "${keys[@]}"; do
    # Randomised so that "first" and "guided" are never the same thing — otherwise anything that
    # drifts over a session (a warm cache, a rate limit) lands entirely on one arm.
    if [ $((RANDOM % 2)) = 0 ]; then ARMS=(control guided); else ARMS=(guided control); fi
    for arm in "${ARMS[@]}"; do run_once "$ticket" "$arm" "$rep"; done
  done
done

echo
echo "results appended to $OUT"
python3 - "$OUT" "$RUN" <<'PY'
import json, sys
rows = [json.loads(l) for l in open(sys.argv[1]) if json.loads(l)["run"] == sys.argv[2]]
for arm in ("control", "guided"):
    a = [r for r in rows if r["arm"] == arm and r["valid"]]
    invalid = len([r for r in rows if r["arm"] == arm and not r["valid"]])
    if not a:
        print(f"{arm:8} no valid runs ({invalid} invalid)")
        continue
    print(f"{arm:8} n={len(a)} completed={sum(r['completed'] for r in a)} "
          f"tests={sum(r['tests'] for r in a)} lint={sum(r['lint'] for r in a)} "
          f"checks={sum(r['checks'][0] for r in a)}/{sum(r['checks'][1] for r in a)} invalid={invalid}")
print("\nThese are a manipulation check and a safety screen, not an effect size.")
PY
