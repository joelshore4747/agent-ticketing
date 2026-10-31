#!/usr/bin/env bash
# Runs the API and the web dev server together; Ctrl-C stops both.
#
#   ./scripts/dev.sh        the API and the web dev server; Ctrl-C stops both
#   ./scripts/dev.sh api    the API alone, in the foreground, on $PORT
#
# `api` is the one launch path for the API, and the dev run below takes it too, so the env block and
# the `go run` line exist once (TP-257). scripts/loop.sh calls it to replace a process that is behind
# the checkout, which is why it frees the port first rather than assuming nothing holds it.
set -euo pipefail
cd "$(dirname "$0")/.."
export DATABASE_URL="${DATABASE_URL:-postgres://ticketing:ticketing@127.0.0.1:5434/ticketing}"
export PORT="${PORT:-8080}"
export CORS_ORIGIN="${CORS_ORIGIN:-http://localhost:5173}"
# What GET /api/version reports, so a caller can tell this process from a checkout that moved on.
export GIT_COMMIT="${GIT_COMMIT:-$(git rev-parse HEAD 2>/dev/null || echo unknown)}"
export PATH="$HOME/.local/go/bin:$PATH"

if [ "${1:-}" = api ]; then
  # Whatever holds $PORT goes first, by port and not by pid: `go run` forks the binary it builds and
  # that child is what listens, so a recorded pid only ever names the wrapper (scripts/lib/throwaway.sh
  # has to kill both). A process started before this mode existed has no pid recorded anywhere, and
  # that is exactly the one the loop needs to replace.
  fuser -k -TERM "$PORT/tcp" 2>/dev/null || true
  for _ in $(seq 1 20); do fuser -s "$PORT/tcp" 2>/dev/null || break; sleep 0.5; done
  cd api && exec go run .
fi

[ -d web/node_modules ] || (cd web && npm install)

trap 'kill 0' EXIT INT TERM
./scripts/dev.sh api &
(cd web && npm run dev) &
wait
