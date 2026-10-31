# API contract

One Go binary (`api/`) serves everything on `PORT` (default 8080):

| Path        | What                                   | Used by            |
|-------------|----------------------------------------|--------------------|
| `/graphql`  | GraphQL (POST) + GraphiQL playground (GET) | web app        |
| `/api/*`    | small REST API                         | Claude Code, curl  |
| `/mcp`      | MCP server, streamable HTTP            | Claude Code        |
| `/healthz`  | `200 ok`                               | scripts            |
| `/api/version` | `{ "commit", "migration" }` — what this process is serving | the loop, scripts |

All three entry points call the same service layer (`api/internal/service/`). Business rules live only there.

Enum values are lowercase snake_case strings everywhere (DB, GraphQL, REST, MCP):
`type` = bug | feature | improvement | chore; `priority` = low | medium | high | urgent;
`status` = triage | open | in_progress | done | closed; `source` = ai | human.

One project (`TP`) is seeded. Every entry point defaults to the oldest project; `project` names another by key. Over REST a write takes it from the body or from `?project=`, and refuses a request whose two disagree (TP-358).

## Service rules

1. **Keys.** `key = <project.key>-<number>`. Numbers are allocated with
   `UPDATE projects SET next_ticket_number = next_ticket_number + 1 ... RETURNING` inside the same transaction as the insert.
2. **Initial status.** `source = human` → `open`. `source = ai` → `triage`, unless the resolved area has `auto_accept_ai = true` → `open`.
3. **Fingerprint dedupe.** If a create carries a `fingerprint` (AI findings always do; the service computes
   `sha256(lowercase(trim(title)) + "|" + first location file_path or area name)` when the caller gives none) and a
   non-closed ticket with the same `(project_id, fingerprint)` exists: do not insert. Return the existing ticket,
   increment `seen_count`, set `last_seen_at = now()`, add a system comment `Seen again by <actor> (<n> times)` and a
   `ticket_events` row with field `seen_again`. Response carries `created: false`.
4. **Status transitions** (enforced in the service, 409 otherwise):
   - `triage → open` (accept), `triage → closed` (dismiss)
   - `open → in_progress`, `open → closed`
   - `in_progress → open`, `in_progress → done`, `in_progress → closed`
   - `done → open` (reopen), `closed → open` (reopen)
   - `done` and `closed` require a non-empty `resolution`. Entering `done` sets `completed_at = now()`; entering `open` from done/closed clears `completed_at` and `resolution`.
   - Any transition clears `claimed_by`/`claimed_at` except `open → in_progress`, which may set them.
   - A refusal names the legal moves from the current status, so the caller never has to guess the table.
5. **Optimistic locking.** Every update/transition takes `version`. `UPDATE ... WHERE id = $1 AND version = $2` returning the row;
   zero rows → 409 `version conflict`. Each successful update bumps `version` and `updated_at`.
6. **Events.** Every changed field writes one `ticket_events` row `(actor, field, old_value, new_value)`. Creation writes field `created`.
7. **Auto-routing.** When a finding has locations and no explicit area, match each `file_path` against every non-archived
   area's `path_globs` (globset). First matching area wins, ordered by the longest glob (most specific). No match → area NULL.
8. **claim_next_ticket.** In one transaction:
   `SELECT ... FROM tickets WHERE project_id = $1 AND status = 'open' AND (claimed_at IS NULL OR claimed_at < now() - interval '30 minutes') [AND area filter] [AND batch filter] AND <in order, rule 15> ORDER BY priority rank (urgent > high > medium > low), created_at ASC LIMIT 1 FOR UPDATE SKIP LOCKED`,
   then transition `open → in_progress` with `claimed_by = actor`, `claimed_at = now()`, events + system comment. Returns null if nothing is claimable.
   The same transaction records one `ticket_claims` row — an unattributable claim fails the claim.
9. **Search.** `search` param → `search @@ websearch_to_tsquery('english', $q)`, ordered by `ts_rank` desc. Also match `key ILIKE`. Empty search = no filter.
10. **Actors.** `TICKETING_TOKENS` names a file of `<sha256-hex> <actor> [human]` lines (`#` comments and blank lines skipped),
    read once at startup: a named file that cannot be read, or that holds no line, is fatal at boot rather than silently open.
    When it is set, every request except `OPTIONS`, `GET /api/version` and `GET /graphql` (the playground) needs
    `Authorization: Bearer <token>` whose sha256 is in that file, or gets 401 `{"error": "..."}` saying how to authenticate;
    the actor and whether it is human come from the credential, and `X-Actor` is ignored. One middleware does this for REST,
    GraphQL and MCP alike, inside CORS so a browser can read the 401. It also overwrites `X-Actor` on the request with the
    actor it settled, because an MCP tool call can read only the header: its context descends from the session's `initialize`
    request, not from the call. With it set, the two Only Joel moves that have an API door — `confirmTripwire` and `createTag` —
    need a credential marked `human`; any other gets 403 / `FORBIDDEN` naming that list in the `the-agents` guidance.
    When it is unset the API is in local mode and logs
    `auth off (TICKETING_TOKENS unset)` once: the actor is the free string in `X-Actor` (default `claude-code` for REST and
    MCP, `human` for GraphQL). No MCP tool takes an `actor` argument; one still sent is ignored, not refused.
    `scripts/issue-token.sh <actor> [--human] [--save]` issues one, printing the token once and recording only its hash.
11. **Lists** are ordered by priority rank desc, then `created_at` desc, default `limit` 50, max 200.
12. **Areas.** Rename = update name; archive = set `archived_at`. Archived areas are hidden by default and cannot be assigned to new tickets. Renaming a name to one that exists → 409.
13. **Guidance resolution.** A ticket in a batch resolves that batch's attached documents, at the
   revision the attachment pins or the document's current one. A ticket in **no** batch resolves the
   project's standing corpus: every non-archived document, since the corpus describes how the
   project is written and that applies to work however it arrived. A batch with nothing attached
   resolves nothing — that, not "no batch", is what a control arm is. Bundle order is
   `(kind rank, weight, batch position, slug)`, so re-claiming a ticket serves byte-identical
   guidance.
