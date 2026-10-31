#!/usr/bin/env python3
"""Turns a run's artefacts into blinded pair packs, or refuses to.

A pack is what a judge sees: one ticket and two unified diffs, nothing else. Everything narrative —
the resolution, the comments, the tickets the agent filed, the transcript, lint and test results —
is left out, because every one of those is prescribed by a document that only the guided arm was
given, and a redaction that preserves meaning is itself arm-correlated. Those are measured
mechanically instead and reported in their own table.

This script is where the experiment is either honest or not, so it fails loudly:

  * a pair whose two sides did not work the same ticket is rejected
  * a run id used in more than one pair is rejected, because the sign test assumes independence
  * an arm token anywhere in the assembled bytes aborts the whole build
  * a verbatim quotation from the corpus flags the pair rather than silently rewriting it
  * an oversize pair is truncated to the SAME byte count on both sides, or not at all

Run `--check-prompts` before any pass: it fails when a reviewer prompt asks a question the corpus
answers, which is the difference between measuring quality and measuring compliance.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
import random
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
PGURL = os.environ.get("EVAL_PGURL", "postgres://ticketing:ticketing@127.0.0.1:5434")
TEMPLATE = os.environ.get("EVAL_TEMPLATE", "eval_template")

# Total characters per pack, and per side. Haiku 4.5 holds far more; the cap is here so that a
# runaway diff cannot quietly become a different kind of stimulus from every other pack.
PACK_CHARS = 120_000
SIDE_CHARS = 50_000

# Tokens that name the experiment machinery itself — a transport header, an MCP tool name, a
# bundle field name. These cannot appear in ordinary product code by coincidence, so a hit here
# means the capture leaked something, not that a word collided, and the build aborts.
HARD_TOKENS = ["X-Eval", "eval_", "report_guidance_drift", "get_guidance",
               "claim_next_ticket", "next_steps"]

# Words that describe the experiment in English rather than name it — and, on THIS project, are
# also ordinary product vocabulary: the ticketing app being evaluated has its own real "guidance"
# feature, and a diff touching anything near it (or the shared Rail component every page test has
# to mock) will legitimately say "guidance" for reasons that have nothing to do with which arm ran.
# Confirmed on a real run: both arms independently wrote `data.includes('query Guidance')` in test
# mock boilerplate, identically, because the app's own shared nav queries a Guidance count — not
# because either arm knew its treatment. A hard abort on a bare dictionary word is indistinguishable
# from a real leak to the script; a human is not. These are flagged (leak_suspected) rather than
# fatal, matching the corpus-quote n-gram scanner below, and a flagged pair is excluded from
# eval-report.py's primary analysis.
#
# Only lines ONE arm wrote can flag. Scanning the whole pack flagged that boilerplate line anyway —
# it sat in both diffs, word for word — and so excluded every pair this project could produce: its
# shared nav queries a Guidance count, so nearly any web diff says the word. A line both arms wrote
# identically cannot tell a judge which arm is which, and neither can the ticket, which both packs
# share, nor a context or removed line, which is the base commit's text rather than the agent's.
SOFT_TOKENS = ["control", "guided", "placebo", "replicate", "guidance",
               "batch brief", "style guide", "house style"]


def sha256(text: str) -> str:
    return hashlib.sha256(text.encode()).hexdigest()


def psql(db: str, sql: str) -> str:
    out = subprocess.run(["psql", f"{PGURL}/{db}", "-qtAX", "-c", sql],
                         capture_output=True, text=True)
    if out.returncode != 0:
        sys.exit(f"psql failed against {db}: {out.stderr.strip()}")
    return out.stdout.strip()


def corpus() -> list[dict]:
    """The served truth, read from the template database — not from docs/guidance, which is only
    the seed and may have drifted from what the runs were actually given."""
    raw = psql(TEMPLATE, """
        SELECT json_agg(json_build_object('slug', d.slug, 'title', d.title,
                                          'body', r.body, 'sha', r.content_sha256) ORDER BY d.slug)
        FROM guidance_documents d JOIN guidance_revisions r ON r.id = d.current_revision_id
        WHERE d.archived_at IS NULL""")
    return json.loads(raw) if raw and raw != "" else []


def template_sha(docs: list[dict]) -> str:
    return sha256("\n".join(f"{d['slug']}:{d['sha']}" for d in docs))


# Six words, not eight. Eight missed "Business rules live only in internal/service" — a whole
# sentence of the corpus, quoted verbatim in a comment — because the sentence is seven words long
# once punctuation is dropped, and the eighth word came from the line after it. A quotation short
# enough to fit in a comment is exactly the quotation that leaks, so the window has to be shorter
# than the sentences being quoted. Six over-fires occasionally; that only flags a pair for
# exclusion, and the report prints the flag rate per arm so over-firing is visible.
def ngrams(text: str, n: int = 6) -> set[str]:
    words = re.findall(r"[a-z0-9]+", text.lower())
    return {" ".join(words[i:i + n]) for i in range(max(0, len(words) - n + 1))}


# --- prompts vs the corpus ---------------------------------------------------

def check_prompts(docs: list[dict]) -> int:
    """A reviewer criterion that is also a sentence in a served document is not a quality judgement:
    it asks 'did this agent read the corpus', which the arm answers on its own. Catch it here rather
    than discovering it in the numbers."""
    corpus_grams: set[str] = set()
    for d in docs:
        corpus_grams |= ngrams(d["body"], 6)

    failures = 0
    for path in sorted((ROOT / "eval" / "reviewers").glob("*.md")):
        body = path.read_text()
        # The probation banner quotes the problem on purpose; it is a comment, not a criterion.
        body = re.sub(r"<!--.*?-->", "", body, flags=re.S)
        hits = sorted(ngrams(body, 6) & corpus_grams)
        if hits:
            failures += 1
            print(f"COLLIDES {path.name}")
            for h in hits[:5]:
                print(f"    “{h}”")
    if failures:
        print(f"\n{failures} reviewer prompt(s) restate the corpus. Either move the criterion into a\n"
              "guidance_probes row, where it is free and deterministic, or ask a different question.")
    else:
        print(f"ok  {len(list((ROOT / 'eval' / 'reviewers').glob('*.md')))} prompts, no corpus overlap")
    return 1 if failures else 0


# --- pairing -----------------------------------------------------------------

def load_runs(results: Path, run: str) -> list[dict]:
    rows = [json.loads(l) for l in results.read_text().splitlines() if l.strip()]
    return [r for r in rows if r["run"] == run]


def pair_up(rows: list[dict]) -> tuple[list[tuple[dict, dict]], list[str]]:
    """Pairs are (ticket, replicate) — never a cross product. Two pairings that share a run share
    its noise, and a sign test over them is not measuring what it claims to."""
    by_key: dict[tuple[str, int], dict[str, dict]] = {}
    for r in rows:
        by_key.setdefault((r["ticket"], r["replicate"]), {})[r["arm"]] = r

    pairs, skipped, seen_runs = [], [], set()
    for (ticket, rep), arms in sorted(by_key.items()):
        name = f"{ticket}#{rep}"
        if set(arms) != {"control", "guided"}:
            skipped.append(f"{name}: has {sorted(arms)}, needs both arms")
            continue
        c, g = arms["control"], arms["guided"]
        if not (c.get("valid") and g.get("valid")):
            skipped.append(f"{name}: a side is invalid, so it says nothing about guidance")
            continue
        for side in (c, g):
            ident = f"{side['ticket']}/{side['arm']}/{side['replicate']}"
            if ident in seen_runs:
                sys.exit(f"{name}: run {ident} is already in another pair")
            seen_runs.add(ident)
        pairs.append((c, g))
    return pairs, skipped


def read_bundle(row: dict) -> dict:
    b = Path(row["bundle"])
    if not b.is_dir():
        sys.exit(f"missing bundle {b} — re-run eval.sh, or point --results at the right file")
    return {
        "diff": (b / "diff.patch").read_text(errors="replace"),
        "ticket": json.loads((b / "ticket.json").read_text() or "{}"),
    }


# --- assembling --------------------------------------------------------------

def ticket_section(t: dict) -> str:
    lines = [f"## The ticket", "",
             f"{t.get('key', '?')} — {t.get('title', '')}", "",
             f"Type: {t.get('type')}  ·  Priority: {t.get('priority')}  ·  Area: {t.get('area') or 'none'}",
             "", (t.get("description") or "").strip()]
    locs = t.get("locations") or []
    if locs:
        lines += ["", "Reported locations:"]
        lines += [f"  {l['file_path']}" + (f":{l['line_start']}" if l.get("line_start") else "")
                  for l in locs]
    return "\n".join(lines)


def authored_lines(diff: str) -> set[str]:
    """The lines of a diff the agent wrote: added lines and file headers, not the base commit's
    context, removed lines or hunk headers. Whitespace-normalised, so re-indenting a shared line
    does not make it look authored by one side alone."""
    return {" ".join(line.lower().split()) for line in diff.splitlines()
            if line and not line.startswith((" ", "@@")) and not (line.startswith("-") and not line.startswith("---"))}


def soft_token_hits(a: str, b: str) -> tuple[list[str], list[str]]:
    """(tokens on a line only one side wrote, tokens seen only on lines both sides wrote). The
    first flags the pair; the second is recorded so the report can show what was let through."""
    la, lb = authored_lines(a), authored_lines(b)
    one_sided, shared = la ^ lb, la & lb
    hits = sorted({t for t in SOFT_TOKENS for line in one_sided if t in line})
    cleared = sorted({t for t in SOFT_TOKENS for line in shared if t in line} - set(hits))
    return hits, cleared


def truncate_pair(a: str, b: str) -> tuple[str, str, bool]:
    """Both sides or neither. Cutting only the long one is the most direct way to manufacture a
    preference for short diffs, and diff length is arm-correlated."""
    if len(a) <= SIDE_CHARS and len(b) <= SIDE_CHARS and len(a) + len(b) <= PACK_CHARS:
        return a, b, False
    cap = min(SIDE_CHARS, PACK_CHARS // 2, max(len(a), len(b)))
    cut = lambda s: s[:cap].rsplit("\n@@", 1)[0] + "\n… truncated …\n"
    return cut(a), cut(b), True


def build_pack(ticket: dict, first: str, second: str, labels: tuple[str, str]) -> str:
    return "\n\n".join([
        "# Change review",
        ticket_section(ticket),
        f"## Change {labels[0]}\n\n```diff\n{first}\n```",
        f"## Change {labels[1]}\n\n```diff\n{second}\n```",
        "## Your task\n\nAnswer with the JSON object only.",
    ]) + "\n"


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--run")
    ap.add_argument("--results", default=os.environ.get("EVAL_RESULTS", str(ROOT / "eval-results.jsonl")))
    ap.add_argument("--out", default=os.environ.get("EVAL_ARTIFACTS", str(ROOT / "eval-artifacts")))
    ap.add_argument("--check-prompts", action="store_true")
    ap.add_argument("--dry-run", action="store_true", help="build and assert, print the first pack, write nothing")
    ap.add_argument("--seed", type=int, default=0, help="0 means derive from the run id, so a rebuild is identical")
    args = ap.parse_args()

    docs = corpus()
    if args.check_prompts:
        return check_prompts(docs)
    if not args.run:
        sys.exit("--run is required unless --check-prompts")
    if not docs:
        sys.exit(f"no guidance documents in {TEMPLATE}: the deny-list would be empty and the scan meaningless")

    rows = load_runs(Path(args.results), args.run)
    if not rows:
        sys.exit(f"no rows for run {args.run} in {args.results}")
    pairs, skipped = pair_up(rows)
    for s in skipped:
        print(f"skip  {s}")
    if not pairs:
        sys.exit("no pairs to build")

    deny = HARD_TOKENS + [d["slug"] for d in docs] + [d["title"] for d in docs] + [args.run]
    corpus_grams: set[str] = set()
    for d in docs:
        corpus_grams |= ngrams(d["body"])

    rng = random.Random(args.seed or sha256(args.run))
    out_root = Path(args.out) / args.run / "pairs"
    built = []

    for control, guided in pairs:
        name = f"{control['ticket']}#{control['replicate']}"
        cb, gb = read_bundle(control), read_bundle(guided)
        if not cb["diff"].strip() or not gb["diff"].strip():
            print(f"skip  {name}: a side changed nothing — that is a completion outcome, not a preference")
            continue
        if cb["diff"] == gb["diff"]:
            print(f"skip  {name}: the two sides are byte-identical")
            continue

        c_text, g_text, truncated = truncate_pair(cb["diff"], gb["diff"])
        labels = (f"{rng.randrange(16**4):04x}", f"{rng.randrange(16**4):04x}")

        packs = {
            1: build_pack(cb["ticket"], c_text, g_text, labels),           # control first
            2: build_pack(cb["ticket"], g_text, c_text, (labels[1], labels[0])),
        }

        for order, text in packs.items():
            hit = next((t for t in deny if t and t.lower() in text.lower()), None)
            if hit:
                sys.exit(f"ABORT {name} order{order}: the pack contains {hit!r}.\n"
                         "That is the arm, or something that names it, inside material a judge is "
                         "meant to be blind to. Fix the capture, do not filter it out here.")

        soft_hits, soft_shared = soft_token_hits(c_text, g_text)
        quoted_corpus = bool((ngrams(c_text) | ngrams(g_text)) & corpus_grams)
        leak = quoted_corpus or bool(soft_hits)
        if quoted_corpus:
            print(f"flag  {name}: a diff quotes the corpus verbatim — excluded from the primary analysis")
        if soft_hits:
            print(f"flag  {name}: {', '.join(soft_hits)!r} on a line only one side wrote — could be the "
                  f"experiment or could be ordinary vocabulary; excluded from the primary analysis, not aborted")
        if soft_shared:
            print(f"note  {name}: {', '.join(soft_shared)!r} only on lines both sides wrote identically — not flagged")

        key = {
            "pair": name, "ticket": control["ticket"], "replicate": control["replicate"],
            "orders": {"1": {"first": "control", "second": "guided"},
                       "2": {"first": "guided", "second": "control"}},
            "labels": {"control": labels[0], "guided": labels[1]},
            "leak_suspected": leak, "soft_token_hits": soft_hits, "soft_token_shared": soft_shared,
            "truncated": truncated,
            "pack_sha256": {str(o): sha256(t) for o, t in packs.items()},
            "pack_bytes": [len(c_text), len(g_text)],
            "template_sha": template_sha(docs),
            "builder_sha256": sha256(Path(__file__).read_text()),
            "redaction_sha256": sha256("\n".join(deny + SOFT_TOKENS)),
        }
        built.append(key)

        if not args.dry_run:
            d = out_root / name.replace("#", "_")
            d.mkdir(parents=True, exist_ok=True)
            for order, text in packs.items():
                (d / f"order{order}.md").write_text(text)
            (d / "pair.json").write_text(json.dumps(key, indent=2))

    if args.dry_run and built:
        first = built[0]["pair"].replace("#", "_")
        print("\n" + "=" * 72)
        print(f"first pack ({built[0]['pair']}, order 1) — read this yourself before paying for a pass")
        print("=" * 72)
        print(build_pack(read_bundle(pairs[0][0])["ticket"],
                         *truncate_pair(read_bundle(pairs[0][0])["diff"],
                                        read_bundle(pairs[0][1])["diff"])[:2],
                         (built[0]["labels"]["control"], built[0]["labels"]["guided"]))[:4000])

    print(f"\n{len(built)} pair(s){' (dry run, nothing written)' if args.dry_run else f' → {out_root}'}")
    print(f"  leak_suspected: {sum(p['leak_suspected'] for p in built)}   truncated: {sum(p['truncated'] for p in built)}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
