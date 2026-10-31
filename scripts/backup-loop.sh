#!/usr/bin/env bash
# The schedule behind the dump (TP-221): backup.sh now, then again every BACKUP_INTERVAL seconds.
#
#   BACKUP_INTERVAL=30 ./scripts/backup-loop.sh        # a dump now, another in 30s, until stopped
#
# THE DEFAULT IS SIX HOURS, and it is stated here only: compose.yml's `backup` service passes an
# override through without restating it. That service is what actually runs this, and `just db`
# starts that service — so dev, test and loop each leave a scheduled dump behind them and nobody has
# to remember `just backup`. The loop lives here rather than inline in compose.yml so that the
# schedule can be run and tested for real without Docker (scripts/test_backup.py runs it at
# BACKUP_INTERVAL=1); backup.sh is its sibling and is found beside it, in the checkout or in /scripts.
#
# backup.sh prints the name of each dump it writes and backup-verify.sh the restore test that proves
# it, so the service's log reads: what was dumped, that it came back whole, when the next one is due,
# repeat.
#
# A FAILED DUMP DOES NOT END THE SCHEDULE. The tracker's whole history is in one Docker volume, so a
# database that was restarting, or a disk that was full for a minute, must not leave the backup
# silently stopped until someone thinks to look. Hence no `set -e`, and the sleep is waited on rather
# than run in the foreground, so a SIGTERM from `just stop` lands immediately instead of ten seconds
# later as a SIGKILL — which, arriving mid-dump, is how a half-written .dump would get left behind.
set -uo pipefail
export TZ=UTC
trap 'exit 0' INT TERM

interval="${BACKUP_INTERVAL:-21600}"
# Checked rather than trusted: `sleep abc` fails instantly, which would turn the schedule into a
# dump as fast as the disk allows, churning the retention rule's fourteen files over and over.
[[ "$interval" =~ ^[0-9]+$ ]] ||
  { echo "BACKUP_INTERVAL must be a whole number of seconds, not '$interval'" >&2; exit 2; }

while :; do
  if dumped="$(bash "$(dirname "$0")/backup.sh")"; then
    printf '%s\n' "$dumped"
    # Every dump is proven by restoring it, not by having been written (TP-222), so the restore test
    # is scheduled too and `docker compose logs backup` says so after each one.
    #
    # THE DUMP JUST WRITTEN, BY NAME, rather than whatever backup-verify.sh would pick as newest.
    # BACKUP_DIR is shared with the host, so a `just backup` landing between the two would otherwise
    # have this line claim to have checked a dump it never opened — and the host's client tools are
    # not the service's, so that dump may well be one this pg_restore cannot read at all. Falling
    # back to the newest if the name cannot be read keeps a check happening either way.
    args=()
    [[ "$dumped" =~ wrote\ ([^[:space:]]+\.dump) ]] && args=("${BASH_REMATCH[1]}")
    # A failed check does not end the schedule: the FAIL line is what a human needs, and the next
    # dump may well be the good one — stopping here replaces one unproven backup with none at all.
    bash "$(dirname "$0")/backup-verify.sh" "${args[@]}" && echo "restore check OK" ||
      echo "restore check FAILED (exit $?) — the schedule continues"
  else
    echo "backup failed (exit $?) — the schedule continues"
  fi
  printf -v now '%(%s)T' -1
  printf 'next backup at %(%Y-%m-%dT%H:%M:%SZ)T (in %ss)\n' "$((now + interval))" "$interval"
  sleep "$interval" &
  wait $!
done
