package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/joelshore/ticketing/api/internal/apperr"
	"github.com/joelshore/ticketing/api/internal/model"
)

// DefaultGuidanceBudget is how many tokens of guidance a claim may carry. The whole corpus is
// roughly 7,900 tokens, far too much for every claim; a batch's own selection fits well inside.
const DefaultGuidanceBudget int32 = 4000

// kindRank orders a bundle: read how we work, then what this area is, then the conventions, then
// what to tick off. An agent reading in this order never has to re-read.
var kindRank = map[model.GuidanceKind]int{
	model.GuidanceWorkflow:   0,
	model.GuidanceArea:       1,
	model.GuidanceConvention: 2,
	model.GuidanceChecklist:  3,
}

// AttachGuidance replaces a batch's guidance with these documents, in the order given. A NULL
// revision follows the document, so improving a document improves work already in flight; pin is
// for experiments, where a mid-run edit would silently change the treatment.
func AttachGuidance(ctx context.Context, db DB, projectID, batchID uuid.UUID, slugs []string, pin bool) error {
	_, err := inTx(ctx, db, func(tx pgx.Tx) (struct{}, error) {
		var zero struct{}
		if _, err := tx.Exec(ctx, `DELETE FROM batch_guidance WHERE batch_id = $1`, batchID); err != nil {
			return zero, apperr.FromDB(err)
		}
		for i, slug := range slugs {
			doc, _, err := GetGuidance(ctx, tx, projectID, slug)
			if err != nil {
				return zero, err
			}
			var revisionID *uuid.UUID
			if pin {
				revisionID = doc.CurrentRevisionID
			}
			if _, err := tx.Exec(ctx,
				`INSERT INTO batch_guidance (batch_id, document_id, revision_id, position) `+
					`VALUES ($1, $2, $3, $4)`, batchID, doc.ID, revisionID, i); err != nil {
				return zero, apperr.FromDB(err)
			}
		}
		return zero, nil
	})
	return err
}

// attachStandingGuidance gives a batch the project's standing corpus, unpinned, unless its documents were already chosen — as
// none, too, which is the evaluation runner's control arm. TP-302: a plan batch's claims were resolved against an empty table.
func attachStandingGuidance(ctx context.Context, tx pgx.Tx, projectID, batchID uuid.UUID) error {
	_, err := tx.Exec(ctx, `INSERT INTO batch_guidance (batch_id, document_id, position) SELECT $1, d.id, row_number() OVER (ORDER BY d.slug) `+
		`FROM guidance_documents d WHERE d.project_id = $2 AND d.archived_at IS NULL `+
		`AND NOT EXISTS (SELECT 1 FROM batch_guidance g WHERE g.batch_id = $1)`, batchID, projectID)
	return apperr.FromDB(err)
}

// ResolveGuidance returns the guidance a ticket is claimed with, in bundle order. The order is
// deterministic so that re-claiming the same ticket produces byte-identical guidance — otherwise
// nothing downstream could compare two runs honestly.
//
// A batch narrows and pins; no batch means no narrowing, not nothing — every ticket filed before
// batches existed, and every ticket a human files by hand, has batch_id NULL. An attached batch
// with nothing on it still resolves to nothing, which the evaluation runner's control arm needs.
func ResolveGuidance(ctx context.Context, db DB, ticket *model.Ticket) ([]resolved, error) {
	if ticket.BatchID == nil {
		return projectGuidance(ctx, db, ticket.ProjectID)
	}
	return BatchGuidance(ctx, db, *ticket.BatchID)
}

// projectGuidance is every standing document in the project, in bundle order — what a ticket that
// belongs to no batch resolves to, because how this project is written applies to a ticket whoever
// it came from. A document with no revision is dropped: it has nothing to say yet.
func projectGuidance(ctx context.Context, db DB, projectID uuid.UUID) ([]resolved, error) {
	all, err := ListGuidanceCurrent(ctx, db, projectID)
	if err != nil {
		return nil, err
	}
	var out []resolved
	for _, item := range all {
		if item.Revision.ID != uuid.Nil {
			out = append(out, item)
		}
	}
	sortBundle(out)
	return out, nil
}

