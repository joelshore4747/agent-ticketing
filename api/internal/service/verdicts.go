package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/joelshore/ticketing/api/internal/apperr"
	"github.com/joelshore/ticketing/api/internal/model"
)

// Convergence verdicts (TP-44/TP-73, built in TP-94): once a ticket is finished, each of its success
// criteria is judged — met, missed, or deliberately met differently. A miss is not a note; it is work,
// so a fail files a follow-up ticket, and in an unfinished plan it joins the batch before its eval.

const verdictColumns = `id, criterion_id, verdict, reason, evidence, judged_by, created_at`

type NewVerdict struct {
	Verdict  model.Verdict
	Reason   string
	Evidence []string
}

// VerdictResult is the verdict and, for a fail, the follow-up it filed. JoinedBatch says whether the
// follow-up went into the ticket's batch; when the batch is finished or its eval has started, it is
// left for the human to place.
type VerdictResult struct {
	Verdict     model.CriterionVerdict `json:"verdict"`
	FollowUp    *model.TicketJSON      `json:"follow_up,omitempty"`
	JoinedBatch bool                   `json:"joined_batch"`
}

// RecordVerdict judges criterion `ordinal` of a finished ticket.
func RecordVerdict(ctx context.Context, db DB, project *model.Project, key string, ordinal int32, in NewVerdict, actor string) (*VerdictResult, error) {
	actor = ActorOr(actor, "claude-code")
	reason := strings.TrimSpace(in.Reason)
	if in.Verdict != model.VerdictPass && reason == "" {
		return nil, apperr.BadRequestf("a %s verdict needs a reason: say what was missed or done differently", in.Verdict)
	}
	evidence := []string{}
	for _, e := range in.Evidence {
		if e = strings.TrimSpace(e); e != "" {
			evidence = append(evidence, e)
		}
	}
	return inTx(ctx, db, func(tx pgx.Tx) (*VerdictResult, error) {
		view, err := RequireTicket(ctx, tx, project.ID, key)
		if err != nil {
			return nil, err
		}
		t := view.Ticket
		if !t.Status.IsTerminal() {
			return nil, apperr.Conflictf("%s is %s; its criteria are judged once it is finished", t.Key, t.Status)
		}
		var criterion model.TicketCriterion
		rows, err := tx.Query(ctx,
			`SELECT id, ticket_id, tier, ordinal, text, tripwire_id, created_at FROM ticket_criteria `+
				`WHERE ticket_id = $1 AND ordinal = $2`, t.ID, ordinal)
		found, err := collectOne[model.TicketCriterion](rows, err)
		if err != nil {
			return nil, err
		}
		if found == nil {
			return nil, apperr.NotFoundf("%s has no criterion %d", t.Key, ordinal)
		}
		criterion = *found

		rows, err = tx.Query(ctx,
			`INSERT INTO criterion_verdicts (criterion_id, verdict, reason, evidence, judged_by) `+
				`VALUES ($1, $2, $3, $4, $5) RETURNING `+verdictColumns,
			criterion.ID, string(in.Verdict), reason, evidence, actor)
		verdict, err := collectOne[model.CriterionVerdict](rows, err)
		if err != nil {
			return nil, err
		}
		if err := recordEvent(ctx, tx, t.ID, actor, "verdict", nil,
			model.Ptr(fmt.Sprintf("criterion %d: %s", ordinal, in.Verdict))); err != nil {
			return nil, err
		}
		out := &VerdictResult{Verdict: *verdict}
		if in.Verdict != model.VerdictFail {
			return out, nil
		}

		// The same criterion failing twice is one piece of work, so the fingerprint dedupes it.
		follow := NewTicket{
			Title:       oneLine(fmt.Sprintf("%s missed: %s", t.Key, criterion.Text)),
			Description: fmt.Sprintf("Criterion %d of %s (%s) was judged a fail by %s.\n\n**Reason**: %s", ordinal, t.Key, criterion.Tier, actor, reason),
			Type:        model.TypeBug,
			Priority:    model.PriorityHigh,
			Source:      model.SourceAI,
			Fingerprint: model.Ptr("verdict:" + criterion.ID.String()),
			Actor:       actor,
		}
		if t.AreaID != nil {
			follow.Area = AreaByID(*t.AreaID)
		}
		created, err := CreateTicket(ctx, tx, project, follow)
		if err != nil {
			return nil, err
		}
		json := created.View.ToJSON()
		out.FollowUp = &json
		if created.Created && t.BatchID != nil {
			out.JoinedBatch, err = joinBeforeEval(ctx, tx, project.ID, *t.BatchID, created.View.Ticket.Key, actor)
			if err != nil {
				return nil, err
			}
		}
		return out, nil
	})
}

