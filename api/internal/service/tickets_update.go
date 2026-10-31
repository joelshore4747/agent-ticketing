package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/joelshore/ticketing/api/internal/apperr"
	"github.com/joelshore/ticketing/api/internal/model"
)

// TicketPatch leaves every field optional. Area is a pointer to a pointer because there are three
// cases and not two: leave it alone, set it, or clear it.
type TicketPatch struct {
	Title       *string
	Description *string
	Type        *model.TicketType
	Priority    *model.Priority
	Area        **AreaSelector
	Branch      *string
	PRURL       *string
	Status      *model.Status
	Resolution  *string
	// Tags replaces the ticket's tag set when non-nil; an empty slice clears it.
	Tags *[]string
	// Goals replaces the key goals the ticket serves when non-nil.
	Goals *[]int32
	// Section B, the AI's proposal. Changing any of the first four after approval clears the
	// approval: the human approved the text as it was.
	Approach         *string
	RiskAndRollback  *string
	WorkedExample    *string
	VerificationPlan *string
	EvidenceRefs     *[]string
	EstimatedTokens  *int32
	EstimatedCostUSD *float64
	// Section C, what the AI reports. gate_passed is deliberately absent: only the runner sets it.
	VerifiedBy      *string
	MeasuredDelta   *string
	SummaryForHuman *string
	// HumanDecision marks a patch from the human's transport. Leaving triage needs it (rule 17). No
	// REST or MCP decoder has the field, so neither can set it.
	HumanDecision bool
}

// ClearArea is the value to give TicketPatch.Area to unset a ticket's area.
func ClearArea() **AreaSelector {
	var none *AreaSelector
	return &none
}

// SetArea is the value to give TicketPatch.Area to move a ticket to one.
func SetArea(selector *AreaSelector) **AreaSelector { return &selector }

// --- transitions ------------------------------------------------------------

// The table itself is model.NextStatuses, so every ticket a transport renders carries its own row.

// LegalMoves renders one row of the transition table, for the error that refuses a move. The
// server instructions promise that a refusal "names the legal ones", and an error that says only
// what was refused leaves the caller to guess — which is the one thing a tool error should never
// make it do.
func LegalMoves(from model.Status) string {
	out := make([]string, 0, len(model.NextStatuses(from)))
	for _, to := range model.NextStatuses(from) {
		out = append(out, string(to))
	}
	if len(out) == 0 {
		return "nothing"
	}
	return strings.Join(out, ", ")
}

func TransitionAllowed(from, to model.Status) bool {
	for _, candidate := range model.NextStatuses(from) {
		if candidate == to {
			return true
		}
	}
	return false
}

