-- Batches: generated tickets that carry the guidance for building them.
--
-- `audit_runs` already grouped the findings of one reporting session, but it was write-only: no
-- query ever read a run back, and nothing outside MCP could see one. This migration finishes that
-- concept, renames it to something that also fits work planned in advance, and hangs reusable
-- guidance documents off it.
--
-- The rename is free: the table was empty and every ticket had audit_run_id = NULL.

ALTER TABLE audit_runs RENAME TO batches;
ALTER TABLE tickets RENAME COLUMN audit_run_id TO batch_id;

-- Renaming a table leaves its constraint and index names behind, so bring them along.
ALTER TABLE batches RENAME CONSTRAINT audit_runs_kind_check TO batches_kind_check;
ALTER TABLE batches RENAME CONSTRAINT audit_runs_project_id_fkey TO batches_project_id_fkey;
ALTER INDEX audit_runs_pkey RENAME TO batches_pkey;
ALTER TABLE tickets RENAME CONSTRAINT tickets_audit_run_id_fkey TO tickets_batch_id_fkey;

-- 'plan' is the point of the rename: a batch can now describe work decided in advance, not only
-- findings reported after the fact.
ALTER TABLE batches DROP CONSTRAINT batches_kind_check;
ALTER TABLE batches ADD CONSTRAINT batches_kind_check
  CHECK (kind IN ('audit', 'review', 'prompt', 'plan'));

ALTER TABLE batches ADD COLUMN title text NOT NULL DEFAULT '';
-- Why this work is being done, in the author's words. Served to whoever picks up its tickets.
ALTER TABLE batches ADD COLUMN brief text NOT NULL DEFAULT '';

-- Neither side of this relationship had an index, so "the tickets in this batch" was a seq scan.
CREATE INDEX batches_project_idx ON batches (project_id);
CREATE INDEX tickets_batch_idx   ON tickets (batch_id);

-- --- guidance ---------------------------------------------------------------
--
-- Documents are mutable; their revisions are not. An edit appends a revision and repoints
-- current_revision_id, so a claim can pin the exact bytes an agent was served by foreign key —
-- an invariant the database holds rather than one the application promises.

CREATE TABLE guidance_documents (
  id                   uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id           uuid        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  slug                 text        NOT NULL CHECK (slug ~ '^[a-z0-9][a-z0-9-]{0,63}$'),
  title                text        NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
  kind                 text        NOT NULL CHECK (kind IN ('workflow', 'convention', 'area', 'checklist')),
  current_revision_id  uuid,                                     -- FK added below, after revisions exists
  -- false means the document is announced but not inlined: too long to spend a claim's budget on.
  inline               boolean     NOT NULL DEFAULT true,
  weight               integer     NOT NULL DEFAULT 0,           -- order within a bundle
  -- The repo paths this document describes. CI compares their latest commit against source_commit
  -- to decide whether the ground has moved under the document since anyone last reviewed it.
  source_paths         text[]      NOT NULL DEFAULT '{}',
  source_commit        text,
  reviewed_at          timestamptz,
  reviewed_by          text,
  review_interval_days integer     NOT NULL DEFAULT 90,
  stale                boolean     NOT NULL DEFAULT false,
  stale_reason         text,
  drift_reported_at    timestamptz,
  archived_at          timestamptz,
  version              integer     NOT NULL DEFAULT 1,           -- optimistic locking, as tickets
  created_at           timestamptz NOT NULL DEFAULT now(),
  updated_at           timestamptz NOT NULL DEFAULT now(),
  UNIQUE (project_id, slug)
);

CREATE TABLE guidance_revisions (
  id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  document_id     uuid        NOT NULL REFERENCES guidance_documents(id) ON DELETE CASCADE,
  revision        integer     NOT NULL,
  body            text        NOT NULL,                          -- markdown
  -- sha256 of body. Identity is (document_id, revision); this is for equality and no-op detection.
  content_sha256  text        NOT NULL,
  author          text        NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  UNIQUE (document_id, revision)
);

ALTER TABLE guidance_documents ADD CONSTRAINT guidance_documents_current_revision_fkey
  FOREIGN KEY (current_revision_id) REFERENCES guidance_revisions(id);

CREATE INDEX guidance_documents_project_idx ON guidance_documents (project_id)
  WHERE archived_at IS NULL;
CREATE INDEX guidance_revisions_document_idx ON guidance_revisions (document_id, revision DESC);

-- A batch's guidance. revision_id NULL means "follow the document", so improving a document
-- improves work already in flight; an experiment pins the revision so a mid-run edit cannot
-- silently change the treatment.
CREATE TABLE batch_guidance (
  batch_id     uuid    NOT NULL REFERENCES batches(id) ON DELETE CASCADE,
  document_id  uuid    NOT NULL REFERENCES guidance_documents(id) ON DELETE CASCADE,
  revision_id  uuid             REFERENCES guidance_revisions(id),
  position     integer NOT NULL DEFAULT 0,
  PRIMARY KEY (batch_id, document_id)
);

-- --- attribution ------------------------------------------------------------
--
-- One row per delivery, because a ticket can be claimed many times: the claim expires after 30
-- minutes, and reopening puts it back in the queue. Columns on `tickets` would overwrite exactly
-- the history worth keeping, and ticket_events is rendered to the user as the History panel.

CREATE TABLE ticket_claims (
  id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  ticket_id       uuid        NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
  project_id      uuid        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  actor           text        NOT NULL,

  -- Observed from the transport, never self-reported: an agent cannot forget or misstate these.
  session_id      text,
  client_name     text,
  client_version  text,

  -- Set by the evaluation runner through request headers, so the arm is not the agent's to choose.
  run_tag         text,
  arm             text        CHECK (arm IN ('control', 'guided', 'placebo')),
  model           text,

  -- text rather than jsonb: jsonb normalises key order and whitespace, which would make the
  -- delivered bytes unreproducible.
  payload         text        NOT NULL,
  -- sha256 over document_id:revision:content_sha256 per served document, in order. Recomputable
  -- from ticket_claim_documents alone, and independent of how the response was serialised.
  guidance_sha256 text,
  guidance_bytes  integer     NOT NULL DEFAULT 0,

  claimed_at      timestamptz NOT NULL DEFAULT now(),
  released_at     timestamptz,
  outcome         text        CHECK (outcome IN ('done', 'closed', 'reopened', 'expired'))
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
-- What turns "it finished" into "it did the job". Neither table is ever served over MCP: an agent
-- that can read the test it will be scored against is not being measured.

CREATE TABLE ticket_checks (
  id           uuid    PRIMARY KEY DEFAULT gen_random_uuid(),
  ticket_id    uuid    NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
  ordinal      integer NOT NULL,
  description  text    NOT NULL,
  kind         text    NOT NULL CHECK (kind IN ('command', 'manual')),
  command      text,
  expect_exit  integer NOT NULL DEFAULT 0,
  UNIQUE (ticket_id, ordinal),
  CHECK (kind <> 'command' OR command IS NOT NULL)
);

-- A guidance document that asserts something checkable ships the check with it: "runs that saw this
-- document complied with it N% of the time" is the most interpretable number the evaluation has.
CREATE TABLE guidance_probes (
  id           uuid    PRIMARY KEY DEFAULT gen_random_uuid(),
  revision_id  uuid    NOT NULL REFERENCES guidance_revisions(id) ON DELETE CASCADE,
  description  text    NOT NULL,
  command      text    NOT NULL,
  expect_exit  integer NOT NULL DEFAULT 0
);
CREATE INDEX guidance_probes_revision_idx ON guidance_probes (revision_id);
