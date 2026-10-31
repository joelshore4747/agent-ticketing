#!/usr/bin/env python3
"""Turns one judge call into one verdict row.

This is the only place the blinding is undone. `choice` is a position — "first" or "second" — and
becomes an arm here, by looking up which arm was shown first in this order. Doing it anywhere else
would mean two pieces of code that both know the mapping, and one of them being wrong.

A call that produced nothing usable is written with choice=null and status="malformed" rather than
dropped. Malformedness is not random: long, dense diffs blow budgets and trip refusals more often,
and diff length is arm-correlated, so a silent drop is a filter that selects on the treatment.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import subprocess
import sys
from datetime import datetime, timezone
from pathlib import Path


def sha256_file(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def cli_version() -> str:
    try:
        return subprocess.run(["claude", "--version"], capture_output=True, text=True,
                              timeout=30).stdout.strip().split()[0]
    except Exception:
        return "unknown"


def main() -> int:
    ap = argparse.ArgumentParser()
    for flag in ("pair", "figure", "order", "model", "run", "key", "prompt", "out"):
        ap.add_argument(f"--{flag}", required=True)
    ap.add_argument("--attempts", type=int, default=1)
    ap.add_argument("--duration", type=int, default=0)
    args = ap.parse_args()

    key = json.loads(Path(args.key).read_text())
    mapping = key["orders"][str(args.order)]

    raw = sys.stdin.read().strip()
    verdict, status, cost, usage = None, "malformed", None, {}
    if raw:
        try:
            envelope = json.loads(raw)
            payload = envelope.get("structured_output")
            if payload is None and isinstance(envelope.get("result"), str):
                payload = json.loads(envelope["result"])
            cost = envelope.get("total_cost_usd")
            # The full object, not just cost. This is the only place that can answer whether the
            # judges' identical-per-figure system prompts are actually being cached: cost alone
            # cannot show a cache_read_input_tokens figure, and eval-report.py had nowhere to read
            # it from until now — it was fetched over the wire and then discarded on every call.
            usage = envelope.get("usage") or {}
            # Validated here rather than trusted to --json-schema: the enum is what the analysis
            # depends on, and a shape that arrives wrong should be a recorded failure, not a crash.
            if (isinstance(payload, dict)
                    and payload.get("choice") in ("first", "second", "tie")
                    and payload.get("confidence") in ("low", "medium", "high")
                    and isinstance(payload.get("reason"), str)):
                verdict, status = payload, "ok"
        except (ValueError, TypeError):
            pass

    choice = verdict["choice"] if verdict else None
    winner = None
    if choice in ("first", "second"):
        winner = mapping[choice]

    row = {
        "review": datetime.now(timezone.utc).strftime("review-%Y%m%d-%H%M%S"),
        "run": args.run,
        "pair": args.pair,
        "ticket": key["ticket"],
        "replicate": key["replicate"],
        "figure": args.figure,
        "model": args.model,
        "cli_version": cli_version(),
        "order": int(args.order),
        "first_arm": mapping["first"],
        "second_arm": mapping["second"],
        "choice": choice,
        "winner": winner,
        "confidence": verdict.get("confidence") if verdict else None,
        "reason": verdict.get("reason") if verdict else None,
        "evidence": verdict.get("evidence", []) if verdict else [],
        "status": status,
        "attempts": args.attempts,
        "leak_suspected": key["leak_suspected"],
        "truncated": key["truncated"],
        "prompt_sha256": sha256_file(Path(args.prompt)),
        "pack_sha256": key["pack_sha256"][str(args.order)],
        "builder_sha256": key["builder_sha256"],
        "redaction_sha256": key["redaction_sha256"],
        "template_sha": key["template_sha"],
        "cost_usd": cost,
        "input_tokens": usage.get("input_tokens", 0),
        "output_tokens": usage.get("output_tokens", 0),
        "cache_creation_input_tokens": usage.get("cache_creation_input_tokens", 0),
        "cache_read_input_tokens": usage.get("cache_read_input_tokens", 0),
        "duration_ms": args.duration,
        "at": datetime.now(timezone.utc).isoformat(timespec="seconds"),
    }
    Path(args.out).write_text(json.dumps(row) + "\n")
    print(f"  {args.pair:<12} {args.figure:<16} order{args.order}  {status:<9} {winner or '-'}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
