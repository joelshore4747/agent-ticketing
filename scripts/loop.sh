#!/usr/bin/env bash
# The loop's hands (goal 8, TP-126): run the step loop.py names, one agent at a time, until it says stop.
#
#   ./scripts/loop.sh                run until the roadmap stops, the batch budget is spent, or a step fails
#   ./scripts/loop.sh --dry-run      print the next step and the prompt it would send, then exit
#   ./scripts/loop.sh --project KEY  work that project instead of the default one
#
#   LOOP_BATCHES=3          batches this run may finish before it pauses for Joel (goal 8's default budget)
#   LOOP_STEP_BUDGET_USD=5  the most one agent step may spend (claude --max-budget-usd)
#   LOOP_MAX_STEPS=80       a hard ceiling on steps per run, whatever else happens
#   LOOP_USAGE_FILE=<path>  where the plan's five-hour and seven-day usage is recorded after each step
#   LOOP_LEDGER=<path>      one JSON line per step (what, which model, tokens, cost), kept across runs
#                           (default: ${XDG_STATE_HOME:-~/.local/state}/ticketing/usage-windows.json)
#   LOOP_USAGE_LIMIT=0.6    how full a window of Joel's plan may be before the run stops or waits: a
#                           spent seven-day window stops it between steps, a spent five-hour one makes
#                           it sleep until that window resets and carry on (loop.py decides both)
#   LOOP_PROJECT=KEY        the same as --project
#   LOOP_ROADMAP=<id>       the roadmap to work (default: the project's newest unarchived one)
#   LOOP_API_WAIT=90        seconds a restarted API has to come up on this checkout's HEAD before the
#                           run stops (a cold `go run .` builds first, so this is not a round trip)
#   REPO=<path>             the checkout to work in (default: the project's repo_path)
#   CLAUDE_BIN=claude       the agent runner; the fake-agent test substitutes its own
#   TICKETING_TOKEN_DIR     each role's bearer credential, one file named after its agent (see below)
#
# With no --project the run works the API's default project on this checkout, as it always did.
# Naming one (goal 7, TP-146) moves the whole run to it: its roadmap, its tripwires, the needs-joel
# ticket that stops it (only one in the batch being worked; any other is reported and the run goes
# on), the batch budget spent on its batches, its key in every agent prompt, and its repo_path as
# the agents' working directory. loop.py decides all three and refuses to start when it cannot.
# The roles themselves are still read from this checkout's .claude/agents, so a connected project's
# first real run needs those files in its own checkout too (TP-147 makes them project-neutral).
#
# Each role runs with only the tools its agent file lists, under --permission-mode dontAsk, so a tool
# it was not given is denied rather than prompted for. Never --dangerously-skip-permissions: unlike
# eval.sh's throwaway clones, this is a real repository. The tracker holds all the state, so Ctrl-C
# is always safe and the next run resumes where the record says.
set -euo pipefail
cd "$(dirname "$0")/.."  # the loop's own checkout: its scripts, its agent files, its batch-merge
export API="${API:-http://localhost:8080}"

dry="" project="${LOOP_PROJECT:-}"
while [ $# -gt 0 ]; do
  case "$1" in
    --dry-run) dry=1 ;;
    --project) project="${2:?--project needs a project key}"; shift ;;
    --project=*) project="${1#--project=}" ;;
    *) echo "loop: unknown argument: $1 (usage: loop.sh [--dry-run] [--project KEY])" >&2; exit 2 ;;
  esac
  shift
done

# One resolution for the whole run, exported so every child — loop.py, the agents, batch-merge.sh —
# works the same project, roadmap and checkout, whatever the record does while the run is going.
scope=()
[ -z "$project" ] || scope=(--project "$project")
read -r PROJECT LOOP_ROADMAP REPO < <(python3 scripts/loop.py where --shell "${scope[@]}")
export LOOP_PROJECT="$PROJECT" LOOP_ROADMAP REPO
git -C "$REPO" rev-parse --git-dir > /dev/null 2>&1 \
  || { echo "loop: project $PROJECT's checkout is not a git repository: $REPO" >&2; exit 1; }
