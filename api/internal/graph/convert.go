package graph

import (
	"context"
	"sort"
	"strconv"

	"github.com/google/uuid"
	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/joelshore/ticketing/api/internal/apperr"
	"github.com/joelshore/ticketing/api/internal/graph/gqlmodel"
	"github.com/joelshore/ticketing/api/internal/model"
	"github.com/joelshore/ticketing/api/internal/service"
)

// actorOf is rule 10: the actor the middleware settled, defaulting to human for GraphQL.
func actorOf(ctx context.Context) string {
	return service.ActorOr(service.ActorFrom(ctx).Name, "human")
}

// gqlErr renders an AppError as the message plus extensions.code, the shape docs/api.md promises.
func gqlErr(err error) error {
	if err == nil {
		return nil
	}
	e := apperr.As(err)
	return &gqlerror.Error{
		Message:    e.Message,
		Extensions: map[string]any{"code": e.Code()},
	}
}

func parseID(raw string) (uuid.UUID, error) {
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, gqlErr(apperr.BadRequestf("%q is not a valid id", raw))
	}
	return id, nil
}

// --- model -> graphql -------------------------------------------------------

func toProject(p *model.Project) *gqlmodel.Project {
	if p == nil {
		return nil
	}
	return &gqlmodel.Project{ID: p.ID.String(), Key: p.Key, Name: p.Name, RepoPath: p.RepoPath}
}

func toArea(a *model.Area) *gqlmodel.Area {
	if a == nil {
		return nil
	}
	return &gqlmodel.Area{
		ID:           a.ID.String(),
		Name:         a.Name,
		Description:  a.Description,
		PathGlobs:    a.PathGlobs,
		AutoAcceptAi: a.AutoAcceptAI,
		ArchivedAt:   a.ArchivedAt,
		CreatedAt:    a.CreatedAt,
		UpdatedAt:    a.UpdatedAt,
		OpenCount:    int32(a.OpenCount),
	}
}

func toAreas(areas []model.Area) []*gqlmodel.Area {
	out := make([]*gqlmodel.Area, 0, len(areas))
	for i := range areas {
		out = append(out, toArea(&areas[i]))
	}
	return out
}

func toTicket(view *model.TicketView) *gqlmodel.Ticket {
	if view == nil {
		return nil
	}
	t := view.Ticket
	return &gqlmodel.Ticket{
		ID:            t.ID.String(),
		Key:           t.Key,
		Number:        t.Number,
		Title:         t.Title,
		Description:   t.Description,
		Type:          t.Type,
		Priority:      t.Priority,
		Status:        t.Status,
		Source:        t.Source,
		NextStatuses:  model.NextStatuses(t.Status),
		Area:          toArea(view.Area),
		Branch:        t.Branch,
		PrURL:         view.PRURL,
		Resolution:    t.Resolution,
		Fingerprint:   t.Fingerprint,
		SeenCount:     t.SeenCount,
		ClaimedBy:     t.ClaimedBy,
		ClaimedAt:     t.ClaimedAt,
		BatchPosition: t.BatchPosition,
		Tags:          model.NonNil(view.Tags),
		Goals:         model.NonNil(t.Goals),
		Intent:        t.Intent, Constraints: t.Constraints, NonGoals: t.NonGoals,
		Approach: t.Approach, RiskAndRollback: t.RiskAndRollback, WorkedExample: t.WorkedExample,
		VerificationPlan: t.VerificationPlan, EvidenceRefs: model.NonNil(t.EvidenceRefs),
		EstimatedTokens: t.EstimatedTokens, EstimatedCostUsd: t.EstimatedCostUSD,
		ApproachApprovedBy: t.ApproachApprovedBy, ApproachApprovedAt: t.ApproachApprovedAt,
		VerifiedBy: t.VerifiedBy, MeasuredDelta: t.MeasuredDelta, GatePassed: t.GatePassed,
		GateCheckedAt: t.GateCheckedAt, SummaryForHuman: t.SummaryForHuman, DecisionID: uuidString(t.DecisionID),
		Version:     t.Version,
		CreatedAt:   t.CreatedAt,
		UpdatedAt:   t.UpdatedAt,
		CompletedAt: t.CompletedAt,
	}
}

func toTickets(views []model.TicketView) []*gqlmodel.Ticket {
	out := make([]*gqlmodel.Ticket, 0, len(views))
	for i := range views {
		out = append(out, toTicket(&views[i]))
	}
	return out
}

