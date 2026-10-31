package service

import (
	"context"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/joelshore/ticketing/api/internal/apperr"
	"github.com/joelshore/ticketing/api/internal/model"
)

const projectColumns = `id, key, name, repo_path, next_ticket_number, created_at`

func ListProjects(ctx context.Context, db DB) ([]model.Project, error) {
	rows, err := db.Query(ctx, `SELECT `+projectColumns+` FROM projects ORDER BY created_at, key`)
	return collect[model.Project](rows, err)
}

// ResolveProject finds the project by key, or falls back to the oldest one.
func ResolveProject(ctx context.Context, db DB, key *string) (*model.Project, error) {
	var (
		found *model.Project
		err   error
	)
	if key != nil {
		found, err = projectByKey(ctx, db, *key)
	} else {
		rows, qerr := db.Query(ctx,
			`SELECT `+projectColumns+` FROM projects ORDER BY created_at, key LIMIT 1`)
		found, err = collectOne[model.Project](rows, qerr)
	}
	if err != nil {
		return nil, err
	}
	if found == nil {
		name := "(default)"
		if key != nil {
			name = *key
		}
		return nil, apperr.NotFoundf("no project %s", name)
	}
	return found, nil
}

// ResolveProjectForTicket uses the fact that a ticket key already names its project — TP-42 belongs
// to TP — so an endpoint addressing one ticket needs no project parameter. Falls back to the default
// when the argument is a uuid rather than a key, or names a project that is not there: a uuid's
// first segment can never be a valid project key, which must start with a letter.
func ResolveProjectForTicket(ctx context.Context, db DB, keyOrID string) (*model.Project, error) {
	if prefix, _, ok := strings.Cut(keyOrID, "-"); ok {
		project, err := projectByKey(ctx, db, strings.ToUpper(prefix))
		if err != nil {
			return nil, err
		}
		if project != nil {
			return project, nil
		}
	}
	return ResolveProject(ctx, db, nil)
}

func projectByKey(ctx context.Context, db DB, key string) (*model.Project, error) {
	rows, err := db.Query(ctx, `SELECT `+projectColumns+` FROM projects WHERE key = $1`, key)
	return collectOne[model.Project](rows, err)
}

// CreateProject starts a project with no areas and no tickets, its own ticket numbering, the
// starter tags and a copy of the core guidance. Those two come with it because both are managed
// lists the API itself enforces against: without the tags plan_batch refuses every tag its tickets
// name, and without key-goals it refuses every ticket outright (rule 24). The key is what every
// ticket in it is named after, so it is fixed at creation and never edited. repoPath is optional: a
// project can be tracked here before anyone says where its code is.
func CreateProject(ctx context.Context, db DB, key, name string, repoPath *string) (*model.Project, error) {
	key, err := validateProjectKey(key)
	if err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 80 {
		return nil, apperr.BadRequestf("project name must be between 1 and 80 characters")
	}
	repo, err := validateRepoPath(repoPath)
	if err != nil {
		return nil, err
	}
	return inTx(ctx, db, func(tx pgx.Tx) (*model.Project, error) {
		rows, qerr := tx.Query(ctx,
			`INSERT INTO projects (key, name, repo_path) VALUES ($1, $2, $3) RETURNING `+projectColumns,
			key, name, repo)
		project, err := collectOne[model.Project](rows, qerr)
		if err != nil {
			if apperr.Is(err, apperr.Conflict) {
				return nil, apperr.Conflictf("project %q already exists", key)
			}
			return nil, err
		}
		if err := seedStarterTags(ctx, tx, project.ID); err != nil {
			return nil, err
		}
		// The guidance is copied from the oldest other project, which is the one this system was
		// built in and the one every unqualified request already resolves to. "Other" is doing real
		// work: the row above is already visible to this transaction, so on an empty database the
		// first project would otherwise be told to copy from itself.
		var sourceID uuid.UUID
		scanErr := tx.QueryRow(ctx,
			`SELECT id FROM projects WHERE id <> $1 ORDER BY created_at, key LIMIT 1`,
			project.ID).Scan(&sourceID)
		if isNoRows(scanErr) {
			return project, nil
		}
		if scanErr != nil {
			return nil, apperr.FromDB(scanErr)
		}
		if err := seedGuidance(ctx, tx, sourceID, project.ID); err != nil {
			return nil, err
		}
		return project, nil
	})
}

// validateRepoPath keeps repo_path a path something can actually cd into. Relative is refused rather
// than resolved, because the API's working directory is the server's, not the caller's, and a path
// that means one checkout to the agent and another to the API is worse than no path at all. Blank
// means "not known", which is the column's null.
func validateRepoPath(raw *string) (*string, error) {
	if raw == nil {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*raw)
	if trimmed == "" {
		return nil, nil
	}
	if !filepath.IsAbs(trimmed) {
		return nil, apperr.BadRequestf("repo path must be absolute, got %q", trimmed)
	}
	clean := filepath.Clean(trimmed)
	return &clean, nil
}
