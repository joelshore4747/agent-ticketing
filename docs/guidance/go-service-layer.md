---
title: How the Go API is written
kind: convention
weight: 0
source_paths: api/internal/service, api/internal/rest, api/internal/graph, api/internal/mcpserver, api/main.go
---
**Business rules live only in `internal/service`.** GraphQL, REST and MCP are shells that parse a
request, call one service function and render the result. The reason is not tidiness: the three
transports disagree about everything — argument shapes, error rendering, what a missing field means —
and a rule implemented in one of them is a rule the other two get wrong. If you find yourself writing
a validation in a handler, the function it calls is missing an argument. A shell decodes its own
transport's shape and nothing more: turning raw strings into a typed service input is the service's
job too, in a `Parse*` beside the input it builds (`ParsePlannedTickets`, `ParseTicketFilter`,
`ParsePatchEnums`, `ParseTypeAndPriority`), or the three transports drift into accepting different filters. The drift is not
hypothetical: a patch's type, priority and status were parsed by REST and by MCP in their own
identical blocks until TP-311, two batches after the same duplication was removed for list filters.
The create path then had the same pair in three — REST, MCP and planning — until TP-312 gave all
three doors one `ParseTypeAndPriority`.
A field a shell really does read differently stays in the shell and is named as such — REST reads a
raw-JSON null to clear a ticket's area where MCP reads `clear_area`, so `ParsePatchEnums` takes the
three enums and leaves area alone. REST's writes resolve their project through `writeProject` (the body's `project`, else `?project=`, and a 400 when they disagree), never `service.ResolveProject` on the body alone, which silently wrote FS guidance into TP twice (TP-358). A rendering helper all three
shells need goes in `model` beside `Ptr` and `Deref` — `model.NonNil`, which renders an empty slice
as `[]` rather than `null`, was written out seven times before it was generic: four across mcpserver
and REST until TP-293, then `graph.nonNil` and `service.nonNilGoals`, one copy per element type,
until TP-308, and `graph.goalsOrEmpty` until TP-340, which that fold left alone because nothing read
goals over GraphQL yet: a copy is invisible until something reads the field it renders.

**Who is acting is settled once per request, never read from the caller's body.** One middleware in
`app` does it for all three transports: with
`TICKETING_TOKENS` set a bearer token's sha256 names the actor and says whether it is a human's, and
`X-Actor` is ignored; unset, the API is in local mode and that header is believed again, with the old
per-transport defaults (rule 10, TP-317). REST and GraphQL read back the one `service.Actor` on the
context. MCP cannot: a tool handler's context descends from the session's `initialize` request, not
from the call, so the middleware also writes the actor it settled onto `X-Actor`, and `actorOf` reads
it off that call's own `req.Extra.Header` (TP-318). No tool takes an `actor` argument; one still sent
is deleted by a receiving middleware before the SDK validates it against the argument struct's
`additionalProperties: false`, so an old caller is ignored rather than refused.
The human's moves were the ones only GraphQL could make,
and the two of them on the Only Joel list (`the-agents`) ask for a credential marked `human` as well:
one `service.OnlyJoel` beside that context value, called by `createTag` and `confirmTripwire`, rather
than a check copied into each resolver, and silent in local mode where there is no credential to read
it from (TP-319). A service function whose rule
depends on who is acting takes it from the shell — `TicketPatch.HumanDecision`, `RecordDecision`'s
`madeBy` — and no request body carries a field for it. A `made_by` or `human` field in a REST or MCP
payload would let any agent claim to be the human, which is the whole reason the credential and not
the caller now answers for it.

**The API never touches the repository or the agent.** It does not run git, read files, shell out,
or measure the session working a ticket. Where work
needs git or the shell, the service names the commands — `ClaimSteps`, `FinishSteps`, and the gate in
`DefaultChecks` that planning writes onto every ticket (TP-173) — and a script or the agent runs
them (`scripts/batch-merge.sh`, `scripts/run-checks.sh`); where it needs figures about a session, a
script reports them in (`scripts/loop.sh` per step, `scripts/usage-report.sh` by hand). An API that could change the code it is
tracking would be one more thing the agent could ask to act for it. A project's `repo_path` is the
one place the API holds a path, and it only stores it (absolute, or null) for the loop to read; the API
never resolves or opens it.
`GET /api/version` reports a commit without breaking any of that, because it reports only what the
process was told and what it carries: `GIT_COMMIT` is set by whoever launched it (`scripts/dev.sh`, from
`git rev-parse HEAD`) and reads `unknown` when unset rather than being looked up, and the migration
beside it is the newest one embedded in the binary, which startup applied (TP-256). A caller holding a
checkout compares the two and learns whether the process is serving its code; the API never finds out.

