---
title: Planning a batch
kind: workflow
weight: 5
source_paths: api/internal/service/plans.go, api/internal/service/order.go, api/internal/service/digest.go, scripts/loop.py
---
Work decided in advance is a plan batch, filed with `plan_batch`; findings from an audit or review go
through `report_findings`. **`plan_batch` is the only door to a plan batch** (TP-171): `start_batch`
and `POST /api/batches` refuse `kind: plan` with a 400 that names `plan_batch`, because plan_batch is
the call that files the eval ticket gating the batch. A plan opened any other way was a batch rule 16
could never let finish. The other kinds — audit, review, prompt — still open through `start_batch`.
A plan's tickets are worked in the order you give them, and it ends in an eval ticket that is added
for you. That eval measures the batch's net lines, and a batch that grew
fails it unless a decision citing the measurement says why, so plan for the batch to come out smaller.
**The ceiling counts non-test lines: net ≤ 0 non-test lines, or the Approver accepts the growth with
evidence.** It used to count every line alike, which charged a builder for the test that proved the
change and made thinning it the cheapest way through — 63% of batch 3's growth was tests, and TP-181
was closed unmerged at net +90 of which +41 was its own two test files (TP-212). `scripts/batch-size.sh`
splits the diff by path and posts both pairs, and the digest shows the test figure beside the code one,
so test lines stay visible without being charged. They are not therefore free: **tests run to about
1.5× the code they cover, and are table-driven**, so a case is a row rather than another copied
function. A file counts as a test by its path alone, so code moved into a test file to duck the ceiling
is caught in review, not by the measure.
That acceptance is the eval's own: the evaluator, or the reviewer before it, records it, naming what the brief
needed the lines for, and growth the brief did not ask for is a finding (Joel, 2026-10-18). The eval ticket `plan_batch` writes says so. **A batch whose diff
touches `.claude/agents/*` or guidance fails that check too unless a `record_decision` on the eval ticket cites the
digest's guidance-score line for each revision it changes, or an `eval.sh` A/B run on 3 past tickets** (TP-189). An edit that only corrects a document to match the code is exempt (Joel, 2026-10-16), and so is one a planned ticket asks for, which the plan's approval covers (Joel, 2026-10-18). So a batch that means to change guidance says so in a ticket.

**Write what, how and why for a human who will not read the code.** What the batch builds, how it
goes about it, why it matters. They are what the human reads at the checkpoint, and `why` is served to
whoever claims each ticket.

**The demo is how the human checks it in under five minutes.** A command to run, a page to open, a
number to read — never "review the diff". If you cannot write one, the batch is not ready to plan.

**Order is dependency order.** Ticket 2 may assume ticket 1 is done, because the claim guarantees it.
One ticket is one change on one branch; code you do not know yet means smaller tickets, not vaguer
ones.

**The gate is attached for you.** `plan_batch` and `add_to_batch` write the standing gate onto each
ticket as a public command check (`just gate`, expect 0), so `scripts/run-checks.sh <key>` records
`gate_passed` instead of finding nothing to gate on (TP-173). The eval ticket is skipped and a ticket
that already carries checks is left alone; never add the gate by hand.

**Name the goals each ticket serves.** Every planned ticket carries `goals`, the numbers of the key
goals it moves (`key-goals` guidance); `plan_batch` refuses one that names none. If work serves no goal,
it is not the next work. The last digest's "Goals served" and "Not touched" are where the next batch
is chosen from.

**Review before and after, from the memory.** A batch starts with a before-review and ends with an
after-review. The loop records the before-review itself: the memory the batch starts from and the size
of the diff since its commit. The after-review is the reviewer's, once the batch is built and before its
eval: it reads the last after-review and the batch's whole diff, not the whole codebase again, fixes and
trims what it finds, and records structure, architecture, hotspots, stale code and what should change,
filing what it could not fix as tickets. The after-review is what the next batch starts from, so plan
nothing per ticket that this one review already does.

**Tag from the managed list.** `list_tags` has them. If work needs a tag that is not there, ask the
human to add it rather than picking the nearest one.

**A lesson seen three times is promoted.** Lessons are counted records, and the loop's strategist
step names every `lesson` ticket at `seen_count` 3 or more, the ones in triage as well as the open ones
(TP-285). Each one named goes into this plan as
a ticket for a mechanical check — a test, a lint rule, a script or a gate line — or, when no check can
catch it, as a rule written into the guidance document that owns it. The lesson is closed when that
ticket is done, citing it; a pattern seen fewer than three times is left to recur. Naming one is not
promoting it: the Approver's step is told which due lessons no ticket in the plan cites — by key, in
the title or description of a ticket that is not the eval — and sends the plan back for them (TP-286).

