#!/usr/bin/env python3
"""Joins runs and reviews and prints what can honestly be claimed from them.

Two tables, kept apart on purpose.

The MECHANICAL table has no model in it: completion, checks, probes, lint. Lint is split by whether
the agent invoked the linter itself, because only one arm is told to — "guided passes lint more
often" is otherwise a measurement of the instruction, not of the code.

The BLIND PREFERENCE table is a per-figure sign test over order-consistent pairs. Per figure is the
primary unit: the figures overlap, and pooling correlated voters would overstate the effective N, so
the pooled line is printed without a p-value. Ties, splits, malformed calls, leaks and truncations
all drop out and all are counted, because a filter nobody can see is indistinguishable from a
result.
"""
from __future__ import annotations

import argparse
import json
import math
import os
import sys
from collections import defaultdict
from math import comb
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
PINNED = ("prompt_sha256", "builder_sha256", "redaction_sha256", "model", "cli_version", "template_sha")


def sign_p(wins: int, n: int) -> float:
    """Exact two-sided binomial. n is decisive pairs; ties are already gone."""
    if n == 0:
        return 1.0
    k = max(wins, n - wins)
    return min(1.0, 2 * sum(comb(n, i) for i in range(k, n + 1)) / 2 ** n)


def wilson(hits: int, n: int, z: float = 1.96) -> tuple[float, float]:
    if n == 0:
        return (0.0, 1.0)
    p = hits / n
    d = 1 + z * z / n
    centre = (p + z * z / (2 * n)) / d
    half = z * math.sqrt(p * (1 - p) / n + z * z / (4 * n * n)) / d
    return (max(0.0, centre - half), min(1.0, centre + half))


def holm(pvals: dict[str, float]) -> dict[str, float]:
    ordered = sorted(pvals.items(), key=lambda kv: kv[1])
    out, running = {}, 0.0
    for i, (name, p) in enumerate(ordered):
        running = max(running, min(1.0, p * (len(ordered) - i)))
        out[name] = running
    return out


def load(path: Path, run: str) -> list[dict]:
    if not path.exists():
        sys.exit(f"missing {path}")
    rows = [json.loads(l) for l in path.read_text().splitlines() if l.strip()]
    return [r for r in rows if r.get("run") == run]


def mechanical(runs: list[dict]) -> None:
    # test_discriminates only exists when a run opted into EVAL_DISCRIMINATES=1 (it doubles the
    # Postgres-backed suite per ticket, so it is off by default). Show the column only when at
    # least one row actually carries it, rather than printing an always-empty one.
    has_discriminates = any("test_discriminates" in r for r in runs)
    print("MECHANICAL   (no judge involved)")
    header = f"{'arm':<9}{'n':>3}{'valid':>7}{'done':>6}{'checks':>9}{'probes':>9}{'ran_lint':>10}{'lint':>6}{'tests':>7}{'+/-':>12}"
    if has_discriminates:
        header += f"{'discrim':>9}"
    print(header)
    for arm in ("control", "guided"):
        a = [r for r in runs if r["arm"] == arm]
        if not a:
            continue
        ck = (sum(r["checks"][0] for r in a), sum(r["checks"][1] for r in a))
        pr = (sum(r["probes"][0] for r in a), sum(r["probes"][1] for r in a))
        added = sum(r["lines_added"] for r in a)
        deleted = sum(r["lines_deleted"] for r in a)
        line = (f"{arm:<9}{len(a):>3}{sum(bool(r['valid']) for r in a):>7}"
              f"{sum(bool(r['completed']) for r in a):>6}"
              f"{f'{ck[0]}/{ck[1]}':>9}{f'{pr[0]}/{pr[1]}':>9}"
              f"{sum(bool(r.get('ran_lint')) for r in a):>10}"
              f"{sum(bool(r['lint']) for r in a):>6}{sum(bool(r['tests']) for r in a):>7}"
              f"{f'+{added}/-{deleted}':>12}")
        if has_discriminates:
            total_disc = sum(1 for r in a if "test_discriminates" in r)
            disc = sum(1 for r in a if r.get("test_discriminates") == "discriminates")
            line += f"{f'{disc}/{total_disc}':>9}"
        print(line)

    clean = {arm: [r for r in runs if r["arm"] == arm and not r.get("ran_lint")] for arm in ("control", "guided")}
    parts = [f"{arm} {sum(bool(r['lint']) for r in rs)}/{len(rs)}" for arm, rs in clean.items() if rs]
    print(f"\n  lint pass rate among runs that never invoked the linter: {', '.join(parts) or 'no such runs'}")
    print("  ^ the only lint comparison that is not measuring the instruction to run it\n")


