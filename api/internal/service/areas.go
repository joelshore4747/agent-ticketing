package service

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/joelshore/ticketing/api/internal/apperr"
	"github.com/joelshore/ticketing/api/internal/model"
)

type NewArea struct {
	Name         string
	Description  string
	PathGlobs    []string
	AutoAcceptAI bool
}

// AreaPatch leaves every field optional; only the ones set change.
type AreaPatch struct {
	Name         *string
	Description  *string
	PathGlobs    *[]string
	AutoAcceptAI *bool
	Archived     *bool
}

func ListAreas(ctx context.Context, db DB, projectID uuid.UUID, includeArchived bool) ([]model.Area, error) {
	rows, err := db.Query(ctx,
		`SELECT `+areaColumns+` FROM areas a WHERE a.project_id = $1 `+
			`AND ($2 OR a.archived_at IS NULL) ORDER BY a.name`,
		projectID, includeArchived)
	return collect[model.Area](rows, err)
}

func GetArea(ctx context.Context, db DB, projectID, id uuid.UUID) (*model.Area, error) {
	area, err := findArea(ctx, db, projectID, id)
	if err != nil {
		return nil, err
	}
	if area == nil {
		return nil, apperr.NotFoundf("no area %s", id)
	}
	return area, nil
}

func findArea(ctx context.Context, db DB, projectID, id uuid.UUID) (*model.Area, error) {
	rows, err := db.Query(ctx,
		`SELECT `+areaColumns+` FROM areas a WHERE a.project_id = $1 AND a.id = $2`, projectID, id)
	return collectOne[model.Area](rows, err)
}

// resolveAreaFilter resolves an area used as a *filter*: unlike resolveArea this accepts archived
// areas, but an unknown name is still a 404 rather than "no results".
func resolveAreaFilter(ctx context.Context, db DB, projectID uuid.UUID, selector *AreaSelector) (uuid.UUID, error) {
	if selector.ID != nil {
		return *selector.ID, nil
	}
	var id uuid.UUID
	err := db.QueryRow(ctx, `SELECT id FROM areas WHERE project_id = $1 AND name = $2`,
		projectID, *selector.Name).Scan(&id)
	if err != nil {
		if e := apperr.FromDB(err); apperr.Is(e, apperr.NotFound) {
			return uuid.Nil, apperr.NotFoundf("no area named %q", *selector.Name)
		}
		return uuid.Nil, apperr.FromDB(err)
	}
	return id, nil
}

// resolveArea looks up an area for assignment. Archived areas are rejected.
func resolveArea(ctx context.Context, db DB, projectID uuid.UUID, selector *AreaSelector) (*model.Area, error) {
	var (
		area *model.Area
		err  error
	)
	if selector.ID != nil {
		r, qerr := db.Query(ctx,
			`SELECT `+areaColumns+` FROM areas a WHERE a.project_id = $1 AND a.id = $2`, projectID, *selector.ID)
		area, err = collectOne[model.Area](r, qerr)
	} else {
		r, qerr := db.Query(ctx,
			`SELECT `+areaColumns+` FROM areas a WHERE a.project_id = $1 AND a.name = $2`, projectID, *selector.Name)
		area, err = collectOne[model.Area](r, qerr)
	}
	if err != nil {
		return nil, err
	}
	if area == nil {
		if selector.ID != nil {
			return nil, apperr.NotFoundf("no area %s", *selector.ID)
		}
		return nil, apperr.NotFoundf("no area named %q", *selector.Name)
	}
	if area.ArchivedAt != nil {
		return nil, apperr.BadRequestf("area %q is archived and cannot be assigned", area.Name)
	}
	return area, nil
}

func CreateArea(ctx context.Context, db DB, projectID uuid.UUID, input NewArea) (*model.Area, error) {
	name, err := validateAreaName(input.Name)
	if err != nil {
		return nil, err
	}
	if err := validatePathGlobs(input.PathGlobs); err != nil {
		return nil, err
	}
	globs := input.PathGlobs
	if globs == nil {
		globs = []string{}
	}
	var id uuid.UUID
	err = db.QueryRow(ctx,
		`INSERT INTO areas (project_id, name, description, path_globs, auto_accept_ai) `+
			`VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		projectID, name, input.Description, globs, input.AutoAcceptAI).Scan(&id)
	if err != nil {
		return nil, uniqueAreaName(apperr.FromDB(err), name)
	}
	return GetArea(ctx, db, projectID, id)
}

func UpdateArea(ctx context.Context, db DB, projectID, id uuid.UUID, patch AreaPatch) (*model.Area, error) {
	if patch.PathGlobs != nil {
		if err := validatePathGlobs(*patch.PathGlobs); err != nil {
			return nil, err
		}
	}
	b := &builder{}
	b.push(`UPDATE areas SET updated_at = now()`)
	newName := ""
	if patch.Name != nil {
		name, err := validateAreaName(*patch.Name)
		if err != nil {
			return nil, err
		}
		newName = name
		b.push(`, name = `).bind(name)
	}
	if patch.Description != nil {
		b.push(`, description = `).bind(*patch.Description)
	}
	if patch.PathGlobs != nil {
		b.push(`, path_globs = `).bind(*patch.PathGlobs)
	}
	if patch.AutoAcceptAI != nil {
		b.push(`, auto_accept_ai = `).bind(*patch.AutoAcceptAI)
	}
	if patch.Archived != nil {
		var archivedAt *time.Time
		if *patch.Archived {
			archivedAt = model.Ptr(time.Now().UTC())
		}
		b.push(`, archived_at = `).bind(archivedAt)
	}
	b.push(` WHERE project_id = `).bind(projectID).push(` AND id = `).bind(id).push(` RETURNING id`)

	var updated uuid.UUID
	if err := db.QueryRow(ctx, b.sql(), b.args...).Scan(&updated); err != nil {
		e := apperr.FromDB(err)
		if apperr.Is(e, apperr.NotFound) {
			return nil, apperr.NotFoundf("no area %s", id)
		}
		return nil, uniqueAreaName(e, newName)
	}
	return GetArea(ctx, db, projectID, updated)
}

func SetAreaArchived(ctx context.Context, db DB, projectID, id uuid.UUID, archived bool) (*model.Area, error) {
	return UpdateArea(ctx, db, projectID, id, AreaPatch{Archived: &archived})
}

func uniqueAreaName(err error, name string) error {
	if apperr.Is(err, apperr.Conflict) {
		return apperr.Conflictf("area %q already exists", name)
	}
	return err
}
