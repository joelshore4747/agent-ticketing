---
title: Engineering principles
kind: convention
weight: 11
source_paths:
---
Joel's bar, in his words: code that is "easy to debug and simple but professional", written the way
the best human engineers write it. This is the short list of ideas behind that, each from people who
have been right about it for decades. They apply to every project the tracker works. The rules a
project's linter enforces are the floor; these are what a reviewer asks after the linter is happy.

**Simple beats clever.** If two versions work, keep the one a tired person understands first time.
Debugging is twice as hard as writing the code (Kernighan), so code written at the limit of your
cleverness cannot be debugged. "Clear is better than clever" (the Go proverbs).

**Deep modules, small surfaces.** A package or module hides a lot behind a few functions (Ousterhout,
*A Philosophy of Software Design*). A caller should need its README example and nothing else. Many
thin layers that each pass the call along are the opposite: delete the layer.

**Decisions in a pure core, I/O at the edges.** Every rule is a plain function from input to output,
tested with a table of cases. The framework, the network, the disk and the clock live in a thin shell
around it (Bernhardt's "functional core, imperative shell"). A bug then reproduces in a unit test.

**Parse at the boundary, then trust the types.** Input from outside (a request, a file, an env var, a
third-party API) is validated once, where it enters, into a typed value (Alexis King, "parse, don't
validate"). Code past that point never re-checks it.

**Errors you can act on.** An error says what failed, with which input, and what to do next, and it is
never swallowed. A message like "invalid config" is a bug; "CONTACT_SMTP_PORT=abc: not a port number" is
not. Every call to something outside has a timeout.

**One place for each rule.** Duplicate knowledge, not duplicate text, is what hurts: two copies of
"what counts as a valid slug" will drift. Three similar lines are fine; three copies of a decision are
not. Do not abstract on first use: wait for the third (the rule of three).

**Boring technology.** The standard library first, then a well-known dependency, and only with a
recorded reason (McKinley, "Choose boring technology"). Every dependency is code you now maintain.

**Names that say what it is.** A reader should not need the body to know what a function returns or a
variable holds. No abbreviations a newcomer has to guess.

**Delete.** Code that is replaced, unused or "for later" goes. Git remembers it. The best change often
removes more lines than it adds.

## Reusable means proven, not just separated
A piece is reusable when another project can take it unchanged:
- its README opens with a working example, which a test runs;
- everything project-specific (brand, domain, paths, the owner's name) is passed in, never written in;
- it imports nothing from the app that uses it;
- it has its own tests, and its contract changes only on purpose.

Each project keeps a **reuse register** (`REUSE.md` at the repository root): one entry per reusable
piece, saying what it does, how to use it, and which projects use it. A piece only one project has used
is "ready", not "proven".

## How a reviewer uses this
For each change, ask the questions in order: Is it correct? Could it be simpler? Can it be debugged
from its error messages and tests alone? Does each rule live in one place? Is anything here dead? Then
update the reuse register for every reusable piece the batch added or changed.
