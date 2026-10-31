---
title: Before you say it is done
kind: checklist
weight: 0
source_paths: Justfile
---
- The change is the ticket, and nothing else is in the diff.
- `just gate` is green, and you ran it rather than assuming — chained with `&&` into the commit, so a
  red gate stops it. It runs `just lint`, `just test` and `just render-check`, stopping at the first
  failure. The render check is the only thing that catches a CSS regression (the web tests run in jsdom
  and never load the stylesheet), so do not swap it for `lint && test`.
- **The gate in full**, which is what a batch's eval ticket has to run: `just gate`, then
  `just guidance-check`, plus `just selftest-quick` when the change touched `scripts/` or `eval/`.
  Run each on its own and never piped through `tail`, so a failure is not swallowed. A ticket's `gate_passed` comes only from `scripts/run-checks.sh`, which also fails it when a guidance document is
  stale and this branch is what moved its `source_paths`: correct that document in this ticket, PUT it, and re-pin only it with `./scripts/guidance-check.sh --accept-only <slug>` (TP-295).
- **A step run as somebody's subagent still reports its usage** (rule 23): its transcript is `~/.claude/projects/<project>/<session>/subagents/agent-<id>.jsonl`, cumulative across that agent's steps, so bound it — `scripts/usage-report.sh --key <KEY> --since <RFC3339> --until <RFC3339> <transcript>`. A claim that reports nothing is named by key in the digest's cost line (TP-296).
- **The dead-code pass is `just deadcode`** (TP-169), and the eval ticket names it. It is a recipe
  rather than a command you type because running the tool from `api/` dies on a missing `go.sum`
  entry: the recipe runs it in a throwaway copy of `api/` so the tracked module graph is never
  touched. `just deadcode -test` shows what only tests reach, which is how the six `internal/testdb`
  helpers it always lists are known to be false positives. What it finds and nobody deleted is a
  finding, not a shrug.
- The move to `done` carries the evidence your ticket's type asks for (`working-a-ticket` has the
  table); `verified_by` quotes the command and what it printed, not "tests pass".
- If this is a batch's eval ticket, every gate command in it passed and its demo was run, and the
  resolution says so. Closing it instead of finishing it does not finish the batch. Run
  `./scripts/batch-size.sh <the batch's id>` too, while you have the numstat in front of you. It
  refuses if the branch carries a ticket the batch does not list, because those lines are not the
  batch's to claim: add the ticket to the batch and measure again. The API
  never runs git, so without it the batch's digest reads "size unknown" and goal 9 goes unmeasured for
  that batch.
- New behaviour has a test that fails without the change. A test that passes either way documents
  nothing and will be deleted by whoever discovers that.
- The schema change, if any, is in a migration **and** in `docs/schema.sql`. Those two disagreeing is
  the most expensive kind of drift here, because the second one is what everybody reads.
- If you edited the GraphQL SDL in `docs/api.md`, you read its diff before `just generate`: gqlgen
  deletes the resolver of any field that disappears, without a word.
- If the loop ran you, what your step cost is already on the ticket and in the ledger (`just cost`):
  `scripts/loop.sh` records both as the step ends. If you were run by hand, report it yourself — `./scripts/usage-report.sh --key <ticket>
  <transcript.jsonl>` — or the digest's cost section can only say it was not measured.
- The resolution says what changed, in one line. It becomes that ticket's line in the batch digest,
  so write it for the human reading the digest, not for yourself.
- Anything you decided — accepted as written, deferred, rejected, reframed — is a `record_decision`
  with evidence, or the digest will not know it happened.
- Nothing secret is in the diff. `.env.example` only.
