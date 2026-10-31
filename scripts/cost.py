#!/usr/bin/env python3
"""What the loop has spent, from the ledger loop.sh appends one line to per step (LOOP_LEDGER).

    cost.py [--ledger PATH] [--project KEY] [--batch ID] [--since YYYY-MM-DD] [--by step|agent|model|batch|ticket]

Prints one row per group, most expensive first: steps, input/output tokens, cache writes and reads,
the cache hit rate, and the CLI's dollar estimate. Tokens are the real signal; the dollar is an
estimate. Several --by flags print several tables; with none it prints step and model.
"""
import argparse
import datetime
import json
import os
from collections import defaultdict

TOKENS = ("input_tokens", "output_tokens", "cache_creation_input_tokens", "cache_read_input_tokens")


def rows(path, project=None, batch=None, since=None):
    try:
        with open(path, encoding="utf-8") as f:
            lines = f.readlines()
    except OSError:
        return []
    out = []
    for line in lines:
        try:
            r = json.loads(line)
        except ValueError:
            continue
        if (project and r.get("project") != project) or (batch and r.get("batch") != batch) \
                or (since and r.get("ts", 0) < since):
            continue
        out.append(r)
    return out


def group(rs, by):
    """{group: totals} with steps, each token figure, cost, and the cache hit rate (reads over all input)."""
    acc = defaultdict(lambda: {"steps": 0, "cost_usd": 0.0, **{t: 0 for t in TOKENS}})
    for r in rs:
        a = acc[r.get(by) or "-"]
        a["steps"] += 1
        a["cost_usd"] += r.get("cost_usd") or 0.0
        for t in TOKENS:
            a[t] += r.get(t) or 0
    for a in acc.values():
        seen = a["input_tokens"] + a["cache_creation_input_tokens"] + a["cache_read_input_tokens"]
        a["cache_hit"] = a["cache_read_input_tokens"] / seen if seen else 0.0
    return dict(sorted(acc.items(), key=lambda kv: -kv[1]["cost_usd"]))


def table(by, groups):
    head = f"{by:<28} {'steps':>5} {'input':>9} {'output':>9} {'cache_w':>10} {'cache_r':>11} {'hit':>5} {'$':>8}"
    lines = [head, "-" * len(head)]
    total = {"steps": 0, "cost_usd": 0.0, **{t: 0 for t in TOKENS}}
    for name, a in groups.items():
        lines.append(f"{name[:28]:<28} {a['steps']:>5} {a['input_tokens']:>9} {a['output_tokens']:>9} "
                     f"{a['cache_creation_input_tokens']:>10} {a['cache_read_input_tokens']:>11} "
                     f"{a['cache_hit']:>5.0%} {a['cost_usd']:>8.2f}")
        for k in total:
            total[k] += a[k]
    lines.append(f"{'total':<28} {total['steps']:>5} {total['input_tokens']:>9} {total['output_tokens']:>9} "
                 f"{total['cache_creation_input_tokens']:>10} {total['cache_read_input_tokens']:>11} "
                 f"{'':>5} {total['cost_usd']:>8.2f}")
    return "\n".join(lines)


def main(argv=None):
    p = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    p.add_argument("--ledger", default=os.environ.get("LOOP_LEDGER") or os.path.join(
        os.environ.get("XDG_STATE_HOME") or os.path.expanduser("~/.local/state"), "ticketing", "ledger.jsonl"))
    p.add_argument("--project")
    p.add_argument("--batch")
    p.add_argument("--since", help="YYYY-MM-DD, local time")
    p.add_argument("--by", action="append", choices=["step", "agent", "model", "batch", "ticket"])
    a = p.parse_args(argv)
    since = int(datetime.datetime.strptime(a.since, "%Y-%m-%d").timestamp()) if a.since else None
    rs = rows(a.ledger, a.project, a.batch, since)
    if not rs:
        print(f"no steps recorded in {a.ledger}")
        return 0
    print("\n\n".join(table(by, group(rs, by)) for by in (a.by or ["step", "model"])))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
