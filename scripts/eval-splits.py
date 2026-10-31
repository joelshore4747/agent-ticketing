#!/usr/bin/env python3
"""Lists every (pair, figure) whose two order-swapped verdicts disagree — the too-close-to-call
cases the adjudicator resolves.

This is `eval-report.py`'s own `score_figure()` split detection, imported rather than re-derived.
Two independent implementations of "what counts as a split" is exactly how the report's printed
split count and the adjudicator's actual worklist would silently drift apart from each other.

    ./scripts/eval-splits.py --run eval-20260921-101500
"""
from __future__ import annotations

import argparse
import importlib.util
import os
import sys
from collections import defaultdict
from pathlib import Path

ROOT = Path(__file__).resolve().parent


def load_report():
    spec = importlib.util.spec_from_file_location("eval_report", ROOT / "eval-report.py")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def splits(reviews: list[dict], score_figure) -> list[tuple[str, str]]:
    by_figure: dict[str, list[dict]] = defaultdict(list)
    for r in reviews:
        if r["figure"] not in ("blind-audit", "adjudicator"):
            by_figure[r["figure"]].append(r)

    out: list[tuple[str, str]] = []
    for figure in sorted(by_figure):
        for pair in score_figure(by_figure[figure])["split_pairs"]:
            out.append((pair, figure))
    return out


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--run", required=True)
    ap.add_argument("--reviews", default=os.environ.get("EVAL_REVIEWS", str(ROOT.parent / "eval-reviews.jsonl")))
    args = ap.parse_args()

    report = load_report()
    reviews = report.load(Path(args.reviews), args.run)
    for pair, figure in splits(reviews, report.score_figure):
        print(f"{pair}\t{figure}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
