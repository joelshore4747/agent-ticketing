package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/joelshore/ticketing/api/internal/model"
)

// Rule 15 has two readers: the claim filters candidates in SQL, because it must pick one under
// FOR UPDATE SKIP LOCKED, and a manual move asks orderBlocker, because its refusal has to name what
// is in the way. The two must agree; the tests exercise both against the same fixtures.

// claimInOrder is appended to the claim's WHERE clause, whose table is the unaliased `tickets`.
const claimInOrder = ` AND NOT EXISTS (SELECT 1 FROM tickets p WHERE p.batch_id = tickets.batch_id` +
	` AND p.batch_position < tickets.batch_position AND p.status NOT IN ('done', 'closed'))` +
	` AND NOT EXISTS (SELECT 1 FROM batches b JOIN batches prev` +
	` ON prev.roadmap_id = b.roadmap_id AND prev.sequence < b.sequence WHERE b.id = tickets.batch_id` +
	` AND (prev.finished_at IS NULL OR prev.checkpoint_decision IS NULL OR prev.checkpoint_decision = 'stop'))` +
	` AND NOT EXISTS (SELECT 1 FROM batches rb WHERE rb.id = tickets.batch_id AND rb.reviews_required` +
	` AND NOT EXISTS (SELECT 1 FROM project_reviews pr WHERE pr.batch_id = rb.id AND pr.phase = 'before'))`

// orderBlocker says why t cannot start yet, or "" when it can.
func orderBlocker(ctx context.Context, db DB, t *model.Ticket) (string, error) {
	if t.BatchID == nil {
		return "", nil
	}
	if t.BatchPosition != nil {
		var key string
		var position int32
		var status model.Status
		err := db.QueryRow(ctx,
			`SELECT key, batch_position, status FROM tickets WHERE batch_id = $1 AND batch_position < $2 `+
				`AND status NOT IN ('done', 'closed') ORDER BY batch_position LIMIT 1`,
			*t.BatchID, *t.BatchPosition).Scan(&key, &position, &status)
		if err == nil {
			return fmt.Sprintf("%s (position %d) is still %s; a batch is worked in order", key, position, status), nil
		}
		if !isNoRows(err) {
			return "", err
		}
	}
	return BatchLockReason(ctx, db, *t.BatchID)
}

// BatchLockReason is the roadmap half of rule 15: why no ticket in this batch may start yet, or ""
// when the batches before it are finished and checkpointed to continue.
func BatchLockReason(ctx context.Context, db DB, batchID uuid.UUID) (string, error) {
	var title string
	var sequence int32
	var finished bool
	var decision *string
	err := db.QueryRow(ctx,
		`SELECT prev.title, prev.sequence, prev.finished_at IS NOT NULL, prev.checkpoint_decision `+
			`FROM batches b JOIN batches prev ON prev.roadmap_id = b.roadmap_id AND prev.sequence < b.sequence `+
			`WHERE b.id = $1 AND (prev.finished_at IS NULL OR prev.checkpoint_decision IS NULL `+
			`OR prev.checkpoint_decision = 'stop') ORDER BY prev.sequence LIMIT 1`,
		batchID).Scan(&title, &sequence, &finished, &decision)
	if isNoRows(err) {
		return reviewLockReason(ctx, db, batchID)
	}
	if err != nil {
		return "", err
	}
	switch {
	case !finished:
		return fmt.Sprintf("roadmap batch %d (%q) has not finished", sequence, title), nil
	case decision == nil:
		return fmt.Sprintf("roadmap batch %d (%q) is waiting for its checkpoint", sequence, title), nil
	default:
		return fmt.Sprintf("roadmap batch %d (%q) was checkpointed stop", sequence, title), nil
	}
}

// finishBlockers is rule 16: what stands between a plan batch and finishing. Empty means it may.
func finishBlockers(ctx context.Context, db DB, batchID uuid.UUID) ([]string, error) {
	rows, err := db.Query(ctx,
		`SELECT key, status, type FROM tickets WHERE batch_id = $1 `+
			`ORDER BY batch_position NULLS LAST, number`, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	evalDone := false
	for rows.Next() {
		var key string
		var status model.Status
		var typ model.TicketType
		if err := rows.Scan(&key, &status, &typ); err != nil {
			return nil, err
		}
		if typ == model.TypeEval && status == model.StatusDone {
			evalDone = true
		}
		if !status.IsTerminal() {
			out = append(out, fmt.Sprintf("%s is %s", key, status))
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if !evalDone {
		out = append(out, "no eval ticket is done")
	}
	var needsAfter bool
	if err := db.QueryRow(ctx,
		`SELECT b.reviews_required AND NOT EXISTS (SELECT 1 FROM project_reviews pr WHERE pr.batch_id = b.id AND pr.phase = 'after') `+
			`FROM batches b WHERE b.id = $1`, batchID).Scan(&needsAfter); err != nil {
		return nil, err
	}
	if needsAfter {
		out = append(out, "no after-review: record_review(phase: after) with the project as this batch left it (goal 2)")
	}
	return out, nil
}

// reviewLockReason is goal 2's half of the lock: a batch that requires reviews does not start until
// the project has been reviewed from the last memory.
func reviewLockReason(ctx context.Context, db DB, batchID uuid.UUID) (string, error) {
	var needsBefore bool
	if err := db.QueryRow(ctx,
		`SELECT b.reviews_required AND NOT EXISTS (SELECT 1 FROM project_reviews pr WHERE pr.batch_id = b.id AND pr.phase = 'before') `+
			`FROM batches b WHERE b.id = $1`, batchID).Scan(&needsBefore); err != nil {
		if isNoRows(err) {
			return "", nil
		}
		return "", err
	}
	if needsBefore {
		return "it has no before-review yet: read get_project_memory, then record_review(phase: before) (goal 2)", nil
	}
	return "", nil
}

func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