14. **Unknown fields are refused.** A request body naming a field the endpoint does not define →
   400 naming the field. The consumer here is a model that guesses field names, and a silent
   no-op returns a success it cannot question: `area_id` where the API documents `area` created an
   unrouted ticket and reported 201.
15. **Order.** A ticket cannot start — by claim or by a move to `in_progress` — while either holds:
   - an earlier `batch_position` in its batch is not `done` or `closed` (a `triage` ticket blocks
     the ones after it; closing a ticket is a decision and unblocks them);
   - an earlier `sequence` in its roadmap is unfinished, has no checkpoint, or was checkpointed
     `stop`.
   Findings batches have no positions, so only the roadmap half can apply to them. A refused move
   names the ticket or batch in the way (409).
16. **Finishing a plan batch.** `finish_batch` on a `plan` batch is refused (409, listing what is
   outstanding) until every ticket is `done` or `closed` and it has an `eval` ticket that is `done`
   — closing the eval ticket skips the gate, so it does not count. Findings batches finish whenever
   their author says, because their tickets are worked later.
   **Retiring one** is the only way out for a plan nobody ever worked, which otherwise stays
   unfinished for good — a stale API created one (TP-175) that no eval could ever pass. GraphQL's
   `checkpointBatch` with `stop` on an unfinished `plan` batch none of whose tickets has started sets
   `finished_at` along with the decision, so the batch reads `stopped` rather than in flight, and no
   digest is computed for work that never happened. Refused (409) when a ticket has started
   (`in_progress`, `done` or `closed`), naming it, because that work's account is its eval; when the
   decision is anything but `stop`; or when the batch is not a `plan`. A batch that has finished takes
   the ordinary checkpoint, unchanged. Inside a roadmap `stop` still means what rule 15 says it means,
   so retiring a slot locks the ones after it.
17. **Triage is left by a human.** Moving a ticket out of `triage` (accept → `open`, dismiss →
   `closed`) is refused on REST and MCP (409, naming this rule); only GraphQL's `acceptTicket`,
   `dismissTicket`, `transitionTicket` and `acceptBatch` may do it. Without auth this is enforcement by
   transport omission — the strongest available — and it closes the leak R3 (TP-48) found, where an
   agent could accept its own finding through `update_ticket`.
18. **The ownership model.** Section A — `intent`, `constraints`, `non_goals`, success criteria — is
   written only by GraphQL `setTicketFoundation`, as one change under the ticket's version; REST and
   MCP have no field for it and refuse one by name (rule 14). The AI's route is a proposal
   (`propose_ticket_edit`, `POST .../proposals`): a `proposal` comment and a `<field>_proposed` event,
   never a write. Section B is written by the AI and approved by `approveApproach`, which a feature
   refuses without a worked example; editing `approach`, `risk_and_rollback`, `worked_example` or
   `verification_plan` after approval clears it. `gate_passed` is set only by the out-of-band check
   runner. Only `public` checks are served; held-out ones never leave the database.
