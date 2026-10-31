package service

import (
	"context"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/joelshore/ticketing/api/internal/apperr"
	"github.com/joelshore/ticketing/api/internal/model"
)

const batchColumns = `id, project_id, kind, actor, title, brief, git_ref, summary, findings_count, ` +
	`started_at, finished_at, roadmap_id, sequence, what, how, checkpoint_decision, checkpoint_note, ` +
	`checkpoint_by, checkpoint_at, digest, digest_at, reviews_required, ` +
	`lines_added, lines_deleted, test_lines_added, test_lines_deleted, size_base_commit, size_reported_at`

// NewBatch is what a batch is opened with. Title and Brief are what an agent picking up one of its
// tickets will read: what this group of work is, and why it is being done.
type NewBatch struct {
	Kind    model.BatchKind
	Title   string
	Brief   string
	Summary *string
	Actor   string
}

func StartBatch(ctx context.Context, db DB, projectID uuid.UUID, input NewBatch) (*model.Batch, error) {
	// A plan batch is born with the eval ticket that gates it, so PlanBatch is its one door. Opening
	// one here made a plan with no gate, and every path that edits a plan carried that case (TP-171).
	// Hence no git_ref on this path either: rule 22 gives a branch only to a plan batch (TP-184).
	if input.Kind == model.BatchPlan {
		return nil, apperr.BadRequestf("a plan batch is opened with plan_batch (POST /api/batches/plan), " +
			"which files its tickets in order and appends the eval ticket that gates the batch")
	}
	rows, err := db.Query(ctx,
		`INSERT INTO batches (project_id, kind, actor, title, brief, summary) `+
			`VALUES ($1, $2, $3, $4, $5, $6) RETURNING `+batchColumns,
		projectID, string(input.Kind), ActorOr(input.Actor, "claude-code"),
		input.Title, input.Brief, input.Summary)
	return collectOne[model.Batch](rows, err)
}

// FinishBatch closes a batch, recording the summary — which is the human-readable account of what
// the batch did, written once the work is known rather than guessed at the start — and computing its
// digest. Finishing again recomputes the digest but keeps the original finish time.
func FinishBatch(ctx context.Context, db DB, id uuid.UUID, summary *string) (*model.Batch, error) {
	return inTx(ctx, db, func(tx pgx.Tx) (*model.Batch, error) {
		var kind model.BatchKind
		var reviewed bool
		var existing *string
		err := tx.QueryRow(ctx, `SELECT kind, reviews_required, summary FROM batches WHERE id = $1 FOR UPDATE`, id).Scan(&kind, &reviewed, &existing)
		if isNoRows(err) {
			return nil, apperr.NotFoundf("no batch %s", id)
		}
		if err != nil {
			return nil, err
		}
		// Rule 16: a plan is finished when its work and its eval are, not when someone says so.
		if kind == model.BatchPlan {
			reasons, err := finishBlockers(ctx, tx, id)
			if err != nil {
				return nil, err
			}
			// Goal 6: a reviewed batch finishes with the Narrator's report, not just a computed digest.
			if reviewed {
				report := existing
				if summary != nil {
					report = summary
				}
				if missing := missingReportSections(model.Deref(report)); len(missing) > 0 {
					reasons = append(reasons, "the report (summary) lacks: "+strings.Join(missing, ", "))
				}
			}
			if len(reasons) > 0 {
				return nil, apperr.Conflictf("cannot finish this batch yet: %s", strings.Join(reasons, "; "))
			}
		}
		rows, err := tx.Query(ctx,
			`UPDATE batches SET finished_at = COALESCE(finished_at, now()), summary = COALESCE($2, summary) WHERE id = $1 `+
				`RETURNING `+batchColumns, id, summary)
		batch, err := collectOne[model.Batch](rows, err)
		if err != nil {
			return nil, err
		}
		digest, err := ComposeDigest(ctx, tx, batch)
		if err != nil {
			return nil, err
		}
		rows, err = tx.Query(ctx,
			`UPDATE batches SET digest = $2, digest_at = now() WHERE id = $1 RETURNING `+batchColumns, id, digest)
		return collectOne[model.Batch](rows, err)
	})
}

