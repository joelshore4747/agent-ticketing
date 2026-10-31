#!/usr/bin/env python3
"""Is the tracker working? One row per batch: how long it took, how much of that was building, what
stopped it and for how long, whether its eval passed first time, what it cost and how much it grew.

    scorecard.py [--project KEY] [--log RUN_LOG] [--ledger PATH] [--api URL]

Joel asked for this on 2026-10-18 to decide whether to keep improving the harness or loosen it into
something more like Jira. The number to watch is **harness stops**: times the loop stopped itself
for a reason that was neither Joel nor the plan's usage limit. If those, and the time they cost,
outweigh the building, the rules are in the way.

It reads three records and writes nothing:
- the ledger loop.sh appends a line to per agent step (cost, wall time, step kind);
- the tracker's batches (name, size, checkpoint time);
- the run log (`fs-run.log`), where every stop and every restart is written with its time.
"""
import argparse
import datetime
import json
import os
import re
import urllib.request

import cost

DATED = re.compile(r"^\[(\d{4}-\d\d-\d\d) (\d\d:\d\d(?::\d\d)?)\]")
TIMED = re.compile(r"^\[(\d\d:\d\d(?::\d\d)?)\]")
STOPPED = re.compile(r"^loop: stopped: (.*)")
STEP_LOG = re.compile(r"(/tmp/ticketing-loop\.\w+/[\w.-]+\.jsonl)")


def classify(reason, step_result=""):
    """Who a stop was waiting on: "you" (a needs-joel ticket or tripwire), "usage" (the plan's limit,
    whether the guard stopped first or a step hit the limit mid-way), or "harness" (anything else:
    the loop's own rules or bugs)."""
    if "waiting on Joel" in reason:
        return "you"
    if "LOOP_USAGE_LIMIT" in reason or re.search(r"hit your .*limit", step_result, re.I):
        return "usage"
    return "harness"


def step_result(reason):
    """The result text of the step log a stop names, or "" when it names none or the log is gone."""
    m = STEP_LOG.search(reason)
    if not m:
        return ""
    try:
        with open(m.group(1), encoding="utf-8", errors="replace") as f:
            for line in f:
                if '"type":"result"' in line:
                    return str(json.loads(line).get("result") or "")
    except (OSError, ValueError):
        pass
    return ""


def stops(lines, read_result=step_result):
    """Every stop in a run log as {ts, kind, reason, resumed}: `resumed` is the time of the next line
    that shows the loop working again, or None if it has not restarted. Undated lines carry only a
    time, so the date comes from the last dated line, moving on a day when the clock goes backwards."""
    out, day, last = [], None, None
    for line in lines:
        dated, timed = DATED.match(line), TIMED.match(line)
        if dated:
            day = datetime.date.fromisoformat(dated.group(1))
            last = _at(day, dated.group(2))
        elif timed and day:
            at = _at(day, timed.group(1))
            if last and at < last - 3600:  # past midnight; a minute's jitter is not a new day
                day += datetime.timedelta(days=1)
                at = _at(day, timed.group(1))
            last = at
        if (dated or timed) and out and out[-1]["resumed"] is None and last > out[-1]["ts"]:
            out[-1]["resumed"] = last
        stopped = STOPPED.match(line)
        if stopped and last:
            reason = stopped.group(1).strip()
            out.append({"ts": last, "kind": classify(reason, read_result(reason)), "reason": reason, "resumed": None})
    return out


def _at(day, clock):
    parts = [int(p) for p in clock.split(":")] + [0]
    return datetime.datetime(day.year, day.month, day.day, parts[0], parts[1], parts[2]).timestamp()


def _utc(stamp):
    return datetime.datetime.fromisoformat(stamp.replace("Z", "+00:00")).timestamp() if stamp else None


