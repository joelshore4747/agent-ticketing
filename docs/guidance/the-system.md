---
title: The system
kind: workflow
weight: 1
source_paths: scripts/loop.py, scripts/loop.sh, .claude/agents
---
**The problem.** A codebase worked on by AI drifts: work gets done out of order, decisions are forgotten, code piles up, and nobody can say whether it is getting better. This system keeps a project, this one or any other connected to the tracker, improving steadily, measurably, and in a way a human can follow in minutes without reading code.

**The route.** Everything goes round one loop, and every step leaves a record the next step reads:

1. **Choose.** The Strategist picks the next batch by the key goals, from the last digest and the project memory. A pattern the loop has already paid for three times stops being a lesson and becomes work: every strategist step names by key the `lesson` tickets seen `PROMOTE_AT` (3) or more times (`scripts/loop.py`) — open, or still in triage, because an AI-filed lesson waits there for a human and its count is real while it waits (TP-285) — and that plan promotes each one into a ticket for a mechanical check — a test, a lint rule, a script or a gate line — or a guidance rule where no check can catch it.
2. **Approve.** The Approver checks the plan against the goals. No plan approves itself. A plan it sends back goes to the Strategist to fix once; a second send-back stops the loop for the human. A refactor must not change behaviour, so rule 21 will not let one be done without a gate criterion linked to a confirmed tripwire: before accepting, the Approver finds the existing test that pins the behaviour being moved, registers it as a tripwire if needed and attaches it as that criterion — or sends the plan back to add the test first. Confirming the tripwire is Joel's (the Only Joel list): if it is unconfirmed, the Approver files a needs-joel ticket and orders the refactor last, and the loop stops there naming it until he confirms. Neither of the two things this step is asked to notice is left to memory: `scripts/loop.py` reads the plan in front of the Approver and puts what it finds in the step's own reason — the lessons that are due and no ticket in the plan cites (TP-286), and a refactor waiting on Joel that is not last before the eval or has no open needs-joel ticket naming its tripwire by id or test reference (TP-288). Both say to send the plan back.
3. **Look first.** The loop records where the batch starts: the memory plus the size of the diff since it.
4. **Build.** The builders work the tickets in order. Each one is a small change on its own branch, with evidence. A refactor that reaches the front of the queue still unguarded does not go to a builder: the loop names an `approver-guard` step instead (`scripts/loop.py`), the Approver does step 2's guarding then and there, and only after that is a builder sent in.
5. **Look again, once.** Before the eval, the reviewer reviews and trims the batch's whole diff, fixes what it finds, corrects the guidance the batch moved, and writes the after-review, which becomes the memory. Its prompt carries the tool calls that failed more than once while the batch was built (`scripts/lib/step_errors.py`) as lesson candidates (TP-190).
6. **Prove.** Nothing is judged against stale state. Before the evaluator takes a batch's eval ticket, the loop settles both halves of it: it asks the API what it is serving (`GET /api/version`) and restarts a process whose commit or newest migration is behind the checkout, and it runs the guidance check — documents the code has moved under become a `doc-review` step that re-reads each one against its own diff, corrects it and re-pins it first, while a served body that differs from its file stops the run rather than being papered over. The evaluator then runs the gate, the tests that replaced the demo scripts, and the verdicts, and a cleanup pass — which also fails a batch that changed an agent prompt or a guidance document without a decision citing the guidance scores or an A/B run, unless the edit only corrects a document to match the code or a planned ticket asked for it — and names in its resolution the version it measured.
7. **Explain.** The Narrator writes the report and finishes the batch.
8. **Steer.** The Approver signs the checkpoint, and the batch merges into main.
A merge that brought `api/` into main is where the running process stops serving it, so the loop brings the API up to the new main there and then rather than leaving the next batch's eval to trip over it.

Then back to 1. The loop (`just loop`, or `just loop --project KEY` for a connected project) runs it in that project's checkout (its `repo_path`); the tracker holds all the state. The agents carry no project of their own: the loop's prompt names it, and its guidance says how that project is built.

