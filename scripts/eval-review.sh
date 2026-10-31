#!/usr/bin/env bash
# Runs the reviewer figures over the packs eval-pack.py built, and appends eval-reviews.jsonl.
#
# One call = one (pair, figure, order). Two orders produce ONE data point, not two: a figure that
# picks a different change when the two are swapped has told us the pair is too close to call, and
# that pair drops out. Budget accordingly — the doubling is the cheapest defence there is against
# position bias, and it is still small next to the agent runs being reviewed.
#
# The judge runs in an empty directory outside this repository, with no tools, no MCP and no
# customisations. That is not belt and braces: CLAUDE.md explains the guidance system in prose, and
# a judge that reads it knows exactly what it is supposed to be blind to.
#
#   ./scripts/eval-review.sh --run eval-20260921-101500 --dry-run   # count and cost, no calls
#   ./scripts/eval-review.sh --run eval-20260921-101500
#   ./scripts/eval-review.sh --run eval-20260921-101500 --audit --sample 20
#   ./scripts/eval-review.sh --run eval-20260921-101500 --adjudicate   # run AFTER reading the
#                                                                       # split counts eval-report.py
#                                                                       # prints — never automatic.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ART="${EVAL_ARTIFACTS:-$ROOT/eval-artifacts}"
OUT="${EVAL_REVIEWS:-$ROOT/eval-reviews.jsonl}"
RESULTS="${EVAL_RESULTS:-$ROOT/eval-results.jsonl}"
SCHEMA="$ROOT/eval/verdict.schema.json"
# Overridable so the self-test can point at deliberately broken figures without putting them in the
# real roster, where `just review-check` would scan them and a reader would take them for judges.
REVIEWERS="${EVAL_REVIEWERS:-$ROOT/eval/reviewers}"
VOTERS=(maintainer correctness contract-keeper scope-auditor)
JOBS=${EVAL_JOBS:-4}
SAMPLE=20
# RUN keeps whatever the environment holds: the xargs children are re-entrant calls of this
# script and inherit it that way. Clearing it unconditionally here wiped it in every child.
RUN="${RUN:-}"
DRY= AUDIT= ADJUDICATE= FIGURES=

# Re-entrant: xargs calls this script back with --one so each verdict lands in its own file.
if [ "${1:-}" = --one ]; then ONE=1; shift; else ONE=; fi

while [ $# -gt 0 ]; do
  case $1 in
    --run)         RUN=$2; shift ;;
    --figures)     FIGURES=$2; shift ;;
    --sample)      SAMPLE=$2; shift ;;
    --jobs)        JOBS=$2; shift ;;
    --dry-run)     DRY=1 ;;
    --audit)       AUDIT=1 ;;
    --adjudicate)  ADJUDICATE=1 ;;
    *) [ -n "$ONE" ] && break || { echo "unknown argument: $1" >&2; exit 2; } ;;
  esac
  shift
done

field() { sed -n "s/^$2: *//p" "$1" | head -1; }
body_of() { awk 'NR==1 && /^---$/ {f=1; next} f && /^---$/ {f=0; next} !f' "$1"; }

# The service rules, as they stood at $BASE. Identical text on both sides of every pair, so it is
# reference material rather than something one side gets credit for having read.
contract_extract() {
  local base=$1
  git -C "$ROOT" show "$base:docs/api.md" 2>/dev/null \
    | awk '/^## Service rules/{p=1} p && /^## /&&!/^## Service rules/{exit} p' || true
}

run_base() {
  python3 -c '
import json, sys
print(next(json.loads(l)["base"] for l in open(sys.argv[1]) if json.loads(l)["run"] == sys.argv[2]))
' "$RESULTS" "$RUN"
}

