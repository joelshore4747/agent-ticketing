-- Decision records (TP-42, consolidated in TP-73).
--
-- A decision is its own row so it can be cited, superseded and revisited: accepting, deferring,
-- rejecting or reframing work, by a human or by the AI, with the evidence it rested on. Rows are
-- never edited; a changed mind is a new row that supersedes the old one. tickets.decision_id points
-- at the current one, as guidance_documents.current_revision_id points at a document's current text.
CREATE TABLE decisions (
  id                 uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  ticket_id          uuid        NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
  kind               text        NOT NULL CHECK (kind IN ('accepted', 'deferred', 'rejected', 'reframed')),
  made_by            text        NOT NULL CHECK (made_by IN ('human', 'ai')),
  evidence_refs      text[]      NOT NULL DEFAULT '{}',
  rationale          text        NOT NULL DEFAULT '',
  revisit_trigger    text,
  supersedes         uuid        REFERENCES decisions(id),
  -- The constitution revision the decision relied on (TP-72); NULL until one is accepted.
  cites_revision_id  uuid        REFERENCES guidance_revisions(id),
  actor              text        NOT NULL,
  created_at         timestamptz NOT NULL DEFAULT now(),
  -- Deferred work lands in the backlog with a reason and a trigger to revisit it, never silently.
  CONSTRAINT decisions_deferred_needs_trigger CHECK (kind <> 'deferred' OR revisit_trigger IS NOT NULL)
);
CREATE INDEX decisions_ticket_idx ON decisions (ticket_id, created_at DESC);

ALTER TABLE tickets ADD COLUMN decision_id uuid REFERENCES decisions(id) ON DELETE SET NULL;