// joinBeforeEval puts a follow-up into an unfinished plan just before its eval ticket. A batch that
// is finished, not a plan, or already past that point refuses, and the follow-up stays unbatched.
func joinBeforeEval(ctx context.Context, tx pgx.Tx, projectID, batchID uuid.UUID, key, actor string) (bool, error) {
	if _, err := AddToBatch(ctx, tx, projectID, batchID, key, nil, actor); err != nil {
		if apperr.Is(err, apperr.Conflict) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// ListVerdicts returns every verdict on a ticket's criteria, newest first.
func ListVerdicts(ctx context.Context, db DB, ticketID uuid.UUID) ([]model.CriterionVerdict, error) {
	rows, err := db.Query(ctx,
		`SELECT v.id, v.criterion_id, v.verdict, v.reason, v.evidence, v.judged_by, v.created_at `+
			`FROM criterion_verdicts v JOIN ticket_criteria c ON c.id = v.criterion_id `+
			`WHERE c.ticket_id = $1 ORDER BY v.created_at DESC, v.id`, ticketID)
	return collect[model.CriterionVerdict](rows, err)
}

// CriterionVerdicts are one criterion's verdicts, newest first.
func CriterionVerdicts(ctx context.Context, db DB, criterionID uuid.UUID) ([]model.CriterionVerdict, error) {
	rows, err := db.Query(ctx,
		`SELECT `+verdictColumns+` FROM criterion_verdicts WHERE criterion_id = $1 ORDER BY created_at DESC, id`, criterionID)
	return collect[model.CriterionVerdict](rows, err)
}

// VerdictRequest is a verdict as REST and MCP receive it.
type VerdictRequest struct {
	Ordinal  int32    `json:"ordinal" jsonschema:"the criterion's number on the ticket, from 1"`
	Verdict  string   `json:"verdict" jsonschema:"pass | fail | deviation"`
	Reason   string   `json:"reason,omitempty" jsonschema:"required for fail and deviation: what was missed or done differently"`
	Evidence []string `json:"evidence,omitempty" jsonschema:"what the judgement rests on: commands, output, file:line"`
}

func (r VerdictRequest) Parse() (NewVerdict, error) {
	v, err := model.ParseVerdict(r.Verdict)
	if err != nil {
		return NewVerdict{}, err
	}
	return NewVerdict{Verdict: v, Reason: r.Reason, Evidence: r.Evidence}, nil
}

// latestVerdict is the verdict that counts for one criterion, with the ticket and criterion it is on.
type latestVerdict struct {
	Key     string        `db:"key"`
	Ordinal int32         `db:"ordinal"`
	Text    string        `db:"text"`
	Verdict model.Verdict `db:"verdict"`
	Reason  string        `db:"reason"`
}

// batchVerdicts are the newest verdict per criterion across a batch's tickets.
func batchVerdicts(ctx context.Context, db DB, batchID uuid.UUID) ([]latestVerdict, error) {
	rows, err := db.Query(ctx,
		`SELECT DISTINCT ON (c.id) t.key, c.ordinal, c.text, v.verdict, v.reason `+
			`FROM criterion_verdicts v JOIN ticket_criteria c ON c.id = v.criterion_id JOIN tickets t ON t.id = c.ticket_id `+
			`WHERE t.batch_id = $1 ORDER BY c.id, v.created_at DESC`, batchID)
	return collect[latestVerdict](rows, err)
}
