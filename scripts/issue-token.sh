#!/usr/bin/env bash
# Issue one actor's bearer credential (rule 10).
#
#   ./scripts/issue-token.sh strategist            # print the token, record its hash
#   ./scripts/issue-token.sh joel --human          # a human credential: the Only Joel moves need one
#   ./scripts/issue-token.sh narrator --save       # also leave it where loop.sh looks (TP-316)
#
# The token is printed once and never stored in plain text unless you ask: the file the API reads
# holds sha256 hashes, so a leaked credentials file is not a set of working tokens. Running this for
# real is Joel's (TP-315) — the API's tests issue throwaway credentials into temp files instead.
set -euo pipefail
actor="${1:?usage: issue-token.sh <actor> [--human] [--save]}"; shift
human="" save=""
for arg in "$@"; do
  case "$arg" in
    --human) human=" human" ;;
    --save) save=1 ;;
    *) echo "issue-token.sh: unknown argument $arg" >&2; exit 2 ;;
  esac
done
file="${TICKETING_TOKENS:-$HOME/.config/ticketing/tokens}"
token=$(python3 -c 'import secrets; print(secrets.token_urlsafe(32))')
mkdir -p "$(dirname "$file")"
printf '%s %s%s\n' "$(printf %s "$token" | sha256sum | cut -d' ' -f1)" "$actor" "$human" >> "$file"
chmod 600 "$file"
echo "$actor: hash recorded in $file. Restart the API to load it (the file is read once at startup)." >&2
if [ -n "$save" ]; then
  dir="${TICKETING_TOKEN_DIR:-$HOME/.config/ticketing/agent-tokens}"
  mkdir -p "$dir"
  ( umask 077; printf %s "$token" > "$dir/$actor" )
  echo "$actor: token saved to $dir/$actor, mode 600." >&2
fi
echo "$token"
