package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/joelshore/ticketing/api/internal/apperr"
	"github.com/joelshore/ticketing/api/internal/model"
)

// Guidance is split three ways, because the three things it does move for different reasons: this
// file writes documents and their revisions (and seeds a new project's copy), guidance_bundle.go
// decides what a claim is served, guidance_drift.go records one that no longer matches the code.

const guidanceColumns = `id, project_id, slug, title, kind, current_revision_id, inline, weight, ` +
	`source_paths, source_commit, reviewed_at, reviewed_by, review_interval_days, stale, ` +
	`stale_reason, drift_reported_at, archived_at, version, created_at, updated_at`

const revisionColumns = `id, document_id, revision, body, content_sha256, author, created_at`

type NewGuidance struct {
	Slug        string
	Title       string
	Kind        model.GuidanceKind
	Body        string
	Inline      *bool
	Weight      *int32
	SourcePaths []string
	Author      string
}

// resolved is one document, the revision that will actually be served, and the place its batch
// attached it at (zero when none did), which sortBundle breaks ties on.
type resolved struct {
	Doc      model.GuidanceDoc
	Revision model.GuidanceRevision
	Position int32
}

func ListGuidance(ctx context.Context, db DB, projectID uuid.UUID) ([]model.GuidanceDoc, error) {
	rows, err := db.Query(ctx,
		`SELECT `+guidanceColumns+` FROM guidance_documents WHERE project_id = $1 `+
			`AND archived_at IS NULL ORDER BY kind, weight, slug`, projectID)
	return collect[model.GuidanceDoc](rows, err)
}

func GetGuidance(ctx context.Context, db DB, projectID uuid.UUID, slug string) (*model.GuidanceDoc, *model.GuidanceRevision, error) {
	rows, err := db.Query(ctx,
		`SELECT `+guidanceColumns+` FROM guidance_documents WHERE project_id = $1 AND slug = $2`,
		projectID, slug)
	doc, err := collectOne[model.GuidanceDoc](rows, err)
	if err != nil {
		return nil, nil, err
	}
	if doc == nil {
		return nil, nil, apperr.NotFoundf("no guidance document %q", slug)
	}
	if doc.CurrentRevisionID == nil {
		return doc, nil, nil
	}
	rows, err = db.Query(ctx,
		`SELECT `+revisionColumns+` FROM guidance_revisions WHERE id = $1`, *doc.CurrentRevisionID)
	revision, err := collectOne[model.GuidanceRevision](rows, err)
	return doc, revision, err
}

// revisionsByID fetches a set of revisions in one round trip. A join against the documents would
// collide with them on id, created_at and author, so the bodies are fetched apart and matched here.
func revisionsByID(ctx context.Context, db DB, ids []uuid.UUID) (map[uuid.UUID]model.GuidanceRevision, error) {
	rows, err := db.Query(ctx,
		`SELECT `+revisionColumns+` FROM guidance_revisions WHERE id = ANY($1)`, ids)
	revisions, err := collect[model.GuidanceRevision](rows, err)
	if err != nil {
		return nil, err
	}
	byID := make(map[uuid.UUID]model.GuidanceRevision, len(revisions))
	for _, rev := range revisions {
		byID[rev.ID] = rev
	}
	return byID, nil
}

