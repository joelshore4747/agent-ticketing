package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/joelshore/ticketing/api/internal/apperr"
	"github.com/joelshore/ticketing/api/internal/model"
)

// The ownership model (TP-41, consolidated in TP-71). Section A — intent, constraints, non-goals and
// success criteria — is the human's: it is absent from TicketPatch, so no transport that decodes a
// patch can write it, and it is set only by SetTicketFoundation, which only GraphQL exposes. The AI
// proposes changes to it instead (ProposeEdit). Sections B and C travel in TicketPatch.

// CriterionInput is one success criterion as the human writes it.
type CriterionInput struct {
	Tier       model.CriterionTier
	Text       string
	TripwireID *uuid.UUID
}

// Foundation is Section A. Nil leaves a field alone; Criteria, when set, replaces the whole list.
type Foundation struct {
	Intent      *string
	Constraints *string
	NonGoals    *string
	Criteria    *[]CriterionInput
}

// SetTicketFoundation writes Section A under the ticket's version, as one change.
func SetTicketFoundation(ctx context.Context, db DB, id uuid.UUID, version int32, f Foundation,
	actor string) (*model.TicketView, error) {

	actor = ActorOr(actor, "human")
	return inTx(ctx, db, func(tx pgx.Tx) (*model.TicketView, error) {
		current, err := loadTicket(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if current.Version != version {
			return nil, apperr.Conflictf("version conflict on %s: expected %d, found %d", current.Key, version, current.Version)
		}
		type change struct{ field, old, new string }
		var changes []change
		b := &builder{}
		b.push(`UPDATE tickets SET version = version + 1, updated_at = now()`)
		for _, f := range []struct {
			column string
			value  *string
			old    string
		}{{"intent", f.Intent, current.Intent}, {"constraints", f.Constraints, current.Constraints}, {"non_goals", f.NonGoals, current.NonGoals}} {
			if f.value == nil {
				continue
			}
			v := strings.TrimSpace(*f.value)
			if v != f.old {
				b.push(`, ` + f.column + ` = `).bind(v)
				changes = append(changes, change{f.column, f.old, v})
			}
		}
		b.push(` WHERE id = `).bind(id).push(` AND version = `).bind(version).push(` RETURNING ` + model.TicketColumns)
		rows, qerr := tx.Query(ctx, b.sql(), b.args...)
		updated, err := collectOne[model.Ticket](rows, qerr)
		if err != nil {
			return nil, err
		}
		if updated == nil {
			return nil, apperr.Conflictf("version conflict on %s: it changed while this was being written", current.Key)
		}
		for _, c := range changes {
			if err := recordEvent(ctx, tx, id, actor, c.field, &c.old, &c.new); err != nil {
				return nil, err
			}
		}
		if f.Criteria != nil {
			if err := replaceCriteria(ctx, tx, updated, *f.Criteria, actor); err != nil {
				return nil, err
			}
		}
		return hydrateOne(ctx, tx, *updated)
	})
}

func replaceCriteria(ctx context.Context, tx pgx.Tx, ticket *model.Ticket, criteria []CriterionInput, actor string) error {
	before, err := ListCriteria(ctx, tx, ticket.ID)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM ticket_criteria WHERE ticket_id = $1`, ticket.ID); err != nil {
		return apperr.FromDB(err)
	}
	for i, c := range criteria {
		text := strings.TrimSpace(c.Text)
		if text == "" || len([]rune(text)) > 500 {
			return apperr.BadRequestf("criterion %d must be 1 to 500 characters", i+1)
		}
		if c.TripwireID != nil {
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM tripwires WHERE id = $1 AND project_id = $2`,
				*c.TripwireID, ticket.ProjectID).Scan(&n); err != nil {
				return apperr.FromDB(err)
			}
			if n == 0 {
				return apperr.BadRequestf("criterion %d names a tripwire this project does not have", i+1)
			}
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO ticket_criteria (ticket_id, tier, ordinal, text, tripwire_id) VALUES ($1, $2, $3, $4, $5)`,
			ticket.ID, string(c.Tier), i+1, text, c.TripwireID); err != nil {
			return apperr.FromDB(err)
		}
	}
	after, err := ListCriteria(ctx, tx, ticket.ID)
	if err != nil {
		return err
	}
	old, now := criteriaSummary(before), criteriaSummary(after)
	if old == now {
		return nil
	}
	return recordEvent(ctx, tx, ticket.ID, actor, "criteria", &old, &now)
}

func criteriaSummary(criteria []model.TicketCriterion) string {
	parts := make([]string, len(criteria))
	for i, c := range criteria {
		parts[i] = fmt.Sprintf("%d. [%s] %s", c.Ordinal, c.Tier, c.Text)
	}
	return strings.Join(parts, "\n")
}

func ListCriteria(ctx context.Context, db DB, ticketID uuid.UUID) ([]model.TicketCriterion, error) {
	rows, err := db.Query(ctx,
		`SELECT id, ticket_id, tier, ordinal, text, tripwire_id, created_at FROM ticket_criteria `+
			`WHERE ticket_id = $1 ORDER BY ordinal`, ticketID)
	return collect[model.TicketCriterion](rows, err)
}

// ListPublicChecks is the gate tier an agent may read. Held-out checks are filtered here, in SQL,
// so nothing above this function can leak one by forgetting to filter.
func ListPublicChecks(ctx context.Context, db DB, ticketID uuid.UUID) ([]model.TicketCheck, error) {
	rows, err := db.Query(ctx,
		`SELECT id, ticket_id, ordinal, description, kind, command, expect_exit FROM ticket_checks `+
			`WHERE ticket_id = $1 AND visibility = 'public' ORDER BY ordinal`, ticketID)
	return collect[model.TicketCheck](rows, err)
}

// DefaultCheck is one command check the service writes itself, expecting exit 0. TP-181 adds the
// tripwire-drift audit to the list below; nothing else should grow it, because every planned ticket
// pays for every entry.
type DefaultCheck struct {
	Description string
	Command     string
}

// DefaultChecks is the gate a planned ticket is closed through, attached when it is planned rather
// than remembered by whoever plans it (TP-173). Before this, no ticket in a plan had a command check
// at all, so `scripts/run-checks.sh <key>` printed "<key> has no command checks; nothing to gate on"
// and wrote nothing: batch 20c65c62 finished four tickets with gate_passed null, and its digest's
// goal-9 line read "the gate passed on 0 of 6" although the gate had in fact been run by hand, green,
// every time. The fact was true and not machine-readable, which for a measure is the same as false.
//
// They are `public`: the command is in the Justfile, so there is nothing to hold out, and the
// held-out tier means an answer key the agent must not read (checks-seed.sh's header). Public also
// leaves rule 21 where it was — a held-out check makes gate_passed a condition of `done`, and turning
// that on for every planned ticket is a different change from recording the number.
var DefaultChecks = []DefaultCheck{
	{Description: "the standing gate: lint, the tests and the render guard", Command: "just gate"},
}

// attachDefaultChecks gives one ticket the default gate, so run-checks.sh has something to run. A
// ticket that already carries checks is left alone: checks-seed.sh bootstraps held-out answer keys
// rather than syncing them, and a default that displaced one would silently replace the thing an
// eval run is scored on.
func attachDefaultChecks(ctx context.Context, tx pgx.Tx, ticketID uuid.UUID) error {
	var existing bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM ticket_checks WHERE ticket_id = $1)`, ticketID).Scan(&existing); err != nil {
		return apperr.FromDB(err)
	}
	if existing {
		return nil
	}
	for i, c := range DefaultChecks {
		if _, err := tx.Exec(ctx,
			`INSERT INTO ticket_checks (ticket_id, ordinal, description, kind, command, expect_exit, visibility) `+
				`VALUES ($1, $2, $3, 'command', $4, 0, 'public')`,
			ticketID, i+1, c.Description, c.Command); err != nil {
			return apperr.FromDB(err)
		}
	}
	return nil
}