def score_figure(rows: list[dict]) -> dict:
    """One figure's order-swapped verdicts, bucketed into decisive/split/tie/excluded.

    The single definition of "split" (this figure's own order-1 and order-2 verdicts disagree),
    shared by the BLIND PREFERENCE table below, by eval-splits.py (which needs exactly this list to
    build the adjudicator's worklist), and by the ADJUDICATED section's contradicts-the-panel check.
    Computing it in more than one place is how a report's printed split count and what actually got
    adjudicated would silently drift apart.
    """
    by_pair: dict[str, dict[int, dict]] = defaultdict(dict)
    for r in rows:
        by_pair[r["pair"]][r["order"]] = r

    decisive = split = tie = guided = control = excluded = 0
    split_pairs: list[str] = []
    decisive_pairs: dict[str, str] = {}
    for pair, orders in by_pair.items():
        if len(orders) < 2:
            excluded += 1
            continue
        a, b = orders.get(1), orders.get(2)
        if any(o["status"] != "ok" for o in (a, b)) or any(o["leak_suspected"] or o["truncated"] for o in (a, b)):
            excluded += 1
            continue
        if a["winner"] is None or b["winner"] is None:
            tie += 1
            continue
        if a["winner"] != b["winner"]:
            split += 1
            split_pairs.append(pair)
            continue
        decisive += 1
        decisive_pairs[pair] = a["winner"]
        if a["winner"] == "guided":
            guided += 1
        else:
            control += 1
    return {"pairs": len(by_pair), "decisive": decisive, "split": split, "tie": tie,
            "guided": guided, "control": control, "excluded": excluded,
            "split_pairs": split_pairs, "decisive_pairs": decisive_pairs}


def preference(reviews: list[dict], runs: list[dict], pool_anyway: bool) -> None:
    by_figure: dict[str, list[dict]] = defaultdict(list)
    for r in reviews:
        by_figure[r["figure"]].append(r)

    for figure, rows in by_figure.items():
        for field in PINNED:
            values = {r.get(field) for r in rows}
            if len(values) > 1:
                msg = (f"{figure}: rows disagree on {field} ({sorted(map(str, values))}).\n"
                       "That is a different stimulus, not a footnote. Re-judge the run, or pass "
                       "--pool-anyway and label the output.")
                if not pool_anyway:
                    sys.exit("REFUSING TO REPORT\n" + msg)
                print("WARNING " + msg)

    malformed = sum(1 for r in reviews if r["status"] != "ok")
    if reviews and malformed / len(reviews) > 0.05:
        worst = defaultdict(int)
        for r in reviews:
            if r["status"] != "ok":
                worst[r["figure"]] += 1
        sys.exit(f"REFUSING TO REPORT: {malformed}/{len(reviews)} calls malformed "
                 f"({dict(worst)}). Above 5% the drops are not noise.")

    print("BLIND PREFERENCE   (sign test over order-consistent pairs; ties and splits dropped)")
    print(f"{'figure':<18}{'pairs':>6}{'decisive':>10}{'split':>7}{'tie':>5}{'guided':>8}{'control':>9}{'p':>8}{'Holm':>7}{'excl':>7}")

    pvals, summary = {}, {}
    for figure in sorted(by_figure):
        s = score_figure(by_figure[figure])
        pvals[figure] = sign_p(s["guided"], s["decisive"])
        summary[figure] = s

    adjusted = holm(pvals)
    for figure, s in summary.items():
        print(f"{figure:<18}{s['pairs']:>6}{s['decisive']:>10}{s['split']:>7}{s['tie']:>5}"
              f"{s['guided']:>8}{s['control']:>9}"
              f"{pvals[figure]:>8.3f}{adjusted[figure]:>7.3f}{s['excluded']:>7}")

    total_dec = sum(s["decisive"] for s in summary.values())
    total_g = sum(s["guided"] for s in summary.values())
    print(f"\n{'pooled':<18}{'':>6}{total_dec:>10}{'':>7}{'':>5}{total_g:>8}{total_dec - total_g:>9}"
          f"{'  —':>8}{'  —':>7}")
    print("  pooled has no p-value on purpose: the figures overlap, so their votes are not independent")

    floor = min((s["decisive"] for s in summary.values()), default=0)
    if floor < 6:
        print(f"\n  Smallest decisive n across figures is {floor}. Below 6 the exact two-sided sign test")
        print("  cannot reach p < 0.05 however lopsided the split, so no figure below that is reporting")
        print("  a result — it is reporting that the run was too small.")


