package service

import (
	"context"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/joelshore/ticketing/api/internal/apperr"
	"github.com/joelshore/ticketing/api/internal/model"
)

// Project reviews (goal 2, TP-108): the project is reviewed before and after each batch, into a
// memory the next review builds on. The API never reads the repository; the reviewer does, and
// records what it found here with the commit it looked at.

const reviewColumns = `id, project_id, batch_id, phase, commit, body, actor, created_at`

var commitPattern = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// RecordReview files a before or after review of the project for a batch. An after-review needs a
// before-review first: the pair is what says what the batch changed.
func RecordReview(ctx context.Context, db DB, projectID, batchID uuid.UUID, phase, commit, body, actor string) (*model.ProjectReview, error) {
	actor = ActorOr(actor, "claude-code")
	phase, commit, body = strings.TrimSpace(phase), strings.ToLower(strings.TrimSpace(commit)), strings.TrimSpace(body)
	if phase != "before" && phase != "after" {
		return nil, apperr.BadRequestf("phase must be before or after, not %q", phase)
	}
	if !commitPattern.MatchString(commit) {
		return nil, apperr.BadRequestf("commit must be the git sha the review describes, not %q", commit)
	}
	if body == "" {
		return nil, apperr.BadRequestf("a review needs a body: structure, architecture, hotspots, debts, what should change")
	}
	if _, err := GetBatch(ctx, db, projectID, batchID); err != nil {
		return nil, err
	}
	if phase == "after" {
		var before bool
		if err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM project_reviews WHERE batch_id = $1 AND phase = 'before')`, batchID).Scan(&before); err != nil {
			return nil, apperr.FromDB(err)
		}
		if !before {
			return nil, apperr.Conflictf("record the before-review first; an after-review is read against it")
		}
	}
	rows, err := db.Query(ctx,
		`INSERT INTO project_reviews (project_id, batch_id, phase, commit, body, actor) VALUES ($1, $2, $3, $4, $5, $6) `+
			`RETURNING `+reviewColumns, projectID, batchID, phase, commit, body, actor)
	return collectOne[model.ProjectReview](rows, err)
}

// ListReviews returns a batch's reviews, oldest first: before, then after.
func ListReviews(ctx context.Context, db DB, batchID uuid.UUID) ([]model.ProjectReview, error) {
	rows, err := db.Query(ctx, `SELECT `+reviewColumns+` FROM project_reviews WHERE batch_id = $1 ORDER BY created_at, id`, batchID)
	return collect[model.ProjectReview](rows, err)
}

// ProjectMemory is where a review starts: the latest after-review, and what to read beside it.
type ProjectMemory struct {
	Review    *model.ProjectReview `json:"review"`
	BatchName string               `json:"batch_title,omitempty"`
	ReadNext  string               `json:"read_next"`
}

// GetProjectMemory returns the project's latest after-review. With none yet, the first review has to
// read the codebase itself, and it says so.
func GetProjectMemory(ctx context.Context, db DB, projectID uuid.UUID) (*ProjectMemory, error) {
	rows, err := db.Query(ctx,
		`SELECT `+reviewColumns+` FROM project_reviews WHERE project_id = $1 AND phase = 'after' ORDER BY created_at DESC LIMIT 1`, projectID)
	review, err := collectOne[model.ProjectReview](rows, err)
	if err != nil {
		return nil, err
	}
	if review == nil {
		return &ProjectMemory{ReadNext: "No memory yet: this first review reads the codebase itself, and becomes the memory the next one builds on."}, nil
	}
	var title string
	if err := db.QueryRow(ctx, `SELECT title FROM batches WHERE id = $1`, review.BatchID).Scan(&title); err != nil {
		return nil, apperr.FromDB(err)
	}
	return &ProjectMemory{Review: review, BatchName: title,
		ReadNext: "Read this, then only what changed since: git diff --stat " + review.Commit + "..HEAD, and the files it names."}, nil
}
