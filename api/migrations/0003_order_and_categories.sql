-- Order, roadmaps and categories.
--
-- Until now a batch was a label: the claim picked the highest-priority open ticket in the project,
-- so ticket 5 of a plan could be done before tickets 1-4 and a build could skip its foundations.
-- This migration gives a batch an order, gives batches an order of their own (a roadmap), and
-- records the human checkpoint between one batch and the next. The rules that read these columns
-- live in the service layer; this file only makes them expressible.

-- --- ticket types ------------------------------------------------------------
--
-- research, design and refactor are kinds of work the old four had no honest word for (design
-- tickets were filed as `feature` with a "Design:" title prefix). eval is the gate that closes every
-- planned batch.
ALTER TABLE tickets DROP CONSTRAINT tickets_type_check;
ALTER TABLE tickets ADD CONSTRAINT tickets_type_check
  CHECK (type IN ('bug', 'feature', 'improvement', 'chore', 'research', 'design', 'refactor', 'eval'));

-- --- order inside a batch -----------------------------------------------------
--
-- NULL for tickets outside a batch, and for findings batches, which have no sequence. Deferred so a
-- reorder can swap two positions inside one transaction.
ALTER TABLE tickets ADD COLUMN batch_position integer CHECK (batch_position > 0);
ALTER TABLE tickets ADD CONSTRAINT tickets_batch_position_uq
  UNIQUE (batch_id, batch_position) DEFERRABLE INITIALLY DEFERRED;

-- Plan batches were already filed in the order they were meant to be worked, so ticket number is
-- that order. Findings batches (audit, review, prompt) stay unordered.
UPDATE tickets t SET batch_position = ranked.position
FROM (
  SELECT t2.id, row_number() OVER (PARTITION BY t2.batch_id ORDER BY t2.number) AS position
  FROM tickets t2 JOIN batches b ON b.id = t2.batch_id
  WHERE b.kind = 'plan'
) ranked
WHERE t.id = ranked.id;

-- --- roadmaps -----------------------------------------------------------------
--
-- An ordered list of batches derived from one specification. The next batch opens only after the
-- previous one has finished and a human has signed off its checkpoint.
CREATE TABLE roadmaps (
  id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id   uuid        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  title        text        NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
  spec         text        NOT NULL DEFAULT '',          -- the specification the roadmap follows
  created_at   timestamptz NOT NULL DEFAULT now(),
  archived_at  timestamptz
);
CREATE INDEX roadmaps_project_idx ON roadmaps (project_id);

ALTER TABLE batches ADD COLUMN roadmap_id uuid REFERENCES roadmaps(id);
ALTER TABLE batches ADD COLUMN sequence integer CHECK (sequence > 0);
ALTER TABLE batches ADD CONSTRAINT batches_roadmap_sequence_uq
  UNIQUE (roadmap_id, sequence) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE batches ADD CONSTRAINT batches_roadmap_sequence_pair
  CHECK ((roadmap_id IS NULL) = (sequence IS NULL));

-- The human read: what the batch builds and how. `brief` already carries why.
ALTER TABLE batches ADD COLUMN what text NOT NULL DEFAULT '';
ALTER TABLE batches ADD COLUMN how  text NOT NULL DEFAULT '';

-- The checkpoint is where direction changes: continue as planned, adjust the remaining batches, or
-- stop. Set by a human after the batch's eval ticket is done.
ALTER TABLE batches ADD COLUMN checkpoint_decision text
  CHECK (checkpoint_decision IN ('continue', 'adjust', 'stop'));
ALTER TABLE batches ADD COLUMN checkpoint_note text;
ALTER TABLE batches ADD COLUMN checkpoint_by   text;
ALTER TABLE batches ADD COLUMN checkpoint_at   timestamptz;

-- --- tags -----------------------------------------------------------------------
--
-- A managed list, like areas, so agents pick a tag rather than invent a synonym for one.
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

INSERT INTO tags (project_id, name, description)
SELECT p.id, t.name, t.description
FROM projects p, (VALUES
  ('consistency',  'Code or docs that disagree with the project''s own conventions'),
  ('eval',         'Measurement, test gates and the evaluation harness'),
  ('bug-check',    'Hunting for defects rather than building'),
  ('architecture', 'Structure and direction of the system'),
  ('tech-debt',    'Cleanup that makes later work cheaper'),
  ('security',     'Anything an attacker could use'),
  ('performance',  'Speed, cost and token spend'),
  ('docs-drift',   'Documentation that no longer matches the code')
) AS t(name, description);
