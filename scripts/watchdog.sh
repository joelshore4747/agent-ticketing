#!/usr/bin/env bash
# Keeps the loops running without a Claude session watching them. A systemd user timer runs it every
# 15 minutes; it spends no tokens itself, only the loops it starts do.
#
#   ./scripts/watchdog.sh           check every track once: start a stopped one if it can go on
#   ./scripts/watchdog.sh status    say what each track is doing, and change nothing
#
# A track is one line of $TRACKS: `name project roadmap repo`. Deleting or commenting out its line is
# how to stop a track for good; `systemctl --user stop loop-<name>` alone is undone at the next tick.
#
# For a track whose loop is not running, it decides from the end of the track's last run log:
#   - no stop line (the process was killed, or the machine slept), or a planned pause: start it again;
#   - any other stop: start it again once, because something may have changed; if the same stop
#     comes back, leave it and tell Joel.
# Before any start, `loop.sh --dry-run` (no agent, no tokens) must name a step to work. If it says
# stop (waiting on Joel, the seven-day usage window spent), nothing starts and Joel is told once.
# A spent five-hour window is not a stop: the loop sleeps until it resets and carries on by itself.
set -euo pipefail
cd "$(dirname "$0")/.."

STATE="${XDG_STATE_HOME:-$HOME/.local/state}/ticketing/watchdog"
TRACKS="${WATCHDOG_TRACKS:-${XDG_CONFIG_HOME:-$HOME/.config}/ticketing/watchdog-tracks}"
export PATH="$HOME/.local/go/bin:$HOME/.local/node/bin:$HOME/.local/bin:$PATH"
export LOOP_USAGE_LIMIT="${LOOP_USAGE_LIMIT:-0.9}"
mkdir -p "$STATE"

say() { echo "[$(date '+%F %T')] $*" | tee -a "$STATE/watchdog.log"; }

# Tells Joel once per distinct message: on the desktop and in the log.
tell_joel() {
  local name=$1 message=$2
  [ "$(cat "$STATE/$name.told" 2>/dev/null)" = "$message" ] && return 0
  echo "$message" >"$STATE/$name.told"
  say "$name: NEEDS YOU: $message"
  notify-send --urgency=critical "Ticketing: $name stopped" "$message" 2>/dev/null || true
}

# The tracker and its database; a reboot leaves both down (the container has no restart policy).
ensure_api() {
  curl -sf -m 3 localhost:8080/api/version >/dev/null && return 0
  say "api: down, starting it"
  docker start ticketing-db >/dev/null
  systemctl --user is-active -q ticketing-api \
    || systemd-run --user --quiet --collect --unit=ticketing-api -p WorkingDirectory="$PWD" \
      --setenv=PATH="$PATH" ./scripts/dev.sh api
  for _ in $(seq 1 45); do curl -sf -m 3 localhost:8080/api/version >/dev/null && return 0; sleep 2; done
  say "api: did not come up; see journalctl --user -u ticketing-api"
  return 1
}

# A loop working this roadmap, whoever started it (this script, a Claude session, a terminal).
running() {
  local pid
  for pid in $(pgrep -f 'scripts/loop.sh' || true); do
    tr '\0' '\n' <"/proc/$pid/environ" 2>/dev/null | grep -qx "LOOP_ROADMAP=$1" && return 0
  done
  return 1
}

# The stop or pause line the track's last run ended on; empty when it ended without one.
last_stop() {
  grep -E '^loop: (stopped|paused):' "$STATE/$1.log" 2>/dev/null | tail -1 || true
}

check_track() {
  local name=$1 project=$2 roadmap=$3 repo=$4
  if running "$roadmap"; then echo "$name: running"; return; fi

  local stop next
  stop=$(last_stop "$name")
  case "$stop" in
    "" | "loop: paused:"* | *"the next run works it"*) ;;
    *)
      if [ "$(cat "$STATE/$name.retried" 2>/dev/null)" = "$stop" ]; then
        tell_joel "$name" "${stop#loop: stopped: } (already retried once; log: $STATE/$name.log)"
        return
      fi
      echo "$stop" >"$STATE/$name.retried"
      ;;
  esac

  next=$(LOOP_ROADMAP=$roadmap REPO=$repo ./scripts/loop.sh --dry-run --project "$project" 2>&1 \
    | grep -m1 -E '^(\[|loop: stopped:)' || true)
  case "$next" in
    "loop: stopped:"* | *"] stop "*)
      tell_joel "$name" "${next#*— }"
      return ;;
    "") tell_joel "$name" "its dry run named no step; run: LOOP_ROADMAP=$roadmap REPO=$repo ./scripts/loop.sh --dry-run --project $project"
      return ;;
  esac

  # Each run gets a fresh log, so last_stop never reads an older run's ending.
  [ -f "$STATE/$name.log" ] && mv "$STATE/$name.log" "$STATE/$name.prev.log"
  rm -f "$STATE/$name.told"
  say "$name: starting (${stop:-no stop line}); next: ${next#*] }"
  # An API the loop restarts is its child and ends with it; ensure_api starts it again at the next tick.
  # The machine does not sleep while a loop runs: a suspended usage-reset sleep oversleeps by hours.
  systemd-run --user --quiet --collect --unit="loop-$name" -p WorkingDirectory="$PWD" \
    --setenv=PATH="$PATH" --setenv=LOOP_ROADMAP="$roadmap" --setenv=REPO="$repo" \
    --setenv=LOOP_USAGE_LIMIT="$LOOP_USAGE_LIMIT" \
    systemd-inhibit --what=sleep:idle --who=ticketing-watchdog --why="loop $name is working" \
    bash -c 'exec ./scripts/loop.sh --project "$1" >"$2" 2>&1' _ "$project" "$STATE/$name.log"
}

status_track() {
  local name=$1 roadmap=$3
  if running "$roadmap"; then
    echo "$name: running; last step: $({ grep -E '^\[' "$STATE/$name.log" 2>/dev/null || true; } | tail -1 | cut -c1-140)"
  else
    echo "$name: not running; ended: $(last_stop "$name" | cut -c1-160)"
    [ -f "$STATE/$name.told" ] && echo "  waiting on Joel: $(cat "$STATE/$name.told")"
  fi
  return 0
}

[ -f "$TRACKS" ] || { echo "watchdog: no tracks file at $TRACKS (lines: name project roadmap repo)" >&2; exit 1; }
action=check_track
[ "${1:-}" = status ] && action=status_track
[ "$action" = check_track ] && { ensure_api || exit 1; }
grep -vE '^\s*(#|$)' "$TRACKS" | while read -r name project roadmap repo; do
  "$action" "$name" "$project" "$roadmap" "${repo/#\~/$HOME}"
done