**Connecting a project.** Three steps, proven end to end by `ConnectASecondProject` in `scripts/test_loop_run.py`:
1. `POST /api/projects {key, name, repo_path}`. `repo_path` is the absolute path to its checkout (relative is a 400) and the only thing on the row saying where that project's code is: without it, a run that names the project has nowhere to work.
2. `create_roadmap`, then `plan_batch` one placeholder batch with no tickets. Write that placeholder as a first review of the codebase: the loop's first step is the Strategist planning into it, and it otherwise chooses from a project memory and a digest that do not exist yet.
3. `just loop --project KEY`, which runs the route above in that checkout.

It arrives with the starter tags and a copy of this project's `key-goals`, `planning-a-batch`, `working-a-ticket` and `the-agents`, each as its own revision 1 and with `source_paths` dropped — those name files in this checkout. It does not inherit the documents about how this repository in particular is built (`go-service-layer`, `the-web-app`, this one): the new project's own conventions are its to write. Nor the `lesson` tag, until TP-278 adds it to the seed, nor any areas — so its first finding lands in `triage` routed to none, which is rule 17 working rather than a gap.

**The parts and what each is for.**
- **Tickets** are the only unit of work (goal 1).
- **Batches** are an ordered group of tickets with a goal, a branch and an eval.
- **Roadmaps** are ordered batches with a checkpoint between each.
- **Goals** are why anything is chosen.
- **Reviews** are the project memory (goal 2).
- **Decisions** are why something was chosen, with evidence (goal 4).
- **Verdicts** say whether it worked.
- **Digest and report** say what happened, in plain English (goal 6).
- **Usage** says what it cost (goal 9).
- **Needs you** is what only the human can do — the **Only Joel** list in `the-agents` names those moves once.

**Simple rules every part keeps.**
1. **The tracker is the truth.** If it is not recorded, it did not happen; if it is recorded, anyone can pick it up cold. For guidance that means the served body and not the file it was seeded from: `just guidance-check` fails when the two differ, and `just guidance --replace` refuses to delete served lines unless you add `--force`.
A document whose code has moved under it is re-read before the next eval rather than believed (step 6), because a document nobody has re-read is handed to every agent that claims a ticket.
2. **One job per part.** An agent, a script or a service function does one thing, and says what it hands back.
3. **Rules live in one place.** The service enforces them; transports, scripts and agents never re-implement one.
4. **Show, don't say.** Done means evidence: a command and its output, a demo, a verdict.
5. **Smaller is better.** Every batch asks what it can delete. A second copy of anything is a finding.
6. **Read the memory, not the world.** Start from the last review and the diff; never re-read everything.
7. **Stop rather than guess.** Anything on the **Only Joel** list (`the-agents`) is never attempted; it is tagged `needs-joel` instead. The loop waits on one in the batch it is working once that batch's work reaches it (one placed after the work it judges waits for that work), and carries on past one anywhere else, naming it.

**When you build a new part, weigh this.**
- **Which goal does it move, and how will we know?** If you cannot name the measurement, it is not ready.
- **Where is its record?** A part with no record in the tracker cannot be resumed, judged or explained.
- **Who is allowed to do it?** The human's moves stay on the human's transport, and every write says which role made it — but no agent types that any more: `loop.sh` gives each step its own credential and the actor comes from it, in local mode from `X-Actor` as before (TP-316; `the-agents` holds the mechanism). So a new part asks for a credential rather than a name, and a part that calls the API sends one — the loop's own scripts send the `loop` role's, and a 401 reaching `loop.py` ends the run.
- **What does it cost to run, every time?** Tokens matter more than lines. Prefer the memory, the diff and the cache.
- **What does it replace?** Delete the old way in the same batch.
- **Would it still work on another project?** Keep project-specific facts in guidance and goals, not in the machinery (goal 7).