echo "loop: project $PROJECT, roadmap $LOOP_ROADMAP, in $REPO"
# Commits here are signed through 1Password, which needs an approval no one is there to give in a
# headless run, so every agent's `git commit` would hang or fail. Signing is switched off for this
# process tree only (TP-130); Joel's global git config is untouched.
export GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=commit.gpgsign GIT_CONFIG_VALUE_0=false
CLAUDE_BIN="${CLAUDE_BIN:-claude}"
BATCHES="${LOOP_BATCHES:-3}" STEP_BUDGET="${LOOP_STEP_BUDGET_USD:-5}" MAX_STEPS="${LOOP_MAX_STEPS:-80}"
# Resolved once and exported, like the project and the roadmap above, so loop.py reads the same file
# this run writes. It sits outside $RUN on purpose: the plan's usage outlives one run, so the next run
# and a dry run — which start no agent and so learn nothing themselves — still see the last figures.
export LOOP_USAGE_FILE="${LOOP_USAGE_FILE:-${XDG_STATE_HOME:-$HOME/.local/state}/ticketing/usage-windows.json}"
# Every step's cost, kept outside $RUN because a run's temp directory does not survive a reboot and a
# claim holds only the last step posted to its ticket (`just cost` reads it).
export LOOP_LEDGER="${LOOP_LEDGER:-${XDG_STATE_HOME:-$HOME/.local/state}/ticketing/ledger.jsonl}"

RUN="$(mktemp -d "${TMPDIR:-/tmp}/ticketing-loop.XXXXXX")"
# Who each step is (TP-316). A role's credential is a file named after it in TICKETING_TOKEN_DIR, put
# there by Joel (TP-315); a role with no file sends none, which is local mode and exactly how every run
# worked before. The loop's own calls — and loop.py, batch-merge.sh and guidance-check.sh under it,
# which inherit this export — are the `loop` role's.
TOKEN_DIR="${TICKETING_TOKEN_DIR:-$HOME/.config/ticketing/agent-tokens}"
token_for() { [ -r "$TOKEN_DIR/$1" ] && tr -d '[:space:]' < "$TOKEN_DIR/$1" || true; }
export TICKETING_TOKEN="${TICKETING_TOKEN:-$(token_for loop)}"

# What an agent file allows; the builders, which predate the roles, get the tools building needs.
tools_for() {
  local listed
  listed=$(sed -n '2,/^---$/{s/^tools: //p}' ".claude/agents/$1.md" 2>/dev/null || true)
  if [ -n "$listed" ]; then echo "$listed"; return; fi
  # Under dontAsk every part of a compound command must be listed, and builders naturally write
  # `cd <repo> && git ...` and run the repo's own scripts (TP-140).
  # pnpm for connected projects that use it instead of npm (TP-356, FS since 2026-10-16). docker so a
  # builder can pin images by digest and build what it wrote (Joel, 2026-10-18; FS's local stack).
  echo "Read,Edit,Write,Glob,Grep,Bash(cd *),Bash(git *),Bash(just *),Bash(go *),Bash(gofmt *),Bash(npm *),Bash(npx *),Bash(pnpm *),Bash(docker *),Bash(curl *),Bash(python3 *),Bash(./scripts/*),Bash(scripts/*),Bash(bash *),Bash(ls *),Bash(cat *),Bash(head *),Bash(tail *),Bash(grep *),Bash(sed *),Bash(wc *),Bash(find *),Bash(diff *),Bash(sha256sum *),Bash(echo *),Bash(mkdir *),Bash(rm *),Bash(mv *),Bash(cp *),Bash(touch *),Bash(which *),Bash(node *),mcp__ticketing"
}

# Goal 1 (TP-257): the evaluator must never judge code the API is not serving. Restarting it by hand
# after a migration was the main thing stopping the loop (Joel on TP-197), and a process left behind
# created orphan batch 2f37317c and left batch 3's gate_passed null for good (TP-175, TP-211).
# /api/version says what the running process carries; this checkout says what it should.
#
# The comparison is against the loop's own checkout rather than $REPO, because the tracker is built
# from here whichever project a run is working. SERVED is what the API last answered; the evaluator
# prompt carries it, so the eval's resolution can name the version it measured (goal 9).
SERVED=unknown

api_serving() { # → "<commit> <migration>", the two halves of GET /api/version
  # A process with no /api/version answers "unknown unknown", which is behind by definition rather
  # than unknown: that endpoint is itself newer than such a process, so there is nothing to weigh.
  curl -sf ${TICKETING_TOKEN:+-H "Authorization: Bearer $TICKETING_TOKEN"} "$API/api/version" 2>/dev/null | python3 -c 'import json, sys
v = json.load(sys.stdin)
print(v.get("commit") or "unknown", v.get("migration") or "unknown")' 2>/dev/null || echo "unknown unknown"
}

