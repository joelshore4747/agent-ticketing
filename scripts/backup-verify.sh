#!/usr/bin/env bash
# The restore test (TP-222): a dump nobody has restored is a hope, not a backup.
#
#   ./scripts/backup-verify.sh                     # the newest dump in BACKUP_DIR
#   ./scripts/backup-verify.sh path/to/one.dump    # that one
#
# It restores the dump into a scratch database, counts the four tables its .counts sidecar names,
# and prints one line, each pair reading restored=dumped:
#
#   restored /…/ticketing-20260929T001122Z.dump: tickets 412=412, decisions 39=39, batches 12=12, projects 2=2: OK
#
# Any mismatch, or a restore pg_restore could not finish, is FAIL and a non-zero exit. scripts/
# backup-loop.sh runs this after every scheduled dump, so `docker compose logs backup` carries the
# same line and the restore test is scheduled rather than remembered.
#
# IT NEVER OPENS THE LIVE DATABASE. The counts come from the scratch database and nothing else, and
# the one connection made outside it is to the `postgres` maintenance database, purely to CREATE and
# DROP the scratch one. That is the whole point: a restore test that could write to `ticketing` would
# be a bigger risk to the tracker's history than the missing backup it was built to rule out.
#
# THE SCRATCH DATABASE GOES AWAY WHATEVER HAPPENS. It is dropped on the way in and again from an EXIT
# trap, so a mismatch, a broken dump, a Ctrl-C or a SIGTERM from `just stop` all leave the cluster as
# they found it. WITH (FORCE) because a connection left behind by a killed run would otherwise keep
# the database alive and make every later check fail on a name that is already taken.
#
# ONE CHECK AT A TIME, and nothing enforces it: two overlapping runs would drop the scratch database
# out from under each other. The schedule's check takes seconds once every six hours, so the cost of
# that race is a loud FAIL, never a wrong OK and never a mark on the live database.
#
# TWO CALLERS, ONE PATH, as in backup.sh: on the host the connection is DATABASE_URL, and inside the
# postgres:16-alpine backup service PGHOST=db and libpq's own environment carries it.
set -euo pipefail

BACKUP_DIR="${BACKUP_DIR:-${HOME}/.local/share/ticketing/backups}"
SCRATCH_DB=ticketing_restore_check
# The tables the counts file names, in the order the line reports them.
TABLES=(tickets decisions batches projects)

# The same connection, pointed at one database or another. With PGHOST set, libpq already knows the
# host, the user and the password, so the bare name is the whole conninfo; with a URL the database is
# the last path segment, and a '?…' tail belongs after the new one rather than inside it.
conn_to() {
  if [ -n "${PGHOST:-}" ]; then
    printf '%s' "$1"
  else
    local url="${DATABASE_URL:-postgres://ticketing:ticketing@127.0.0.1:5434/ticketing}"
    local head="${url%%\?*}"
    printf '%s' "${head%/*}/$1${url:${#head}}"
  fi
}

for tool in pg_restore psql; do
  command -v "$tool" >/dev/null ||
    { echo "$tool not on PATH: install the Postgres client tools, or run this in the backup service, where they ship" >&2; exit 2; }
done

dump="${1:-}"
if [ -z "$dump" ]; then
  # The glob sorts ascending and backup.sh's UTC stamp sorts with it, so the last is the newest.
  shopt -s nullglob
  dumps=("$BACKUP_DIR"/ticketing-*.dump)
  [ ${#dumps[@]} -gt 0 ] ||
    { echo "no dump to restore in $BACKUP_DIR: run ./scripts/backup.sh first" >&2; exit 2; }
  dump="${dumps[-1]}"
fi
counts="${dump%.dump}.counts"
[ -r "$counts" ] ||
  { echo "$dump has no counts file beside it ($counts): there is nothing to check the restore against" >&2; exit 2; }

admin="$(conn_to postgres)"
scratch="$(conn_to "$SCRATCH_DB")"

drop_scratch() {
  psql -qtAX -d "$admin" -c "DROP DATABASE IF EXISTS $SCRATCH_DB WITH (FORCE)" >/dev/null 2>&1 || true
}
trap drop_scratch EXIT
drop_scratch
psql -qtAX -v ON_ERROR_STOP=1 -d "$admin" -c "CREATE DATABASE $SCRATCH_DB" >/dev/null

# pg_restore's own exit status cannot be the verdict, and --exit-on-error would be worse than no
# check at all. The host's client tools are newer than the server compose.yml runs — 18.6 against
# 16.14 as this is written — so pg_dump writes settings into the archive's preamble that the server
# has never heard of (`SET transaction_timeout = 0`, a 17 addition). With --exit-on-error pg_restore
# stops on the first one and restores nothing, so the counts would all read 0 and every dump this
# project takes would look broken; without it every row lands and pg_restore still exits 1. Inside
# the backup service both tools are the image's own 16 and none of this arises.
#
# So the verdict is the row counts below, and what is judged here is only what the counts could not
# see: any pg_restore error that is not that version skew. Unknown wording fails rather than passes.
# --no-owner keeps the check about rows rather than about roles.
restore_log="$(pg_restore --no-owner -d "$scratch" "$dump" 2>&1)" || true
# Tolerated is not hidden: whatever pg_restore said goes to stderr, while stdout carries the verdict.
[ -z "$restore_log" ] || printf '%s\n' "$restore_log" >&2
if grep '^pg_restore: error:' <<<"$restore_log" | grep -qv 'unrecognized configuration parameter'; then
  echo "restored $dump: pg_restore could not finish: FAIL"
  exit 1
fi

query=""
for table in "${TABLES[@]}"; do
  query+="${query:+ UNION ALL }SELECT '$table=' || count(*) FROM $table"
done
# A table that did not come back at all makes this query fail rather than return a low number, and
# that is a FAIL with a line like any other — not a script that dies having printed no verdict.
if ! restored="$(psql -qtAX -v ON_ERROR_STOP=1 -d "$scratch" -c "$query" 2>&1)"; then
  printf '%s\n' "$restored" >&2
  echo "restored $dump: the restored database could not be counted: FAIL"
  exit 1
fi

declare -A want got
while IFS='=' read -r table n; do
  [ -n "$table" ] && want["$table"]="$n"
done <"$counts"
while IFS='=' read -r table n; do
  [ -n "$table" ] && got["$table"]="$n"
done <<<"$restored"

# A table missing from either side reads '?' and can never equal the other, so a counts file that has
# lost a line is a FAIL rather than a silent pass over the table it stopped naming.
line="restored $dump:"
status=OK
for table in "${TABLES[@]}"; do
  line+=" $table ${got[$table]:-?}=${want[$table]:-?},"
  [ "${got[$table]:-?}" = "${want[$table]:-!}" ] || status=FAIL
done
echo "${line%,}: $status"
[ "$status" = OK ]