**pgx, no ORM.** `pgx.CollectRows` with `RowToStructByName`, hand-written SQL, and the small generic
helpers already in `service.go` (`collect`, `collectOne`, `inTx`, `builder`). This is a deliberate
trade: the queries here are joins and partial indexes that an ORM makes harder to read, not easier.
When you need a new access pattern, write the SQL.

**Anything that must be true together happens in one transaction.** `inTx` exists for this. The
failure mode it prevents is not a crash, it is a half-written record that looks complete — a batch
whose count was raised for findings that were rolled back, a claim handed out with no record of who
got it. `inTx` on a `pgx.Tx` nests as a savepoint, so service functions compose: `CreateProject`
inserts the project, its starter tags and its copied core guidance (through `UpsertGuidance`) as one
unit.

**Errors carry their status.** Return `apperr.BadRequestf` / `NotFoundf` / `Conflictf` from the
service, and let the transport map it. A bare `fmt.Errorf` becomes a 500 and tells the caller
nothing they can act on.

**No new dependencies without a clear need.** The module list is short on purpose; every addition is
something the next person has to understand before they can read a file.

**Comments explain why, not what.** The good ones name the thing that went wrong and the date. A
comment restating the line below it is worse than nothing, because it has to be maintained.

**The contracts are `docs/schema.sql` (the data model) and `docs/api.md` (GraphQL, REST, MCP and the
service rules).** Follow them exactly and never edit one to fit an implementation; if a contract is
impossible, build the closest thing and report the deviation. Migrations live in `api/migrations/` and
run on startup, and every schema change is mirrored into `docs/schema.sql`. Migrations are history and are never
edited: data a migration seeded that new rows also need gets a forward copy in the service
(`starterTags`), with a test that fails when the two copies disagree. The GraphQL schema is
generated from the SDL in `docs/api.md` by `just generate`, so `internal/graph/generated.go` and
`internal/graph/gqlmodel/` are never hand-edited. The integration tests to write are the ones
`docs/api.md` lists, and `internal/testdb` gives each test its own migrated database. The rule above
is about the API, not about its tests: where a repo script is the only writer of something the service
refuses to accept without — `scripts/run-checks.sh` and `gate_passed` — the test pins both halves by
running the script itself against the test database, which is what the deleted demo scripts used to
show against a live API (TP-269). A test that shells out that way names the script and skips when its
tools are absent, rather than failing on a machine that has no `psql`. It also bounds what the script
may judge: `run-checks.sh` fails a gate for every stale guidance document the branch moved, so the
case passes `GATE_BASE=HEAD` and asserts its own checks, not the state of `docs/guidance/`. Without
it one ticket's drift turned every later ticket's `just test` red (TP-339).

**The configuration is five environment variables**, four of them wiring, read in `api/main.go`: `DATABASE_URL`, `PORT`,
`CORS_ORIGIN`, `TICKETING_TOKENS`. No tracing stack, no config framework, and no auth beyond that one
file of hashed bearer tokens — no users table, no sessions, no OAuth. Each of those would be another thing to
run, understand and keep working, for a tracker one person runs on one machine.
`TICKETING_TOKENS` is read in `main.go` like the rest and handed to `app.New`, which loads the file there
so an unreadable one is fatal before the listener opens, exactly as a bad `CORS_ORIGIN` is (TP-317).
The fifth, `GIT_COMMIT`, is not wiring but a value one function reports, so it is read where it is reported — `service.CurrentVersion`,
for `GET /api/version` (TP-256) — rather than threaded through `app.New` and its four call sites for one
string nothing else ever looks at. That is the one exception and stays one: anything the app is built
*with* is read in `main.go` and passed in, because a second place reaching for the environment to build
something is a second place to look when a process behaves unlike its launcher.

**Unreachable Go is found, not guessed**: `just deadcode`, with `just deadcode -test` to see what only
tests reach. This is the dead-code command the scouts and the evaluator are told to run for this part
of the project. Run from `api/` the tool dies on a missing `go.sum` entry for `golang.org/x/telemetry`,
a dependency of the tool and not of this project, so the recipe runs it in a throwaway copy of `api/`
and never touches the tracked module graph for a dev-only tool (TP-169). The six `internal/testdb`
helpers it always lists are reached only by tests, and `-test` shows them going quiet.
