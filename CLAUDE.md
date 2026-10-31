# Ticketing — instructions for Claude Code

This repo is a small ticketing system (Go API in `api/`, Solid 2 app in `web/`). It is also the
tracker for its own work, and the work queue for Claude Code over MCP. Keep it small.

## Running
- `just dev` starts Postgres (Docker), the API on http://localhost:8080 and the web app on http://localhost:5173.
- `just test` runs API and UI tests. `just lint` runs gofmt, `go vet` and a production build.
- The API is the source of truth. Talk to it over REST (`/api`) or MCP (`/mcp`, streamable HTTP):
  `claude mcp add --transport http ticketing http://localhost:8080/mcp --header "Authorization: Bearer $TICKETING_TOKEN"`
  The credential in that header is who you are: it names your actor, and REST and GraphQL take the same
  one. Without it the API is in local mode and still reads `X-Actor`, as it always did.

## How this project says it wants to be worked on
**It tells you itself.** How a ticket is worked, how a finding is written, how the Go and the web
code are expected to look, and what "done" means here are guidance documents served by the API, not
prose in this file:

- Over MCP, `claim_next_ticket` attaches whatever applies to the ticket it hands you, and
  `get_guidance` reads the rest.
- Over REST, `POST /api/claim` returns the same envelope, and `GET /api/guidance` lists the corpus.
- The markdown that seeds them is in `docs/guidance/`, but **the database is the served truth** —
  `just guidance` loads the files once and then leaves them alone.

They live there rather than here because this file cannot be versioned per batch, cannot be pinned
to the revision an agent actually read, and — as three statements in this repo proved within a week
of being written — rots without anything noticing. `just guidance-check` compares each document
against the code it describes and fails when the ground has moved. It also compares each served body
against its `docs/guidance/` file and fails on any difference, because the file falling behind is how
the served text gets deleted: the PUT replaces the whole body, so `just guidance --replace` refuses a
file that would drop served lines unless you add `--force`.

If a document is wrong, say so with `report_guidance_drift` while you still have the code and the
document in front of you. You are the best-placed observer of that gap there will ever be.

## Contracts
`docs/schema.sql` (data model) and `docs/api.md` (GraphQL, REST, MCP, service rules). The GraphQL
schema is generated from the SDL in `docs/api.md`: edit it there, then `just generate`. Never
hand-edit `api/internal/graph/generated.go` or `gqlmodel/`.

## Facts the API will not forgive you for guessing
- Every update needs the ticket's current `version` (optimistic locking). A 409 means it changed:
  re-GET and retry.
- Enum values are lowercase snake_case everywhere (`in_progress`).
- Areas are a managed list with path globs (`GET /api/areas`; created and edited over GraphQL only). Do not file into a made-up area; if
  work does not fit one, propose a new area to the user first.
- Everything is scoped by project (`GET /api/projects`); an unqualified request resolves to the
  oldest. You never need it to address one ticket — a key names its project.
- Connecting another project is written down once: the "Connecting a project" section of `the-system` guidance.
- AI tickets land in `triage` unless the area has `auto_accept_ai`. Only `open` tickets are workable, and
  only a human moves a ticket out of triage: REST and MCP refuse it (rule 17).
- Decisions are records, not edits: `record_decision` (MCP) or `POST /api/tickets/:key/decisions`,
  citing `evidence_refs`. A deferral needs a `revisit_trigger`; a decision that cites nothing is
  flagged to the human (rule 19).
- Finishing a batch computes its digest from the record (rule 20); nobody writes it. What waits on the
  human is `GET /api/needs-you`.
- A plan batch has a branch (`git_ref`, rule 22): branch tickets from it, and merge it into main with
  `scripts/batch-merge.sh` once the batch finishes. The API only names the commands.
- Session usage comes in through `POST /api/tickets/:key/usage` (rule 23), posted by `scripts/loop.sh`
  after each agent step; a hand-worked session posts its own (`usage-report.sh --key`). Tokens first, $ an estimate.
- Every ticket names the key goals (1-9) it serves; `plan_batch` refuses one that names none (rule 24,
  `key-goals` guidance). A newly planned batch needs a before-review to start and an after-review to
  finish; start each review from `get_project_memory` plus the diff since its commit.
- The loop (`just loop`, the `the-agents` guidance) runs plan → approve → build each ticket (one Sonnet step) →
  one review-and-trim step per batch (Opus: whole diff, fixes, deletions, guidance, memory) → eval → report →
  checkpoint → merge. It pauses after 3 batches and stops on failure, a second send-back, usage at or above
  `LOOP_USAGE_LIMIT` (default 0.6), or an open `needs-joel` ticket the batch it would work next has reached. `--dry-run`
  shows the next step; `just cost` shows what the steps spent. Which moves are Joel's
  is the **Only Joel** list in `the-agents` guidance, written once: an agent files a `needs-joel` ticket rather than
  attempting one, and does its own housekeeping rather than asking him.
- `done` needs evidence by type (rule 21): `verified_by` for most, plus a worked example for a
  feature; the 400 names what is missing. `gate_passed` comes only from `scripts/run-checks.sh`.
- A ticket's intent, constraints, non-goals and success criteria are the human's. You write the
  approach, verification plan and `verified_by`; to change the human's part, `propose_ticket_edit`.
- A plan batch is worked in order: the claim will not hand out ticket 5 while tickets 1-4 are unfinished,
  and a roadmap's next batch stays locked until the previous one is finished and checkpointed. When a
  claim returns null, `get_batch` says what is in the way. Plan work with `plan_batch`; tags come from
  `list_tags`.
