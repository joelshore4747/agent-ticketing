-- Convergence verdicts (TP-44, consolidated in TP-73; built in TP-94).
--
-- A judgement on one success criterion of a finished ticket: did the work meet it (pass), miss it
-- (fail), or meet something close to it on purpose (deviation, with the reason)? One row per
-- judgement, never edited; the newest per criterion is the one that counts. A criterion the human
-- later removes takes its verdicts with it.
CREATE TABLE criterion_verdicts (
  id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  criterion_id  uuid        NOT NULL REFERENCES ticket_criteria(id) ON DELETE CASCADE,
  verdict       text        NOT NULL CHECK (verdict IN ('pass', 'fail', 'deviation')),
  reason        text        NOT NULL DEFAULT '',
  evidence      text[]      NOT NULL DEFAULT '{}',
  judged_by     text        NOT NULL,
  created_at    timestamptz NOT NULL DEFAULT now(),
  -- A fail or a deviation says why; a pass may simply pass.
  CONSTRAINT criterion_verdicts_reason CHECK (verdict = 'pass' OR length(btrim(reason)) > 0)
);
CREATE INDEX criterion_verdicts_criterion_idx ON criterion_verdicts (criterion_id, created_at DESC);