api_serves_head() { # why — read what the API serves into SERVED, and restart it when that is behind
  local want served
  want="$(git rev-parse HEAD) $(ls api/migrations/*.sql | sed 's|.*/||' | tail -1)"
  served=$(api_serving)
  SERVED=${served%% *}
  [ "$served" != "$want" ] || return 0
  echo "loop: the API serves $served; this checkout is at $want"
  # A dry run reports and changes nothing: --dry-run is what Joel types to see where a run stands.
  [ -z "$dry" ] || return 0
  echo "loop: restarting the API $1"
  setsid ./scripts/dev.sh api > "$RUN/api.log" 2>&1 < /dev/null &
  for _ in $(seq 1 "${LOOP_API_WAIT:-90}"); do
    sleep 1
    served=$(api_serving)
    SERVED=${served%% *}
    [ "$served" = "$want" ] || continue
    echo "loop: the API now serves $served"
    return 0
  done
  # Carrying on here would mean an eval whose verdicts are about code the process it records them
  # through does not have, which is how TP-175's orphan batch came to exist.
  echo "loop: stopped: the API serves commit $SERVED, not this checkout's ${want%% *}; its log is $RUN/api.log" >&2
  exit 1
}

# Goal 2 (TP-258): the other half of stale state. A guidance document whose source_paths have moved
# under it is handed to every agent that claims a ticket and believed, and it stopped the loop in
# slots 5, 7 and 8 — a human running the same routine each time. loop.py weighs the report: STALE
# alone becomes a doc-review step, anything else stops the run. The check itself lives here because it
# is a command with side effects; what it means is decided in one testable place, as every other step is.
guidance_gate() { # rewrites step/agent/reason into a doc review when the check asks for one
  local report code=0 verdict
  report=$(./scripts/guidance-check.sh 2>&1) || code=$?
  # A dry run reads the report too. Skipping it would have --dry-run name the evaluator where a real
  # run takes a doc-review step, which is the one thing --dry-run exists not to do; and unlike a
  # restart the check changes nothing but each document's own staleness flag, which is its whole job.
  verdict=$(printf '%s\n' "$report" | python3 scripts/loop.py guidance "$code" --shell) \
    || { echo "loop: stopped: the guidance check's report could not be read" >&2; exit 1; }
  [ -n "$verdict" ] || return 0
  read -r step agent reason <<< "$verdict"
  if [ "$step" = stop ]; then
    printf '%s\n' "$report" >&2
    echo "loop: stopped: $reason" >&2
    exit 1
  fi
  echo "[$(date +%H:%M:%S)] $step ${ticket/#-/} — $reason"
}

