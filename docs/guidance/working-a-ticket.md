---
title: Working a ticket
kind: workflow
weight: 0
source_paths: api/internal/service/tickets.go, api/internal/service/tickets_update.go, api/internal/service/tickets_claim.go
---
One ticket, one branch, one push. Branch `ticket/<key-in-lowercase>-<short-slug>`, commit subject
`<KEY>: <what changed>`. Both exist so that six months later `git log --grep` finds the reasoning
without anyone having to remember it. In a plan batch with a `git_ref`, branch from that batch branch
and merge back into it, not into main: the claim's `next_steps` give the exact commands, and main
takes the whole batch at once when it finishes.

**Work a planned batch in its order.** The claim hands out a plan batch's tickets by position, and a
roadmap's next batch only after the previous one's checkpoint. When a claim returns null, `get_batch`
says why: `progress.locked_by` for the roadmap, `blocked_by` for the ticket in front. Do not move a
ticket to `in_progress` by hand to get round it — the API refuses, and the order is the point: later
tickets are built on earlier ones. The last ticket is the eval gate; run what it says.

**The human owns what; you own how.** A claim carries the ticket's intent, constraints, non-goals and
success criteria, plus any public checks: that is what done means, and it is the human's. You cannot
edit it — if it is wrong or missing, say so with `propose_ticket_edit` and carry on with what you can.
Before writing code, write your `approach` (short; citations go in `evidence_refs`) and the exact
commands of your `verification_plan`; a feature also needs a `worked_example`. For anything
non-trivial, stop there and ask for approval in a comment — editing an approved approach clears the
approval. When you finish, `verified_by` holds the command you ran and its output, naming the test.

**Done needs its evidence, and the API checks.** Send it with the move to `done`:

| type | evidence |
|---|---|
| bug, chore, improvement, eval | `verified_by`: the command you ran and what it printed |
| feature | `verified_by` and a `worked_example` (one input, what it now produces) |
| refactor | `verified_by`, and a gate criterion linked to a confirmed tripwire |
| research | `evidence_refs`: citations, not recollections |
| design | an approved approach: the human's sign-off is its verification |

If the ticket has held-out checks you cannot see, the gate is run for you with
`scripts/run-checks.sh`; until it passes, done is refused and the refusal says so. Closing a ticket
as won't-do needs only a resolution. After a ticket is finished, its criteria are judged with
`record_verdict`; a fail is not a black mark, it is a follow-up ticket before the eval.

**Record what you decide, with what it rests on.** When you accept a ticket as written, defer it,
reject it or reframe it into a different problem, say so with `record_decision` and cite
`evidence_refs` — a file and line, a ticket key, an eval run. A decision that cites nothing is shown to
the human as uncited, which is the point: it is a guess until it has evidence. A deferral needs a
`revisit_trigger` that someone could check ("when TP-37 is merged", not "later"). Decisions are never
edited; a new one supersedes the current one. The claim envelope carries the ticket's decisions, so
read them before you start — somebody may already have decided what you were about to.

**Read the whole ticket before you touch anything.** Its `locations` are where the reporter was
looking, not a fix list — the cause is often one call up. If the ticket turns out to describe a
different problem than its title says, change the title. A ticket whose title lies is worse than no
ticket, because it will be closed as a duplicate of something it is not.

**Finish the ticket in the same session you started it.** The claim expires, and an expired claim
silently returns the work to the queue while you are still holding it, so two agents end up editing
the same file. If you cannot finish, put it back yourself and say why in a comment.

**The resolution is the deliverable, not a formality.** One line, what actually changed, in the past
tense: "Version conflicts now return 409 with the current version in the body." Nobody reads the
diff; everybody reads the resolution. If you cannot write it in one line, you probably changed more
than the ticket asked for.

**An update keeps a `needs-joel` ticket followable.** Rule 27 is judged on the tags and description
an update would leave behind, and only when the patch touches one of them: a patch that leaves the
tag on without a **What you do** section (heading or bold line, then two or more numbered steps from
1) is refused with a 400. Closing the ticket, or dropping the tag, is never blocked.

**Do not widen the scope.** If you find a second problem while fixing the first, file it as its own
ticket and carry on. A pull request that fixes three things gets reviewed as one thing and reverted
as one thing.

**Write code a careful human would, and could debug.** This is the standard every builder writes to
and the reviewer holds the batch to:
- **Plain:** names that say what a thing is, small functions that each do one thing, no cleverness a
  reader has to decode. Follow the project's own layout and conventions before your habits.
- **Debuggable:** an error says what failed and with which input; nothing fails silently or is
  swallowed; log at the boundaries, not everywhere.
- **Structured:** a rule lives in one place. Before adding code, look for what already does the job,
  and delete what your change replaces in the same ticket.
- **Explained where it is not obvious:** comments say why, never what. Tests read as examples of how
  the code is meant to be used.
- **Cheap to build:** run the narrowest test that proves your change while you work, and the project's
  full check once before done. Read the project memory and the files you change, not the codebase.
