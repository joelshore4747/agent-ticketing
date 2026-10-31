-- Canonical data model for the ticketing system (PostgreSQL 16).
-- api/migrations/ must produce exactly this schema. Enum-like columns are text
-- with CHECK constraints so they bind cleanly from pgx and compare as plain strings.

-- repo_path is where the project's code is checked out on this machine: absolute, and null until
-- somebody says. It is the machine's fact, not the schema's, so no migration fills it in.
CREATE TABLE projects (
  id                  uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  key                 text        NOT NULL UNIQUE CHECK (key ~ '^[A-Z][A-Z0-9]{1,9}$'),
  name                text        NOT NULL,
  repo_path           text        CHECK (repo_path IS NULL OR repo_path LIKE '/%'),
  next_ticket_number  integer     NOT NULL DEFAULT 1,
  created_at          timestamptz NOT NULL DEFAULT now()
);

-- Areas route work to a part of the codebase. path_globs match file paths
-- reported by AI findings (e.g. '{api/src/auth/**}').
CREATE TABLE areas (
  id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id      uuid        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  name            text        NOT NULL CHECK (name ~ '^[a-z0-9][a-z0-9._/-]{0,63}$'),
  description     text        NOT NULL DEFAULT '',
  path_globs      text[]      NOT NULL DEFAULT '{}',
  auto_accept_ai  boolean     NOT NULL DEFAULT false,
  archived_at     timestamptz,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  UNIQUE (project_id, name)
);

-- An ordered list of batches derived from one specification. The next batch opens only after the
-- previous one has finished and a human has signed off its checkpoint.
CREATE TABLE roadmaps (
  id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id   uuid        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  title        text        NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
  spec         text        NOT NULL DEFAULT '',                    -- the specification the roadmap follows
  created_at   timestamptz NOT NULL DEFAULT now(),
  archived_at  timestamptz
);
CREATE INDEX roadmaps_project_idx ON roadmaps (project_id);

-- A batch of work that belongs together: the findings of one audit, review or prompt, or a set of
-- tickets planned in advance. Carries what it builds, how, and why (brief), and the guidance
-- documents describing how to do it (see batch_guidance). A plan batch's tickets are ordered by
-- tickets.batch_position and end with an eval ticket.
CREATE TABLE batches (
  id                   uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id           uuid        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  kind                 text        NOT NULL CHECK (kind IN ('audit', 'review', 'prompt', 'plan')),
  actor                text        NOT NULL DEFAULT 'claude-code',
  title                text        NOT NULL DEFAULT '',
  brief                text        NOT NULL DEFAULT '',            -- why this work is being done
  git_ref              text,
  summary              text,                                       -- written when the batch finishes
  findings_count       integer     NOT NULL DEFAULT 0,             -- findings REPORTED, dedupes included
  started_at           timestamptz NOT NULL DEFAULT now(),
  finished_at          timestamptz,
  roadmap_id           uuid        REFERENCES roadmaps(id),
  sequence             integer     CHECK (sequence > 0),           -- position in the roadmap
  what                 text        NOT NULL DEFAULT '',            -- what the batch builds
  how                  text        NOT NULL DEFAULT '',            -- how it goes about it
  -- Set by a human once the eval ticket is done: continue, adjust the remaining batches, or stop.
  checkpoint_decision  text        CHECK (checkpoint_decision IN ('continue', 'adjust', 'stop')),
  checkpoint_note      text,
  checkpoint_by        text,
  checkpoint_at        timestamptz,
  -- Computed by finish_batch, never written by a caller (unlike summary). Markdown.
  digest               text,
  digest_at            timestamptz,
  -- Set for batches planned since TP-108: a before-review to start, an after-review to finish.
  reviews_required     boolean     NOT NULL DEFAULT false,
  -- How big the batch's change was (TP-160). Reported in by scripts/batch-size.sh, never measured
  -- here: the API does not run git and never opens repo_path (rule 22). size_base_commit is what the
  -- counts were taken against, so the figures can be reproduced with git diff --stat.
  lines_added          integer     CHECK (lines_added   IS NULL OR lines_added   >= 0),
  lines_deleted        integer     CHECK (lines_deleted IS NULL OR lines_deleted >= 0),
  -- How much of that diff was test files (TP-212). The ceiling counts code, so it reads the
  -- difference; the digest shows both, so growth in tests stays visible without being charged. The
  -- two above stay the WHOLE diff, which is what every row recorded before TP-212 means. Null where
  -- nobody split it. A file is a test by its path alone, and scripts/batch-size.sh holds the only
  -- pattern, so code hidden in a test file shows up in review rather than in the figure.
  test_lines_added     integer     CHECK (test_lines_added   IS NULL OR test_lines_added   >= 0),
  test_lines_deleted   integer     CHECK (test_lines_deleted IS NULL OR test_lines_deleted >= 0),
  size_base_commit     text        CHECK (size_base_commit IS NULL OR size_base_commit ~ '^[0-9a-f]{7,40}$'),
  size_reported_at     timestamptz,
  CONSTRAINT batches_size_together CHECK ((lines_added IS NULL) = (lines_deleted IS NULL)),
  CONSTRAINT batches_test_size_together CHECK ((test_lines_added IS NULL) = (test_lines_deleted IS NULL)),
  -- Test lines are part of the diff they were split out of: a half larger than the whole is a bad post.
  CONSTRAINT batches_test_size_within_total CHECK (
    test_lines_added IS NULL
    OR (lines_added IS NOT NULL
        AND test_lines_added <= lines_added AND test_lines_deleted <= lines_deleted)),
  CONSTRAINT batches_roadmap_sequence_uq UNIQUE (roadmap_id, sequence) DEFERRABLE INITIALLY DEFERRED,
  CONSTRAINT batches_roadmap_sequence_pair CHECK ((roadmap_id IS NULL) = (sequence IS NULL))
);
CREATE INDEX batches_project_idx ON batches (project_id);

