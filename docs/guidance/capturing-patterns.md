---
title: Capturing a pattern
kind: workflow
weight: 20
source_paths: api/internal/app/webqueries_test.go, api/internal/service/evidence.go, api/internal/service/lessons_test.go
---
Most lessons are local: a fix, a test, done. Some keep coming back. Those belong in the guidance, where
the next agent reads them before the mistake, not in a third fix after it.

**Three times is a convention.** When the same gap shows up on a third ticket — the same kind of
refusal, the same missed step, the same review finding — stop fixing it silently. File a `chore` in
the `docs` area, tagged `consistency`, that names the three tickets and says in one sentence what
should be true everywhere. Two times can be coincidence; three is how the code is being written.

**Count them, do not remember them.** File a lesson like any other finding, tagged `lesson`, with the
fingerprint `lesson:<kebab-pattern>` — the pattern, never your wording, so a repeat counts onto the
ticket already there (`seen_count` up, nothing new filed) and "three times" is a number to read
instead of three reviews' prose. One whose ticket is already done or closed starts a fresh count at
1: the count is of occurrences since somebody last decided the lesson was handled.

**Write the rule where it will be read.** A convention about Go goes in `go-service-layer`, about the
web in `the-web-app`, about finishing in `before-you-finish`. A new document is for a new kind of
work, not a new rule. Each paragraph leads with the rule in bold and follows with the reason; the
reason is what lets someone apply it to a case you did not foresee.

**Prefer a check to a sentence.** If the pattern can be caught mechanically — a test, a lint, a
refusal in the service — that is the real fix, and the guidance points at it. This repository's own
examples: the query guard (`webqueries_test.go`) exists because a schema field vanished unnoticed, and
rule 21 exists because "done" meant whatever the agent said it meant.

**If a document is wrong, say so first.** `report_guidance_drift` while you still have the code and
the document open; a convention that disagrees with the code teaches the wrong thing to everyone who
reads it after you.