# One retry-guarded call, shared by every judge invocation — voter or adjudicator. Sets
# JUDGE_ATTEMPT and JUDGE_RAW directly rather than returning them through a captured string: a
# combined "count\nbody" return once broke silently, because $(...) strips TRAILING newlines, and
# an empty body (the malformed/unreachable-model case, i.e. exactly the case this exists to record
# correctly) has nothing after the count to keep that newline embedded — the caller's split then
# left the raw body as the literal digits "2", which json.loads() parsed as an int, not a verdict.
# Reproduced and confirmed via the self-test's own malformed-path probe before this fix.
#
# A retry is the identical call in a fresh session, never a shortened reprompt: telling a model it
# got the format wrong nudges it toward whatever it half-said the first time, which is a verdict
# shaped by the harness rather than by the code.
call_judge() {
  local model=$1 prompt=$2 pack=$3
  local scratch; scratch=$(mktemp -d)
  JUDGE_ATTEMPT=0
  JUDGE_RAW=
  while [ "$JUDGE_ATTEMPT" -lt 2 ]; do
    JUDGE_ATTEMPT=$((JUDGE_ATTEMPT + 1))
    JUDGE_RAW=$(cd "$scratch" && printf '%s' "$pack" | timeout 300 claude -p \
      --model "$model" \
      --system-prompt "$(body_of "$prompt")" \
      --json-schema "$(cat "$SCHEMA")" \
      --output-format json --tools "" --safe-mode --strict-mcp-config \
      --no-session-persistence --max-budget-usd "${EVAL_MAX_USD:-0.50}" 2>/dev/null) || JUDGE_RAW=
    [ -n "$JUDGE_RAW" ] && python3 -c '
import json,sys
d = json.load(sys.stdin)
v = d.get("structured_output")
sys.exit(0 if isinstance(v, dict) and v.get("choice") in ("first","second","tie") else 1)
' <<<"$JUDGE_RAW" 2>/dev/null && break
    JUDGE_RAW=
  done
  rm -rf "$scratch"
}

judge_one() {
  local pair=$1 figure=$2 order=$3
  local dir="$ART/$RUN/pairs/$pair"
  # contract-keeper reads a precomputed file (base body + the contract extract, built once at the
  # top of a normal run — see EFFECTIVE_CONTRACT_KEEPER below) rather than the bare reviewer file.
  # prompt_sha256 downstream hashes whatever path it is given, so this still pins the verdict to
  # the exact bytes the model actually saw.
  local prompt="$REVIEWERS/$figure.md"
  if [ "$figure" = contract-keeper ] && [ -n "${EFFECTIVE_CONTRACT_KEEPER:-}" ] && [ -s "$EFFECTIVE_CONTRACT_KEEPER" ]; then
    prompt="$EFFECTIVE_CONTRACT_KEEPER"
  fi
  local verdicts="$ART/$RUN/verdicts"; mkdir -p "$verdicts"
  local dest="$verdicts/$pair.$figure.$order.json"
  [ -s "$dest" ] && return 0   # resumable: a finished verdict is never re-bought

  local model; model=$(field "$prompt" model)
  local pack; pack=$(cat "$dir/order$order.md")

  # Found in passing: %3N is GNU date's "3-digit milliseconds" — this machine's date is uutils
  # coreutils, which does not truncate %N, so %3N silently returned full nanoseconds and every
  # duration_ms in this file was nonsense (a 0.3s call recorded as ~300 SECONDS). %N / 1_000_000
  # is portable across both.
  local started; started=$(( $(date +%s%N) / 1000000 ))
  call_judge "$model" "$prompt" "$pack"
  local attempt=$JUDGE_ATTEMPT raw=$JUDGE_RAW
  local elapsed=$(( $(date +%s%N) / 1000000 - started ))

  python3 "$ROOT/scripts/eval-verdict.py" \
    --pair "$pair" --figure "$figure" --order "$order" --model "$model" \
    --run "$RUN" --attempts "$attempt" --duration "$elapsed" \
    --key "$dir/pair.json" --prompt "$prompt" --out "$dest" <<<"${raw:-}"
}

