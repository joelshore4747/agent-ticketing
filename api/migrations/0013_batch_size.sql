-- How big a batch's change was, for the digest's Measures section (goal 9; TP-160). The figures are
-- reported in by scripts/batch-size.sh, never measured here: the API does not run git and does not
-- open repo_path (rule 22). base_commit is what the counts were taken against, so the human can
-- reproduce them with git diff --stat.
ALTER TABLE batches
  ADD COLUMN lines_added      integer     CHECK (lines_added   IS NULL OR lines_added   >= 0),
  ADD COLUMN lines_deleted    integer     CHECK (lines_deleted IS NULL OR lines_deleted >= 0),
  ADD COLUMN size_base_commit text        CHECK (size_base_commit IS NULL OR size_base_commit ~ '^[0-9a-f]{7,40}$'),
  ADD COLUMN size_reported_at timestamptz,
  ADD CONSTRAINT batches_size_together CHECK ((lines_added IS NULL) = (lines_deleted IS NULL));
