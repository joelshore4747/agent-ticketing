#!/usr/bin/env bash
# Loads the hidden acceptance checks into ticket_checks, and conformance probes into guidance_probes.
#
# WHERE THESE LIVE, AND WHY NOT HERE. A check is the answer key to a ticket the agent is about to be
# scored on, and `eval.sh` clones this repository into the agent's working directory. A file under
# docs/ is the first thing anything looks at — and the guided arm is told to read the docs, so an
# in-repo answer key is available to the treatment group more often than to the control. That is the
# worst possible shape for a confound: it inflates exactly the effect being measured. Deleting the
# file from the clone does not help either, because `git clone --mirror` carries the history.
#
# So $EVAL_CHECKS points outside this repository, and this script refuses to run if it does not.
#
# It writes over psql rather than the API on purpose. These tables have no REST, GraphQL or MCP
# surface and must not acquire one: the API has no authentication and the agent knows the port from
# its own MCP config, so any route that serves a check is a check the agent can read. Going straight
# to Postgres also keeps the rows out of ticket_events, which the web app renders as History.
#
# BOOTSTRAP, not sync: a ticket that already has checks is skipped and SAID SO, loudly, with the
# file's hash. A silently skipped edit means the file in your editor and the check being scored have
# diverged, and nothing tells you. --replace is how you change one.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PGURL="${EVAL_PGURL:-postgres://ticketing:ticketing@127.0.0.1:5434}"
DB="${EVAL_SOURCE_DB:-ticketing}"
CHECKS="${EVAL_CHECKS:-$ROOT/../ticketing-eval/checks}"
REPLACE=${1:-}

case "$(realpath -m "$CHECKS")/" in
  "$(realpath "$ROOT")"/*)
    echo "EVAL_CHECKS is inside the repository under test ($CHECKS)." >&2
    echo "The agent's clone would contain the answer key, and the guided arm reads docs/ more." >&2
    exit 2 ;;
esac

[ -d "$CHECKS" ] || { echo "no checks directory at $CHECKS (set EVAL_CHECKS)"; exit 0; }
command -v psql >/dev/null || { echo "psql not on PATH" >&2; exit 2; }

shopt -s nullglob
files=("$CHECKS"/*.toml)
[ ${#files[@]} -gt 0 ] || { echo "no .toml files in $CHECKS"; exit 0; }

for file in "${files[@]}"; do
  sha=$(sha256sum "$file" | cut -c1-12)
  sql=$(python3 - "$file" <<'PY'
import sys, tomllib

# TOML rather than YAML: half of all useful checks are a grep with a regex, and YAML treats `!`,
# `*` and `#` as indicators, so every one of them would need a block scalar. A TOML literal string
# has no escape processing at all — paste the command in and it means what it says.
doc = tomllib.load(open(sys.argv[1], "rb"))
key = doc.get("ticket")
if not key:
    sys.exit(f"{sys.argv[1]}: no `ticket = \"TP-…\"` at the top level")

def lit(v):
    return "NULL" if v is None else "'" + str(v).replace("'", "''") + "'"

rows = []
for i, c in enumerate(doc.get("check", []), start=1):
    if "description" not in c:
        sys.exit(f"{sys.argv[1]}: check {i} has no description")
    kind = "command" if c.get("command") else "manual"
    rows.append(f"({i}, {lit(c['description'])}, {lit(kind)}, {lit(c.get('command'))}, "
                f"{int(c.get('expect_exit', 0))})")
if not rows:
    sys.exit(f"{sys.argv[1]}: no [[check]] blocks")

print(f"""
DO $$
DECLARE tid uuid;
BEGIN
  SELECT id INTO tid FROM tickets WHERE key = {lit(key)};
  IF tid IS NULL THEN
    RAISE EXCEPTION 'no ticket {key} in this database';
  END IF;
  DELETE FROM ticket_checks WHERE ticket_id = tid;
  INSERT INTO ticket_checks (ticket_id, ordinal, description, kind, command, expect_exit)
  SELECT tid, v.ordinal, v.description, v.kind, v.command, v.expect_exit
  FROM (VALUES {','.join(rows)}) AS v(ordinal, description, kind, command, expect_exit);
END $$;""")
print(f"-- ticket {key} :: {len(rows)} check(s)")
PY
  ) || exit 1

  key=$(grep -oP '^-- ticket \K[A-Z0-9]+-[0-9]+' <<<"$sql")
  existing=$(psql "$PGURL/$DB" -qtAX -c "
    SELECT count(*) FROM ticket_checks c JOIN tickets t ON t.id = c.ticket_id WHERE t.key = '$key'" \
    | tr -d '[:space:]')

  if [ "$existing" != 0 ] && [ "$REPLACE" != "--replace" ]; then
    echo "skip    $key ($existing check(s) already in the database; file is $sha)"
    echo "        the file and the scored check may have diverged — --replace to load this one"
    continue
  fi

  psql "$PGURL/$DB" -qtAX -v ON_ERROR_STOP=1 -c "$sql" >/dev/null
  echo "loaded  $key from $(basename "$file") ($sha)"
done
