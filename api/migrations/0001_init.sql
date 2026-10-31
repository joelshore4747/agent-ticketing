-- Canonical data model for the ticketing system (PostgreSQL 16).
-- api/migrations/ must produce exactly this schema. Enum-like columns are text
-- with CHECK constraints so they bind cleanly from pgx and compare as plain strings.

CREATE TABLE projects (
  id                  uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  key                 text        NOT NULL UNIQUE CHECK (key ~ '^[A-Z][A-Z0-9]{1,9}$'),
  name                text        NOT NULL,
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

-- One row per audit / review / prompt session that reports findings.
CREATE TABLE audit_runs (
  id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id      uuid        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  kind            text        NOT NULL CHECK (kind IN ('audit', 'review', 'prompt')),
  actor           text        NOT NULL DEFAULT 'claude-code',
  git_ref         text,
  summary         text,
  findings_count  integer     NOT NULL DEFAULT 0,
  started_at      timestamptz NOT NULL DEFAULT now(),
  finished_at     timestamptz
);

CREATE TABLE tickets (
  id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id      uuid        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  number          integer     NOT NULL,
  key             text        NOT NULL UNIQUE,                      -- '<PROJECT.key>-<number>', e.g. TP-42
  title           text        NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
  description     text        NOT NULL DEFAULT '',                  -- markdown
  type            text        NOT NULL CHECK (type IN ('bug', 'feature', 'improvement', 'chore')),
  priority        text        NOT NULL DEFAULT 'medium' CHECK (priority IN ('low', 'medium', 'high', 'urgent')),
  status          text        NOT NULL CHECK (status IN ('triage', 'open', 'in_progress', 'done', 'closed')),
  source          text        NOT NULL CHECK (source IN ('ai', 'human')),
  area_id         uuid        REFERENCES areas(id) ON DELETE SET NULL,
  audit_run_id    uuid        REFERENCES audit_runs(id) ON DELETE SET NULL,
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
  search          tsvector    GENERATED ALWAYS AS (
                    setweight(to_tsvector('english', coalesce(title, '')), 'A') ||
                    setweight(to_tsvector('english', coalesce(description, '')), 'B')
                  ) STORED,
  UNIQUE (project_id, number)
);

-- Only one non-closed ticket per fingerprint; the service resolves conflicts by
-- returning the existing ticket and adding a "seen again" comment.
CREATE UNIQUE INDEX tickets_open_fingerprint_uq
  ON tickets (project_id, fingerprint)
  WHERE fingerprint IS NOT NULL AND status NOT IN ('done', 'closed');
CREATE INDEX tickets_search_idx ON tickets USING gin (search);
CREATE INDEX tickets_list_idx   ON tickets (project_id, source, status, created_at DESC);
CREATE INDEX tickets_area_idx   ON tickets (area_id);

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
  kind        text        NOT NULL DEFAULT 'comment' CHECK (kind IN ('comment', 'system')),
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