// UpdateTicket covers rules 4, 5 and 6: patch fields and/or move status, under optimistic locking.
// It takes no project: a ticket id already names one, and the area it may be moved to must come
// from the ticket's own project rather than whichever one the caller happened to resolve.
func UpdateTicket(ctx context.Context, db DB, id uuid.UUID, version int32, patch TicketPatch,
	actor string) (*model.TicketView, error) {

	actor = ActorOr(actor, "claude-code")
	var prURL *string
	if raw := strings.TrimSpace(model.Deref(patch.PRURL)); raw != "" {
		url, err := validatePRURL(raw)
		if err != nil {
			return nil, err
		}
		prURL = &url
	}

	tx, err := db.Begin(ctx)
	if err != nil {
		return nil, apperr.FromDB(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	current, err := loadTicket(ctx, tx, id)
	if err != nil {
		return nil, err
	}

	// Rule 27, judged on the ticket the patch would leave behind, and only when the patch touches
	// the tags or the description. An update that touches neither is not the one that made the
	// ticket unfollowable, and refusing it would trap a bad ticket: closing it, or dropping the tag,
	// has to stay possible.
	if patch.Tags != nil || patch.Description != nil {
		tags := model.Deref(patch.Tags)
		if patch.Tags == nil {
			byTicket, err := ticketTags(ctx, tx, []uuid.UUID{current.ID})
			if err != nil {
				return nil, err
			}
			tags = byTicket[current.ID]
		}
		description := current.Description
		if patch.Description != nil {
			description = *patch.Description
		}
		if err := checkNeedsJoel(tags, description, current.Key); err != nil {
			return nil, err
		}
	}

	// Collect (field, old, new) so events mirror exactly what changed.
	type change struct {
		field    string
		old, new *string
	}
	var events []change
	// Rule 19: the human leaving triage is a decision, filed beside the move in the same transaction.
	var triageDecision *NewDecision

	b := &builder{}
	b.push(`UPDATE tickets SET version = version + 1, updated_at = now()`)

	if patch.Title != nil {
		title, err := validateTitle(*patch.Title)
		if err != nil {
			return nil, err
		}
		if title != current.Title {
			b.push(`, title = `).bind(title)
			events = append(events, change{"title", model.Ptr(current.Title), model.Ptr(title)})
		}
	}
	if patch.Description != nil && *patch.Description != current.Description {
		b.push(`, description = `).bind(*patch.Description)
		events = append(events, change{"description", model.Ptr(current.Description), model.Ptr(*patch.Description)})
	}
	if patch.Type != nil && *patch.Type != current.Type {
		b.push(`, type = `).bind(string(*patch.Type))
		events = append(events, change{"type", model.Ptr(string(current.Type)), model.Ptr(string(*patch.Type))})
	}
	if patch.Priority != nil && *patch.Priority != current.Priority {
		b.push(`, priority = `).bind(string(*patch.Priority))
		events = append(events, change{"priority", model.Ptr(string(current.Priority)), model.Ptr(string(*patch.Priority))})
	}
	approvalStale := false
	for _, f := range []struct {
		column string
		value  *string
		old    string
		gated  bool
	}{
		{"approach", patch.Approach, current.Approach, true},
		{"risk_and_rollback", patch.RiskAndRollback, current.RiskAndRollback, true},
		{"worked_example", patch.WorkedExample, current.WorkedExample, true},
		{"verification_plan", patch.VerificationPlan, current.VerificationPlan, true},
	} {
		if f.value == nil || strings.TrimSpace(*f.value) == f.old {
			continue
		}
		v := strings.TrimSpace(*f.value)
		b.push(`, ` + f.column + ` = `).bind(v)
		events = append(events, change{f.column, model.Ptr(f.old), model.Ptr(v)})
		approvalStale = approvalStale || f.gated
	}
	if approvalStale && current.ApproachApprovedAt != nil {
		b.push(`, approach_approved_by = NULL, approach_approved_at = NULL`)
		events = append(events, change{"approach_approved", current.ApproachApprovedBy, nil})
	}
	if patch.EvidenceRefs != nil {
		refs := make([]string, 0, len(*patch.EvidenceRefs))
		for _, r := range *patch.EvidenceRefs {
			if r = strings.TrimSpace(r); r != "" {
				refs = append(refs, r)
			}
		}
		if strings.Join(refs, "\n") != strings.Join(current.EvidenceRefs, "\n") {
			b.push(`, evidence_refs = `).bind(refs)
			events = append(events, change{"evidence_refs", model.Ptr(strings.Join(current.EvidenceRefs, ", ")), model.Ptr(strings.Join(refs, ", "))})
		}
	}
	if patch.Goals != nil {
		goals, err := normaliseGoals(*patch.Goals)
		if err != nil {
			return nil, err
		}
		if fmt.Sprint(goals) != fmt.Sprint(model.NonNil(current.Goals)) {
			b.push(`, goals = `).bind(goals)
			events = append(events, change{"goals", model.Ptr(fmt.Sprint(model.NonNil(current.Goals))), model.Ptr(fmt.Sprint(goals))})
		}
	}
	if patch.EstimatedTokens != nil && (current.EstimatedTokens == nil || *patch.EstimatedTokens != *current.EstimatedTokens) {
		if *patch.EstimatedTokens < 0 {
			return nil, apperr.BadRequestf("estimated_tokens cannot be negative")
		}
		b.push(`, estimated_tokens = `).bind(*patch.EstimatedTokens)
		var old *string
		if current.EstimatedTokens != nil {
			old = model.Ptr(itoa32(*current.EstimatedTokens))
		}
		events = append(events, change{"estimated_tokens", old, model.Ptr(itoa32(*patch.EstimatedTokens))})
	}
	if patch.EstimatedCostUSD != nil && (current.EstimatedCostUSD == nil || *patch.EstimatedCostUSD != *current.EstimatedCostUSD) {
		if *patch.EstimatedCostUSD < 0 {
			return nil, apperr.BadRequestf("estimated_cost_usd cannot be negative")
		}
		b.push(`, estimated_cost_usd = `).bind(*patch.EstimatedCostUSD)
		events = append(events, change{"estimated_cost_usd", nil, model.Ptr(fmt.Sprintf("%.4f", *patch.EstimatedCostUSD))})
	}
	for _, f := range []struct {
		column string
		value  *string
		old    *string
	}{
		{"verified_by", patch.VerifiedBy, current.VerifiedBy},
		{"measured_delta", patch.MeasuredDelta, current.MeasuredDelta},
		{"summary_for_human", patch.SummaryForHuman, current.SummaryForHuman},
	} {
		if f.value == nil || strings.TrimSpace(*f.value) == model.Deref(f.old) {
			continue
		}
		v := strings.TrimSpace(*f.value)
		if f.column == "summary_for_human" && len([]rune(v)) > 280 {
			return nil, apperr.BadRequestf("summary_for_human is a glance: 280 characters at most")
		}
		b.push(`, ` + f.column + ` = `).bind(v)
		events = append(events, change{f.column, f.old, model.Ptr(v)})
	}
	if patch.Branch != nil && *patch.Branch != model.Deref(current.Branch) {
		b.push(`, branch = `).bind(*patch.Branch)
		events = append(events, change{"branch", current.Branch, model.Ptr(*patch.Branch)})
	}
	if patch.Area != nil {
		var newArea *model.Area
		if *patch.Area != nil {
			newArea, err = resolveArea(ctx, tx, current.ProjectID, *patch.Area)
			if err != nil {
				return nil, err
			}
		}
		var newID *uuid.UUID
		var newName *string
		if newArea != nil {
			newID = &newArea.ID
			newName = &newArea.Name
		}
		if !sameUUID(newID, current.AreaID) {
			b.push(`, area_id = `).bind(newID)
			var oldName *string
			if current.AreaID != nil {
				old, err := findArea(ctx, tx, current.ProjectID, *current.AreaID)
				if err != nil {
					return nil, err
				}
				if old != nil {
					oldName = &old.Name
				}
			}
			events = append(events, change{"area", oldName, newName})
		}
	}

	// Rule 4 handled inline so one PATCH can change fields and status together.
	if patch.Status != nil {
		to := *patch.Status
		switch {
		case to != current.Status:
			if !TransitionAllowed(current.Status, to) {
				return nil, apperr.Conflictf("cannot move %s from %s to %s; from %s the legal moves are %s",
					current.Key, current.Status, to, current.Status, LegalMoves(current.Status))
			}
			// Rule 17: accepting or dismissing a finding is the human's decision, not the agent's.
			if current.Status == model.StatusTriage && !patch.HumanDecision {
				return nil, apperr.Conflictf("%s stays in triage until a human accepts or dismisses it (rule 17); "+
					"say what you need in a comment", current.Key)
			}
			if current.Status == model.StatusTriage {
				kind := model.DecisionAccepted
				if to.IsTerminal() {
					kind = model.DecisionRejected
				}
				triageDecision = &NewDecision{Kind: kind, Rationale: model.Deref(patch.Resolution)}
			}
			// Rule 15: the claim is not the only way into in_progress, so the order is checked here too.
			if to == model.StatusInProgress {
				reason, err := orderBlocker(ctx, tx, current)
				if err != nil {
					return nil, err
				}
				if reason != "" {
					return nil, apperr.Conflictf("cannot start %s: %s", current.Key, reason)
				}
			}
			resolution := strings.TrimSpace(model.Deref(patch.Resolution))
			if to.IsTerminal() && resolution == "" {
				return nil, apperr.BadRequestf("status %s requires a non-empty resolution", to)
			}
			// Rule 21: done means shown, not said.
			if to == model.StatusDone && !patch.HumanDecision {
				missing, err := missingEvidence(ctx, tx, current, patch, approvalStale)
				if err != nil {
					return nil, err
				}
				if len(missing) > 0 {
					return nil, apperr.BadRequestf("%s (%s) cannot be done without its evidence: %s",
						current.Key, current.Type, strings.Join(missing, "; "))
				}
			}
			b.push(`, status = `).bind(string(to))
			events = append(events, change{"status", model.Ptr(string(current.Status)), model.Ptr(string(to))})

			// Reopening clears the completion bookkeeping, and a reopen wins over any resolution
			// sent with it — Postgres rejects two assignments to one column in a single UPDATE, so
			// only one of these branches may write `resolution`.
			reopening := to == model.StatusOpen && current.Status.IsTerminal()
			if resolution != "" && !reopening {
				b.push(`, resolution = `).bind(resolution)
				events = append(events, change{"resolution", current.Resolution, model.Ptr(resolution)})
			}
			switch {
			case to == model.StatusDone:
				b.push(`, completed_at = now()`)
			case reopening:
				b.push(`, completed_at = NULL, resolution = NULL`)
				if current.Resolution != nil {
					events = append(events, change{"resolution", current.Resolution, nil})
				}
			}
			// Any transition clears the claim; only open -> in_progress may set one.
			if current.ClaimedBy != nil || current.ClaimedAt != nil {
				b.push(`, claimed_by = NULL, claimed_at = NULL`)
				events = append(events, change{"claimed", current.ClaimedBy, nil})
			}
		case patch.Resolution != nil:
			return nil, apperr.Conflictf("%s is already %s", current.Key, current.Status)
		}
	}

	b.push(` WHERE id = `).bind(id).push(` AND version = `).bind(version).
		push(` RETURNING ` + model.TicketColumns)

	rows, qerr := tx.Query(ctx, b.sql(), b.args...)
	updated, err := collectOne[model.Ticket](rows, qerr)
	if err != nil {
		return nil, err
	}
	// Rule 5: zero rows means somebody else moved first.
	if updated == nil {
		return nil, apperr.Conflictf("version conflict on %s: expected %d, found %d",
			current.Key, version, current.Version)
	}

	for _, e := range events {
		if err := recordEvent(ctx, tx, id, actor, e.field, e.old, e.new); err != nil {
			return nil, err
		}
	}
	if patch.Tags != nil {
		if err := retagTicket(ctx, tx, current, *patch.Tags, actor); err != nil {
			return nil, err
		}
	}
	if triageDecision != nil {
		if _, err := fileDecision(ctx, tx, updated, MadeByHuman, *triageDecision, actor); err != nil {
			return nil, err
		}
	}

	if prURL != nil {
		if _, err := upsertPullRequest(ctx, tx, id, *prURL, nil, nil, model.PRStateOpen); err != nil {
			return nil, err
		}
		if err := recordEvent(ctx, tx, id, actor, "pr_url", nil, prURL); err != nil {
			return nil, err
		}
	}

	view, err := hydrateOne(ctx, tx, *updated)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, apperr.FromDB(err)
	}
	return view, nil
}

func sameUUID(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// TransitionTicket is rule 4 as its own entry point: GraphQL transitionTicket, acceptTicket and
// dismissTicket, the human's transport, which is why it may leave triage (rule 17).
func TransitionTicket(ctx context.Context, db DB, id uuid.UUID, version int32, status model.Status,
	resolution *string, actor string) (*model.TicketView, error) {

	return UpdateTicket(ctx, db, id, version, TicketPatch{Status: &status, Resolution: resolution, HumanDecision: true}, actor)
}
