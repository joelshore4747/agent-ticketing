---
title: The key goals
kind: workflow
weight: 0
---
Every batch and ticket is chosen, approved and judged against these. Each agent asks of any work: which goal does this move, and by how much?

1. **Every change is a ticket in an ordered batch**, and counts only once its evaluation passes.
2. **The project is reviewed before and after each batch, not continuously.** The review writes a consistent project memory (structure, architecture, hotspots, debts, and the commit it describes), so the next review reads that memory and the diff since, rather than the whole codebase again. Better ways of doing things become tickets.
3. **Stale, messy and bloated code gets deleted.** Each batch leaves the codebase no bigger than it needs to be; a cleanup pass is part of every evaluation. The ceiling counts **non-test** lines — net ≤ 0 of them, or the Approver accepts the growth with evidence — because counting a test line the same as a code line charged a builder for the test that proved the change, and made thinning it the cheapest way through (TP-212). Test lines are reported beside the code figure, not against it, and are expected to run to about 1.5× the code they cover, table-driven.
4. **Decisions are stored with their evidence**, so they can be revisited without re-deriving them.
5. **The next batch is chosen by these goals**, from what the last batch found, not by whatever is loudest.
6. **Every batch is explained in plain English**: what was added, why, what it achieved with results, where in the code it lives, and how it moves toward the goals.
7. **This project can be connected to another project**, and the same machinery that keeps improving this one to a better and more consistent standard does the same for that one.
8. **It runs on its own, and the human can always steer.** It keeps going without asking, stops for anything only the human can do, and any checkpoint can change direction. Default budget: a run pauses after 3 batches, or sooner when a batch's cost is flagged.
9. **Progress is measured**: tokens and cost, lines deleted against lines added — code and tests counted apart by path, so the ceiling reads code and neither figure is hidden (TP-212) — and evaluation pass rates over time.

Agents, one job each: Strategist (proposes and scores the next batch), specialists (builders, the reviewer, evaluator), Approver (approves batch plans against the goals, never the planner's own), Narrator (writes each batch's human report, with where in the code).

**Name the goals you serve.** Every planned ticket carries `goals` (their numbers); plan_batch refuses one that names none. A batch digest says which goals were served and which were not touched, and the next batch is chosen from that.
