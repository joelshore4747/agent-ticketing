-- The ownership model (TP-41, consolidated in TP-71 after the research in TP-46..51).
--
-- A ticket gets three sections with hard owners. Section A is the human's: intent, constraints,
-- non-goals and success criteria, set only through GraphQL. Section B is the AI's proposal, which a
-- human approves: approach, risk and rollback, worked example, evidence, verification plan, estimate.
-- Section C is what the AI reports and a machine checks: verified_by, measured_delta, and the
-- held-out gate verdict. `resolution` already is the result, so it is not duplicated.
--
-- Plain columns rather than a side table: each is one current value, as title and description are,
-- and ticket_events already records every change to them.

-- --- sections ----------------------------------------------------------------
ALTER TABLE tickets
  ADD COLUMN intent               text    NOT NULL DEFAULT '',
  ADD COLUMN constraints          text    NOT NULL DEFAULT '',
  ADD COLUMN non_goals            text    NOT NULL DEFAULT '',
  ADD COLUMN approach             text    NOT NULL DEFAULT '',
  ADD COLUMN risk_and_rollback    text    NOT NULL DEFAULT '',
  ADD COLUMN worked_example       text    NOT NULL DEFAULT '',
  ADD COLUMN evidence_refs        text[]  NOT NULL DEFAULT '{}',
  ADD COLUMN verification_plan    text    NOT NULL DEFAULT '',
  ADD COLUMN estimated_tokens     integer CHECK (estimated_tokens >= 0),
  ADD COLUMN estimated_cost_usd   numeric(10,4) CHECK (estimated_cost_usd >= 0),
  ADD COLUMN approach_approved_by text,
  ADD COLUMN approach_approved_at timestamptz,
  ADD COLUMN verified_by          text,
  ADD COLUMN measured_delta       text,
  -- Set only by the out-of-band check runner from held-out checks; no transport carries it.
  ADD COLUMN gate_passed          boolean,
  ADD COLUMN gate_checked_at      timestamptz,
  -- A plain-English line for a dashboard glance when the title is too technical.
  ADD COLUMN summary_for_human    text CHECK (length(summary_for_human) <= 280);

-- --- tripwires ------------------------------------------------------------------
--
-- A test counts as a tripwire only once a human has confirmed, in one sentence, what it protects.
-- The confirmation is pinned to the test's source: when body_sha256 changes, the confirmation is
-- cleared, so a test cannot keep a meaning it earned in an earlier form (R4, TP-49).
CREATE TABLE tripwires (
  id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id    uuid        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  test_ref      text        NOT NULL,                      -- 'path:TestName' or 'path > describe > it'
  meaning       text        NOT NULL,
  body_sha256   text        NOT NULL,
  confirmed_by  text,
  confirmed_at  timestamptz,
  created_at    timestamptz NOT NULL DEFAULT now(),
  UNIQUE (project_id, test_ref)
);

-- --- success criteria --------------------------------------------------------------
--
-- A child table, not arrays: a convergence verdict must name the one criterion it judged (R1).
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

-- --- checks ---------------------------------------------------------------------------
--
-- Public checks are the gate tier the agent may read; held-out ones stay psql-only, because an
-- agent that can read the test it is scored against is not being measured (R6, TP-51). Existing
-- rows were written as hidden, so they stay hidden.
ALTER TABLE ticket_checks ADD COLUMN visibility text NOT NULL DEFAULT 'held_out'
  CHECK (visibility IN ('public', 'held_out'));

-- --- proposals ---------------------------------------------------------------------------
--
-- The AI never edits Section A; it proposes, as a comment of this kind, and a human applies it.
ALTER TABLE comments DROP CONSTRAINT comments_kind_check;
ALTER TABLE comments ADD CONSTRAINT comments_kind_check
  CHECK (kind IN ('comment', 'system', 'proposal'));
