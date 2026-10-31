package service

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/joelshore/ticketing/api/internal/apperr"
	"github.com/joelshore/ticketing/api/internal/model"
)

// A roadmap is an ordered list of batches following one specification. It carries no rules of its
// own: rule 15 reads batches.roadmap_id and batches.sequence. What it adds is the one view a human
// needs between batches — where the plan stands and what is waiting on them.

const roadmapColumns = `id, project_id, title, spec, created_at, archived_at`

func CreateRoadmap(ctx context.Context, db DB, projectID uuid.UUID, title, spec string) (*model.Roadmap, error) {
	title, err := validateTitle(title)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(ctx,
		`INSERT INTO roadmaps (project_id, title, spec) VALUES ($1, $2, $3) RETURNING `+roadmapColumns,
		projectID, title, strings.TrimSpace(spec))
	return collectOne[model.Roadmap](rows, err)
}

func ListRoadmaps(ctx context.Context, db DB, projectID uuid.UUID) ([]model.Roadmap, error) {
	rows, err := db.Query(ctx,
		`SELECT `+roadmapColumns+` FROM roadmaps WHERE project_id = $1 AND archived_at IS NULL `+
			`ORDER BY created_at`, projectID)
	return collect[model.Roadmap](rows, err)
}

func GetRoadmap(ctx context.Context, db DB, projectID, id uuid.UUID) (*model.Roadmap, error) {
	rows, err := db.Query(ctx,
		`SELECT `+roadmapColumns+` FROM roadmaps WHERE project_id = $1 AND id = $2`, projectID, id)
	roadmap, err := collectOne[model.Roadmap](rows, err)
	if err != nil {
		return nil, err
	}
	if roadmap == nil {
		return nil, apperr.NotFoundf("no roadmap %s", id)
	}
	return roadmap, nil
}

// RoadmapState is where one batch stands, read in sequence order.
type RoadmapState string

const (
	StateLocked             RoadmapState = "locked"              // an earlier batch has not cleared its checkpoint
	StateUnplanned          RoadmapState = "unplanned"           // only its eval ticket so far; tickets come at the previous checkpoint
	StateOpen               RoadmapState = "open"                // work remaining
	StateAtEval             RoadmapState = "at_eval"             // the work is done; the eval ticket is not
	StateAwaitingCheckpoint RoadmapState = "awaiting_checkpoint" // finished; a human has not decided yet
	StateFinished           RoadmapState = "finished"            // checkpointed continue or adjust
	StateStopped            RoadmapState = "stopped"             // checkpointed stop
)

type RoadmapBatch struct {
	Batch model.Batch  `json:"batch"`
	State RoadmapState `json:"state"`
	// Work counts the batch's tickets other than its eval ticket; WorkDone the ones done or closed.
	Work     int `json:"work"`
	WorkDone int `json:"work_done"`
}

func RoadmapBatches(ctx context.Context, db DB, roadmapID uuid.UUID) ([]RoadmapBatch, error) {
	rows, err := db.Query(ctx,
		`SELECT `+batchColumns+` FROM batches WHERE roadmap_id = $1 ORDER BY sequence`, roadmapID)
	batches, err := collect[model.Batch](rows, err)
	if err != nil {
		return nil, err
	}
	type counts struct{ work, workDone, evalDone int }
	tally := map[uuid.UUID]*counts{}
	crows, err := db.Query(ctx,
		`SELECT t.batch_id, t.type = 'eval', t.status IN ('done', 'closed'), t.status = 'done' `+
			`FROM tickets t JOIN batches b ON b.id = t.batch_id WHERE b.roadmap_id = $1`, roadmapID)
	if err != nil {
		return nil, apperr.FromDB(err)
	}
	for crows.Next() {
		var batchID uuid.UUID
		var isEval, settled, done bool
		if err := crows.Scan(&batchID, &isEval, &settled, &done); err != nil {
			crows.Close()
			return nil, apperr.FromDB(err)
		}
		c := tally[batchID]
		if c == nil {
			c = &counts{}
			tally[batchID] = c
		}
		switch {
		case isEval && done:
			c.evalDone++
		case !isEval:
			c.work++
			if settled {
				c.workDone++
			}
		}
	}
	crows.Close()
	if err := crows.Err(); err != nil {
		return nil, apperr.FromDB(err)
	}

	out := make([]RoadmapBatch, 0, len(batches))
	blocked := false
	for _, b := range batches {
		c := tally[b.ID]
		if c == nil {
			c = &counts{}
		}
		entry := RoadmapBatch{Batch: b, Work: c.work, WorkDone: c.workDone}
		decision := model.Deref(b.CheckpointDecision)
		switch {
		case b.FinishedAt != nil && decision == "":
			entry.State = StateAwaitingCheckpoint
		case b.FinishedAt != nil && decision == model.CheckpointStop:
			entry.State = StateStopped
		case b.FinishedAt != nil:
			entry.State = StateFinished
		case blocked:
			entry.State = StateLocked
		case c.work == 0:
			entry.State = StateUnplanned
		case c.workDone == c.work:
			entry.State = StateAtEval
		default:
			entry.State = StateOpen
		}
		if entry.State != StateFinished {
			blocked = true
		}
		out = append(out, entry)
	}
	return out, nil
}
