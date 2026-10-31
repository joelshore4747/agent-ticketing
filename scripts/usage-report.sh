#!/usr/bin/env bash
# Reports what a Claude Code session cost to the ticket it worked (TP-45, built in TP-102).
#
#   ./scripts/usage-report.sh --key TP-42 [--session ID] [--since TS] [--until TS] [--dry-run] <transcript.jsonl>
#
# It sums the transcript's assistant usage — input, output, cache creation and cache read tokens,
# counting a message once even when streaming wrote it across several lines — and the wall time from
# the first timestamp to the last, then posts them to POST /api/tickets/{key}/usage. The transcript
# has no dollar figure, so cost_usd is left out; --session names a claim's session when there were
# several, and --since/--until are RFC3339 bounds parsed as instants, cutting one step out of the
# transcript a whole supervising session shares (TP-296). scripts/loop.sh posts each agent step's
# figures from that step's own log as it ends (TP-182), so the loop's work is measured with nothing
# installed and nobody remembering; every other session reports itself with this.
set -euo pipefail

API="${API:-http://localhost:8080}"
key="" session="" since="" until="" dry="" transcript=""
while [ $# -gt 0 ]; do
  case "$1" in
    --key) key=$2; shift 2 ;;
    --session) session=$2; shift 2 ;;
    --since) since=$2; shift 2 ;;  --until) until=$2; shift 2 ;;
    --dry-run) dry=1; shift ;;
    *) transcript=$1; shift ;;
  esac
done
[ -n "$key" ] && [ -n "$transcript" ] \
  || { echo "usage: usage-report.sh --key KEY [--session ID] [--since TS] [--until TS] [--dry-run] <transcript.jsonl>" >&2; exit 2; }
[ -f "$transcript" ] || { echo "no transcript at '$transcript'" >&2; exit 2; }

body=$(SESSION="$session" SINCE="$since" UNTIL="$until" python3 - "$transcript" <<'PY'
import json, os, sys
from datetime import datetime, timezone

def when(s, field="timestamp"):  # TP-303: compared as text, a +01:00 bound — the instant the
    try:                         # transcript writes with a Z — was above every line and selected none
        return datetime.fromisoformat(s.replace("Z", "+00:00"))
    except ValueError:
        sys.exit(f"--{field} {s!r} is not a time")

usage, stamps = {}, []
since = when(os.environ["SINCE"], "since") if os.environ["SINCE"] else datetime.min.replace(tzinfo=timezone.utc)
until = when(os.environ["UNTIL"], "until") if os.environ["UNTIL"] else datetime.max.replace(tzinfo=timezone.utc)
for line in open(sys.argv[1], encoding="utf-8"):
    try:
        d = json.loads(line)
    except ValueError:
        continue
    if (stamp := d.get("timestamp") or "") and not since <= when(stamp) <= until:
        continue  # outside the bounds: this line counts for nothing, tokens or wall time
    if stamp:
        stamps.append(stamp)
    msg = d.get("message") or {}
    if d.get("type") == "assistant" and msg.get("usage"):
        usage[msg.get("id") or len(usage)] = msg["usage"]  # the last line of a streamed message wins

if not stamps and not usage:
    sys.exit("that window holds no lines; posting zeros would count this claim as measured at nothing")
out = {f: sum(int(u.get(f) or 0) for u in usage.values()) for f in
       ("input_tokens", "output_tokens", "cache_creation_input_tokens", "cache_read_input_tokens")}
out["wall_time_seconds"] = int((when(max(stamps)) - when(min(stamps))).total_seconds()) if len(stamps) > 1 else 0
if os.environ["SESSION"]:
    out["session_id"] = os.environ["SESSION"]
print(json.dumps(out))
PY
)
if [ -n "$dry" ]; then
  echo "would post to $API/api/tickets/$key/usage: $body"
  exit 0
fi
reply=$(curl -s -w '\n%{http_code}' -X POST "$API/api/tickets/$key/usage" -H 'Content-Type: application/json' \
  -H 'X-Actor: usage-report' ${TICKETING_TOKEN:+-H "Authorization: Bearer $TICKETING_TOKEN"} -d "$body")
status=$(tail -n1 <<< "$reply")
[ "$status" = 200 ] || { echo "refused ($status): $(sed '$d' <<< "$reply")" >&2; exit 1; }
sed '$d' <<< "$reply"
