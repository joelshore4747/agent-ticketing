---
title: Filing a finding
kind: workflow
weight: 10
source_paths: api/internal/service/tickets_create.go
---
A finding is worth filing when someone who has not read the code could act on it. Everything else is
a comment on an existing ticket.

**Search before you file.** The API dedupes exact repeats by fingerprint, but a fingerprint is the
title, the first file path and the area — a finding described differently slips straight past it.
One search for the words a person would use costs less than a duplicate costs everyone else.

**Write the title as the defect, not the area.** "Version conflict returns 500 instead of 409" can
be triaged by someone who has never opened the file; "Error handling in tickets.go" cannot.

**Include locations.** They route the ticket to an area automatically, and they are the difference
between a finding that can be picked up cold and one that has to be re-investigated from scratch.

**Say what is wrong and what you would do, separately.** The first is a fact and outlives you; the
second is a judgement and may be overruled. Keep them in different sections so a reviewer can accept
one without the other.

**Tag from the managed list.** `list_tags` has it; a finding with no tag is invisible to the tag
filter, and one with an invented tag is refused. Leave intent and success criteria alone: they are the
human's to write when they accept the finding.

**A `needs-joel` finding says what Joel does, or it is refused.** Rule 27: a create carrying that tag
needs a **What you do** section in the description — a markdown heading or bold line beginning "What
you do", then at least two numbered steps starting at 1, before the next heading — or the API answers
400 naming what is missing. Write the steps as moves: where to go (page, file or command), what to
look at and what counts as OK, what to click or run, what happens next, what to do if it is not OK.

**Priority is about consequence, not effort.** Urgent means someone is losing data or the build is
red. A one-line fix to a cosmetic problem is low priority however cheap it is.
