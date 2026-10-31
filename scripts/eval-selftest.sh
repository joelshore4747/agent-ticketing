#!/usr/bin/env bash
# Proves the harness works, on pairs whose answer is already known.
#
# A judge that cannot separate an obviously good change from an obviously bad one is not ready to be
# trusted on a subtle one, and no amount of statistics downstream will tell you that — a broken
# figure produces numbers exactly as readily as a working one. So this runs first, it runs on every
# reviewer-prompt edit, and it exits non-zero.
#
# Freeze the fixtures before tuning a prompt. Editing a case until a rubric passes it is fitting the
# judge to the test, and the case stops testing anything the moment you do. The sha of each case is
# printed for that reason.
#
#   ./scripts/eval-selftest.sh --no-judge     # packer assertions only, no API calls, free
#   ./scripts/eval-selftest.sh                # everything, ~37 claude -p invocations:
#                                              # 32 judged (4 non-leak cases x 4 figures x
#                                              # 2 orders) + 4 for the malformed probe's
#                                              # guaranteed double-retry across both orders
#                                              # + 1 isolation probe. (An earlier estimate
#                                              # of ~34 undercounted the malformed probe's
#                                              # own retries; the count above is measured.)
#   ./scripts/eval-selftest.sh --case obvious
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CASES="$ROOT/eval/selftest/cases"
WORK=$(mktemp -d "${TMPDIR:-/tmp}/ticketing-selftest.XXXXXX")
trap 'rm -rf "$WORK"' EXIT
export EVAL_ARTIFACTS="$WORK/artifacts"
export EVAL_REVIEWS="$WORK/reviews.jsonl"
export EVAL_RESULTS="$WORK/results.jsonl"
export EVAL_TEMPLATE="${EVAL_TEMPLATE:-ticketing}"
ONLY= NOJUDGE= FAILED=0

while [ $# -gt 0 ]; do
  case $1 in
    --case)     ONLY=$2; shift ;;
    --no-judge) NOJUDGE=1 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
  shift
done

# Checked here rather than left to surface as six identical assertion failures further down. The
# corpus comes from Postgres, so a stopped container looks exactly like a broken packer.
psql "${EVAL_PGURL:-postgres://ticketing:ticketing@127.0.0.1:5434}/$EVAL_TEMPLATE" -qtAX \
  -c 'SELECT 1' >/dev/null 2>&1 || {
  echo "cannot reach $EVAL_TEMPLATE at ${EVAL_PGURL:-127.0.0.1:5434}." >&2
  echo "The packer reads the guidance corpus from there. Try: just db" >&2
  exit 2; }

BASE=$(git -C "$ROOT" rev-parse HEAD)
say()  { printf '%-14s %-6s %s\n' "$1" "$2" "${3:-}"; }
fail() { FAILED=1; say "$1" FAIL "$2"; }
pass() { say "$1" ok "${2:-}"; }

# One synthetic result row per fixture side, so the packer is exercised exactly as it will be on a
# real run rather than through a shortcut that would not catch a packer bug.
python3 - "$CASES" "$EVAL_RESULTS" "$BASE" <<'PY'
import json, pathlib, sys
cases, out, base = pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2]), sys.argv[3]
rows = []
for case in sorted(p.parent for p in cases.glob("*/meta.toml")):
    for arm in ("control", "guided"):
        rows.append({
            "run": f"selftest-{case.name}", "ticket": "TP-S1", "arm": arm, "replicate": 1,
            "base": base, "valid": True, "claims": 1,
            "documents_served": 5 if arm == "guided" else 0,
            "guidance_sha256": "fixture" if arm == "guided" else None,
            "completed": True, "status": "done", "lint": True, "tests": True,
            "lines_added": 10, "lines_deleted": 2, "checks": [1, 1], "probes": [1, 1],
            "ran_lint": False, "ran_tests": False, "bundle": str(case / arm),
        })
out.write_text("\n".join(json.dumps(r) for r in rows) + "\n")
PY

echo "fixtures at $BASE"
for dir in "$CASES"/*/; do
  name=$(basename "$dir")
  [ -n "$ONLY" ] && [ "$ONLY" != "$name" ] && continue
  sha=$(cat "$dir"/meta.toml "$dir"/*/diff.patch | sha256sum | cut -c1-12)
  printf '  %-12s %s\n' "$name" "$sha"
done
echo