def audit(reviews: list[dict]) -> None:
    rows = [r for r in reviews if r["figure"] == "blind-audit" and r["status"] == "ok"]
    if not rows:
        return
    hits = sum(1 for r in rows if r["winner"] == "guided")
    lo, hi = wilson(hits, len(rows))
    print(f"\nBLINDING AUDIT   (a strong model guessing which side was guided)")
    print(f"  {len(rows)} pairs, {hits} correct ({hits / len(rows):.0%}), 95% CI [{lo:.0%}, {hi:.0%}]")
    if lo > 0.5:
        print("  THE BLINDING FAILED. The arm is guessable from the material judges were shown, so")
        print("  every preference number above is worthless regardless of what it says. Read the")
        print("  audit reasons, close the channel they name, re-pack and re-judge.")
    else:
        print("  the interval includes 50%, so the arm is not guessable from what judges were shown")
    for r in rows[:5]:
        print(f"    “{(r['reason'] or '')[:100]}”")


def _cost_line(label: str, rows: list[dict], count_col: bool = False) -> str | None:
    priced = [r for r in rows if r.get("cost_usd") is not None]
    if not priced:
        return None
    usd = sum(r["cost_usd"] for r in priced)
    i = sum(r.get("input_tokens", 0) for r in priced)
    o = sum(r.get("output_tokens", 0) for r in priced)
    cw = sum(r.get("cache_creation_input_tokens", 0) for r in priced)
    cr = sum(r.get("cache_read_input_tokens", 0) for r in priced)
    hit = cr / (cr + i) if (cr + i) else 0.0
    prefix = f"{label:<16}{len(priced):>6}" if count_col else f"{label:<9}"
    return f"{prefix}{i:>10}{o:>9}{cw:>9}{cr:>9}{hit:>6.0%}{usd:>10.3f}"


def cost(runs: list[dict], reviews: list[dict]) -> None:
    """What this run actually used, and whether the judges' identical-per-figure system prompts are
    being cached. Older rows (pre-dating this capture) simply have no cost_usd, so every section
    here is skipped rather than printed as zeroes — a silent zero would read as "this run was
    free," which is never true and would be actively misleading.

    Token counts lead and $ trails, labelled an estimate, because $ (total_cost_usd from the CLI)
    does not reliably track the token counts it is supposedly computed from: a real guided/control
    pair from this harness had guided using MORE tokens in every column, including premium-priced
    cache_creation, yet costed LOWER. Treat $ as a rough estimate, not a comparative signal — and
    on a subscription account it is not a real charge at all, only a rough proxy for usage against
    the rolling rate-limit window."""
    has_agent = any(r.get("cost_usd") is not None for r in runs)
    has_judge = any(r.get("cost_usd") is not None for r in reviews)
    if not has_agent and not has_judge:
        return

    print("\nCOST")
    if has_agent:
        print("  agent-under-test, by arm:")
        print(f"  {'arm':<9}{'input':>10}{'output':>9}{'cache_w':>9}{'cache_r':>9}{'hit%':>7}{'$ (est.)':>10}")
        for arm in ("control", "guided"):
            line = _cost_line(arm, [r for r in runs if r["arm"] == arm])
            if line:
                print("  " + line)
    else:
        print("  agent-under-test: no cost data on these rows (pre-dates capture, or every run's")
        print("  stream-json log never reached a final result line)")

    if has_judge:
        print("\n  judges, by figure:")
        print(f"  {'figure':<16}{'calls':>6}{'input':>10}{'output':>9}{'cache_w':>9}{'cache_r':>9}{'hit%':>7}{'$ (est.)':>10}")
        by_figure: dict[str, list[dict]] = defaultdict(list)
        for r in reviews:
            by_figure[r["figure"]].append(r)
        for figure in sorted(by_figure):
            line = _cost_line(figure, by_figure[figure], count_col=True)
            if line:
                print("  " + line)
        print("  cache_r high relative to input means the CLI is already caching that figure's")
        print("  identical-per-call system prompt automatically — no explicit caching directive is")
        print("  set anywhere in this harness, so a high rate here is the API's own default")
    print("\n  $ (est.) is total_cost_usd as reported by the CLI. It does not necessarily move with the")
    print("  token counts to its left — verified against this harness's own real data — and on a")
    print("  subscription account it is not a charge at all. Read it as a rough estimate only.")


