package service

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/joelshore/ticketing/api/internal/apperr"
	"github.com/joelshore/ticketing/api/internal/model"
)

// Rule 19: a decision about a ticket is a row, never an edit. The human's accept and dismiss file
// theirs automatically; the agent records its own with the evidence it rests on. A changed mind is a
// new row that supersedes the old one, and the ticket points at whichever is current.

const decisionColumns = `id, ticket_id, kind, made_by, evidence_refs, rationale, revisit_trigger, ` +
	`supersedes, cites_revision_id, actor, created_at`

// MadeBy values. The transport a decision arrives on sets it; a caller never chooses it.
const (
	MadeByHuman = "human"
	MadeByAI    = "ai"
)

// NewDecision is one decision to record. Supersedes defaults to the ticket's current decision, so
// the chain stays linear unless a caller names an older one on purpose.
type NewDecision struct {
	Kind            model.DecisionKind
	EvidenceRefs    []string
	Rationale       string
	RevisitTrigger  *string
	Supersedes      *uuid.UUID
	CitesRevisionID *uuid.UUID
}

// RecordDecision files a decision on a ticket and makes it current. madeBy is the transport's to
// pass (GraphQL MadeByHuman, REST and MCP MadeByAI), never a caller's argument.
func RecordDecision(ctx context.Context, db DB, ticketID uuid.UUID, madeBy string, d NewDecision, actor string) (*model.Decision, error) {
	if madeBy != MadeByHuman && madeBy != MadeByAI {
		return nil, apperr.BadRequestf("made_by must be %s or %s", MadeByHuman, MadeByAI)
	}
	actor = ActorOr(actor, "claude-code")
	return inTx(ctx, db, func(tx pgx.Tx) (*model.Decision, error) {
		current, err := loadTicket(ctx, tx, ticketID)
		if err != nil {
			return nil, err
		}
		return fileDecision(ctx, tx, current, madeBy, d, actor)
	})
}

// fileDecision validates and inserts a decision, repoints the ticket at it and records the event.
// The ticket's version is left alone: a decision is appended beside the ticket, not an edit of it,
// so it must not make an agent's in-flight update fail with a 409.
func fileDecision(ctx context.Context, tx DB, t *model.Ticket, madeBy string, d NewDecision, actor string) (*model.Decision, error) {
	rationale := strings.TrimSpace(d.Rationale)
	var trigger *string
	if s := strings.TrimSpace(model.Deref(d.RevisitTrigger)); s != "" {
		trigger = &s
	}
	if d.Kind == model.DecisionDeferred && trigger == nil {
		return nil, apperr.BadRequestf("a deferral needs a revisit_trigger: say what would bring %s back", t.Key)
	}
	if d.Kind == model.DecisionReframed && rationale == "" {
		return nil, apperr.BadRequestf("a reframing needs a rationale: say what %s is now", t.Key)
	}
	refs := []string{}
	for _, r := range d.EvidenceRefs {
		if r = strings.TrimSpace(r); r != "" {
			refs = append(refs, r)
		}
	}
	supersedes := d.Supersedes
	if supersedes == nil {
		supersedes = t.DecisionID
	} else {
		var owner uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT ticket_id FROM decisions WHERE id = $1`, *supersedes).Scan(&owner); err != nil {
			if isNoRows(err) {
				return nil, apperr.BadRequestf("supersedes names no decision")
			}
			return nil, apperr.FromDB(err)
		}
		if owner != t.ID {
			return nil, apperr.BadRequestf("supersedes names a decision on another ticket")
		}
	}

	rows, err := tx.Query(ctx,
		`INSERT INTO decisions (ticket_id, kind, made_by, evidence_refs, rationale, revisit_trigger, `+
			`supersedes, cites_revision_id, actor) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) `+
			`RETURNING `+decisionColumns,
		t.ID, string(d.Kind), madeBy, refs, rationale, trigger, supersedes, d.CitesRevisionID, actor)
	dec, err := collectOne[model.Decision](rows, err)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE tickets SET decision_id = $2 WHERE id = $1`, t.ID, dec.ID); err != nil {
		return nil, apperr.FromDB(err)
	}
	t.DecisionID = &dec.ID
	if err := recordEvent(ctx, tx, t.ID, actor, "decision", nil, model.Ptr(madeBy+" "+string(dec.Kind))); err != nil {
		return nil, err
	}
	return dec, nil
}

// ListDecisions returns a ticket's decisions, newest first; the first is current unless superseded
// out of order.
func ListDecisions(ctx context.Context, db DB, ticketID uuid.UUID) ([]model.Decision, error) {
	rows, err := db.Query(ctx,
		`SELECT `+decisionColumns+` FROM decisions WHERE ticket_id = $1 ORDER BY created_at DESC, id`, ticketID)
	return collect[model.Decision](rows, err)
}

// ListUncitedDecisions finds the agent's current decisions that rest on no evidence. A deferral is
// exempt: its revisit trigger is what it rests on. Only the current decision counts, so citing
// evidence in a superseding decision clears the flag.
func ListUncitedDecisions(ctx context.Context, db DB, projectID uuid.UUID) ([]DecisionOnTicket, error) {
	return decisionsInForce(ctx, db, scopeProject, projectID, true)
}

// DecisionRequest is a decision as REST and MCP receive it, in strings. There is no made_by field:
// the transport supplies it.
type DecisionRequest struct {
	Kind            string   `json:"kind" jsonschema:"accepted | deferred | rejected | reframed"`
	Rationale       string   `json:"rationale,omitempty" jsonschema:"why; required for reframed"`
	EvidenceRefs    []string `json:"evidence_refs,omitempty" jsonschema:"what it rests on: file:line, ticket keys, eval runs, URLs"`
	RevisitTrigger  *string  `json:"revisit_trigger,omitempty" jsonschema:"required for deferred: what would bring it back"`
	Supersedes      *string  `json:"supersedes,omitempty" jsonschema:"decision id; defaults to the ticket's current decision"`
	CitesRevisionID *string  `json:"cites_revision_id,omitempty" jsonschema:"the guidance revision the decision relied on"`
}

func (r DecisionRequest) Parse() (NewDecision, error) {
	kind, err := model.ParseDecisionKind(r.Kind)
	if err != nil {
		return NewDecision{}, err
	}
	d := NewDecision{Kind: kind, Rationale: r.Rationale, EvidenceRefs: r.EvidenceRefs, RevisitTrigger: r.RevisitTrigger}
	for _, f := range []struct {
		name string
		raw  *string
		out  **uuid.UUID
	}{{"supersedes", r.Supersedes, &d.Supersedes}, {"cites_revision_id", r.CitesRevisionID, &d.CitesRevisionID}} {
		if f.raw == nil || strings.TrimSpace(*f.raw) == "" {
			continue
		}
		id, err := uuid.Parse(strings.TrimSpace(*f.raw))
		if err != nil {
			return NewDecision{}, apperr.BadRequestf("%s is not an id: %q", f.name, *f.raw)
		}
		*f.out = &id
	}
	return d, nil
}