**One feature per batch, planned when the loop reaches it.** A batch takes one feature all the way —
its eval ticket, its before and after reviews, its checkpoint — instead of building six and evaluating
none. So a roadmap's later batches start as placeholders: `plan_batch` with a roadmap and a sequence,
a title and what/how/why, no tickets, and a steering comment on the eval ticket naming the one feature
that batch is for (the steering for the next roadmap goes on the last placeholder's eval ticket). Every
roadmap ends with a roadmap review batch: the one time the whole codebase is read, the goals are scored
across the roadmap, the human's asks are checked against what was delivered, and the harness is audited. Their
tickets are planned only at the checkpoint before them, from what the previous batch found. Fill one
with `plan_batch(batch: <the placeholder's id>)`, which plans into the batch that is already there
rather than making a new one: it keeps its id, its place in the roadmap and its eval ticket, overwrites
the title and what/how/why with what you now know, and lands in one call. Do not file the work as
findings and move it in — that arrives without the batch's context and is not atomic. `add_to_batch` is
for a ticket that already exists. A checkpoint of `adjust` means the batches after it change before
they open.

**Finishing writes the digest, and the digest is only as good as the record.** `finish_batch`
computes a plain-English digest from the batch's what/how/why, each ticket's resolution, the
decisions in force and the verdicts, lists what is still waiting on the human, measures the batch
— lines added against deleted, the pass rate beside the batches before it in the roadmap, and a line per guidance revision a claim
in the batch carried, clean first try (the gate passed and every criterion ever judged passed) against reworked (a criterion missed,
or the ticket rejected), claims with no gate counted as neither (TP-188); a plan batch has such a line only because planning
attaches its standing guidance, unpinned (TP-302) — and
says what the batch cost when its sessions reported usage. It is the first thing read at the
checkpoint. Two of its figures come from outside, because the API never runs git and never measures a
session: run `./scripts/batch-size.sh <batch id>` before finishing, or the Measures section reads
"size unknown", the same way the cost section reads "not measured" unless a session reported usage —
`scripts/loop.sh` posts it per step, and `scripts/usage-report.sh --key` by hand (TP-182). It cannot
say more than you recorded: a vague resolution is a vague line, and a choice you
made without `record_decision` is simply missing. Read it back from the reply; if it misleads, fix the
record — a resolution, a decision — and finish again, which recomputes it.

**A batch lands on main once.** `plan_batch` names the batch's branch by one rule on both paths
(TP-170): the `git_ref` you give wins, else the one the batch already carries, else a name derived
from its roadmap place or id — so filling a placeholder keeps the branch it was planned with unless
you name another. Its tickets branch from it and merge back. When the batch has finished, `scripts/batch-merge.sh <id>` checks that every ticket branch
is in and merges the batch into main with one merge commit. Opening it as a pull request instead needs a
GitHub remote, which is waiting on TP-23.

**Change a plan with the commands, not around them.** `reorder_batch`, `add_to_batch` and
`remove_from_batch` keep started work in place and the eval ticket last. Something found mid-batch
that the rest depends on goes in before the eval ticket; anything else is its own ticket. **A fix you
commit onto the batch branch is in the batch, so put it there with `add_to_batch`**: its lines are in
the branch either way, and `scripts/batch-size.sh` refuses to report a figure that mixes them in
(TP-180), because a batch is judged and remembered by what its own tickets did. A `fail`
verdict on a criterion does this for you: its follow-up joins before the eval, in triage, so the eval
waits until the human has accepted it.
**The one change those three commands cannot make is abandoning
the batch, and that is a `stop` checkpoint**: on an unfinished plan batch none of whose tickets has
started, `checkpointBatch(decision: stop)` sets `finished_at` with the decision, so a batch that should
never have existed — a stale API created one (TP-175) — or a placeholder the roadmap no longer wants
reads `stopped` instead of sitting in the live list unfinished for good (rule 16). No digest is written,
because nothing happened to digest, and it is refused once any ticket has started: that work's account
is its eval, not a retirement. Inside a roadmap, though, `stop` still means what rule 15 says it means,
so retiring a slot locks the ones after it — a roadmap being replanned wants its placeholder *filled*
with `plan_batch(batch: <the placeholder's id>)`, not retired.