func toComments(rows []model.Comment) []*gqlmodel.Comment {
	out := make([]*gqlmodel.Comment, 0, len(rows))
	for _, c := range rows {
		out = append(out, &gqlmodel.Comment{
			ID: c.ID.String(), Author: c.Author, Body: c.Body, Kind: c.Kind, CreatedAt: c.CreatedAt,
		})
	}
	return out
}

// ticket_events has a bigint id rather than a uuid, and GraphQL only promises an ID.
func toEvents(rows []model.TicketEvent) []*gqlmodel.TicketEvent {
	out := make([]*gqlmodel.TicketEvent, 0, len(rows))
	for _, e := range rows {
		out = append(out, &gqlmodel.TicketEvent{
			ID:        strconv.FormatInt(e.ID, 10),
			Actor:     e.Actor,
			Field:     e.Field,
			OldValue:  e.OldValue,
			NewValue:  e.NewValue,
			CreatedAt: e.CreatedAt,
		})
	}
	return out
}

func toLocations(rows []model.TicketLocation) []*gqlmodel.TicketLocation {
	out := make([]*gqlmodel.TicketLocation, 0, len(rows))
	for _, l := range rows {
		out = append(out, &gqlmodel.TicketLocation{
			ID:        l.ID.String(),
			FilePath:  l.FilePath,
			LineStart: l.LineStart,
			LineEnd:   l.LineEnd,
			Snippet:   l.Snippet,
		})
	}
	return out
}

func toPullRequests(rows []model.PullRequest) []*gqlmodel.PullRequest {
	out := make([]*gqlmodel.PullRequest, 0, len(rows))
	for _, p := range rows {
		out = append(out, &gqlmodel.PullRequest{
			ID:        p.ID.String(),
			URL:       p.URL,
			Number:    p.Number,
			Branch:    p.Branch,
			State:     string(p.State),
			CreatedAt: p.CreatedAt,
		})
	}
	return out
}

// --- graphql -> service -----------------------------------------------------

func toFilter(in *gqlmodel.TicketFilter) (service.TicketFilter, error) {
	out := service.TicketFilter{}
	if in == nil {
		return out, nil
	}
	out.Source = in.Source
	out.Status = in.Status
	out.Type = in.Type
	out.Priority = in.Priority
	out.CreatedAfter = in.CreatedAfter
	out.UpdatedAfter = in.UpdatedAfter
	out.Tag = in.Tag
	if in.AreaID != nil {
		id, err := parseID(*in.AreaID)
		if err != nil {
			return out, err
		}
		out.Area = service.AreaByID(id)
	}
	return out, nil
}

func toBatch(b *model.Batch) *gqlmodel.Batch {
	if b == nil {
		return nil
	}
	return &gqlmodel.Batch{
		ID:                 b.ID.String(),
		Kind:               b.Kind,
		Actor:              b.Actor,
		Title:              b.Title,
		Brief:              b.Brief,
		GitRef:             b.GitRef,
		Summary:            b.Summary,
		FindingsCount:      int32(b.FindingsCount),
		StartedAt:          b.StartedAt,
		FinishedAt:         b.FinishedAt,
		What:               b.What,
		How:                b.How,
		RoadmapID:          uuidString(b.RoadmapID),
		Sequence:           b.Sequence,
		CheckpointDecision: b.CheckpointDecision,
		CheckpointNote:     b.CheckpointNote,
		CheckpointBy:       b.CheckpointBy,
		CheckpointAt:       b.CheckpointAt,
		Digest:             b.Digest,
		ReviewsRequired:    b.ReviewsRequired,
		DigestAt:           b.DigestAt,
	}
}

func toBatches(batches []model.Batch) []*gqlmodel.Batch {
	out := make([]*gqlmodel.Batch, 0, len(batches))
	for i := range batches {
		out = append(out, toBatch(&batches[i]))
	}
	return out
}

// toGuidance carries the document and the revision it currently points at, because "the body" is a
// property of a revision and every consumer wants them together.
func toGuidance(doc model.GuidanceDoc, revision *model.GuidanceRevision) *gqlmodel.GuidanceDoc {
	out := &gqlmodel.GuidanceDoc{
		ID:              doc.ID.String(),
		Slug:            doc.Slug,
		Title:           doc.Title,
		Kind:            doc.Kind,
		Inline:          doc.Inline,
		Weight:          doc.Weight,
		SourcePaths:     doc.SourcePaths,
		SourceCommit:    doc.SourceCommit,
		ReviewedAt:      doc.ReviewedAt,
		ReviewedBy:      doc.ReviewedBy,
		Stale:           doc.Stale,
		StaleReason:     doc.StaleReason,
		DriftReportedAt: doc.DriftReportedAt,
		UpdatedAt:       doc.UpdatedAt,
	}
	if revision != nil {
		out.Body, out.Revision = revision.Body, revision.Revision
	}
	return out
}