-- The project memory: a review before and after each batch, at a commit (TP-108). The next review
-- reads the last after-review plus the diff since its commit, not the whole codebase.
CREATE TABLE project_reviews (
  id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id  uuid        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  batch_id    uuid        NOT NULL REFERENCES batches(id) ON DELETE CASCADE,
  phase       text        NOT NULL CHECK (phase IN ('before', 'after')),
  commit      text        NOT NULL CHECK (commit ~ '^[0-9a-f]{7,40}$'),
  body        text        NOT NULL CHECK (length(btrim(body)) > 0),
  actor       text        NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX project_reviews_batch_idx ON project_reviews (batch_id, phase, created_at DESC);
CREATE INDEX project_reviews_memory_idx ON project_reviews (project_id, created_at DESC) WHERE phase = 'after';

CREATE TABLE tickets (
  id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id      uuid        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  number          integer     NOT NULL,
  key             text        NOT NULL UNIQUE,                      -- '<PROJECT.key>-<number>', e.g. TP-42
  title           text        NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
  description     text        NOT NULL DEFAULT '',                  -- markdown
  type            text        NOT NULL CHECK (type IN ('bug', 'feature', 'improvement', 'chore',
                                                       'research', 'design', 'refactor', 'eval')),
  priority        text        NOT NULL DEFAULT 'medium' CHECK (priority IN ('low', 'medium', 'high', 'urgent')),
  status          text        NOT NULL CHECK (status IN ('triage', 'open', 'in_progress', 'done', 'closed')),
  source          text        NOT NULL CHECK (source IN ('ai', 'human')),
  area_id         uuid        REFERENCES areas(id) ON DELETE SET NULL,
  batch_id        uuid        REFERENCES batches(id) ON DELETE SET NULL,
  batch_position  integer     CHECK (batch_position > 0),           -- order in a plan batch; NULL otherwise
  fingerprint     text,                                             -- dedupe key for AI findings
  seen_count      integer     NOT NULL DEFAULT 1,
  last_seen_at    timestamptz NOT NULL DEFAULT now(),
  branch          text,
  resolution      text,                                             -- required when status is done/closed
  claimed_by      text,
  claimed_at      timestamptz,                                      -- claim expires after 30 minutes
  version         integer     NOT NULL DEFAULT 1,                   -- optimistic locking
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  completed_at    timestamptz,
  -- Section A: the human's (set over GraphQL only). Success criteria are ticket_criteria rows.
  intent               text          NOT NULL DEFAULT '',
  constraints          text          NOT NULL DEFAULT '',
  non_goals            text          NOT NULL DEFAULT '',
  -- Section B: the AI's proposal, approved by a human.
  approach             text          NOT NULL DEFAULT '',
  risk_and_rollback    text          NOT NULL DEFAULT '',
  worked_example       text          NOT NULL DEFAULT '',
  evidence_refs        text[]        NOT NULL DEFAULT '{}',
  verification_plan    text          NOT NULL DEFAULT '',
  estimated_tokens     integer       CHECK (estimated_tokens >= 0),
  estimated_cost_usd   numeric(10,4) CHECK (estimated_cost_usd >= 0),
  approach_approved_by text,
  approach_approved_at timestamptz,
  -- Section C: what the AI reports and a machine checks. `resolution` is the result.
  verified_by          text,
  measured_delta       text,
  gate_passed          boolean,                                   -- set only by the check runner
  gate_checked_at      timestamptz,
  summary_for_human    text          CHECK (length(summary_for_human) <= 280),
  decision_id          uuid,                                      -- current decision; FK added after decisions
  -- The key goals (1-9) this ticket serves; see the Self-improving tracker roadmap (TP-107).
  goals               integer[]   NOT NULL DEFAULT '{}' CONSTRAINT tickets_goals_known CHECK (goals <@ '{1,2,3,4,5,6,7,8,9}'::integer[]),
  search          tsvector    GENERATED ALWAYS AS (
                    setweight(to_tsvector('english', coalesce(title, '')), 'A') ||
                    setweight(to_tsvector('english', coalesce(description, '')), 'B')
                  ) STORED,
  UNIQUE (project_id, number),
  -- Deferred so a reorder can swap two positions inside one transaction.
  CONSTRAINT tickets_batch_position_uq UNIQUE (batch_id, batch_position) DEFERRABLE INITIALLY DEFERRED
);

-- Only one non-closed ticket per fingerprint; the service resolves conflicts by
-- returning the existing ticket and adding a "seen again" comment.
CREATE UNIQUE INDEX tickets_open_fingerprint_uq
  ON tickets (project_id, fingerprint)
  WHERE fingerprint IS NOT NULL AND status NOT IN ('done', 'closed');
CREATE INDEX tickets_search_idx ON tickets USING gin (search);
CREATE INDEX tickets_list_idx   ON tickets (project_id, source, status, created_at DESC);
CREATE INDEX tickets_area_idx   ON tickets (area_id);
CREATE INDEX tickets_batch_idx  ON tickets (batch_id);

-- Code locations attached to a ticket (from AI findings).
CREATE TABLE ticket_locations (
  id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  ticket_id   uuid        NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
  file_path   text        NOT NULL,
  line_start  integer,
  line_end    integer,
  snippet     text,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ticket_locations_ticket_idx ON ticket_locations (ticket_id);

-- Tags are a managed list, like areas, so agents pick one rather than invent a synonym.
CREATE TABLE tags (
  id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id   uuid        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  name         text        NOT NULL CHECK (name ~ '^[a-z0-9][a-z0-9-]{0,31}$'),
  description  text        NOT NULL DEFAULT '',
  archived_at  timestamptz,
  created_at   timestamptz NOT NULL DEFAULT now(),
  UNIQUE (project_id, name)
);

CREATE TABLE ticket_tags (
  ticket_id  uuid NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
  tag_id     uuid NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
  PRIMARY KEY (ticket_id, tag_id)
);
CREATE INDEX ticket_tags_tag_idx ON ticket_tags (tag_id);

-- A test counts as a tripwire once a human has confirmed what it protects. The confirmation is pinned
-- to the test's source: a new body_sha256 clears it. What that hash is taken over is defined once, by
-- scripts/tripwire-hash.sh, and by nothing here — the API never reads the repository (api.md rule 26).
CREATE TABLE tripwires (
  id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id    uuid        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  test_ref      text        NOT NULL,                               -- 'path:TestName' or 'path > describe > it'
  meaning       text        NOT NULL,
  body_sha256   text        NOT NULL,
  confirmed_by  text,
  confirmed_at  timestamptz,
  created_at    timestamptz NOT NULL DEFAULT now(),
  UNIQUE (project_id, test_ref)
);

-- Success criteria, one row each, so a convergence verdict can name the criterion it judged.
CREATE TABLE ticket_criteria (
  id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  ticket_id    uuid        NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
  tier         text        NOT NULL CHECK (tier IN ('behavioural', 'measurement', 'gate')),
  ordinal      integer     NOT NULL CHECK (ordinal > 0),
  text         text        NOT NULL CHECK (length(text) BETWEEN 1 AND 500),
  tripwire_id  uuid        REFERENCES tripwires(id) ON DELETE SET NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  UNIQUE (ticket_id, ordinal)
);
CREATE INDEX ticket_criteria_ticket_idx ON ticket_criteria (ticket_id, ordinal);

-- A judgement on one criterion of a finished ticket; the newest per criterion counts (TP-94).
CREATE TABLE criterion_verdicts (
  id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  criterion_id  uuid        NOT NULL REFERENCES ticket_criteria(id) ON DELETE CASCADE,
  verdict       text        NOT NULL CHECK (verdict IN ('pass', 'fail', 'deviation')),
  reason        text        NOT NULL DEFAULT '',
  evidence      text[]      NOT NULL DEFAULT '{}',
  judged_by     text        NOT NULL,
  created_at    timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT criterion_verdicts_reason CHECK (verdict = 'pass' OR length(btrim(reason)) > 0)
);
CREATE INDEX criterion_verdicts_criterion_idx ON criterion_verdicts (criterion_id, created_at DESC);

-- Decisions are rows so they can be cited, superseded and revisited; never edited. tickets.decision_id
-- points at the current one.
CREATE TABLE decisions (
  id                 uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  ticket_id          uuid        NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
  kind               text        NOT NULL CHECK (kind IN ('accepted', 'deferred', 'rejected', 'reframed')),
  made_by            text        NOT NULL CHECK (made_by IN ('human', 'ai')),
  evidence_refs      text[]      NOT NULL DEFAULT '{}',
  rationale          text        NOT NULL DEFAULT '',
  revisit_trigger    text,
  supersedes         uuid        REFERENCES decisions(id),
  cites_revision_id  uuid,                                           -- FK added after guidance_revisions
  actor              text        NOT NULL,
  created_at         timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT decisions_deferred_needs_trigger CHECK (kind <> 'deferred' OR revisit_trigger IS NOT NULL)
);
CREATE INDEX decisions_ticket_idx ON decisions (ticket_id, created_at DESC);
ALTER TABLE tickets ADD CONSTRAINT tickets_decision_id_fkey
  FOREIGN KEY (decision_id) REFERENCES decisions(id) ON DELETE SET NULL;

-- Ticket-to-ticket relations.
CREATE TABLE ticket_links (
  id                uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  ticket_id         uuid        NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
  linked_ticket_id  uuid        NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
  kind              text        NOT NULL CHECK (kind IN ('relates_to', 'blocks', 'duplicates')),
  created_at        timestamptz NOT NULL DEFAULT now(),
  UNIQUE (ticket_id, linked_ticket_id, kind),
  CHECK (ticket_id <> linked_ticket_id)
);

CREATE TABLE pull_requests (
  id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  ticket_id   uuid        NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
  url         text        NOT NULL,
  number      integer,
  branch      text,
  state       text        NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'merged', 'closed')),
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (ticket_id, url)
);