// UpsertGuidance writes a document and appends a revision. Revisions are immutable and append-only,
// so there is no update path here by design: a claim pins a revision by foreign key, and a mutable
// body would silently rewrite what an agent was recorded as having read. Saving a body identical to
// the current one is a no-op rather than a new revision — that is what the content hash is for.
func UpsertGuidance(ctx context.Context, db DB, projectID uuid.UUID, input NewGuidance) (*model.GuidanceDoc, error) {
	body := strings.TrimSpace(input.Body)
	if body == "" {
		return nil, apperr.BadRequestf("guidance body must not be empty")
	}
	title := strings.TrimSpace(input.Title)
	if title == "" {
		return nil, apperr.BadRequestf("guidance title must not be empty")
	}
	sum := sha256.Sum256([]byte(body))
	hash := hex.EncodeToString(sum[:])
	author := ActorOr(input.Author, "claude-code")
	inline := input.Inline == nil || *input.Inline
	weight := model.Deref(input.Weight)
	// A nil slice binds as NULL and source_paths is NOT NULL, so always send a real one.
	paths := append([]string{}, input.SourcePaths...)

	return inTx(ctx, db, func(tx pgx.Tx) (*model.GuidanceDoc, error) {
		rows, err := tx.Query(ctx,
			`INSERT INTO guidance_documents (project_id, slug, title, kind, inline, weight, source_paths) `+
				`VALUES ($1, $2, $3, $4, $5, $6, $7) `+
				`ON CONFLICT (project_id, slug) DO UPDATE SET `+
				`  title = EXCLUDED.title, kind = EXCLUDED.kind, inline = EXCLUDED.inline, `+
				`  weight = EXCLUDED.weight, source_paths = EXCLUDED.source_paths, `+
				`  version = guidance_documents.version + 1, updated_at = now() `+
				`RETURNING `+guidanceColumns,
			projectID, input.Slug, title, string(input.Kind), inline, weight, paths)
		doc, err := collectOne[model.GuidanceDoc](rows, err)
		if err != nil {
			return nil, err
		}

		// Unchanged text keeps its revision, so re-saving a document does not invent history.
		if doc.CurrentRevisionID != nil {
			var current string
			if err := tx.QueryRow(ctx,
				`SELECT content_sha256 FROM guidance_revisions WHERE id = $1`,
				*doc.CurrentRevisionID).Scan(&current); err != nil {
				return nil, apperr.FromDB(err)
			}
			if current == hash {
				return doc, nil
			}
		}

		var next int32 = 1
		if err := tx.QueryRow(ctx,
			`SELECT coalesce(max(revision), 0) + 1 FROM guidance_revisions WHERE document_id = $1`,
			doc.ID).Scan(&next); err != nil {
			return nil, apperr.FromDB(err)
		}

		var revisionID uuid.UUID
		if err := tx.QueryRow(ctx,
			`INSERT INTO guidance_revisions (document_id, revision, body, content_sha256, author) `+
				`VALUES ($1, $2, $3, $4, $5) RETURNING id`,
			doc.ID, next, body, hash, author).Scan(&revisionID); err != nil {
			return nil, apperr.FromDB(err)
		}

		rows, err = tx.Query(ctx,
			`UPDATE guidance_documents SET current_revision_id = $2, updated_at = now() `+
				`WHERE id = $1 RETURNING `+guidanceColumns, doc.ID, revisionID)
		return collectOne[model.GuidanceDoc](rows, err)
	})
}

// ListGuidanceCurrent is ListGuidance plus each document's current revision, which is where the
// body lives. A document with no revision yet is still listed with a zero one: it exists, it is
// just empty, and hiding it would make the corpus page disagree with the API about what is there.
func ListGuidanceCurrent(ctx context.Context, db DB, projectID uuid.UUID) ([]resolved, error) {
	docs, err := ListGuidance(ctx, db, projectID)
	if err != nil || len(docs) == 0 {
		return nil, err
	}
	ids := make([]uuid.UUID, 0, len(docs))
	for _, doc := range docs {
		if doc.CurrentRevisionID != nil {
			ids = append(ids, *doc.CurrentRevisionID)
		}
	}
	byID, err := revisionsByID(ctx, db, ids)
	if err != nil {
		return nil, err
	}
	out := make([]resolved, 0, len(docs))
	for _, doc := range docs {
		var revision model.GuidanceRevision
		if doc.CurrentRevisionID != nil {
			revision = byID[*doc.CurrentRevisionID]
		}
		out = append(out, resolved{Doc: doc, Revision: revision})
	}
	return out, nil
}

// seedGuidanceSlugs is the corpus a new project starts with: how work is decided and run here,
// which is the same wherever the code is, and not how any one language is written — go-service-layer
// describes this repository's files and would be a lie in another one. key-goals in particular is
// the list plan_batch measures a ticket against (rule 24): a project without one refuses every batch.
var seedGuidanceSlugs = []string{"key-goals", "planning-a-batch", "working-a-ticket", "the-agents"}

// seedGuidance copies seedGuidanceSlugs into a new project as each document's first revision, so
// nothing links and editing one project's goals leaves everyone else's alone. Called by
// CreateProject inside its transaction (a nested Begin on a pgx.Tx is a SAVEPOINT, so
// UpsertGuidance's own inTx joins it rather than committing early). Whatever the source does not
// have is skipped and is not an error: the corpus is loaded by `just guidance` against a running
// API, not by a migration, so a database where nobody has run it must still create projects.
//
// source_paths are deliberately left behind. They name files in the repository the document
// describes — `api/internal/service/plans.go`, `.claude/agents` — and the same strings resolved
// against a different checkout point at nothing, which guidance-check would report as MISSING.
func seedGuidance(ctx context.Context, tx DB, sourceProjectID, projectID uuid.UUID) error {
	sources, err := ListGuidanceCurrent(ctx, tx, sourceProjectID)
	if err != nil {
		return err
	}
	for _, src := range sources {
		if !slices.Contains(seedGuidanceSlugs, src.Doc.Slug) || src.Revision.Body == "" {
			continue
		}
		if _, err := UpsertGuidance(ctx, tx, projectID, NewGuidance{
			Slug:   src.Doc.Slug,
			Title:  src.Doc.Title,
			Kind:   src.Doc.Kind,
			Body:   src.Revision.Body,
			Inline: &src.Doc.Inline,
			Weight: &src.Doc.Weight,
			Author: "create-project",
		}); err != nil {
			return err
		}
	}
	return nil
}