19. **Decisions are records.** What was decided about a ticket — `accepted`, `deferred`, `rejected`,
   `reframed` — is a row in `decisions`, never an edit: a changed mind is a new row that `supersedes`
   the old one (by default the ticket's current decision, and only one on the same ticket), and
   `tickets.decision_id` points at whichever is current. `made_by` is set by the transport — GraphQL
   files `human`, REST and MCP file `ai` — and no transport takes it as an argument. The human's
   triage exits file theirs in the same transaction as the move: accept → `accepted`, dismiss →
   `rejected` with the resolution as rationale, `acceptBatch` one per ticket. A deferral needs a
   `revisit_trigger`; a reframing needs a rationale. Recording one does not bump the ticket's
   `version`, so it cannot 409 the agent's next update. An AI decision still in force that is not a
   deferral and cites no `evidence_refs` is **uncited** (`GET /api/decisions/uncited`,
   `uncitedDecisions`) and is flagged to the human.
20. **The digest is computed; the queue is the human's.** Finishing a batch writes `batches.digest`
   (markdown) in the same transaction, from the record alone: what/how/why, each ticket's status and
   resolution, the decisions in force (routine triage accepts collapsed to one line), deferrals with
   their revisit triggers, and open questions — pending proposals, uncited AI decisions, unconfirmed
   tripwires the batch's criteria rely on. Its **Measures** section (goal 9) gives the size of the
   change — code added, deleted and net, with the test figure beside it and the branch and base commit
   they were counted on, because the ceiling counts code only and a test line is reported rather than
   charged (TP-212); a batch measured before that split says so — and the pass rate: the newest verdict per criterion, the gate on the finished tickets,
   and the same criteria figure for up to the three finished batches before it in the roadmap. Size is
   reported in by `POST /api/batches/:id/size` (`scripts/batch-size.sh <id>`), never measured here
   (rule 22); a batch nobody measured reads "size unknown" and names that command rather than failing
   the finish. No demo run is recorded in the schema, so the gate is the second pass count.
   No transport writes the digest; `summary` stays the author's narrative.
   Finishing again recomputes it and keeps `finished_at`. `needsYou` / `GET /api/needs-you` is
   everything in a project waiting on a human decision: triage tickets, roadmap batches awaiting a
   checkpoint, pending proposals, uncited AI decisions and unconfirmed tripwires. MCP has no tool for
   it; every item on it is acted on over GraphQL.
21. **Done is shown, not said.** On REST and MCP, moving to `done` needs the evidence the ticket's
   type calls for, read as it will be after the patch: `verified_by` for bug, feature, improvement,
   chore, refactor and eval; also a `worked_example` for a feature and a gate criterion linked to a
   confirmed tripwire for a refactor; `evidence_refs` for research; an approved approach for design
   (an edit in the same patch clears it, so it does not count). Every ticket a plan takes on —
   `plan_batch` and `add_to_batch`, but not the eval ticket, whose gate is its description — is given
   one `public` command check, `just gate` expecting exit 0, so the runner has something to run and
   `gate_passed` is recorded rather than left null (TP-173); a ticket that already carries checks keeps
   them untouched. A ticket with held-out checks also
   needs `gate_passed = true`, which only `scripts/run-checks.sh` writes, by psql. The 400 names
   everything missing. Closing as `closed` needs only a resolution, and the human's own GraphQL move
   to done is their judgement. Once finished, each criterion gets **verdicts** (`record_verdict`,
   `POST .../verdicts`): pass, fail or deviation, the last two with a reason; the newest counts. A
   fail files a follow-up ticket (deduped per criterion) that joins an unfinished plan just before its
   eval, or stays unbatched if the eval has started. The digest counts verdicts and lists each miss.
22. **A plan batch has a branch; the API never runs git.** `plan_batch` sets `git_ref` to an
   integration branch (`batch/<roadmap place or id prefix>-<title slug>`) unless one is given. The
   claim's `next_steps` say to branch `ticket/<key>-<slug>` from it and merge back with `--no-ff`, and
   end with a finish step naming the evidence rule 21 asks of that type; a ticket outside a branched
   batch branches from main. `finish_batch` returns `next_steps` naming `scripts/batch-merge.sh <id>`,
   which merges the batch into main only once the batch has finished and every done ticket's branch is
   in it, and the `gh pr create` form, which waits on a GitHub remote (TP-23).
23. **Usage is reported in, never measured by the API.** `POST /api/tickets/:key/usage` puts a
   session's tokens (input, output, cache written, cache read), wall time and optional dollar figure
   on that session's claim, or the newest; a retry replaces rather than adds. `scripts/loop.sh` posts each
   agent step's figures as it ends; `scripts/usage-report.sh --key` posts a hand-worked session's from its
   transcript, bounded to one step of a shared one with `--since`/`--until`. The digest's cost section
   names any claim that reported nothing, by key, and leads with the summed tokens,
   the cache-hit rate `cache_read / (cache_read + input)` and wall time, labels any dollars an estimate,
   and flags a hit rate more than 10 points below the last five finished batches with usage, naming
   the guidance revised while the batch ran. Queries slower than `SLOW_QUERY_MS` (default 200) are
   logged as structured lines, not stored.
24. **Work is chosen by the key goals, and a batch is reviewed before and after.** Every ticket carries
   `goals`, the key goals (1-9) it serves; `plan_batch` refuses a planned ticket that names none, and its
   eval ticket serves goal 1. The goals' text is the `key-goals` guidance document and the Self-improving
   tracker roadmap's spec. The digest says which goals a batch served and which it left untouched. A
   batch planned since TP-108 (`reviews_required`) is not claimable until a before-review is recorded
   (`record_review`, `POST /api/batches/:id/reviews`) and cannot finish without an after-review, which
   needs the before-review first. The latest after-review is the project memory (`get_project_memory`):
   the next review reads it and `git diff <commit>..HEAD`, not the whole codebase. Triage tickets in a
   roadmap batch still locked behind earlier ones are left off Needs you.
25. **A reviewed batch finishes with its report.** A batch with `reviews_required` needs a `summary`
   (sent with the finish, or already stored) with five markdown headings, any level or case: What was
   added, Why, What it achieved, Where in the code, How it moves the goals. The refusal names the missing
   ones. The Narrator agent writes it; the digest stays computed. The agents are defined in
   `.claude/agents/` and described in the `the-agents` guidance.
26. **A tripwire's `body_sha256` is computed by one script, never by hand.** `RegisterTripwire` clears
   `confirmed_by` whenever the hash it is sent differs from the row's, and the API cannot check the
   hash it is handed — it never touches the repository (rule 22's reason). So the definition lives in
   `scripts/tripwire_hash.py`, behind `scripts/tripwire-hash.sh <test_ref>`, and every caller runs it.
   What is hashed, by the shape of `test_ref`: `path:TestName` on a Go file is the lines from
   `func TestName(` through the first following line that is exactly `}`, inclusive, each ending in one
   `\n`; `path` alone, or `path:<basename without extension>` (a whole script that is one check, like
   `web/scripts/render-check.mjs:render-check`), is the file's bytes as they are on disk;
   `path > describe > it` is the `it` line through the line that closes it at the same indentation.
   A `test_ref` naming none of those is an error, never a hash of something else: the fallback of
   hashing the whole file when the function is not found would let a misspelt test name pin a human's
   confirmation to a file they never read. `scripts/test_tripwire_hash.py` pins all of it against
   fixtures in `scripts/testdata/`, so changing what is sliced is a failing test, not a silent
   re-registration.
27. **A `needs-joel` ticket says what Joel does.** The tag is the only thing in the system that
   interrupts a human, so a create (`POST /api/tickets`, `report_findings`) or an update that would
   leave a ticket carrying `needs-joel` is refused (400) unless its `description` has a **What you
   do** section: a markdown heading or bold line beginning "What you do" — trailing words like
   "(about 3 minutes)" are fine — followed by at least two numbered steps, starting at 1, before the
   next markdown heading. A step may be wrapped in bold (`**1. …**`), and a bold line that is not a
   step does not end the section: TP-242 puts one between two steps. The 400 names the one thing missing
   (no section, no numbered steps, only one, or not starting at 1) and what the steps should cover:
   where to go, what counts as OK, what to click or run, what happens next, and what to do if it is
   not OK. An update is judged on the tags and description it would leave behind, and only when it
   touches one of them: a ticket that already breaks the rule stays closable, and dropping the tag
   stays a way out. What the steps *say* is not checked — only that they are there (rule 25's
   approach). The moves that earn the tag are the "Only Joel" section of the `the-agents` guidance.

## GraphQL schema (SDL, the web app builds against exactly this)

```graphql
scalar DateTime   # RFC 3339

enum TicketType { bug feature improvement chore research design refactor eval }
enum Priority { low medium high urgent }
enum Status { triage open in_progress done closed }
enum Source { ai human }

type Project { id: ID!  key: String!  name: String!  repoPath: String }   # repoPath: absolute checkout path, null until known

type Area {
  id: ID!  name: String!  description: String!  pathGlobs: [String!]!
  autoAcceptAi: Boolean!  archivedAt: DateTime  createdAt: DateTime!  updatedAt: DateTime!
  openCount: Int!          # tickets in triage/open/in_progress
}

type Ticket {
  id: ID!  key: String!  number: Int!
  title: String!  description: String!
  type: TicketType!  priority: Priority!  status: Status!  source: Source!
  nextStatuses: [Status!]!                # where this ticket may move from its status (rule 4)
  area: Area
  branch: String  prUrl: String            # prUrl = url of the most recent pull request
  resolution: String  fingerprint: String  seenCount: Int!
  claimedBy: String  claimedAt: DateTime
  batchPosition: Int                      # order in a plan batch; null outside one
  tags: [String!]!                        # names from the managed tag list, sorted
  goals: [Int!]!                          # the key goals (1-9) it serves, sorted (TP-107)
  # Section A, the human's: set with setTicketFoundation only.
  intent: String!  constraints: String!  nonGoals: String!
  criteria: [Criterion!]!
  # Section B, the AI's proposal; approveApproach records the human's approval.
  approach: String!  riskAndRollback: String!  workedExample: String!  verificationPlan: String!
  evidenceRefs: [String!]!  estimatedTokens: Int  estimatedCostUsd: Float
  approachApprovedBy: String  approachApprovedAt: DateTime
  # Section C, what the AI reports; gatePassed is set only by the out-of-band check runner.
  verifiedBy: String  measuredDelta: String  gatePassed: Boolean  gateCheckedAt: DateTime
  summaryForHuman: String
  checks: [TicketCheck!]!                 # public checks only; held-out ones are never served
  decisions: [Decision!]!                 # newest first (rule 19)
  decisionId: ID                          # the current one
  version: Int!
  createdAt: DateTime!  updatedAt: DateTime!  completedAt: DateTime
  comments: [Comment!]!
  events: [TicketEvent!]!
  locations: [TicketLocation!]!
  pullRequests: [PullRequest!]!
}

type Comment { id: ID!  author: String!  body: String!  kind: String!  createdAt: DateTime! }
type TicketEvent { id: ID!  actor: String!  field: String!  oldValue: String  newValue: String  createdAt: DateTime! }
type TicketLocation { id: ID!  filePath: String!  lineStart: Int  lineEnd: Int  snippet: String }
type PullRequest { id: ID!  url: String!  number: Int  branch: String  state: String!  createdAt: DateTime! }

type TicketPage { items: [Ticket!]!  total: Int! }

enum BatchKind { audit review prompt plan }
enum GuidanceKind { workflow convention area checklist }
enum CheckpointDecision { continue adjust stop }
enum CriterionTier { behavioural measurement gate }

type Criterion {
  id: ID!  tier: CriterionTier!  ordinal: Int!  text: String!  tripwire: Tripwire
  verdicts: [CriterionVerdict!]!          # newest first; the first is the one that counts (rule 21)
}
enum Verdict { pass fail deviation }
type CriterionVerdict { id: ID!  verdict: Verdict!  reason: String!  evidence: [String!]!  judgedBy: String!  createdAt: DateTime! }
type TicketCheck { id: ID!  ordinal: Int!  description: String!  kind: String!  command: String  expectExit: Int! }
# A test whose meaning a human has confirmed; a changed test body clears the confirmation.
type Tripwire { id: ID!  testRef: String!  meaning: String!  confirmedBy: String  confirmedAt: DateTime }
enum DecisionKind { accepted deferred rejected reframed }
# Never edited: a changed mind is a new decision that supersedes it. madeBy (human | ai) is set by the
# transport it arrived on — GraphQL is the human's, REST and MCP the agent's.
type Decision {
  id: ID!  kind: DecisionKind!  madeBy: String!  evidenceRefs: [String!]!  rationale: String!
  revisitTrigger: String  supersedes: ID  citesRevisionId: ID  actor: String!  createdAt: DateTime!
}
# The agent's current decision on a ticket that cites no evidence (deferrals are exempt).
type UncitedDecision { decision: Decision!  ticketKey: String!  ticketTitle: String! }
# Claude's change to a human-owned field that nobody has acted on: the field has not changed since.
type PendingProposal { ticketKey: String!  ticketTitle: String!  field: String!  proposed: String!  actor: String!  createdAt: DateTime! }
# Everything in a project waiting on a human decision; each item names where it is made.
type NeedsYou {
  triage: [Ticket!]!  checkpoints: [Batch!]!  proposals: [PendingProposal!]!
  uncited: [UncitedDecision!]!  tripwires: [Tripwire!]!  total: Int!
}
enum RoadmapState { locked unplanned open at_eval awaiting_checkpoint finished stopped }

# A batch is provenance and intent: which tickets were produced together, and why. Its guidance is
# what an agent picking up one of those tickets is handed along with the work.
# The human read of a batch is what / how / brief (why). A plan batch's tickets are ordered and end
# in an eval ticket; its checkpoint is the human's decision once it has finished.
type Batch {
  id: ID!  kind: BatchKind!  actor: String!
  title: String!  what: String!  how: String!  brief: String!
  gitRef: String  summary: String  findingsCount: Int!
  startedAt: DateTime!  finishedAt: DateTime
  roadmapId: ID  sequence: Int
  checkpointDecision: CheckpointDecision  checkpointNote: String  checkpointBy: String  checkpointAt: DateTime
  digest: String  digestAt: DateTime       # markdown computed by finishing the batch; nobody writes it
  reviewsRequired: Boolean!
  reviews: [ProjectReview!]!               # the before and after project reviews (goal 2)
  guidance: [GuidanceDoc!]!
  tickets: [Ticket!]!                      # in batch order
  progress: BatchProgress                  # plan batches only
}

# The project memory at one commit, taken before or after a batch (goal 2).
type ProjectReview { id: ID!  phase: String!  commit: String!  body: String!  actor: String!  createdAt: DateTime! }

type BatchProgress {
  next: String                             # key of the ticket to work next; null when locked or done
  lockedBy: String                         # why an earlier roadmap batch holds this one
  blockedBy: [BlockedTicket!]!
}
type BlockedTicket { key: String!  behind: String! }

type Roadmap {
  id: ID!  title: String!  spec: String!  createdAt: DateTime!  archivedAt: DateTime
  batches: [RoadmapBatch!]!                # in sequence
}
type RoadmapBatch { batch: Batch!  state: RoadmapState!  work: Int!  workDone: Int! }

type Tag { id: ID!  name: String!  description: String!  archivedAt: DateTime  createdAt: DateTime! }

# The document is mutable; its revisions are not. `body` is the current revision's text.
type GuidanceDoc {
  id: ID!  slug: String!  title: String!  kind: GuidanceKind!
  body: String!  revision: Int!
  inline: Boolean!  weight: Int!
  sourcePaths: [String!]!  sourceCommit: String
  reviewedAt: DateTime  reviewedBy: String
  stale: Boolean!  staleReason: String  driftReportedAt: DateTime
  updatedAt: DateTime!
}

input TicketFilter {
  source: Source  status: [Status!]  type: TicketType  areaId: ID  priority: Priority
  createdAfter: DateTime  updatedAfter: DateTime   # inclusive lower bounds
  tag: String
}

# `project` is a project KEY everywhere it appears. Omitted, it resolves to the oldest project,
# so a single-project install never has to name one. Mutations addressing a ticket by id take no
# project: the id already names one.
type Query {
  project(key: String): Project!
  projects: [Project!]!                            # oldest first; the first is the default
  tickets(project: String, filter: TicketFilter, search: String, limit: Int = 50, offset: Int = 0): TicketPage!
  ticket(project: String, key: String!): Ticket
  areas(project: String, includeArchived: Boolean = false): [Area!]!
  batches(project: String): [Batch!]!
  batch(project: String, id: ID!): Batch
  guidance(project: String): [GuidanceDoc!]!
  roadmaps(project: String): [Roadmap!]!
  roadmap(project: String, id: ID!): Roadmap
  tags(project: String, includeArchived: Boolean = false): [Tag!]!
  tripwires(project: String): [Tripwire!]!
  uncitedDecisions(project: String): [UncitedDecision!]!
  needsYou(project: String): NeedsYou!
}

input CreateTicketInput {
  title: String!  description: String = ""
  type: TicketType!  priority: Priority = medium
  source: Source = human
  areaId: ID  branch: String
  tags: [String!]                          # names from the managed tag list
  goals: [Int!]                            # key goals 1-9
}

# Every field optional; only provided fields change.
input UpdateTicketInput {
  title: String  description: String  type: TicketType  priority: Priority
  areaId: ID  clearArea: Boolean  branch: String  prUrl: String
  tags: [String!]                          # replaces the set; [] clears it, omitted leaves it
  goals: [Int!]                            # replaces the key goals the ticket serves
  approach: String  riskAndRollback: String  workedExample: String  verificationPlan: String
  evidenceRefs: [String!]  estimatedTokens: Int  estimatedCostUsd: Float
  verifiedBy: String  measuredDelta: String  summaryForHuman: String
}

input CriterionInput { tier: CriterionTier!  text: String!  tripwireId: ID }
# Section A. Omitted fields are left alone; criteria, when given, replaces the whole list.
input FoundationInput { intent: String  constraints: String  nonGoals: String  criteria: [CriterionInput!] }

# A deferral needs revisitTrigger; a reframing needs a rationale. supersedes defaults to the current one.
input DecisionInput {
  kind: DecisionKind!  rationale: String = ""  evidenceRefs: [String!]  revisitTrigger: String
  supersedes: ID  citesRevisionId: ID
}

input AreaInput { name: String!  description: String = ""  pathGlobs: [String!] = []  autoAcceptAi: Boolean = false }
input AreaPatch { name: String  description: String  pathGlobs: [String!]  autoAcceptAi: Boolean }

type Mutation {
  createProject(key: String!, name: String!, repoPath: String): Project!   # key ^[A-Z][A-Z0-9]{1,9}$, upper-cased; repoPath absolute
  createTicket(project: String, input: CreateTicketInput!): Ticket!
  updateTicket(id: ID!, version: Int!, patch: UpdateTicketInput!): Ticket!
  transitionTicket(id: ID!, version: Int!, status: Status!, resolution: String): Ticket!
  acceptTicket(id: ID!, version: Int!): Ticket!                      # triage → open
  dismissTicket(id: ID!, version: Int!, resolution: String = "Dismissed from inbox"): Ticket!   # triage → closed
  addComment(ticketId: ID!, body: String!): Comment!
  createArea(project: String, input: AreaInput!): Area!
  updateArea(project: String, id: ID!, patch: AreaPatch!): Area!
  archiveArea(project: String, id: ID!): Area!
  unarchiveArea(project: String, id: ID!): Area!
  # The human's moves on a plan. Deliberately absent from MCP and REST, like acceptTicket.
  acceptBatch(project: String, id: ID!): Batch!                       # every triage ticket in it → open
  checkpointBatch(project: String, id: ID!, decision: CheckpointDecision!, note: String = ""): Batch!
  createTag(project: String, name: String!, description: String = ""): Tag!
  archiveTag(project: String, id: ID!): Tag!
  unarchiveTag(project: String, id: ID!): Tag!
  # The human's side of the ownership model.
  setTicketFoundation(id: ID!, version: Int!, input: FoundationInput!): Ticket!
  approveApproach(id: ID!, version: Int!): Ticket!
  confirmTripwire(project: String, id: ID!, meaning: String): Tripwire!
  recordDecision(ticketId: ID!, input: DecisionInput!): Decision!    # made_by human
}
```

## REST

JSON in, JSON out. Errors: `{ "error": "message" }` with 400 / 404 / 409. Ticket JSON is the flat DB row plus
`key`, `area` (`{id,name}` or null), `pr_url` and `tags` (names, sorted). Timestamps RFC 3339.

```
GET    /api/projects       → [ project, ... ] (oldest first; the first is the default)
POST   /api/projects       { "key", "name", "repo_path"? }   → 201 + project
         A new project starts with the starter tags and a copy of the default project's key-goals,
         planning-a-batch, working-a-ticket and the-agents guidance, as each copy's revision 1: both
         are managed lists the API enforces against, so without them plan_batch refuses every tag a
         ticket names and every ticket for naming no goal (rule 24). Language-specific documents are
         not copied, and a copy carries no source_paths — they name files in another repository.
         repo_path is where the project's code is checked out: absolute, or omitted/null when not known.
         Relative is a 400 — the API's working directory is the server's, not the caller's.
         Connecting a project is one written procedure: see that section of `the-system` guidance.
GET    /api/tickets?project=&source=&status=&type=&area=&priority=&tag=&created_after=&updated_after=&search=&limit=&offset=
         status may be repeated or comma-separated; area is the area *name*; project is the project *key*
         and defaults to the oldest; created_after/updated_after are RFC 3339 instants (inclusive).
         → { "items": [...], "total": n }
GET    /api/tickets/:key                     (key like TP-42; a bare uuid also works)
         → the ticket plus "criteria" (the human's success criteria), "checks" (public only) and "decisions" (newest first).
         A key names its project — TP-42 is TP's — so the by-key endpoints take no project. A uuid
         falls back to the default project.
POST   /api/tickets
         { "title", "description"?, "type", "priority"?, "source"?="human", "area"? (name), "branch"?,
           "fingerprint"?, "locations"?: [{ "file_path", "line_start"?, "line_end"?, "snippet"? }], "batch_id"?,
           "tags"?: [name] }
         → 201 + ticket when created, 200 + existing ticket when deduped by fingerprint (body includes "created": bool)
PATCH  /api/tickets/:key
         { "version": n (required), "title"?, "description"?, "type"?, "priority"?, "area"? (name or null), "branch"?,
           "pr_url"?, "status"?, "resolution"?, "tags"?: [name],
           Section B: "approach"?, "risk_and_rollback"?, "worked_example"?, "verification_plan"?,
           "evidence_refs"?: [ref], "estimated_tokens"?, "estimated_cost_usd"?,
           Section C: "verified_by"?, "measured_delta"?, "summary_for_human"? (≤280) }
         tags replaces the set ([] clears it) with names from GET /api/tags; an unknown name is a 400.
         Section A (intent, constraints, non_goals, criteria) has no field here: it is the human's,
         set with GraphQL setTicketFoundation, and naming it is a 400 (rule 14). Editing an approved
         approach clears the approval.
POST   /api/tickets/:key/proposals   { "field": intent|constraints|non_goals|criteria, "proposed", "rationale" }
POST   /api/tickets/:key/decisions   { "kind": accepted|deferred|rejected|reframed, "rationale"?, "evidence_refs"?,
                                       "revisit_trigger"? (deferred), "supersedes"?, "cites_revision_id"? }
         → 201 + decision, made_by "ai" (there is no made_by field; the human's decisions are GraphQL's)
GET    /api/decisions/uncited?project=  → the agent's current decisions that cite nothing, with key and title
POST   /api/tickets/:key/verdicts   { "ordinal", "verdict": pass|fail|deviation, "reason"? (fail/deviation), "evidence"? }
         → 201 + { "verdict", "follow_up"?, "joined_batch" }: a fail files a follow-up, before the eval when it can
POST   /api/tickets/:key/usage   { "input_tokens", "output_tokens", "cache_creation_input_tokens",
                                   "cache_read_input_tokens", "cost_usd"? (an estimate), "wall_time_seconds", "session_id"? }
         → the claim it landed on: that session's claim, else the newest. A retry replaces, never adds.
POST   /api/batches/:id/reviews   { "phase": before|after, "commit", "body", "project"? } → 201 + review
         (a batch planned since TP-108 is not claimable without a before-review nor finishable without an
         after-review; an after-review needs the before-review first)
GET    /api/batches/:id/reviews   → the batch's reviews, before then after
POST   /api/batches/:id/size      { "lines_added", "lines_deleted", "test_lines_added"?, "test_lines_deleted"?,
                                   "base_commit"? (7-40 hex), "project"? }
         → the batch. What the digest's Measures section reports; posted by scripts/batch-size.sh, because
         the API never runs git (rule 22). A retry replaces, never adds. lines_* are the WHOLE diff;
         test_lines_* say how much of it was test files, so the ceiling can count code only (TP-212).
         They come together or not at all, and neither may exceed the pair it was split out of (400).
GET    /api/project-memory?project=   → { "review" (the latest after-review, or null), "batch_title", "read_next" }
GET    /api/needs-you?project=  → { "triage", "checkpoints", "proposals", "uncited", "tripwires", "total" }:
                                  everything waiting on a human decision
         → 201 + a comment of kind "proposal". Nothing changes until a human applies it.
GET    /api/tripwires?project=       → tripwires
POST   /api/tripwires      { "test_ref", "meaning", "body_sha256", "project"? } → tripwire
         CI or the harness registers a test; a changed body_sha256 clears the human's confirmation,
         and confirming is GraphQL's (confirmTripwire).
         body_sha256 is NOT the caller's to define: it is whatever `scripts/tripwire-hash.sh <test_ref>`
         prints, and `scripts/tripwire_hash.py` is that definition (rule 26). The API cannot check it —
         it never touches the repository — so two callers hashing the same test differently would
         silently throw away a confirmation a human gave. Never hash a test by hand.
POST   /api/claim          { "area"?, "batch_id"?, "project"? }  (an empty body is fine; rule 15 applies)
         → the claim envelope: { "ticket", "area_detail", "batch"?, "guidance"? }, or null when
         nothing is workable. "guidance" is absent, never empty, when nothing was served.
         X-Eval-Run / X-Eval-Arm / X-Eval-Model headers are recorded on the claim, not trusted from
         the body — the arm is the runner's to decide, not the agent's.
GET    /api/batches?project=
POST   /api/batches        { "kind", "title"?, "brief"?, "summary"?, "guidance"?: [slug], "pin"? }
         → 201 + batch. "guidance" attaches documents to the batch; "pin" freezes them at their
         current revision, which an experiment needs and ordinary work does not.
         `kind: plan` is refused (400, naming plan_batch): a plan batch is opened by
         POST /api/batches/plan, the one door that files the eval ticket gating it.
GET    /api/batches/:id    → { "batch", "tickets" (in batch order), "progress"? }
         progress (plan batches only): { "next": key|null, "locked_by"?: reason, "blocked_by": { key: key } }
PATCH  /api/batches/:id    { "finish": true, "summary"? } → batch with its computed "digest" (rule 16 for
                           plan batches; finishing again recomputes the digest, keeps finished_at)
POST   /api/batches/plan   { "title", "what", "how", "why", "demo", "tickets": [{ "title", "description"?,
           "type", "priority"?, "area"?, "tags"? }], "roadmap_id"?, "sequence"?, "batch_id"?, "project"? }
         → 201 + the batch response above. Tickets are filed in order as AI tickets (triage), and an
         eval ticket carrying the gate commands and the demo is appended last.
         `batch_id` fills an existing roadmap placeholder instead of creating a batch → 200: the batch
         keeps its id, roadmap and sequence, its what/how/why and title are overwritten, its tickets take
         positions 1..n and its eval ticket is rewritten for the new demo and moved last. Refused (409)
         if that batch already holds a planned ticket, is not a `plan` batch, has finished, or its eval
         ticket has started; `roadmap_id`, `sequence` or no tickets alongside it is a 400.
PUT    /api/batches/:id/order      { "keys": [every key in the batch, in the new order] }
POST   /api/batches/:id/tickets    { "key", "position"? }   (default: just before the eval ticket)
DELETE /api/batches/:id/tickets/:key                        (the ticket leaves the batch; nothing is deleted)
         All three answer with the batch response. Started tickets keep their place; the eval ticket
         stays last and cannot be removed; a finished batch cannot change, except that a ticket which
         never started may leave a finished batch that has no checkpoint (batches finished before
         rule 16 can hold such tickets).
GET    /api/roadmaps?project=      → roadmaps
POST   /api/roadmaps       { "title", "spec"?, "project"? } → 201 + roadmap
GET    /api/roadmaps/:id   → { "roadmap", "batches": [{ "batch", "state", "work", "work_done" }] }
         state: locked | unplanned | open | at_eval | awaiting_checkpoint | finished | stopped
GET    /api/tags?project=&include_archived=   → the managed tag list (managed over GraphQL)
PUT    /api/batches/:id/guidance   { "guidance": [slug], "pin"? }
GET    /api/guidance?project=      → documents (without bodies)
GET    /api/guidance/:slug         → { "document", "revision" }  (revision carries the body)
PUT    /api/guidance/:slug { "title", "kind", "body", "inline"?, "weight"?, "source_paths"? }
         Creates or edits. Revisions are append-only; saving identical text is a no-op.
POST   /api/guidance/:slug/review  { "source_commit"?, "stale"?, "stale_reason"?, "accepted"? }
         What `just guidance-check` writes back. The API never sees the repository, so it records
         a judgement made somewhere that can.
GET    /api/areas?project=&include_archived=
         Areas are listed here and managed over GraphQL only (createArea, updateArea, archiveArea):
         they are the human's list, and auto_accept_ai would let an agent route findings past triage.
GET    /healthz
GET    /api/version           → { "commit", "migration" }
         What the running process is serving, for a caller holding a checkout to compare against:
         `commit` is GIT_COMMIT, which scripts/dev.sh sets from `git rev-parse HEAD` at launch, or
         "unknown" when it is unset; `migration` is the newest migration embedded in the binary,
         which startup applied. The API never runs git, so it reports only what it was told and
         what it carries.
```

## MCP tools (`/mcp`, streamable HTTP via the official Go SDK)

No tool takes an `actor`: it comes from the caller's credential, per rule 10. Return values are the
same JSON shapes as REST, except `claim_next_ticket` — see below.

The server also sends an **instructions** block at `initialize`, carrying the work loop and the
invariants that are expensive to discover by trial. It deliberately carries no conventions: those
change, and instructions cannot change without a server restart and an agent reconnect.

| tool                 | args                                                                                  |
|----------------------|---------------------------------------------------------------------------------------|
| `start_batch`        | `kind` (audit\|review\|prompt), `title`?, `brief`?, `summary`? → batch. `plan` is refused (400): use `plan_batch`; only a plan batch has a branch (rule 22), so there is no `git_ref` here |
| `finish_batch`       | `id`, `summary`? → batch, with the `digest` it computed (markdown; nobody writes it)  |
| `report_findings`    | `batch_id`?, `findings: [{ title, description, type, priority, area?, fingerprint?, locations?, tags? }]` → `[{ key, created, status }]`. Source is always `ai`; each finding is routed and deduped per the service rules. The whole batch is one transaction. |
| `claim_next_ticket`  | `area`?, `batch`? → **an envelope**, or null. The ticket's own fields at the top level, plus `area_detail` (the area's description and globs), `batch` (`title`, `what`, `how`, `brief`), `guidance` and `next_steps`. Rule 15 decides what is claimable. |
| `get_guidance`       | `slug`? → one document with its body, or every document for the project               |
| `report_guidance_drift` | `slug`, `problem`, `evidence`? → files a ticket in the `docs` area, fingerprinted `guidance:<slug>:<revision hash>` so repeats dedupe and an edit lets the next report through |
| `list_batches`       | → batches, newest first                                                               |
| `get_batch`          | `id` → `{ batch, tickets, progress? }`, as `GET /api/batches/:id`                     |
| `plan_batch`         | `title`, `what`, `how`, `why`, `demo`, `tickets: [{ title, description?, type, priority?, area?, tags? }]`, `roadmap`?, `sequence`?, `batch`? (fill that roadmap placeholder in place, keeping its id, sequence and eval ticket) → as `POST /api/batches/plan` |
| `reorder_batch`      | `id`, `keys` → the batch                                                              |
| `add_to_batch`       | `id`, `key`, `position`? → the batch                                                  |
| `remove_from_batch`  | `id`, `key` → the batch                                                               |
| `create_roadmap`     | `title`, `spec`? → roadmap                                                            |
| `get_roadmap`        | `id`? → `{ roadmap, batches }`, or every roadmap when `id` is omitted                 |
| `list_tags`          | → the managed tag list                                                                |
| `update_ticket`      | `key`, `version`, plus any PATCH fields, including `tags` (replaces the set)          |
| `link_pull_request`  | `key`, `url`, `number`?, `branch`?, `state`? → ticket                                 |
| `add_comment`        | `key`, `body` → comment                                                               |
| `list_tickets`       | same filters as `GET /api/tickets`, including `tag`                                   |
| `propose_ticket_edit` | `key`, `field` (intent\|constraints\|non_goals\|criteria), `proposed`, `rationale` → a proposal comment; nothing changes until a human applies it |
| `get_project_memory` | → the latest after-review, its commit, and `read_next` (the diff to read beside it) |
| `record_review`      | `batch_id`, `phase` (before\|after), `commit`, `body` → the review; gates a planned batch's start and finish |
| `record_verdict`     | `key`, `ordinal`, `verdict` (pass\|fail\|deviation), `reason` (fail/deviation), `evidence` → the verdict, and a fail's `follow_up` ticket |
| `record_decision`    | `key`, `kind` (accepted\|deferred\|rejected\|reframed), `rationale`, `evidence_refs`, `revisit_trigger` (deferred), `supersedes`, `cites_revision_id` → the decision, made_by ai |
| `get_ticket`         | `key` → the ticket's own fields at the top level, plus `criteria`, `checks` (public only), `decisions` (newest first), `comments`, `events`, `locations`, `pull_requests`. Same shape as the claim envelope, because the instructions send an agent from one to the other on a 409 |
| `list_areas`         | `include_archived`? → areas, each with its description, path globs and open count      |

Accepting a batch, signing off its checkpoint and managing tags are GraphQL only (`acceptBatch`,
`checkpointBatch`, `createTag`/`archiveTag`): they are the human's decisions, and the agent's
transports do not carry them. Retiring a plan nobody worked is `checkpointBatch` with `stop` on a
batch that never finished (rule 16), so it is the human's move for the same reason.

### The claim envelope

`claim_next_ticket` is the one place guidance reaches an agent without it knowing to ask, so the
documents ride along in the response rather than waiting to be fetched:

```json
{ "key": "TP-42", "status": "in_progress", "version": 8,
  "area": { "id": "…", "name": "api" },
  "area_detail": { "description": "Go API: GraphQL, REST, MCP, service layer", "path_globs": ["api/**"] },
  "batch": { "title": "post-port audit", "brief": "why this work is being done" },
  "guidance": { "budget_tokens": 4000, "used_tokens": 2310,
                "docs": [ { "slug": "go-service-layer", "revision": 3, "body": "…" } ],
                "omitted": [ { "slug": "solid-2-patterns", "fetch": "get_guidance(slug: \"solid-2-patterns\")" } ],
                "sha256": "…" },
  "next_steps": [ "Read every body under guidance.docs before editing code.", "…" ] }
```

The envelope is additive: every field the tool returned before is still at the top level. It is
MCP's shape alone — `GET /api/tickets` is unchanged, because widening the shared ticket JSON would
change REST and GraphQL for every consumer to buy one field.

A document over the token budget still appears, under `omitted`, with the call that fetches it.
Being told what you did not receive is more use than silence.

`guidance.sha256` is taken over `document:revision:content` per served document, in order. It is
what pins a claim to exactly the bytes it was given, and it is recomputable from
`ticket_claim_documents` alone.

## Tests (`api/internal/*/\*_test.go`)

Against real Postgres (`DATABASE_URL` must be set). `internal/testdb` gives every test its own
migrated database: the migration runs once into a template, and each test clones it, so two tests
can hold two live connections at the same time — which the fingerprint-race tests need.
- create human ticket → status open, key `TP-1`, `created` event.
- create ai ticket → triage; with auto_accept area → open; second create with same fingerprint returns the same key, `seen_count = 2`, one system comment.
- update with stale version → conflict; valid update bumps version and writes events per changed field.
- transition `open → done` without resolution → error; `open → in_progress → done` with resolution sets `completed_at`.
- filters: source/status/type/area via the service list function; search finds by word in title.
- claim_next_ticket returns the highest-priority open ticket and marks it in_progress; a second call returns the next one.
- auto-routing: finding at `api/internal/x.go` lands in area `api`.
- REST: `POST /api/tickets` then `GET /api/tickets?source=human` returns it; `PATCH` with status change works (via `httptest` against the real router).
- order (rule 15): a plan batch hands out ticket 1 before an urgent ticket 3; a triage ticket holds up the ones after it until it is decided; a move to `in_progress` cannot skip the order and names the blocker; a roadmap's next batch waits for the previous one to finish and be checkpointed continue or adjust.
- finishing (rule 16): a plan batch will not finish while work is open or its eval ticket is not done (closing the eval does not count); a review batch finishes with its tickets open.
- plans: `start_batch` refuses `kind: plan` and names `plan_batch`, so every plan batch has the eval ticket that gates it; `plan_batch` files tickets in order and appends the eval ticket, refuses a plan with no demo or an unknown tag and leaves nothing behind; every planned ticket and every ticket added to a plan carries the `just gate` command check, the eval ticket carries none, and a ticket that already had checks keeps exactly those; `batch` fills a roadmap placeholder in one call, keeping its id, sequence and eval ticket, and refuses a batch that already holds planned work; reorder keeps started work and the eval ticket in place; add/remove close up positions; a roadmap reports each batch's state; a `stop` checkpoint retires a plan nobody started and the roadmap then reads it `stopped`, while the same call is refused for a batch holding a started ticket, for any other decision, and for a findings batch; acceptBatch and checkpointBatch are GraphQL only, and MCP does not list them.