// BatchGuidance is the same resolution keyed on the batch itself, which is what the web app asks
// for: a batch page shows its documents whether or not any ticket has been claimed from it.
//
// Three small queries rather than one join: a join of documents and revisions collides on id,
// created_at and author, and hand-scanning that is more fragile than the round trips are slow.
func BatchGuidance(ctx context.Context, db DB, batchID uuid.UUID) ([]resolved, error) {
	type attachment struct {
		DocumentID uuid.UUID  `db:"document_id"`
		RevisionID *uuid.UUID `db:"revision_id"`
		Position   int32      `db:"position"`
	}
	rows, err := db.Query(ctx,
		`SELECT document_id, revision_id, position FROM batch_guidance WHERE batch_id = $1`,
		batchID)
	attachments, err := collect[attachment](rows, err)
	if err != nil || len(attachments) == 0 {
		return nil, err
	}

	docIDs := make([]uuid.UUID, 0, len(attachments))
	for _, a := range attachments {
		docIDs = append(docIDs, a.DocumentID)
	}
	rows, err = db.Query(ctx,
		`SELECT `+guidanceColumns+` FROM guidance_documents WHERE id = ANY($1) AND archived_at IS NULL`,
		docIDs)
	docs, err := collect[model.GuidanceDoc](rows, err)
	if err != nil {
		return nil, err
	}
	byID := make(map[uuid.UUID]model.GuidanceDoc, len(docs))
	for _, doc := range docs {
		byID[doc.ID] = doc
	}

	// Each attachment resolves to the pinned revision if it names one, otherwise to whatever the
	// document is now. Dropped here: a document archived since it was attached, and one with no
	// revision, which has nothing to say yet.
	kept := attachments[:0]
	revIDs := make([]uuid.UUID, 0, len(attachments))
	for _, a := range attachments {
		doc, ok := byID[a.DocumentID]
		if !ok {
			continue
		}
		if a.RevisionID == nil {
			a.RevisionID = doc.CurrentRevisionID
		}
		if a.RevisionID == nil {
			continue
		}
		kept = append(kept, a)
		revIDs = append(revIDs, *a.RevisionID)
	}
	if len(revIDs) == 0 {
		return nil, nil
	}
	revByID, err := revisionsByID(ctx, db, revIDs)
	if err != nil {
		return nil, err
	}

	out := make([]resolved, 0, len(kept))
	for _, a := range kept {
		rev, ok := revByID[*a.RevisionID]
		if !ok {
			continue
		}
		out = append(out, resolved{Doc: byID[a.DocumentID], Revision: rev, Position: a.Position})
	}
	sortBundle(out)
	return out, nil
}

// sortBundle puts a bundle in the order it is served and hashed: kind, then weight, then the
// batch's own position, then slug to break every remaining tie. Nothing here may depend on row
// order from the database, or the same claim would hash differently on two runs.
func sortBundle(out []resolved) {
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if kindRank[a.Doc.Kind] != kindRank[b.Doc.Kind] {
			return kindRank[a.Doc.Kind] < kindRank[b.Doc.Kind]
		}
		if a.Doc.Weight != b.Doc.Weight {
			return a.Doc.Weight < b.Doc.Weight
		}
		if a.Position != b.Position {
			return a.Position < b.Position
		}
		return a.Doc.Slug < b.Doc.Slug
	})
}

// Pack turns resolved documents into what an agent receives, spending the budget in bundle order
// and announcing whatever did not fit.
func Pack(docs []resolved, budget int32) model.GuidanceBundle {
	if budget <= 0 {
		budget = DefaultGuidanceBudget
	}
	bundle := model.GuidanceBundle{BudgetTokens: budget}
	var hashed strings.Builder

	for _, item := range docs {
		tokens := estimateTokens(item.Revision.Body)
		served := model.ServedGuidance{
			Slug:     item.Doc.Slug,
			Title:    item.Doc.Title,
			Kind:     item.Doc.Kind,
			Revision: item.Revision.Revision,
			Stale:    item.Doc.Stale,
			Tokens:   tokens,
		}
		if !item.Doc.Inline || bundle.UsedTokens+tokens > budget {
			served.Omitted = fmt.Sprintf("over the %d-token budget", budget)
			if !item.Doc.Inline {
				served.Omitted = "too long to inline"
			}
			served.Fetch = fmt.Sprintf("get_guidance(slug: %q)", item.Doc.Slug)
			bundle.Omitted = append(bundle.Omitted, served)
			continue
		}
		served.Body = DeliverBody(item.Doc, item.Revision.Body)
		bundle.UsedTokens += tokens
		bundle.Docs = append(bundle.Docs, served)
		fmt.Fprintf(&hashed, "%s:%d:%s\n", item.Doc.ID, item.Revision.Revision, item.Revision.ContentSHA256)
	}

	if hashed.Len() > 0 {
		sum := sha256.Sum256([]byte(hashed.String()))
		bundle.SHA256 = hex.EncodeToString(sum[:])
	}
	return bundle
}

// DeliverBody is what a reader gets, whichever path they arrive by: a warning only one transport
// applies is a warning whoever reads the document the other way will miss. The banner goes inside
// the body, not beside it, because an agent reading markdown reads the markdown and a sibling
// boolean is easy to skip. A stale document is still delivered — no guidance is worse than old.
func DeliverBody(doc model.GuidanceDoc, body string) string {
	stale := doc.Stale
	if !stale && doc.ReviewedAt != nil && doc.ReviewIntervalDays > 0 {
		stale = time.Since(*doc.ReviewedAt) > time.Duration(doc.ReviewIntervalDays)*24*time.Hour
	}
	if !stale {
		return body
	}
	reason := "it has not been reviewed recently"
	if doc.StaleReason != nil && *doc.StaleReason != "" {
		reason = *doc.StaleReason
	}
	return fmt.Sprintf("> STALE: %s. Treat specific paths, versions and commands below as "+
		"unverified — check the code. If this document is wrong, call report_guidance_drift.\n\n",
		reason) + body
}

// estimateTokens is good enough for spending a budget and wrong for anything else, so it is never
// surfaced as an authoritative count.
func estimateTokens(body string) int32 { return int32(len(body)/4) + 1 }