def scorecard(batches, ledger_rows, run_stops, now):
    """One row per batch that has been worked, in roadmap order. A batch's window runs from its first
    agent step (its `started_at` is when it was planned as a placeholder, often days earlier) to its
    checkpoint, or to `now` while it is open; each stop is charged to the batch whose window holds it."""
    rows = []
    for b in sorted(batches, key=lambda b: b.get("sequence") or 0):
        steps = [r for r in ledger_rows if r.get("batch") == b["id"]]
        if not steps:
            continue
        start = min(r["ts"] for r in steps)
        end = _utc(b.get("checkpoint_at")) or now
        mine = [s for s in run_stops if start <= s["ts"] <= end]
        lost = {k: sum(min(s["resumed"] or now, end) - s["ts"] for s in mine if s["kind"] == k)
                for k in ("harness", "you", "usage")}
        added, deleted = b.get("lines_added"), b.get("lines_deleted")
        rows.append({
            "name": b["title"], "done": bool(b.get("checkpoint_at")),
            "elapsed": end - start, "building": sum(r.get("wall_time_seconds") or 0 for r in steps),
            "harness_stops": sum(1 for s in mine if s["kind"] == "harness"), "lost": lost,
            "evals": sum(1 for r in steps if r.get("step") == "evaluator"),
            "cost": sum(r.get("cost_usd") or 0 for r in steps),
            "code_net": None if added is None else (added - (b.get("test_lines_added") or 0))
                                                   - (deleted - (b.get("test_lines_deleted") or 0)),
            "tests_added": b.get("test_lines_added"),
        })
    return rows


def hours(seconds):
    return f"{seconds / 3600:.1f}h"


def render(rows):
    head = (f"{'batch':<34} {'took':>6} {'built':>6} {'harness stops':>13} {'lost to: harness':>16} "
            f"{'you':>6} {'usage':>6} {'evals':>5} {'$':>6} {'code net':>8} {'tests +':>7}")
    lines = [head, "-" * len(head)]
    for r in rows:
        name = r["name"][:32] + ("" if r["done"] else " *")
        lines.append(f"{name:<34} {hours(r['elapsed']):>6} {hours(r['building']):>6} {r['harness_stops']:>13} "
                     f"{hours(r['lost']['harness']):>16} {hours(r['lost']['you']):>6} {hours(r['lost']['usage']):>6} "
                     f"{r['evals']:>5} {r['cost']:>6.2f} {_signed(r['code_net']):>8} {_signed(r['tests_added']):>7}")
    if rows:
        lines += ["", verdict(rows)]
    return "\n".join(lines)


def _signed(n):
    return "?" if n is None else f"{n:+d}"


def verdict(rows):
    """The two sentences the table is for: how often the harness got in its own way, and what share
    of the time went on building rather than waiting."""
    n = len(rows)
    harness = sum(r["harness_stops"] for r in rows)
    built = sum(r["building"] for r in rows)
    took = sum(r["elapsed"] for r in rows) or 1
    lost = sum(r["lost"]["harness"] for r in rows)
    first_time = sum(1 for r in rows if r["evals"] == 1)
    return (f"Harness stops: {harness} in {n} batches ({harness / n:.1f} a batch, {hours(lost)} lost); aim for 0.\n"
            f"Building was {built / took:.0%} of the time the batches took; evals that passed first time: "
            f"{first_time} of {n}.\n"
            "* still open. Evals counts the loop's evaluator steps; one run by hand in a session is not in the ledger.")


def fetch_batches(api, project):
    with urllib.request.urlopen(f"{api}/api/batches?project={project}") as r:
        data = json.load(r)
    return data.get("batches", data) if isinstance(data, dict) else data


def main(argv=None):
    p = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    p.add_argument("--project", default="FS")
    p.add_argument("--log", default=os.path.expanduser("~/.local/share/ticketing/fs-run.log"))
    p.add_argument("--ledger", default=os.environ.get("LOOP_LEDGER") or os.path.join(
        os.environ.get("XDG_STATE_HOME") or os.path.expanduser("~/.local/state"), "ticketing", "ledger.jsonl"))
    p.add_argument("--api", default=os.environ.get("API", "http://localhost:8080"))
    a = p.parse_args(argv)
    try:
        with open(a.log, encoding="utf-8", errors="replace") as f:
            run_stops = stops(f)
    except OSError:
        run_stops = []
    rows = scorecard(fetch_batches(a.api, a.project), cost.rows(a.ledger, a.project), run_stops,
                     datetime.datetime.now().timestamp())
    print(render(rows) if rows else f"no worked batches for {a.project} in {a.ledger}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
