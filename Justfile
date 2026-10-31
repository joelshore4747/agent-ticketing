set shell := ["bash", "-euo", "pipefail", "-c"]
export DATABASE_URL := env_var_or_default("DATABASE_URL", "postgres://ticketing:ticketing@127.0.0.1:5434/ticketing")
# The credentials file the API reads (rule 10). Empty by default, which is local mode: every recipe
# here runs open, exactly as before, and switching login on is this one variable in the environment.
export TICKETING_TOKENS := env_var_or_default("TICKETING_TOKENS", "")
# Go is installed under ~/.local/go rather than system-wide.
export PATH := env_var("HOME") + "/.local/go/bin:" + env_var("PATH")
# Where the dumps go. Exported so that the directory `db` creates and the one compose mounts into
# the backup service are the same path by construction, not by two defaults agreeing.
export BACKUP_DIR := env_var_or_default("BACKUP_DIR", env_var("HOME") + "/.local/share/ticketing/backups")

# Start db, api and web together
dev: db
    ./scripts/dev.sh

# Start Postgres in Docker and wait for it, and the backup service beside it that dumps it on a
# schedule (TP-221) — so dev, test and loop all leave a scheduled dump behind them. `--wait` still
# names only `db`: the backup service is started on its own line and waited on by nothing, so a dump
# in progress, or a backup container that has died, can never hold `just db` up. BACKUP_DIR is
# created first because Docker would otherwise create the bind source as root, leaving `just backup`
# on the host unable to write into its own backup directory.
db:
    mkdir -p "$BACKUP_DIR"
    docker compose up -d --wait db
    docker compose up -d backup

# Dump the tracker into BACKUP_DIR (default ~/.local/share/ticketing/backups, deliberately outside the
# checkout), beside a counts file read in the same snapshot as the dump, so a restore can be checked
# against exactly what was dumped rather than against the database as it has become since.
# RETENTION: the newest BACKUP_KEEP dumps are kept, default 14, and a pruned dump is deleted together
# with its counts file.
backup: db
    bash ./scripts/backup.sh

# Prove the newest dump in BACKUP_DIR (or the one you name) by restoring it into a scratch database,
# checking its row counts against the counts file written with it, and dropping it again — because a
# dump nobody has restored is a hope, not a backup. The live `ticketing` database is never opened.
# The scheduled backup service runs this after every dump, so `docker compose logs backup` carries
# the same line without anyone asking for it.
backup-verify *DUMP: db
    bash ./scripts/backup-verify.sh {{DUMP}}

# Install web dependencies
setup:
    cd web && npm install

# Load docs/guidance/*.md into the tracker (needs the API running; skips slugs that already exist;
# --replace overwrites a slug that is already there). *ARGS because both these recipes' scripts take
# flags and tell the operator so in their own output — without it just reads `--replace` as the name
# of a recipe and the flag never reaches the script (TP-225).
guidance *ARGS:
    ./scripts/guidance-seed.sh {{ARGS}}

# Flag guidance documents whose code has changed since anyone last read them
# (--accept records HEAD for every document instead: the "I have just reviewed these" button)
guidance-check *ARGS:
    ./scripts/guidance-check.sh {{ARGS}}

# Copy the documented SDL into the schema gqlgen generates from, then regenerate
schema:
    ./scripts/schema.sh

# Regenerate the GraphQL server from internal/graph/schema.graphqls
generate: schema
    cd api && go run github.com/99designs/gqlgen generate

# Run all tests
test: db
    cd api && go test ./...
    cd web && npm test -- --run
    python3 -m unittest scripts/test_loop.py scripts/test_tripwire_hash.py scripts/test_gate.py scripts/test_guidance_check.py scripts/test_guidance_lister.py scripts/test_batch_size.py scripts/test_backup.py scripts/test_stream_usage.py scripts/test_step_errors.py scripts/test_batch_merge.py scripts/test_cost.py scripts/test_scorecard.py scripts/test_before_review.py scripts/test_agent_def.py scripts/test_loop_run.py


# Run the agents in a loop until the roadmap stops or the batch budget is spent
# (--dry-run to peek; --project KEY to work a connected project instead of the default one)
loop *ARGS: db
    ./scripts/loop.sh {{ARGS}}

# What the loop has spent, per step and per model (--by batch|ticket|agent, --since, --project, --batch)
cost *ARGS:
    python3 scripts/cost.py {{ARGS}}

# Is the tracker working? Per batch: time taken vs building, stops by cause, first-time evals, cost, size
scorecard *ARGS:
    python3 scripts/scorecard.py {{ARGS}}

# Everything a change must pass before it is committed, with the render guard chained in so a CSS
# regression cannot slip through unnoticed (TP-141). Stops at the first failure.
# A batch's eval ticket runs this and then `just guidance-check`, `just selftest-quick` (when the
# change touched scripts/ or eval/). A ticket's `gate_passed` column
# still comes only from scripts/run-checks.sh, which runs that ticket's own recorded checks.
gate:
    just lint
    just test
    just render-check

# Lint everything
lint:
    cd api && gofmt -l . | (! grep .) && go vet ./...
    cd web && npm run build

# Unreachable Go (TP-169). The dead-code command for the scouts and for the eval's cleanup pass, so
# nobody re-derives it: run it from api/ and it dies on a missing go.sum entry for
# golang.org/x/telemetry, a dependency of the tool and not of this project. Adding that entry would
# pollute the tracked module graph for a dev-only tool, so this runs in a throwaway copy of api/ with
# GOFLAGS=-mod=mod, where the tool may resolve its own dependencies and api/go.sum is never touched.
# Six internal/testdb helpers are known false positives: they are reached only by tests, so `just
# deadcode -test` shows them going quiet. Needs the network the first time (`go run ...@latest`).
deadcode *ARGS:
    #!/usr/bin/env bash
    set -euo pipefail
    scratch=$(mktemp -d)
    trap 'rm -rf "$scratch"' EXIT
    cp -R api "$scratch/api"
    cd "$scratch/api"
    GOFLAGS=-mod=mod go run golang.org/x/tools/cmd/deadcode@latest {{ARGS}} ./...

# Load the hidden acceptance checks (they live OUTSIDE this repo on purpose: see the script)
checks:
    ./scripts/checks-seed.sh

# Refuse to spend on judges whose criteria are sentences the guidance already says
review-check:
    python3 ./scripts/eval-pack.py --check-prompts

# Prove the reviewer figures work, on pairs whose answer is already known (needs db; ~37 calls)
selftest: db
    ./scripts/eval-selftest.sh

# The free half of the self-test: the packer's assertions, no API calls
selftest-quick: db
    ./scripts/eval-selftest.sh --no-judge

# The standing guard for CSS: screenshots the built web app against fixed seed data and compares it
# with committed baselines (--update rewrites them). Nothing else catches a stylesheet regression —
# the web tests run in jsdom and never load global.css — so `just gate` chains this in rather than
# leaving it to be remembered. Needs a locally cached Playwright + Chromium; see the script's header.
render-check *ARGS: db
    bash ./scripts/render-check.sh {{ARGS}}

# Stop the database and the scheduled backup beside it (data is kept in a volume)
stop:
    docker compose stop backup db
