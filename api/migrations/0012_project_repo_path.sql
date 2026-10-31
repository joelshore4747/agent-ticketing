-- Where a project's code lives (goal 7; TP-144). Nullable and absolute: the loop and the agents
-- need a second project's checkout path, but the path belongs to the machine, not to the schema, so
-- no row is filled in here. Backfill an existing project with the one-liner in docs/api.md.
ALTER TABLE projects ADD COLUMN repo_path text CHECK (repo_path IS NULL OR repo_path LIKE '/%');
