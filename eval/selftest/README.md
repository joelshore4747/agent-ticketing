# The self-test

Eight pairs whose answer is already known, and two probes. It runs before any paid pass and on every
reviewer-prompt edit: `./scripts/eval-selftest.sh`, or `--no-judge` for the free half.

A broken figure produces numbers exactly as readily as a working one, and no statistic downstream
will tell you which you have. That is what this is for.

## The cases

| case | proves |
|---|---|
| `obvious` | The figures can separate a real fix with a discriminating test from a Go-side dedupe whose map iteration is itself unordered, no test, and an unrelated file reformatted. Every figure that owns an axis here must see it **in both orders**. A figure that splits on this has a prompt problem, and no subtler case can tell you anything until it is fixed. |
| `subtle` | Two one-line `ORDER BY` changes of identical shape. One breaks the tie on `actor`, which repeats, so the reshuffling the ticket describes survives the "fix". Only `correctness` owns that; everyone else should tie. |
| `identical` | The same change, wrapped differently. Nobody should have a preference. A figure that consistently picks the first one has a **within-order** position bias, which swapping the order cannot cancel — the swap only cancels a preference for a position, not a preference for a wrapping. |
| `scope` | The same fix twice, one dragging 40 lines of reformatting through a file the ticket never mentions. `scope-auditor` must see it; nobody else is asked to. |
| `leak-quote` | A diff quoting a sentence of the corpus verbatim, with no slug and no arm word. The packer must set `leak_suspected` and the pair must leave the primary analysis. No judge is called. |
| `leak-soft` | The same fix twice, one side adding a comment only it wrote that says "guidance". A soft token on a line one arm alone authored could be the arm talking: the packer must set `leak_suspected`. No judge is called. |
| `leak-soft-shared` | Both sides add the same test-mock line, word for word, which says "Guidance" because the app's nav queries it. A line both arms wrote identically cannot reveal the arm: the packer must build the pair **without** flagging it. This is the real boilerplate that excluded every pair before TP-52. No judge is called. |
| `leak-token` | A diff naming a guidance document by slug. The packer must **abort the build**, not filter it out: a pack that needs filtering came from a capture that is already broken. |

## The probes

- **`unreachable`** — a figure pointed at a model that does not exist, so a call that never returns
  lands as `status: "malformed"`, `attempts: 2`, and is counted rather than dropped. The obvious
  version of this probe (a prompt demanding prose) does not work: `--json-schema` constrains the
  output, so a figure told to write paragraphs still returns a valid verdict. Good property of the
  harness, bad property of the probe.
- **`isolation-probe`** — asks the judge what project it is in and what a guidance document is. It
  must not know. This is what proves `--safe-mode`, `--tools ""` and the empty working directory did
  something: `CLAUDE.md` explains the guidance system in prose, and a judge that reads it knows
  exactly what it is meant to be blind to. A call that errors is reported `SKIP`, never `FAIL` —
  reporting a rate limit as a blinding failure would train you to ignore the one assertion that has
  to be trusted.

## Expectations

`meta.toml` per case. `better` names the side that should win, or `neither`.

- `better` — must pick that side in **both** orders.
- `tie` — must **not** have a consistent preference: a tie in either order, or the two orders
  disagreeing. Both are the figure declining to invent a difference.
- `any` — recorded, not asserted. The axis is not this figure's.
- `overlap` — a figure that should tie here and measurably does not. Recorded, not asserted, and
  printed loudly. See below.

## Freeze the fixtures before tuning a prompt

Tuning prompts against frozen fixtures is the intended workflow. Editing a fixture until a rubric
passes it is not — the case stops testing anything the moment you do, and it will still be sitting
there looking like a test. Each case's sha is printed on every run so a change is visible.

There is one legitimate reason to change a case: it is **invalid**, meaning its answer is not
derivable from what the judge is shown. The first `subtle` turned on `NULLS LAST`, which is dead
code — `started_at` is `NOT NULL` — and the judges never see the schema, so it tested their luck.
It was replaced, and the replacement says so in its own note.

## The overlap this measured

`maintainer` is marked `overlap` on `subtle`. Shown two one-line changes where one does not fix the
bug, Sonnet picks the working one in both orders, even when told in as many words that whether the
change works is another reviewer's question. Two rounds of prompt tightening did not move it and a
third would have been fitting the prompt to this one fixture.

So it is recorded rather than hidden: **`maintainer` and `correctness` are not independent voters on
a pair whose only difference is correctness.** That is exactly why `eval-report.py` makes per-figure
results primary and prints the pooled line without a p-value. A suite that is permanently red gets
ignored; a suite that quietly drops the case stops measuring. This does neither.