// --- the read path ----------------------------------------------------------
//
// None of this existed before: batches could be created, incremented and finished, but never read
// back by any transport. A batch nothing can see cannot carry guidance to anyone.

func ListBatches(ctx context.Context, db DB, projectID uuid.UUID) ([]model.Batch, error) {
	rows, err := db.Query(ctx,
		`SELECT `+batchColumns+` FROM batches WHERE project_id = $1 ORDER BY started_at DESC`, projectID)
	return collect[model.Batch](rows, err)
}

func GetBatch(ctx context.Context, db DB, projectID, id uuid.UUID) (*model.Batch, error) {
	rows, err := db.Query(ctx,
		`SELECT `+batchColumns+` FROM batches WHERE project_id = $1 AND id = $2`, projectID, id)
	batch, err := collectOne[model.Batch](rows, err)
	if err != nil {
		return nil, err
	}
	if batch == nil {
		return nil, apperr.NotFoundf("no batch %s", id)
	}
	return batch, nil
}

func ListBatchTickets(ctx context.Context, db DB, batchID uuid.UUID) ([]model.TicketView, error) {
	rows, err := db.Query(ctx,
		`SELECT `+model.TicketColumns+` FROM tickets WHERE batch_id = $1 `+
			`ORDER BY batch_position NULLS LAST, `+priorityRank+` DESC, created_at DESC`, batchID)
	tickets, err := collect[model.Ticket](rows, err)
	if err != nil {
		return nil, err
	}
	return hydrateMany(ctx, db, tickets)
}

// bumpFindingsCount counts findings REPORTED, not tickets created: the dedupe path bumps it too, so
// a finding reported five times raises it five times while creating one ticket. That is deliberate —
// it measures how noisy a run was — but it means findings_count and the ticket count disagree.
func bumpFindingsCount(ctx context.Context, db DB, id uuid.UUID) error {
	_, err := db.Exec(ctx, `UPDATE batches SET findings_count = findings_count + 1 WHERE id = $1`, id)
	return apperr.FromDB(err)
}

// FindingOutcome is one reported finding, after routing and dedupe.
type FindingOutcome struct {
	Key     string
	Created bool
	Status  model.Status
}

// ReportFindings files a whole batch of findings atomically. It used to run each CreateTicket in its
// own transaction, so a failure partway through left the earlier findings committed and the batch's
// count already raised for them — a half-reported audit that looked complete.
func ReportFindings(ctx context.Context, db DB, project *model.Project,
	findings []NewTicket) ([]FindingOutcome, error) {

	return inTx(ctx, db, func(tx pgx.Tx) ([]FindingOutcome, error) {
		out := make([]FindingOutcome, 0, len(findings))
		for _, finding := range findings {
			finding.Source = model.SourceAI
			outcome, err := CreateTicket(ctx, tx, project, finding)
			if err != nil {
				return nil, err
			}
			out = append(out, FindingOutcome{
				Key:     outcome.View.Ticket.Key,
				Created: outcome.Created,
				Status:  outcome.View.Ticket.Status,
			})
		}
		return out, nil
	})
}

// ReportSections are the Narrator's report, in order (goal 6). Each must appear as a markdown
// heading of any level; the API checks the report has them, not what it says.
var ReportSections = []string{"What was added", "Why", "What it achieved", "Where in the code", "How it moves the goals"}

func missingReportSections(report string) []string {
	var missing []string
	for _, section := range ReportSections {
		heading := regexp.MustCompile(`(?mi)^#{1,6}\s+` + regexp.QuoteMeta(section) + `\s*$`)
		if !heading.MatchString(report) {
			missing = append(missing, section)
		}
	}
	return missing
}
