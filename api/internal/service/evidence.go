package service

import (
	"context"
	"strings"

	"github.com/joelshore/ticketing/api/internal/model"
)

// Rule 21: a ticket the agent calls done shows the evidence its type calls for (TP-44, built in
// TP-93). The human's own move to done is their judgement and is not asked for evidence; closing as
// `closed` (won't do) needs only a resolution. Values are read as they will be after the patch, so one
// PATCH can supply the evidence and the status together.
func missingEvidence(ctx context.Context, db DB, t *model.Ticket, patch TicketPatch, approvalCleared bool) ([]string, error) {
	after := func(p *string, current string) string {
		if p != nil {
			return strings.TrimSpace(*p)
		}
		return strings.TrimSpace(current)
	}
	verifiedBy := after(patch.VerifiedBy, model.Deref(t.VerifiedBy))
	workedExample := after(patch.WorkedExample, t.WorkedExample)
	refs := t.EvidenceRefs
	if patch.EvidenceRefs != nil {
		refs = nil
		for _, r := range *patch.EvidenceRefs {
			if strings.TrimSpace(r) != "" {
				refs = append(refs, r)
			}
		}
	}

	var missing []string
	needVerified := func() {
		if verifiedBy == "" {
			missing = append(missing, "verified_by (the command you ran and what it printed)")
		}
	}
	switch t.Type {
	case model.TypeResearch:
		if len(refs) == 0 {
			missing = append(missing, "evidence_refs (a research ticket's citations are its evidence)")
		}
	case model.TypeDesign:
		if t.ApproachApprovedAt == nil || approvalCleared {
			missing = append(missing, "an approved approach (a design is verified by the human signing it off)")
		}
	case model.TypeEval:
		needVerified()
	default: // bug, feature, improvement, chore, refactor
		needVerified()
		if t.Type == model.TypeFeature && workedExample == "" {
			missing = append(missing, "worked_example (one input and what it now produces)")
		}
		if t.Type == model.TypeRefactor {
			var guarded bool
			if err := db.QueryRow(ctx,
				`SELECT EXISTS (SELECT 1 FROM ticket_criteria c JOIN tripwires tw ON tw.id = c.tripwire_id `+
					`WHERE c.ticket_id = $1 AND c.tier = 'gate' AND tw.confirmed_at IS NOT NULL)`, t.ID).Scan(&guarded); err != nil {
				return nil, err
			}
			if !guarded {
				missing = append(missing, "a gate criterion linked to a confirmed tripwire (a refactor must not change behaviour, and a tripwire is how that is known)")
			}
		}
		var heldOut bool
		if err := db.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM ticket_checks WHERE ticket_id = $1 AND visibility = 'held_out')`, t.ID).Scan(&heldOut); err != nil {
			return nil, err
		}
		if heldOut && (t.GatePassed == nil || !*t.GatePassed) {
			missing = append(missing, "a passing held-out gate (run scripts/run-checks.sh "+t.Key+"; only it records the verdict)")
		}
	}
	return missing, nil
}