# One resolution of a too-close-to-call pair: $voter_figure's own two order-swapped verdicts
# disagreed (score_figure's "split", in eval-report.py), and this is Opus reading both of that
# figure's readings plus the pair itself and saying what the difference actually is.
#
# Never order-swapped: this call is not fed into any sign test, so it needs none of the
# position-bias defence that swap exists for. Order 1's pack is reused verbatim for that reason.
adjudicate_one() {
  local pair=$1 voter_figure=$2
  local dir="$ART/$RUN/pairs/$pair"
  local prompt="$REVIEWERS/adjudicator.md"
  local verdicts="$ART/$RUN/verdicts"; mkdir -p "$verdicts"
  # voter_figure, not an order, is what makes this destination unique: two different voters can
  # each split on the same pair, and each gets its own adjudication.
  local dest="$verdicts/$pair.adjudicator.$voter_figure.json"
  [ -s "$dest" ] && return 0

  local model; model=$(field "$prompt" model)
  local pack; pack=$(cat "$dir/order1.md")

  # The two verdicts this call exists to resolve, read straight out of what the voter pass already
  # wrote — never re-derived, so the adjudicator sees exactly the disagreement eval-report.py
  # already counted as a split, worded exactly as that reviewer wrote it.
  local reasons; reasons=$(python3 -c '
import json, sys
rows = [json.loads(l) for l in open(sys.argv[1]) if l.strip()]
rows = [r for r in rows if r["run"] == sys.argv[2] and r["pair"] == sys.argv[3]
        and r["figure"] == sys.argv[4] and r["status"] == "ok"]
for r in sorted(rows, key=lambda r: r["order"]):
    side = "first" if r["order"] == 1 else "second"
    choice, conf, reason = r["choice"], r["confidence"], r["reason"]
    print(f"- Shown {side}: chose {choice} ({conf} confidence) — {reason}")
' "$OUT" "$RUN" "$pair" "$voter_figure")
  pack="$pack

## Another reviewer'\''s two readings of this pair, one per order

$reasons"

  local started; started=$(( $(date +%s%N) / 1000000 ))
  call_judge "$model" "$prompt" "$pack"
  local attempt=$JUDGE_ATTEMPT raw=$JUDGE_RAW
  local elapsed=$(( $(date +%s%N) / 1000000 - started ))

  python3 "$ROOT/scripts/eval-verdict.py" \
    --pair "$pair" --figure adjudicator --order 1 --model "$model" \
    --run "$RUN" --attempts "$attempt" --duration "$elapsed" \
    --key "$dir/pair.json" --prompt "$prompt" --out "$dest" <<<"${raw:-}"
}

if [ -n "$ONE" ]; then
  if [ "$2" = adjudicate ]; then adjudicate_one "$1" "$3"; else judge_one "$1" "$2" "$3"; fi
  exit 0
fi

# --- the work list -----------------------------------------------------------
[ -n "$RUN" ] || { echo "name the run: --run eval-YYYYmmdd-HHMMSS" >&2; exit 2; }
[ -d "$ART/$RUN/pairs" ] || { echo "no packs for $RUN — run scripts/eval-pack.py first" >&2; exit 2; }
command -v claude >/dev/null || { echo "claude CLI not on PATH" >&2; exit 2; }

# A prompt that restates the corpus measures compliance, not quality. Refuse to spend on that.
python3 "$ROOT/scripts/eval-pack.py" --check-prompts >/dev/null || {
  echo "reviewer prompts overlap the guidance corpus; run eval-pack.py --check-prompts" >&2; exit 2; }

figures=("${VOTERS[@]}")
[ -n "$FIGURES" ] && IFS=, read -ra figures <<< "$FIGURES"
[ -n "$AUDIT" ] && figures=(blind-audit)

work=()
if [ -n "$ADJUDICATE" ]; then
  # Not auto-chained onto a normal pass: an operator runs this after reading eval-report.py's
  # printed split counts, matching the same "see the cost before you spend it" discipline as
  # --dry-run below. An empty result here is a real, good outcome — nothing was too close to call.
  mapfile -t splits < <(python3 "$ROOT/scripts/eval-splits.py" --run "$RUN" --reviews "$OUT")
  if [ ${#splits[@]} -eq 0 ]; then
    echo "no split verdicts for $RUN — nothing to adjudicate"
    exit 0
  fi
  for s in "${splits[@]}"; do
    IFS=$'\t' read -r pair voter_figure <<<"$s"
    work+=("$pair adjudicate $voter_figure")
  done
elif [ -n "$AUDIT" ]; then
  mapfile -t pairs < <(find "$ART/$RUN/pairs" -mindepth 1 -maxdepth 1 -type d -printf '%f\n' | sort)
  [ ${#pairs[@]} -gt 0 ] || { echo "no pairs under $ART/$RUN/pairs" >&2; exit 2; }
  # One order per pair, sampled: the audit measures whether the arm is guessable at all, and both
  # orders of the same pair are not two independent chances to guess it.
  mapfile -t sampled < <(printf '%s\n' "${pairs[@]}" | shuf | head -n "$SAMPLE")
  for p in "${sampled[@]}"; do work+=("$p blind-audit $(( (RANDOM % 2) + 1 ))"); done
else
  mapfile -t pairs < <(find "$ART/$RUN/pairs" -mindepth 1 -maxdepth 1 -type d -printf '%f\n' | sort)
  [ ${#pairs[@]} -gt 0 ] || { echo "no pairs under $ART/$RUN/pairs" >&2; exit 2; }

  # contract-keeper's contract extract used to be refetched via a fresh git-show+awk+python3
  # subprocess triple on EVERY call — up to 2x the pair count per run — and rode in the piped user
  # turn rather than the system prompt, the one place caching reliably applies. Built once here.
  if printf '%s\n' "${figures[@]}" | grep -qx contract-keeper; then
    EFFECTIVE_CONTRACT_KEEPER="$ART/$RUN/contract-keeper.effective.md"
    if [ ! -s "$EFFECTIVE_CONTRACT_KEEPER" ]; then
      base=$(run_base)
      { cat "$REVIEWERS/contract-keeper.md"
        echo
        echo "## The contract, as it stood before either change"
        echo
        contract_extract "$base"
      } > "$EFFECTIVE_CONTRACT_KEEPER"
    fi
  fi

  for p in "${pairs[@]}"; do for f in "${figures[@]}"; do for o in 1 2; do work+=("$p $f $o"); done; done; done
fi

if [ -n "$DRY" ]; then
  echo "run       $RUN"
  if [ -n "$ADJUDICATE" ]; then
    echo "mode      adjudicate"
    echo "calls     ${#work[@]}  (one per too-close-to-call pair, no order-swap)"
  else
    echo "figures   ${figures[*]}"
    echo "calls     ${#work[@]}  (two orders per pair produce one data point)"
    bytes=$(cat "$ART/$RUN"/pairs/*/order1.md | wc -c)
    npairs=$(find "$ART/$RUN/pairs" -mindepth 1 -maxdepth 1 -type d | wc -l)
    echo "packs     ~$(( bytes / npairs / 4 )) tokens each, ~$(( bytes * ${#figures[@]} * 2 / npairs / 4 )) input tokens per pair"
  fi
  echo
  echo "nothing was called."
  [ -n "$ADJUDICATE" ] || echo "Read a pack before paying for a pass: less $ART/$RUN/pairs/*/order1.md"
  exit 0
fi

# xargs hands each child only its work item; everything else it needs, including which run it is
# judging, has to arrive through the environment.
export RUN OUT EFFECTIVE_CONTRACT_KEEPER
printf '%s\n' "${work[@]}" | xargs -P "$JOBS" -L 1 "$0" --one
mkdir -p "$(dirname "$OUT")"
cat "$ART/$RUN"/verdicts/*.json >> "$OUT"
echo "appended $(find "$ART/$RUN/verdicts" -name '*.json' | wc -l) verdict(s) to $OUT"
if [ -n "$ADJUDICATE" ]; then
  echo "now: python3 scripts/eval-report.py --run $RUN"
else
  echo "now: python3 scripts/eval-report.py --run $RUN"
  echo "or, on any split pairs it prints: ./scripts/eval-review.sh --run $RUN --adjudicate"
fi