CREATE TABLE comments (
  id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  ticket_id   uuid        NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
  author      text        NOT NULL,
  body        text        NOT NULL,                                  -- markdown
  kind        text        NOT NULL DEFAULT 'comment' CHECK (kind IN ('comment', 'system', 'proposal')),
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX comments_ticket_idx ON comments (ticket_id, created_at);

-- Audit trail: one row per changed field per update.
CREATE TABLE ticket_events (
  id          bigserial   PRIMARY KEY,
  ticket_id   uuid        NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
  actor       text        NOT NULL,
  field       text        NOT NULL,                                  -- e.g. 'status', 'title', 'created', 'seen_again', 'claimed'
  old_value   text,
  new_value   text,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ticket_events_ticket_idx ON ticket_events (ticket_id, created_at);

-- --- guidance ---------------------------------------------------------------
--
-- Documents are mutable; their revisions are not. An edit appends a revision and repoints
-- current_revision_id, so a claim can pin the exact bytes an agent was served by foreign key --
-- an invariant the database holds rather than one the application promises.

CREATE TABLE guidance_documents (
  id                   uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id           uuid        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  slug                 text        NOT NULL CHECK (slug ~ '^[a-z0-9][a-z0-9-]{0,63}$'),
  title                text        NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
  kind                 text        NOT NULL CHECK (kind IN ('workflow', 'convention', 'area', 'checklist')),
  current_revision_id  uuid,                                      -- FK added below: the two tables reference each other
  inline               boolean     NOT NULL DEFAULT true,         -- rides a claim, or is only announced
  weight               integer     NOT NULL DEFAULT 0,            -- order within a bundle
  source_paths         text[]      NOT NULL DEFAULT '{}',         -- repo paths this document describes
  source_commit        text,                                      -- their HEAD at the last review
  reviewed_at          timestamptz,
  reviewed_by          text,
  review_interval_days integer     NOT NULL DEFAULT 90,
  stale                boolean     NOT NULL DEFAULT false,
  stale_reason         text,
  drift_reported_at    timestamptz,
  archived_at          timestamptz,
  version              integer     NOT NULL DEFAULT 1,            -- optimistic locking, as tickets
  created_at           timestamptz NOT NULL DEFAULT now(),
  updated_at           timestamptz NOT NULL DEFAULT now(),
  UNIQUE (project_id, slug)
);
CREATE INDEX guidance_documents_project_idx ON guidance_documents (project_id) WHERE archived_at IS NULL;

CREATE TABLE guidance_revisions (
  id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  document_id     uuid        NOT NULL REFERENCES guidance_documents(id) ON DELETE CASCADE,
  revision        integer     NOT NULL,
  body            text        NOT NULL,                           -- markdown
  content_sha256  text        NOT NULL,                           -- equality and no-op detection
  author          text        NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  UNIQUE (document_id, revision)
);
CREATE INDEX guidance_revisions_document_idx ON guidance_revisions (document_id, revision DESC);

ALTER TABLE guidance_documents ADD CONSTRAINT guidance_documents_current_revision_fkey
  FOREIGN KEY (current_revision_id) REFERENCES guidance_revisions(id);
ALTER TABLE decisions ADD CONSTRAINT decisions_cites_revision_id_fkey
  FOREIGN KEY (cites_revision_id) REFERENCES guidance_revisions(id);

-- revision_id NULL follows the document, so improving it improves work already in flight; an
-- experiment pins it so a mid-run edit cannot silently change the treatment.
CREATE TABLE batch_guidance (
  batch_id     uuid    NOT NULL REFERENCES batches(id) ON DELETE CASCADE,
  document_id  uuid    NOT NULL REFERENCES guidance_documents(id) ON DELETE CASCADE,
  revision_id  uuid             REFERENCES guidance_revisions(id),
  position     integer NOT NULL DEFAULT 0,
  PRIMARY KEY (batch_id, document_id)
);

-- --- attribution ------------------------------------------------------------
--
-- One row per delivery, because a ticket can be claimed many times: a claim expires after 30
-- minutes and reopening returns the ticket to the queue. Columns on `tickets` would overwrite
-- exactly the history worth keeping, and ticket_events is rendered to the user as History.

CREATE TABLE ticket_claims (
  id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  ticket_id       uuid        NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
  project_id      uuid        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  actor           text        NOT NULL,
  session_id      text,                                           -- observed from the transport,
  client_name     text,                                           -- never self-reported
  client_version  text,
  run_tag         text,                                           -- set by the evaluation runner
  arm             text        CHECK (arm IN ('control', 'guided', 'placebo')),
  model           text,
  payload         text        NOT NULL,                           -- text, so the bytes reproduce
  guidance_sha256 text,                                           -- NULL when nothing was served
  guidance_bytes  integer     NOT NULL DEFAULT 0,
  claimed_at      timestamptz NOT NULL DEFAULT now(),
  released_at     timestamptz,
  outcome         text        CHECK (outcome IN ('done', 'closed', 'reopened', 'expired')),
  -- What the session cost, reported by a session-end hook (TP-101); cost_usd is the CLI's estimate.
  input_tokens                bigint CHECK (input_tokens >= 0),
  output_tokens               bigint CHECK (output_tokens >= 0),
  cache_creation_input_tokens bigint CHECK (cache_creation_input_tokens >= 0),
  cache_read_input_tokens     bigint CHECK (cache_read_input_tokens >= 0),
  cost_usd                    numeric(12,4) CHECK (cost_usd >= 0),
  wall_time_seconds           integer CHECK (wall_time_seconds >= 0),
  usage_reported_at           timestamptz
);
CREATE INDEX ticket_claims_ticket_idx ON ticket_claims (ticket_id, claimed_at DESC);
CREATE INDEX ticket_claims_run_idx    ON ticket_claims (run_tag) WHERE run_tag IS NOT NULL;

CREATE TABLE ticket_claim_documents (
  claim_id     uuid    NOT NULL REFERENCES ticket_claims(id) ON DELETE CASCADE,
  revision_id  uuid    NOT NULL REFERENCES guidance_revisions(id),
  position     integer NOT NULL,
  PRIMARY KEY (claim_id, revision_id)
);

-- --- scoring ----------------------------------------------------------------
--
-- Never served over MCP: an agent that can read the test it will be scored against is not being
-- measured.

CREATE TABLE ticket_checks (
  id           uuid    PRIMARY KEY DEFAULT gen_random_uuid(),
  ticket_id    uuid    NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
  ordinal      integer NOT NULL,
  description  text    NOT NULL,
  kind         text    NOT NULL CHECK (kind IN ('command', 'manual')),
  command      text,
  expect_exit  integer NOT NULL DEFAULT 0,
  -- public checks are the gate tier the agent may read; held-out ones never leave psql.
  visibility   text    NOT NULL DEFAULT 'held_out' CHECK (visibility IN ('public', 'held_out')),
  UNIQUE (ticket_id, ordinal),
  CHECK (kind <> 'command' OR command IS NOT NULL)
);

CREATE TABLE guidance_probes (
  id           uuid    PRIMARY KEY DEFAULT gen_random_uuid(),
  revision_id  uuid    NOT NULL REFERENCES guidance_revisions(id) ON DELETE CASCADE,
  description  text    NOT NULL,
  command      text    NOT NULL,
  expect_exit  integer NOT NULL DEFAULT 0
);
CREATE INDEX guidance_probes_revision_idx ON guidance_probes (revision_id);

-- Seed: one project and a starter set of areas for this repo.
INSERT INTO projects (key, name) VALUES ('TP', 'Ticketing');
INSERT INTO areas (project_id, name, description, path_globs, auto_accept_ai)
SELECT id, a.name, a.description, a.path_globs, a.auto_accept_ai
FROM projects, (VALUES
  ('api',   'Go API: GraphQL, REST, MCP, service layer', ARRAY['api/**'],                                   false),
  ('web',   'Solid 2 web app',                             ARRAY['web/**'],                                   false),
  ('docs',  'Docs, README, CLAUDE.md',                     ARRAY['docs/**', 'README.md', 'CLAUDE.md'],        true),
  ('infra', 'Build, dev scripts, compose, CI',             ARRAY['Justfile', 'compose.yml', 'scripts/**', '.github/**'], false)
) AS a(name, description, path_globs, auto_accept_ai)
WHERE projects.key = 'TP';
INSERT INTO tags (project_id, name, description)
SELECT id, t.name, t.description
FROM projects, (VALUES
  ('consistency',  'Code or docs that disagree with the project''s own conventions'),
  ('eval',         'Measurement, test gates and the evaluation harness'),
  ('bug-check',    'Hunting for defects rather than building'),
  ('architecture', 'Structure and direction of the system'),
  ('tech-debt',    'Cleanup that makes later work cheaper'),
  ('security',     'Anything an attacker could use'),
  ('performance',  'Speed, cost and token spend'),
  ('docs-drift',   'Documentation that no longer matches the code'),
  -- The loop stops while any open ticket carries it (migration 0011; description 0014). Which moves
  -- are Joel's is the "Only Joel" section of the-agents guidance, written once and pointed at here.
  ('needs-joel',   'Waiting on a move on the Only Joel list (the-agents guidance). The loop stops while any open ticket carries it.')
) AS t(name, description)
WHERE projects.key = 'TP';