// --- resolver helpers -------------------------------------------------------
//
// These live here rather than at the foot of schema.resolvers.go because gqlgen treats anything it
// does not recognise in that file as orphaned and comments it out on the next `just generate`.

func (r *mutationResolver) setArchived(ctx context.Context, project *string, id string, archived bool) (*gqlmodel.Area, error) {
	proj, areaID, err := r.area(ctx, project, id)
	if err != nil {
		return nil, err
	}
	area, err := service.SetAreaArchived(ctx, r.DB, proj.ID, areaID, archived)
	if err != nil {
		return nil, gqlErr(err)
	}
	return toArea(area), nil
}

func (r *mutationResolver) area(ctx context.Context, project *string, id string) (*model.Project, uuid.UUID, error) {
	proj, err := service.ResolveProject(ctx, r.DB, project)
	if err != nil {
		return nil, uuid.Nil, gqlErr(err)
	}
	areaID, err := parseID(id)
	if err != nil {
		return nil, uuid.Nil, err
	}
	return proj, areaID, nil
}

func derefOr[T any](v *T, fallback T) T {
	if v == nil {
		return fallback
	}
	return *v
}

func uuidString(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	return model.Ptr(id.String())
}

func toTag(t *model.Tag) *gqlmodel.Tag {
	return &gqlmodel.Tag{ID: t.ID.String(), Name: t.Name, Description: t.Description,
		ArchivedAt: t.ArchivedAt, CreatedAt: t.CreatedAt}
}

func toTags(tags []model.Tag) []*gqlmodel.Tag {
	out := make([]*gqlmodel.Tag, 0, len(tags))
	for i := range tags {
		out = append(out, toTag(&tags[i]))
	}
	return out
}

func toRoadmap(r *model.Roadmap) *gqlmodel.Roadmap {
	return &gqlmodel.Roadmap{ID: r.ID.String(), Title: r.Title, Spec: r.Spec, CreatedAt: r.CreatedAt,
		ArchivedAt: r.ArchivedAt}
}

func toProgress(p service.Progress) *gqlmodel.BatchProgress {
	out := &gqlmodel.BatchProgress{Next: p.Next, BlockedBy: []*gqlmodel.BlockedTicket{}}
	if p.LockedBy != "" {
		out.LockedBy = model.Ptr(p.LockedBy)
	}
	keys := make([]string, 0, len(p.BlockedBy))
	for key := range p.BlockedBy {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		out.BlockedBy = append(out.BlockedBy, &gqlmodel.BlockedTicket{Key: key, Behind: p.BlockedBy[key]})
	}
	return out
}

func toTripwire(t *model.Tripwire) *gqlmodel.Tripwire {
	return &gqlmodel.Tripwire{ID: t.ID.String(), TestRef: t.TestRef, Meaning: t.Meaning,
		ConfirmedBy: t.ConfirmedBy, ConfirmedAt: t.ConfirmedAt}
}

func toDecision(d *model.Decision) *gqlmodel.Decision {
	return &gqlmodel.Decision{ID: d.ID.String(), Kind: d.Kind, MadeBy: d.MadeBy, EvidenceRefs: model.NonNil(d.EvidenceRefs),
		Rationale: d.Rationale, RevisitTrigger: d.RevisitTrigger, Supersedes: uuidString(d.Supersedes),
		CitesRevisionID: uuidString(d.CitesRevisionID), Actor: d.Actor, CreatedAt: d.CreatedAt}
}

// optionalID parses an optional ID argument; nil stays nil.
func optionalID(raw *string) (*uuid.UUID, error) {
	if raw == nil {
		return nil, nil
	}
	id, err := parseID(*raw)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

func toUncited(rows []service.DecisionOnTicket) []*gqlmodel.UncitedDecision {
	out := make([]*gqlmodel.UncitedDecision, 0, len(rows))
	for i := range rows {
		out = append(out, &gqlmodel.UncitedDecision{Decision: toDecision(&rows[i].Decision),
			TicketKey: rows[i].Key, TicketTitle: rows[i].Title})
	}
	return out
}