// ProposableFields are the Section A fields an AI may propose a change to. It is a closed list:
// nothing outside it can be proposed, let alone written.
var ProposableFields = []string{"intent", "constraints", "non_goals", "criteria"}

// ProposeEdit records the AI's proposed change to Section A as a proposal comment and an event. It
// never touches the field and never bumps the version: a proposal is narration, and a human who
// agrees applies it with SetTicketFoundation. Silence is refusal; there is no pending state.
func ProposeEdit(ctx context.Context, db DB, ticketID uuid.UUID, field, proposed, rationale, actor string) (*model.Comment, error) {
	actor = ActorOr(actor, "claude-code")
	allowed := false
	for _, f := range ProposableFields {
		allowed = allowed || f == field
	}
	if !allowed {
		return nil, apperr.BadRequestf("%q cannot be proposed; proposals are for %s", field, strings.Join(ProposableFields, ", "))
	}
	proposed, rationale = strings.TrimSpace(proposed), strings.TrimSpace(rationale)
	if proposed == "" || rationale == "" {
		return nil, apperr.BadRequestf("a proposal needs the proposed text and the reason for it")
	}
	return inTx(ctx, db, func(tx pgx.Tx) (*model.Comment, error) {
		ticket, err := loadTicket(ctx, tx, ticketID)
		if err != nil {
			return nil, err
		}
		var current string
		switch field {
		case "intent":
			current = ticket.Intent
		case "constraints":
			current = ticket.Constraints
		case "non_goals":
			current = ticket.NonGoals
		case "criteria":
			criteria, err := ListCriteria(ctx, tx, ticketID)
			if err != nil {
				return nil, err
			}
			current = criteriaSummary(criteria)
		}
		body := fmt.Sprintf("**Proposed change to %s** (a human decides; nothing has changed)\n\n**Now:**\n\n%s\n\n**Proposed:**\n\n%s\n\n**Why:** %s",
			field, quoteOrNone(current), quoteOrNone(proposed), rationale)
		comment, err := insertComment(ctx, tx, ticketID, actor, body, "proposal")
		if err != nil {
			return nil, err
		}
		if err := recordEvent(ctx, tx, ticketID, actor, field+"_proposed", &current, &proposed); err != nil {
			return nil, err
		}
		return comment, nil
	})
}

func quoteOrNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "_(empty)_"
	}
	return "> " + strings.ReplaceAll(s, "\n", "\n> ")
}

// ApproveApproach records the human's approval of Section B. A feature must carry a worked example
// first: the brief asks for one concrete end-to-end demonstration before work starts.
func ApproveApproach(ctx context.Context, db DB, id uuid.UUID, version int32, actor string) (*model.TicketView, error) {
	actor = ActorOr(actor, "human")
	return inTx(ctx, db, func(tx pgx.Tx) (*model.TicketView, error) {
		current, err := loadTicket(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		switch {
		case current.Version != version:
			return nil, apperr.Conflictf("version conflict on %s: expected %d, found %d", current.Key, version, current.Version)
		case strings.TrimSpace(current.Approach) == "":
			return nil, apperr.Conflictf("%s has no approach to approve", current.Key)
		case current.Type == model.TypeFeature && strings.TrimSpace(current.WorkedExample) == "":
			return nil, apperr.Conflictf("%s is a feature; it needs a worked example before its approach is approved", current.Key)
		}
		rows, qerr := tx.Query(ctx,
			`UPDATE tickets SET approach_approved_by = $2, approach_approved_at = now(), version = version + 1, `+
				`updated_at = now() WHERE id = $1 RETURNING `+model.TicketColumns, id, actor)
		updated, err := collectOne[model.Ticket](rows, qerr)
		if err != nil {
			return nil, err
		}
		if err := recordEvent(ctx, tx, id, actor, "approach_approved", current.ApproachApprovedBy, &actor); err != nil {
			return nil, err
		}
		return hydrateOne(ctx, tx, *updated)
	})
}

// --- tripwires --------------------------------------------------------------------

const tripwireColumns = `id, project_id, test_ref, meaning, body_sha256, confirmed_by, confirmed_at, created_at`

// RegisterTripwire records a test and the hash of its body. A test seen with a new body keeps its
// meaning but loses its confirmation: a human confirmed what the old body protected, not this one.
func RegisterTripwire(ctx context.Context, db DB, projectID uuid.UUID, testRef, meaning, bodySHA256 string) (*model.Tripwire, error) {
	testRef, meaning, bodySHA256 = strings.TrimSpace(testRef), strings.TrimSpace(meaning), strings.TrimSpace(bodySHA256)
	if testRef == "" || meaning == "" || bodySHA256 == "" {
		return nil, apperr.BadRequestf("a tripwire needs a test_ref, a meaning and a body_sha256")
	}
	rows, err := db.Query(ctx,
		`INSERT INTO tripwires (project_id, test_ref, meaning, body_sha256) VALUES ($1, $2, $3, $4) `+
			`ON CONFLICT (project_id, test_ref) DO UPDATE SET `+
			`  body_sha256 = EXCLUDED.body_sha256, `+
			`  confirmed_by = CASE WHEN tripwires.body_sha256 = EXCLUDED.body_sha256 THEN tripwires.confirmed_by END, `+
			`  confirmed_at = CASE WHEN tripwires.body_sha256 = EXCLUDED.body_sha256 THEN tripwires.confirmed_at END `+
			`RETURNING `+tripwireColumns, projectID, testRef, meaning, bodySHA256)
	return collectOne[model.Tripwire](rows, err)
}

// ConfirmTripwire is the human's: they read the meaning, may reword it, and confirm it.
func ConfirmTripwire(ctx context.Context, db DB, projectID, id uuid.UUID, meaning *string, actor string) (*model.Tripwire, error) {
	actor = ActorOr(actor, "human")
	rows, err := db.Query(ctx,
		`UPDATE tripwires SET meaning = COALESCE(NULLIF(btrim($3), ''), meaning), confirmed_by = $4, confirmed_at = now() `+
			`WHERE project_id = $1 AND id = $2 RETURNING `+tripwireColumns, projectID, id, meaning, actor)
	tw, err := collectOne[model.Tripwire](rows, err)
	if err != nil {
		return nil, err
	}
	if tw == nil {
		return nil, apperr.NotFoundf("no tripwire %s", id)
	}
	return tw, nil
}

func ListTripwires(ctx context.Context, db DB, projectID uuid.UUID) ([]model.Tripwire, error) {
	rows, err := db.Query(ctx, `SELECT `+tripwireColumns+` FROM tripwires WHERE project_id = $1 ORDER BY test_ref`, projectID)
	return collect[model.Tripwire](rows, err)
}

func itoa32(n int32) string { return strconv.Itoa(int(n)) }

// TripwiresByID loads the tripwires a set of criteria points at, in one query.
func TripwiresByID(ctx context.Context, db DB, ids []uuid.UUID) (map[uuid.UUID]model.Tripwire, error) {
	out := map[uuid.UUID]model.Tripwire{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := db.Query(ctx, `SELECT `+tripwireColumns+` FROM tripwires WHERE id = ANY($1)`, ids)
	tws, err := collect[model.Tripwire](rows, err)
	if err != nil {
		return nil, err
	}
	for _, tw := range tws {
		out[tw.ID] = tw
	}
	return out, nil
}