def adjudicated(reviews: list[dict]) -> None:
    """Opus's read of the pairs where a voter's own two order-swapped verdicts disagreed — the
    "too close to call" case. Reported on its own, never fed into any p-value: an order-flip is
    the evidence saying "we don't know", and a third opinion replaces that missing evidence with a
    confident one rather than adding to it."""
    rows = [r for r in reviews if r["figure"] == "adjudicator"]
    if not rows:
        return
    ok = [r for r in rows if r["status"] == "ok"]
    print("\nADJUDICATED   (Opus, secondary — never an input to any p above)")
    print(f"  {len(ok)}/{len(rows)} call(s), resolving close calls the voter panel could not settle")

    # For each adjudicated pair, every OTHER voter's decisive verdict on it — naturally excludes
    # whichever figure was split or tied on that pair, since neither produces a decisive one. If
    # those agree with each other and the adjudicator disagrees, that is a finding about the
    # voters' rubrics, not about guidance: see the module docstring on `score_figure`.
    by_figure: dict[str, list[dict]] = defaultdict(list)
    for r in reviews:
        if r["figure"] not in ("blind-audit", "adjudicator"):
            by_figure[r["figure"]].append(r)
    directions: dict[str, list[str]] = defaultdict(list)
    for figure_rows in by_figure.values():
        for pair, winner in score_figure(figure_rows)["decisive_pairs"].items():
            directions[pair].append(winner)

    contradicts = checkable = 0
    for r in ok:
        votes = directions.get(r["pair"], [])
        if not votes:
            continue
        checkable += 1
        majority = max(set(votes), key=votes.count)
        if r["winner"] and r["winner"] != majority:
            contradicts += 1
    if checkable:
        print(f"  contradicts the other voters' majority direction: {contradicts}/{checkable} "
              f"(pairs with no other decisive voter are not checkable)")
    for r in ok[:5]:
        print(f"    {r['pair']:<12} -> {r['winner'] or 'tie':<8} \u201c{(r['reason'] or '')[:90]}\u201d")


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--run", required=True)
    ap.add_argument("--results", default=os.environ.get("EVAL_RESULTS", str(ROOT / "eval-results.jsonl")))
    ap.add_argument("--reviews", default=os.environ.get("EVAL_REVIEWS", str(ROOT / "eval-reviews.jsonl")))
    ap.add_argument("--pool-anyway", action="store_true")
    args = ap.parse_args()

    runs = load(Path(args.results), args.run)
    if not runs:
        sys.exit(f"no runs for {args.run}")
    reviews = load(Path(args.reviews), args.run) if Path(args.reviews).exists() else []

    print(f"run {args.run}   base {runs[0]['base'][:8]}\n")
    mechanical(runs)
    if reviews:
        # adjudicator rows are never a vote: each one has a single order (no order-2 counterpart),
        # so left in here it would show up as its own "figure" with decisive=0 forever, dragging
        # the floor diagnostic to 0 and masking every real result underneath it — reproduced and
        # confirmed against synthetic data before this filter was added.
        preference([r for r in reviews if r["figure"] not in ("blind-audit", "adjudicator")],
                   runs, args.pool_anyway)
        audit(reviews)
        adjudicated(reviews)
    else:
        print("no reviews yet: scripts/eval-review.sh --run " + args.run)
    cost(runs, reviews)

    print("\nThis is a manipulation check and a safety screen, not an effect size. The control arm is")
    print("not fully controlled either — the codebase itself instructs, so what is measured is the")
    print("marginal effect of served guidance on top of code that already teaches.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
