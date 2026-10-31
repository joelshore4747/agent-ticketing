# Sourced by scripts/render-check.sh: a fresh database and an API on a spare port, both gone on exit,
# so a run never touches the live tracker. After `start_throwaway`, $api is the API's base URL.
export PATH="$HOME/.local/go/bin:$PATH"

start_throwaway() {
  throwaway_db="ticketing_throwaway_$$"
  throwaway_psql() { docker exec ticketing-db psql -qtAX -U ticketing -d postgres -c "$1"; }
  throwaway_psql "CREATE DATABASE $throwaway_db" >/dev/null
  local port
  port=$(python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()')
  api="http://127.0.0.1:$port"
  throwaway_log=$(mktemp)
  # env -u: its own fresh database holds no credentials, so it runs in local mode and reads the actor from X-Actor however Joel has login set for the live API (TP-316).
  ( cd api && env -u TICKETING_TOKENS DATABASE_URL="postgres://ticketing:ticketing@127.0.0.1:5434/$throwaway_db" PORT="$port" go run . >"$throwaway_log" 2>&1 ) &
  throwaway_server=$!
  trap stop_throwaway EXIT
  for _ in $(seq 1 60); do curl -sf "$api/healthz" >/dev/null && return 0; sleep 1; done
  echo "the throwaway API did not start:"; cat "$throwaway_log"; exit 1
}

stop_throwaway() {
  pkill -P "$throwaway_server" 2>/dev/null || true
  kill "$throwaway_server" 2>/dev/null || true
  wait "$throwaway_server" 2>/dev/null || true
  throwaway_psql "DROP DATABASE IF EXISTS $throwaway_db WITH (FORCE)" >/dev/null || true
  rm -f "$throwaway_log"
}