prompt_for() { # step batch ticket reason
  # Headless shells run under dontAsk: shell expansions ($VAR, $?, $(...)) and heredocs are refused
  # whatever the allow list says, so the agent is told how to work within that (TP-140). Every prompt
  # names the project, because an MCP call without one answers for the default project (TP-146).
  # It names .scratch/, not just the trick: batch 16 left six messages in the repo root (lesson TP-334).
  local how="You are working on project $PROJECT, checked out at $REPO: pass project \"$PROJECT\" on any MCP call that takes one, and change nothing outside that checkout. Follow your agent instructions, the project's guidance (get_guidance) and the claim's next_steps. Your shell refuses \$ expansions (\$VAR, \$?, \$(...)) and heredocs: write multi-line text to a file under .scratch/ with the Write tool and pass it by path (git commit -F .scratch/<file>, python3 .scratch/<file>) — .scratch/ is gitignored, so nothing you write there is left behind in the checkout — and chain commands with && instead of checking \$?. Write each command plainly: no ( ) subshells, no 2>/dev/null or other redirections, no || fallbacks — run 'git branch --list <name>' first rather than 'git switch x || git switch -c x'. A refusal means that command's form, not the shell: rewrite it simpler and carry on; never stop because one command was denied."
  case "$1" in
    strategist) [ "$2" = - ] \
      && echo "The roadmap this run is working is complete: get_ticket the eval ticket of its last batch and read the comments — a human's steering comment there is the brief for what comes next — then propose the next roadmap with create_roadmap and plan_batch a placeholder batch per step, in order, each with its what/how/why and the goals it serves, saying in the why where you departed from that steering. $4. $how" \
      || echo "Plan batch $2 of the roadmap: first get_ticket its eval ticket and read the comments, because a human's steering comment on it is the brief this batch is planned to, and a plan that departs from it says why in its why. Then add its tickets with add_to_batch (or file new ones and add them), every one naming its goals and saying what done looks like. $4. If the Approver sent the plan back, read its comment on the eval ticket, fix what it asks, then record_decision(kind: accepted, rationale: what you changed) on the eval ticket so it re-reviews. $how" ;;
    approver-accept) echo "Approve or send back the plan in batch $2 (accept it over GraphQL as 'approver (delegated by joel)', or comment and record_decision reframed on its eval ticket). $4. $how" ;;
    # One deep pass per batch, on a strong model: the rigour moves here from per-ticket steps. TP-190's
    # lesson candidates (tool calls that failed twice or more this run) come with it.
    review) echo "Review and trim batch $2 before its eval. Start from get_project_memory, then read the batch's whole diff (git diff main...HEAD on the batch branch). Review it for correctness, structure, readability and how easy it is to debug, against the project's guidance. Fix what you find directly, and delete what is bloated, duplicated or dead, in commits on the batch branch (each message starting 'review:'), keeping the lint and the tests green. Correct any guidance document the batch's code has made wrong, PUT it and re-pin only it (guidance-check.sh --accept-only <slug>). File as findings only what is too big to fix here. The loop measured the batch before this step (scripts/batch-size.sh $2: ${SIZE:-not measured}). If after your trimming the code net is still above zero, record_decision(kind: accepted) on the batch's eval ticket whose evidence_refs quote that measurement, saying why the growth is needed; the loop measures again before the eval. Last, record_review(phase: after) as the new project memory. $4. $(python3 scripts/lib/step_errors.py "$RUN"/*.jsonl 2>/dev/null | awk '$1 + 0 > 1' | sed 's/^/Lesson candidate, a tool call that failed this often while the batch was built: /' | tr '\n' ' ')$how" ;;
    approver-guard) echo "Ticket $3 in batch $2 is a refactor with no confirmed tripwire: apply your rule 7 to it now (find the existing test that pins its behaviour, register it if needed and attach it as a gate criterion; never confirm it yourself, since confirming a tripwire is Joel's: if it is unconfirmed, file a needs-joel ticket with numbered What you do steps and move the refactor last in the batch). If no test pins it, comment on the ticket and tag it needs-joel. $how" ;;
    doc-review) echo "Guidance has gone stale: the code moved under these documents and nobody has re-read them since, so batch $2's eval must not be run against them. $4. Take each document named there in turn: read 'git diff <its pin>..HEAD -- <its paths>', with the pin and the paths given for it above, and correct in docs/guidance/<slug>.md what that diff made wrong, editing in place — keep every line the diff does not contradict, and say what the change now means. Then, for each document you read, PUT the body from 'python3 scripts/lib/guidance_file.py payload docs/guidance/<slug>.md' to /api/guidance/<slug>, and re-pin only those with './scripts/guidance-check.sh --accept-only <slug>...'. Do not run 'just guidance --replace': it re-pins every document, including ones nobody read (lesson TP-283). Do not touch code, and do not bend a document to match code that is itself wrong: file that as a finding instead. $how" ;;
    builder) echo "Work ticket $3 in batch $2 to done: claim it (claim_next_ticket with batch_id $2), branch as next_steps say, make the change, run just lint and just test, commit, merge back into the batch branch, then update_ticket to done with its evidence. $how" ;;
    evaluator) echo "Run eval ticket $3 of batch $2: the gate, the demos, verdicts on every criterion, the cleanup pass; close it with evidence, or leave it open naming what failed. The loop measured the batch just now (scripts/batch-size.sh $2: ${SIZE:-not measured}). If its code net is above zero and no decision on $3 already cites a measurement at or above it, judge the growth yourself against the batch's brief: record_decision(kind: accepted) on $3 with that measurement in evidence_refs and what the brief needed the lines for, or leave the eval open naming what should be cut. Growth the brief did not ask for is a finding, not an acceptance. The tracker API you record through is serving commit ${SERVED:-unknown}: name it in the eval's resolution, so the batch's digest says which version was measured. $how" ;;
    narrator) echo "Write batch $2's report in the five sections and finish it with finish_batch. $4. $how" ;;
    approver-checkpoint) echo "Sign batch $2's checkpoint over GraphQL as 'approver (delegated by joel)': continue, adjust or stop, with a note citing its digest. Stop for anything only Joel can do. $how" ;;
  esac
}

