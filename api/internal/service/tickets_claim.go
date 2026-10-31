package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/joelshore/ticketing/api/internal/apperr"
	"github.com/joelshore/ticketing/api/internal/model"
)

// ClaimRequest is everything a claim needs. The transport facts — session, client, run tag, arm,
// model — are observed by the server or set by the evaluation runner through request headers, never
// taken from a tool argument: an agent asked to report which arm it is in will sometimes be wrong
// and sometimes decline, and either ruins the record.
type ClaimRequest struct {
	Area *string
	// Batch restricts the claim to one batch, so an agent working a plan stays inside it.
	Batch         *uuid.UUID
	Actor         string
	SessionID     string
	ClientName    string
	ClientVersion string
	RunTag        string
	Arm           string
	Model         string
	Budget        int32
}

// ClaimResult is the claimed ticket together with the context for doing the work: why this batch of
// work exists, and the documents saying how this project is written.
type ClaimResult struct {
	View     model.TicketView
	Batch    *model.Batch
	Guidance model.GuidanceBundle
	// What done means, as the human wrote it, and the checks the agent may read.
	Criteria []model.TicketCriterion
	Checks   []model.TicketCheck
	// What has been decided about it, newest first (rule 19).
	Decisions []model.Decision
	// Judgements on its criteria, newest first (rule 21); a reopened ticket carries its old ones.
	Verdicts []model.CriterionVerdict
}

// ClaimNextTicket is rule 8: take the oldest highest-priority claimable ticket and start it.
func ClaimNextTicket(ctx context.Context, db DB, project *model.Project,
	req ClaimRequest) (*ClaimResult, error) {

	area := req.Area
	actor := ActorOr(req.Actor, "claude-code")
	// An unknown area name is a 404, not "nothing to claim".
	var areaID *uuid.UUID
	if area != nil {
		id, err := resolveAreaFilter(ctx, db, project.ID, AreaByName(*area))
		if err != nil {
			return nil, err
		}
		areaID = &id
	}

	return inTx(ctx, db, func(tx pgx.Tx) (*ClaimResult, error) {
		b := &builder{}
		b.push(`SELECT ` + model.TicketColumns + ` FROM tickets`)
		b.push(` WHERE project_id = `).bind(project.ID)
		b.push(` AND status = 'open'`)
		b.push(` AND (claimed_at IS NULL OR claimed_at < now() - interval '` + ClaimTTL + `')`)
		if areaID != nil {
			b.push(` AND area_id = `).bind(*areaID)
		}
		if req.Batch != nil {
			b.push(` AND batch_id = `).bind(*req.Batch)
		}
		b.push(claimInOrder)
		b.push(` ORDER BY ` + priorityRank + ` DESC, created_at ASC LIMIT 1 FOR UPDATE SKIP LOCKED`)

		rows, qerr := tx.Query(ctx, b.sql(), b.args...)
		candidate, err := collectOne[model.Ticket](rows, qerr)
		if err != nil {
			return nil, err
		}
		if candidate == nil {
			return nil, nil
		}

		rows, qerr = tx.Query(ctx,
			`UPDATE tickets SET status = 'in_progress', claimed_by = $2, claimed_at = now(), `+
				`version = version + 1, updated_at = now() WHERE id = $1 RETURNING `+model.TicketColumns,
			candidate.ID, actor)
		claimed, err := collectOne[model.Ticket](rows, qerr)
		if err != nil {
			return nil, err
		}

		if err := recordEvent(ctx, tx, claimed.ID, actor, "status",
			model.Ptr(string(model.StatusOpen)), model.Ptr(string(model.StatusInProgress))); err != nil {
			return nil, err
		}
		if err := recordEvent(ctx, tx, claimed.ID, actor, "claimed", candidate.ClaimedBy, &actor); err != nil {
			return nil, err
		}
		if _, err := insertComment(ctx, tx, claimed.ID, actor,
			fmt.Sprintf("Claimed by %s", actor), "system"); err != nil {
			return nil, err
		}

		view, err := hydrateOne(ctx, tx, *claimed)
		if err != nil {
			return nil, err
		}
		result := &ClaimResult{View: *view}
		if result.Criteria, err = ListCriteria(ctx, tx, claimed.ID); err != nil {
			return nil, err
		}
		if result.Checks, err = ListPublicChecks(ctx, tx, claimed.ID); err != nil {
			return nil, err
		}
		if result.Decisions, err = ListDecisions(ctx, tx, claimed.ID); err != nil {
			return nil, err
		}
		if result.Verdicts, err = ListVerdicts(ctx, tx, claimed.ID); err != nil {
			return nil, err
		}

		if claimed.BatchID != nil {
			batch, err := GetBatch(ctx, tx, project.ID, *claimed.BatchID)
			if err != nil {
				return nil, err
			}
			result.Batch = batch
		}

		docs, err := ResolveGuidance(ctx, tx, claimed)
		if err != nil {
			return nil, err
		}
		result.Guidance = Pack(docs, req.Budget)

		// Recorded inside the claim's own transaction: if the record cannot be written the claim
		// does not happen. In a system whose point is attribution, an unattributable claim is worse
		// than no claim.
		if err := recordClaim(ctx, tx, project.ID, claimed.ID, actor, req, result, docs); err != nil {
			return nil, err
		}
		return result, nil
	})
}

// recordClaim writes one row per delivery, plus the revisions it carried. The payload is the
// guidance bundle as it was serialised for this claim, stored as text rather than jsonb because
// jsonb normalises key order and whitespace, which would make those bytes unreproducible.
func recordClaim(ctx context.Context, db DB, projectID, ticketID uuid.UUID, actor string,
	req ClaimRequest, result *ClaimResult, docs []resolved) error {

	payload, err := json.Marshal(result.Guidance)
	if err != nil {
		return apperr.Internalf("could not record the claim payload: %v", err)
	}

	var sha *string
	if result.Guidance.SHA256 != "" {
		sha = &result.Guidance.SHA256
	}

	// Bytes of guidance delivered, not bytes of the envelope that carried it. The serialised empty
	// bundle is 50 characters, so len(payload) recorded 50 bytes of guidance for every claim that
	// served none, and anything summing the column over a run counted it as guidance.
	var guidanceBytes int32
	for _, served := range result.Guidance.Docs {
		guidanceBytes += int32(len(served.Body))
	}
	optional := func(v string) *string {
		if v == "" {
			return nil
		}
		return &v
	}

	var claimID uuid.UUID
	if err := db.QueryRow(ctx,
		`INSERT INTO ticket_claims (ticket_id, project_id, actor, session_id, client_name, `+
			`client_version, run_tag, arm, model, payload, guidance_sha256, guidance_bytes) `+
			`VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) RETURNING id`,
		ticketID, projectID, actor, optional(req.SessionID), optional(req.ClientName),
		optional(req.ClientVersion), optional(req.RunTag), optional(req.Arm), optional(req.Model),
		string(payload), sha, guidanceBytes).Scan(&claimID); err != nil {
		return apperr.FromDB(err)
	}

	for i, doc := range docs {
		if _, err := db.Exec(ctx,
			`INSERT INTO ticket_claim_documents (claim_id, revision_id, position) VALUES ($1, $2, $3)`,
			claimID, doc.Revision.ID, i); err != nil {
			return apperr.FromDB(err)
		}
	}
	return nil
}
