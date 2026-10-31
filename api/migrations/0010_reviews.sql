-- Project reviews (goal 2 of the Self-improving tracker roadmap; TP-108).
--
-- The project is reviewed before and after each batch, not continuously. A review is the project
-- memory at one commit: structure, architecture, hotspots, debts, what should change. The next
-- review reads the last after-review and `git diff <commit>..HEAD` instead of the whole codebase,
-- which is the point: a consistent memory costs a fraction of re-reading everything.
--
-- A batch planned from here on requires both: its first ticket is not handed out until a before-
-- review exists, and it cannot finish without an after-review. Batches planned earlier are unchanged.
ALTER TABLE batches ADD COLUMN reviews_required boolean NOT NULL DEFAULT false;

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
