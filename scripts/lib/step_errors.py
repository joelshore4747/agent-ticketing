#!/usr/bin/env python3
"""Failed tool calls in a loop step's log, grouped by tool and message so a repeat shows (TP-190).

    step_errors.py LOG...   one line per group, "<count>  <tool>: <error>", commonest first

A tool_result carrying is_error is the cheapest evidence there is of a mistake the loop keeps making
— a 409 nobody re-read, a path that is not there, a status move the API refuses — and it used to be
thrown away with the log. Counting is all this does; which counts are worth acting on is the caller's
(loop.sh hands the after-review the ones above one). The reader is stream_usage's: one parser (goal 3).
"""
import sys
from collections import Counter

from stream_usage import events


def failures(paths):
    """How often each (tool, its error collapsed to one line) failed across these logs."""
    names, failed = {}, []
    for path in paths:
        for d in events(path):  # last first, so a result is read before the tool_use that names it
            for b in (b for b in (d.get("message") or {}).get("content") or [] if isinstance(b, dict)):
                if b.get("type") == "tool_use":
                    names[b.get("id")] = b.get("name") or "a tool"
                elif b.get("type") == "tool_result" and b.get("is_error"):
                    # Collapsed and cut short: two runs of one mistake must land in the same group.
                    failed.append((b.get("tool_use_id"), " ".join(str(b.get("content")).split())[:120]))
    return Counter((names.get(i, "a tool"), line) for i, line in failed)


if __name__ == "__main__":
    for (tool, line), n in failures(sys.argv[1:]).most_common():
        print(f"{n}  {tool}: {line}")
