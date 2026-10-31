#!/usr/bin/env bash
# A pg_dump of the tracker, beside a counts file that says exactly what is in it (TP-220).
#
#   ./scripts/backup.sh                                       # -> $BACKUP_DIR/ticketing-<UTC>.dump
#   BACKUP_DIR=/tmp/scratch BACKUP_KEEP=2 ./scripts/backup.sh
#
# RETENTION: the newest BACKUP_KEEP dumps are kept, default 14, and a pruned dump is deleted together
# with its counts file. That rule is stated here and in the Justfile comment, nowhere else.
#
# WHY THE COUNTS, AND WHY THE SNAPSHOT. ticketing-<stamp>.counts holds the row counts for batches,
# decisions, projects and tickets, so that a restore (scripts/backup-verify.sh) can be checked
# against what was dumped rather than against the database as it has become by then. For that
# comparison to mean anything, the counts and the dump have to see the same rows: one psql session
# opens a REPEATABLE READ transaction and exports its snapshot, pg_dump joins that snapshot with
# --snapshot, and the counts are read inside the same still-open transaction. Counted just before or
# just after instead, they would describe a moment the dump does not, and a later mismatch could
# never be told apart from a write that landed in between.
#
# TWO CALLERS, ONE PATH. On the host, the client tools reach Postgres through DATABASE_URL. Inside the
# postgres:16-alpine backup service (TP-221) PGHOST=db, and libpq's own environment carries the
# connection. That is the only difference between the two, and it is decided once, below.
set -euo pipefail

BACKUP_DIR="${BACKUP_DIR:-${HOME}/.local/share/ticketing/backups}"
BACKUP_KEEP="${BACKUP_KEEP:-14}"

# An empty CONN means libpq's PGHOST/PGUSER/PGPASSWORD already say where to connect.
if [ -n "${PGHOST:-}" ]; then
  CONN=()
else
  CONN=("${DATABASE_URL:-postgres://ticketing:ticketing@127.0.0.1:5434/ticketing}")
fi

for tool in pg_dump psql; do
  command -v "$tool" >/dev/null ||
    { echo "$tool not on PATH: install the Postgres client tools, or run this in the backup service, where they ship" >&2; exit 2; }
done

mkdir -p "$BACKUP_DIR"

# A second-resolution stamp is unique only if we wait for the next second. Sharing a name would mean
# one dump overwriting another, leaving the retention rule to count one backup where there are two —
# the very thing it exists to prevent — so this waits rather than adding an unsortable suffix.
name="$BACKUP_DIR/ticketing-$(date -u +%Y%m%dT%H%M%SZ)"
while [ -e "$name.dump" ]; do
  sleep 1
  name="$BACKUP_DIR/ticketing-$(date -u +%Y%m%dT%H%M%SZ)"
done

# The transaction the dump and the counts share. It stays open until psql reaches \q below.
coproc SNAPSHOT { psql -qtAX -v ON_ERROR_STOP=1 "${CONN[@]}"; }
printf '%s\n' "BEGIN TRANSACTION ISOLATION LEVEL REPEATABLE READ;" "SELECT pg_export_snapshot();" >&"${SNAPSHOT[1]}"
IFS= read -r -t 60 -u "${SNAPSHOT[0]}" snapshot ||
  { echo "psql exported no snapshot, so the counts cannot be tied to the dump" >&2; exit 1; }

pg_dump --format=custom --snapshot="$snapshot" "${CONN[@]}" >"$name.dump"

printf '%s\n' "\\o '$name.counts'" \
  "SELECT 'batches=' || count(*) FROM batches
   UNION ALL SELECT 'decisions=' || count(*) FROM decisions
   UNION ALL SELECT 'projects=' || count(*) FROM projects
   UNION ALL SELECT 'tickets=' || count(*) FROM tickets ORDER BY 1;" \
  "\\o" "\\q" >&"${SNAPSHOT[1]}"
wait "$SNAPSHOT_PID"

echo "wrote $name.dump ($(wc -c <"$name.dump") bytes): $(tr '\n' ' ' <"$name.counts")"

# Prune. The glob sorts ascending and the UTC stamp sorts with it, so the oldest dumps come first.
shopt -s nullglob
dumps=("$BACKUP_DIR"/ticketing-*.dump)
for ((i = 0; i < ${#dumps[@]} - BACKUP_KEEP; i++)); do
  rm -f "${dumps[i]}" "${dumps[i]%.dump}.counts"
  echo "pruned ${dumps[i]} (keeping the newest $BACKUP_KEEP)"
done
