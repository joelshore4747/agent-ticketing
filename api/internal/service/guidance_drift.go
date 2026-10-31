package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/joelshore/ticketing/api/internal/apperr"
	"github.com/joelshore/ticketing/api/internal/model"
)

// DriftReport is what an agent noticed: a document that no longer matches the code.
type DriftReport struct {
	Slug     string
	Problem  string
	Evidence string
	Actor    string
}

// ReportGuidanceDrift files the gap between a document and the code as an ordinary ticket. The API
// never sees the repository, so it cannot verify that a document is stale — but an agent that has
// just read both the code and the document in one context window is the best-positioned observer of
// the gap there will ever be, and only for as long as that window lasts.
//
// The fingerprint is guidance:<slug>:<revision content hash>, so a second agent reporting the same
// problem against the same revision bumps seen_count instead of filing a duplicate, while an edit
// to the document lets the next report through — the new revision hashes differently.
func ReportGuidanceDrift(ctx context.Context, db DB, project *model.Project,
	report DriftReport) (*CreateOutcome, error) {

	doc, revision, err := GetGuidance(ctx, db, project.ID, report.Slug)
	if err != nil {
		return nil, err
	}
	if revision == nil {
		return nil, apperr.BadRequestf("guidance %q has no revision to report against", report.Slug)
	}
	if strings.TrimSpace(report.Problem) == "" {
		return nil, apperr.BadRequestf("say what is wrong with the document")
	}

	description := fmt.Sprintf(
		"Reported against `%s` revision %d while working in this repository.\n\n"+
			"## What is wrong\n\n%s\n", doc.Slug, revision.Revision, strings.TrimSpace(report.Problem))
	if evidence := strings.TrimSpace(report.Evidence); evidence != "" {
		description += "\n## Evidence\n\n" + evidence + "\n"
	}
	description += fmt.Sprintf("\n## The document as it stands\n\n> %s\n",
		strings.ReplaceAll(strings.TrimSpace(revision.Body), "\n", "\n> "))

	// Locations come from source_paths, so the ticket lands next to the code it is wrong about.
	locations := make([]NewLocation, 0, len(doc.SourcePaths))
	for _, path := range doc.SourcePaths {
		locations = append(locations, NewLocation{FilePath: path})
	}

	outcome, err := CreateTicket(ctx, db, project, NewTicket{
		Title:       fmt.Sprintf("Guidance %q no longer matches the code", doc.Slug),
		Description: description,
		Type:        model.TypeChore,
		Priority:    model.PriorityMedium,
		Source:      model.SourceAI,
		Area:        AreaByName("docs"),
		Fingerprint: model.Ptr("guidance:" + doc.Slug + ":" + revision.ContentSHA256),
		Locations:   locations,
		Actor:       ActorOr(report.Actor, "claude-code"),
	})
	if err != nil {
		return nil, err
	}

	// Marked on the document so guidance-check and the web view can say someone has already
	// reported this without having to find the ticket first.
	if _, err := db.Exec(ctx,
		`UPDATE guidance_documents SET drift_reported_at = now(), updated_at = now() WHERE id = $1`,
		doc.ID); err != nil {
		return nil, apperr.FromDB(err)
	}
	return outcome, nil
}

// ReviewMark is what `just guidance-check` writes back after comparing a document's source_paths
// against the repository: a judgement made somewhere that can see the code, which the API cannot.
type ReviewMark struct {
	SourceCommit *string
	Stale        *bool
	StaleReason  *string
	// Accepted means a human (or CI on a clean tree) has looked: it moves reviewed_at forward, which
	// is what the review interval counts from.
	Accepted   bool
	ReviewedBy string
}

func ReviewGuidance(ctx context.Context, db DB, projectID uuid.UUID, slug string,
	mark ReviewMark) (*model.GuidanceDoc, error) {

	rows, err := db.Query(ctx,
		`UPDATE guidance_documents SET
		   source_commit = COALESCE($3, source_commit),
		   stale         = COALESCE($4, stale),
		   -- Cleared with the flag rather than left behind: a document that has stopped being stale
		   -- must not keep explaining why it was.
		   stale_reason  = CASE WHEN COALESCE($4, stale) THEN COALESCE($5, stale_reason) ELSE NULL END,
		   reviewed_at   = CASE WHEN $6 THEN now() ELSE reviewed_at END,
		   reviewed_by   = CASE WHEN $6 THEN $7 ELSE reviewed_by END,
		   updated_at    = now(),
		   version       = version + 1
		 WHERE project_id = $1 AND slug = $2
		 RETURNING `+guidanceColumns,
		projectID, slug, mark.SourceCommit, mark.Stale, mark.StaleReason,
		mark.Accepted, ActorOr(mark.ReviewedBy, "guidance-check"))
	doc, err := collectOne[model.GuidanceDoc](rows, err)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, apperr.NotFoundf("no guidance %q", slug)
	}
	return doc, nil
}
