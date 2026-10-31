-- The batch digest (TP-43, consolidated in TP-73; built in TP-87).
--
-- A plain-English account of a finished batch, computed by finish_batch from what the batch's
-- tickets, decisions, proposals and tripwires actually say. Kept apart from `summary`, which a person
-- or agent writes: mixing computed facts and authored narrative in one field makes it impossible to
-- tell which parts are machine output. Single-version: a re-finish recomputes it.
ALTER TABLE batches
  ADD COLUMN digest    text,
  ADD COLUMN digest_at timestamptz;
