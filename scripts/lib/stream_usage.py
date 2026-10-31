#!/usr/bin/env python3
"""What a `claude -p --output-format stream-json` run cost, and how much of the plan it has used.

    stream_usage.py LOG               -> "cost input output cache_write cache_read" (cost is "null" if absent)
    stream_usage.py --json LOG        -> the same as the body POST /api/tickets/:key/usage takes
    stream_usage.py --windows LOG     -> the plan's five-hour and seven-day windows as JSON, or {}
    stream_usage.py --windows LOG OUT -> the same plus seen_at, written to OUT (nothing when {})
    stream_usage.py --ledger LOG OUT k=v... -> appends one JSON line to OUT: the k=v fields, the time,
                                       the model(s) and the usage, so every step's cost outlives its run

The cost comes from the final `type:"result"` line; the windows from the last `rate_limit_event`,
which the CLI emits unasked, so neither figure costs an extra call (TP-229). Both scan from the tail
rather than assuming the literal last line, since a truncated or empty log (the process was killed by
a timeout) must not crash the caller; they report as no data. Shared by eval.sh and loop.sh, and
`events` by step_errors.py (TP-190), so there is one parser of a step log (goal 3).
"""
import json
import os
import sys
import tempfile
import time

WINDOWS = ("five_hour", "seven_day")


def events(path):
    """Every JSON object in the log, last first. A line that is blank or half-written is skipped, and
    a log that cannot be opened at all yields nothing."""
    try:
        with open(path, encoding="utf-8", errors="replace") as f:
            lines = f.readlines()
    except OSError:
        return
    for line in reversed(lines):
        line = line.strip()
        if not line:
            continue
        try:
            yield json.loads(line)
        except ValueError:
            continue


def usage(path):
    for d in events(path):
        if d.get("type") == "result":
            u = d.get("usage") or {}
            return {"cost_usd": d.get("total_cost_usd"), "input_tokens": u.get("input_tokens", 0),
                    "output_tokens": u.get("output_tokens", 0),
                    "cache_creation_input_tokens": u.get("cache_creation_input_tokens", 0),
                    "cache_read_input_tokens": u.get("cache_read_input_tokens", 0),
                    "wall_time_seconds": int((d.get("duration_ms") or 0) / 1000)}
    return {"cost_usd": None, "input_tokens": 0, "output_tokens": 0, "cache_creation_input_tokens": 0,
            "cache_read_input_tokens": 0, "wall_time_seconds": 0}


def windows(path):
    """How much of the plan the account had used, as {five_hour, seven_day} x {utilization, resets_at}.

    `utilization` is the fraction the CLI reports (0.35 is 35%) and `resets_at` is epoch seconds.
    Scanning last first means the newest event wins without comparing timestamps. An event carrying no
    usable window is passed over rather than returned as the answer, so an older one that has them
    still counts; a log with none at all gives {}, which reads as "usage unknown".
    """
    for d in events(path):
        if d.get("type") != "rate_limit_event":
            continue
        unified = (d.get("rate_limit_info") or {}).get("unifiedWindows") or {}
        found = {}
        for name in WINDOWS:
            w = unified.get(name) or {}
            if "utilization" in w or "resetsAt" in w:
                found[name] = {"utilization": w.get("utilization", 0), "resets_at": w.get("resetsAt", 0)}
        if found:
            return found
    return {}


def model(path):
    """Which model(s) the step ran on: the result's per-model breakdown when the CLI gives one (a step
    that delegated may name several), else the init event's model, else "unknown"."""
    init = None
    for d in events(path):
        if d.get("type") == "result" and d.get("modelUsage"):
            return ",".join(sorted(d["modelUsage"]))
        if d.get("type") == "system" and d.get("subtype") == "init" and d.get("model"):
            init = d["model"]
    return init or "unknown"


def append_ledger(path, out, fields):
    """One line per step in a ledger that lives outside the run's temp directory, because the claim a
    ticket step posts to holds only the last step on that ticket and every other step posts nowhere."""
    row = {"ts": int(time.time()), **fields, "model": model(path), **usage(path)}
    os.makedirs(os.path.dirname(out) or ".", exist_ok=True)
    with open(out, "a", encoding="utf-8") as f:
        f.write(json.dumps(row) + "\n")
    return row


def write_windows(path, out):
    """Stamp the log's windows with the time they were seen and put them in `out`, atomically so a
    reader never sees half a file. Empty windows leave whatever is already there alone: a step whose
    log carried no event says nothing about the plan, and the last real figures beat no figures."""
    found = windows(path)
    if not found:
        return {}
    found["seen_at"] = int(time.time())
    os.makedirs(os.path.dirname(out) or ".", exist_ok=True)
    fd, tmp = tempfile.mkstemp(dir=os.path.dirname(out) or ".")
    try:
        with os.fdopen(fd, "w") as f:
            json.dump(found, f)
        os.replace(tmp, out)
    except BaseException:
        os.unlink(tmp)
        raise
    return found


if __name__ == "__main__":
    if sys.argv[1:2] == ["--json"]:
        u = usage(sys.argv[2])
        if u["cost_usd"] is None:
            del u["cost_usd"]
        print(json.dumps(u))
    elif sys.argv[1:2] == ["--ledger"]:
        append_ledger(sys.argv[2], sys.argv[3], dict(a.split("=", 1) for a in sys.argv[4:]))
    elif sys.argv[1:2] == ["--windows"]:
        if sys.argv[3:4]:
            write_windows(sys.argv[2], sys.argv[3])
        else:
            print(json.dumps(windows(sys.argv[2])))
    else:
        u = usage(sys.argv[1])
        print("null" if u["cost_usd"] is None else u["cost_usd"], u["input_tokens"], u["output_tokens"],
              u["cache_creation_input_tokens"], u["cache_read_input_tokens"])