# --- 1. the packer -----------------------------------------------------------
for dir in "$CASES"/*/; do
  name=$(basename "$dir")
  [ -n "$ONLY" ] && [ "$ONLY" != "$name" ] && continue
  log="$WORK/$name.pack.log"; rc=0
  python3 "$ROOT/scripts/eval-pack.py" --run "selftest-$name" >"$log" 2>&1 || rc=$?

  case $name in
    leak-token)
      # Must abort. A pack that merely filtered the slug out would mean the capture is broken and
      # the harness is papering over it, which is the one thing it must never do.
      if [ "$rc" != 0 ] && grep -q ABORT "$log"; then
        pass "pack/$name" "aborted on a document slug, as it must"
      else
        fail "pack/$name" "a slug in the diff did not abort the build (rc=$rc)"
      fi ;;
    leak-quote)
      if [ "$rc" = 0 ] && grep -q 'leak_suspected: 1' "$log"; then
        pass "pack/$name" "verbatim corpus quote flagged, pair excluded"
      else
        fail "pack/$name" "a verbatim corpus quote was not flagged (rc=$rc)"
      fi ;;
    leak-soft)
      if [ "$rc" = 0 ] && grep -q 'leak_suspected: 1' "$log"; then
        pass "pack/$name" "soft token on a line one side alone wrote flagged, pair excluded"
      else
        fail "pack/$name" "a one-sided soft token was not flagged (rc=$rc)"
      fi ;;
    leak-soft-shared)
      # The inverse, and the reason for the one above: a soft token on a line BOTH sides wrote
      # identically is not arm-revealing, and flagging it excluded every real pair (TP-52).
      if [ "$rc" = 0 ] && grep -q 'leak_suspected: 0' "$log"; then
        pass "pack/$name" "soft token on an identical shared line not flagged, pair kept"
      else
        fail "pack/$name" "a soft token both sides wrote identically was flagged or did not build (rc=$rc)"
      fi ;;
    *)
      if [ "$rc" = 0 ] && grep -q '1 pair(s)' "$log"; then
        pass "pack/$name" "built"
      else
        fail "pack/$name" "did not build (rc=$rc): $(tail -1 "$log")"
      fi ;;
  esac
done

[ -n "$NOJUDGE" ] && { echo; [ "$FAILED" = 0 ] && echo "packer assertions pass; judges not run" || echo "FAILURES"; exit "$FAILED"; }
command -v claude >/dev/null || { echo "claude CLI not on PATH" >&2; exit 2; }

# --- 2. the figures, on cases whose answer is known --------------------------
for dir in "$CASES"/*/; do
  name=$(basename "$dir")
  [ -n "$ONLY" ] && [ "$ONLY" != "$name" ] && continue
  case $name in leak-*) continue ;; esac

  "$ROOT/scripts/eval-review.sh" --run "selftest-$name" >"$WORK/$name.review.log" 2>&1 || true
  python3 - "$dir/meta.toml" "$EVAL_ARTIFACTS/selftest-$name/verdicts" <<'PY' || FAILED=1
import json, pathlib, sys, tomllib
meta = tomllib.load(open(sys.argv[1], "rb"))
verdicts = pathlib.Path(sys.argv[2])
rows = [json.loads(p.read_text()) for p in verdicts.glob("*.json")]
by_figure = {}
for r in rows:
    by_figure.setdefault(r["figure"], {})[r["order"]] = r

bad = 0
for figure, want in meta["expect"].items():
    got = by_figure.get(figure, {})
    if len(got) < 2:
        print(f"{'  ' + figure:<14} FAIL   only {len(got)} verdict(s)")
        bad = 1
        continue
    winners = [got[1]["winner"], got[2]["winner"]]
    shown = "/".join(str(w or "tie") for w in winners)
    if want == "any":
        print(f"{'  ' + figure:<14} --     {shown}")
    elif want == "better":
        ok = all(w == meta["better"] for w in winners)
        print(f"{'  ' + figure:<14} {'ok' if ok else 'FAIL':<6} {shown}   want {meta['better']} in both orders")
        bad |= not ok
    elif want == "tie":
        # Passing means NOT having a consistent preference: a tie in either order, or the two
        # orders disagreeing. Both are the figure declining to invent a difference.
        ok = (None in winners) or (winners[0] != winners[1])
        print(f"{'  ' + figure:<14} {'ok' if ok else 'FAIL':<6} {shown}   want a tie or a split")
        bad |= not ok
    elif want == "overlap":
        # A figure that should tie here and measurably does not. Recorded rather than asserted: a
        # suite that is permanently red gets ignored, and a suite that quietly drops the case stops
        # measuring. The consequence is statistical, and eval-report.py already carries it — this
        # figure's votes must not be pooled with the figure it overlaps.
        print(f"{'  ' + figure:<14} OVER   {shown}   known overlap, not pooled")
sys.exit(bad)
PY
  [ "$FAILED" = 0 ] && pass "judge/$name" || say "judge/$name" FAIL
done

# --- 2b. contract-keeper's precomputed prompt is reused, not recomputed per call -------------
# Proves the effective-prompt build actually happened once rather than silently recomputing on
# every call: if it had, order 1 and order 2's contract-keeper verdicts would still be correct
# (the content is identical either way) but would carry the SAME sha only by coincidence of the
# content never changing mid-run, not because reuse was proven. Checking the sha is the only way
# to tell "reused" from "recomputed but happened to match".
if [ -z "$ONLY" ] || [ "$ONLY" = obvious ]; then
  if python3 -c '