steps=0 finished=0 last=""
while :; do
  read -r step batch ticket agent until_at reason < <(python3 scripts/loop.py next --shell)
  echo "[$(date +%H:%M:%S)] $step ${ticket/#-/} — $reason"
  if [ "$step" = stop ]; then echo "loop: stopped: $reason"; exit 0; fi
  # Settled before the prompt is written, because the prompt names the version the eval records
  # against (TP-257). A dry run reads it too and says whether a real run would restart.
  # The eval's acceptance cites a batch-size.sh figure, and a doc review commits guidance changes the
  # figure counts, so a stale-guidance eval turns into a doc review and is measured again the next
  # time round: slot 13's +83 became +89 when it was not (TP-292).
  # The batch's size is measured here, by script, before the review and before the eval's steps: an
  # agent working a connected project cannot reach scripts/batch-size.sh from that checkout, and FS's
  # first batch went unmeasured for exactly that reason (lean harness).
  SIZE=""
  if [ -z "$dry" ] && { [ "$step" = review ] || [ "$step" = evaluator ]; }; then
    SIZE=$(./scripts/batch-size.sh "$batch" 2>&1 | head -1) || SIZE="unmeasured: $SIZE"
  fi
  if [ "$step" = evaluator ]; then
    api_serves_head "before $ticket's eval"
    # After the API is current, never before: the check reads the corpus the API is serving, and
    # weighing what a process left behind still holds would be measuring the wrong tree (TP-258).
    guidance_gate
  fi
  if [ -n "$dry" ]; then prompt_for "$step" "$batch" "$ticket" "$reason"; exit 0; fi
  if [ "$step $batch $ticket" = "$last" ]; then
    echo "loop: stopped: the same step came up twice with nothing changed ($step $ticket); see $RUN" >&2; exit 1
  fi
  last="$step $batch $ticket"
  steps=$((steps + 1))
  if [ "$steps" -gt "$MAX_STEPS" ]; then echo "loop: stopped: $MAX_STEPS steps is this run's ceiling" >&2; exit 1; fi

  # Goal 8: the five-hour window is spent, so the run slows down rather than stopping (TP-230). loop.py
  # names the moment it frees; a minute past that the figures in LOOP_USAGE_FILE are stale and it
  # decides again. A wait that comes up twice with nothing changed is caught by the guard above.
  if [ "$step" = wait ]; then
    now=$(date +%s)
    nap=60
    [ "$until_at" = - ] || [ "$until_at" -le "$now" ] || nap=$((until_at - now + 60))
    echo "loop: waiting ${nap}s: $reason"
    sleep "$nap"
    continue
  fi

  # The before-review is the memory plus the size of the diff since it: mechanical, so no agent (lean
  # harness). The review step reads the code once, after the batch is built.
  if [ "$step" = review-before ]; then
    python3 scripts/lib/before_review.py "$batch" \
      || { echo "loop: stopped: the before-review of batch $batch could not be recorded" >&2; exit 1; }
    continue
  fi

  if [ "$step" = merge ]; then
    ./scripts/batch-merge.sh "$batch" \
      || { echo "loop: stopped: the merge of batch $batch was refused" >&2; exit 1; }
    # A merge that brought api/ into main is the moment the running process stops serving the
    # checkout, so it is caught here rather than left for the next batch's eval to trip over
    # (TP-257). HEAD^ is main before the --no-ff merge, so this is the merge's own diff; a merge
    # touching only web/ or docs/ leaves the process current and asks nothing.
    if ! git -C "$REPO" diff --quiet HEAD^ HEAD -- api/; then
      api_serves_head "after merging batch $batch, whose diff touches api/"
    fi
    continue
  fi

  log="$RUN/$steps-$step.jsonl"
  # The prompt and the tool list are read here, from the loop's own checkout; the agent itself runs
  # in the project's, so its edits, its git and its `just` are that project's (TP-146).
  prompt=$(prompt_for "$step" "$batch" "$ticket" "$reason") tools=$(tools_for "$agent")
  # One identity per step, written here rather than once per run: X-Actor names the role, Authorization
  # carries its credential when it has one, and the step's environment gets the same token so an agent's
  # own curl (the Approver's GraphQL accept) sends what its MCP calls send. MCP ignores both until
  # TP-317 reads them, which is why this lands first: the agents working this batch stay attributed.
  step_token=$(token_for "$agent")
  printf '{"mcpServers":{"ticketing":{"type":"http","url":"%s/mcp","headers":{"X-Actor":"%s"%s}}}}\n' \
    "$API" "$agent" "${step_token:+,\"Authorization\":\"Bearer $step_token\"}" > "$RUN/mcp.json"
  # Sonnet by default (the role files); Opus only where one decision steers many batches: proposing a
  # roadmap. The review's role file asks for its own strong model.
  model=()
  [ "$step $batch" = "strategist -" ] && model=(--model opus)
  # The role's definition goes with the step: `--agent` alone is looked up in the working directory,
  # which for a connected project is that project's checkout, holding none of these role files.
  python3 scripts/lib/agent_def.py ".claude/agents/$agent.md" > "$RUN/agents.json" \
    || { echo "loop: stopped: no role file .claude/agents/$agent.md for the $step step" >&2; exit 1; }
  if ! ( cd "$REPO" && TICKETING_TOKEN="$step_token" exec "$CLAUDE_BIN" -p "$prompt" --agents "$RUN/agents.json" --agent "$agent" "${model[@]}" \
        --mcp-config "$RUN/mcp.json" --strict-mcp-config --permission-mode dontAsk --allowedTools "$tools" \
        --max-budget-usd "$STEP_BUDGET" --output-format stream-json --verbose ) < /dev/null > "$log" 2>&1; then
    echo "loop: stopped: the $agent step failed; its log is $log" >&2; exit 1
  fi
  # Goal 9: a ticket step's cost goes onto its claim as the step ends, so nothing has to be installed
  # for it to be measured (TP-182); review, planning and report steps are logged here and nowhere else.
  usage=$(python3 scripts/lib/stream_usage.py --json "$log")
  echo "$steps $step $batch $ticket $usage" >> "$RUN/usage.log"
  python3 scripts/lib/stream_usage.py --ledger "$log" "$LOOP_LEDGER" project="$PROJECT" step="$step" \
    agent="$agent" batch="$batch" ticket="$ticket" log="$log" || echo "loop: the step's cost could not be written to $LOOP_LEDGER; carrying on" >&2
  # Goal 8: how much of Joel's plan is left, read from the log the step already wrote, so knowing it
  # costs no extra call (TP-229). A log carrying no rate_limit_event leaves the last figures alone.
  python3 scripts/lib/stream_usage.py --windows "$log" "$LOOP_USAGE_FILE" \
    || echo "loop: the plan's usage could not be recorded in $LOOP_USAGE_FILE; carrying on" >&2
  if [ "$ticket" != - ]; then
    curl -sf -X POST "$API/api/tickets/$ticket/usage" -H 'Content-Type: application/json' -H "X-Actor: loop" ${TICKETING_TOKEN:+-H "Authorization: Bearer $TICKETING_TOKEN"} -d "$usage" > /dev/null || echo "loop: $ticket's usage report was refused; carrying on" >&2
  fi
  # Rule 21 and goal 9 for a project worked somewhere else (TP-346): the gate is recorded by this
  # checkout's run-checks.sh, and a builder working another checkout cannot reach it — under dontAsk
  # a path outside the agent's own checkout is denied whatever the allow list says (the-agents). So
  # the loop runs it from here as the step ends, and REPO (exported above) is where it runs each
  # check, so what is recorded is that project's own gate and not this project's. A ticket worked in
  # this checkout is left alone: there the builder can and does run it itself. Only the builder step:
  # an eval ticket carries no command checks by design (service/plans.go), its gate being the
  # commands in its description, which the evaluator runs and reports in its resolution. A red gate
  # is a measure and not a stop — the batch's evaluator is what judges it — and it says so on stdout,
  # in the order the run happened, rather than on stderr after everything else.
  if [ "$step" = builder ] && [ "$ticket" != - ] && [ "$REPO" != "$PWD" ]; then
    ./scripts/run-checks.sh "$ticket" \
      || echo "loop: $ticket's gate did not pass; carrying on, the batch's evaluator judges it"
  fi
  # A run works the one roadmap it resolved before it started, so proposing the next one is its last
  # step: the roadmap the Strategist has just created is what the next run resolves to, being the
  # newest unarchived one (loop.py pick_roadmap). Deciding again here would only name this step again.
  if [ "$step $batch" = "strategist -" ]; then
    echo "loop: stopped: $reason; the next run works it. Logs: $RUN"; exit 0
  fi
  # The budget counts this project's batches and no other's: a run works one project's roadmap.
  if [ "$step" = approver-checkpoint ]; then
    finished=$((finished + 1))
    if [ "$finished" -ge "$BATCHES" ]; then
      echo "loop: paused: $finished batches finished in $PROJECT, this run's budget (LOOP_BATCHES=$BATCHES). Logs: $RUN"; exit 0
    fi
  fi
done