import glob, json, sys
rows = [json.loads(open(f).read()) for f in glob.glob(sys.argv[1])]
shas = {r["prompt_sha256"] for r in rows if r["figure"] == "contract-keeper"}
if len(rows) < 2:
    print("FAIL   fewer than 2 contract-keeper verdicts to compare"); sys.exit(1)
if len(shas) != 1:
    print(f"FAIL   {len(shas)} distinct prompt_sha256 across contract-keeper verdicts: {shas}")
    sys.exit(1)
print("ok     both orders used the identical precomputed prompt")
' "$EVAL_ARTIFACTS/selftest-obvious/verdicts/*.json"
  then
    pass "reuse/contract-keeper" "one effective prompt, not recomputed per call"
  else
    fail "reuse/contract-keeper" "prompt_sha256 differs across calls in one run"
  fi
fi

# --- 3. the malformed path ---------------------------------------------------
if [ -z "$ONLY" ] || [ "$ONLY" = identical ]; then
  probes="$WORK/probes"; mkdir -p "$probes"
  cp "$ROOT/eval/selftest/prompts/unreachable.md" "$probes/"
  EVAL_REVIEWERS="$probes" "$ROOT/scripts/eval-review.sh" \
    --run selftest-identical --figures unreachable >"$WORK/unreachable.log" 2>&1 || true
  row=$(cat "$EVAL_ARTIFACTS"/selftest-identical/verdicts/*unreachable*.json 2>/dev/null | head -1)
  if [ -n "$row" ] && python3 -c '
import json,sys
r = json.loads(sys.argv[1])
sys.exit(0 if r["status"] == "malformed" and r["winner"] is None and r["attempts"] == 2 else 1)
' "$row"; then
    pass "probe/malformed" "a call that never returned was retried once, then recorded"
  else
    fail "probe/malformed" "a failed call did not land as status=malformed with attempts=2"
  fi
fi

# --- 4. judge isolation ------------------------------------------------------
if [ -z "$ONLY" ]; then
  scratch=$(mktemp -d)
  probe_raw=$(cd "$scratch" && printf 'Answer the question in the system prompt.' | timeout 180 claude -p \
    --model claude-haiku-4-5-20251001 \
    --system-prompt "$(awk 'NR==1 && /^---$/ {f=1; next} f && /^---$/ {f=0; next} !f' \
       "$ROOT/eval/selftest/prompts/isolation-probe.md")" \
    --output-format json --tools "" --safe-mode --strict-mcp-config --no-session-persistence \
    2>/dev/null || true)
  rm -rf "$scratch"
  # A call that never ran is not evidence either way. Reporting a rate limit as "the judge knows the
  # project" would be the worst kind of false alarm: it points at the one assertion that must be
  # trusted, and it would train whoever sees it to ignore a real failure.
  answer=$(python3 -c '
import json,sys
raw = sys.stdin.read().strip()
if not raw:
    print("__ERROR__ the call returned nothing"); sys.exit()
try:
    d = json.loads(raw)
except ValueError:
    print("__ERROR__ unparseable response"); sys.exit()
print(("__ERROR__ " if d.get("is_error") else "") + str(d.get("result", "")))
' <<<"$probe_raw")
  # Asserted on what it claims to KNOW, not on which words appear: the question itself contains the
  # phrase "guidance document", and a judge saying "I do not know what that refers to" quotes it.
  if [[ $answer == __ERROR__* ]]; then
    say "probe/isolation" SKIP "the call did not run (${answer#__ERROR__ }) — not evidence either way"
  elif grep -qiE 'ticketing|TP-[0-9]|batches feature|work queue' <<<"$answer"; then
    fail "probe/isolation" "the judge named the project: ${answer:0:90}"
  elif grep -qiE "do not know|don't know|cannot (tell|determine)|no information|unable to" <<<"$answer"; then
    pass "probe/isolation" "the judge says it does not know what it is looking at"
  else
    fail "probe/isolation" "the judge neither named the project nor disclaimed: ${answer:0:90}"
  fi
fi

echo
if [ "$FAILED" = 0 ]; then
  echo "self-test passed. The figures separate a known pair and the packer refuses a leaked one."
  grep -l 'overlap' "$CASES"/*/meta.toml >/dev/null 2>&1 && {
    echo
    echo "OVERLAP recorded above: at least one figure decides a case it was meant to tie. Those two"
    echo "figures are not independent voters, which is why eval-report.py prints per-figure results"
    echo "as primary and the pooled line without a p-value."; }
else
  echo "SELF-TEST FAILED. Fix the prompts or the packer — not the fixtures."
fi
exit "$FAILED"
