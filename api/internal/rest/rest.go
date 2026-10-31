// Package rest is the JSON surface at /api. It is a thin shell: every rule lives in service.
package rest

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/joelshore/ticketing/api/internal/apperr"
	"github.com/joelshore/ticketing/api/internal/model"
	"github.com/joelshore/ticketing/api/internal/service"
)

// Register mounts every /api route plus /healthz on mux.
func Register(mux *http.ServeMux, db service.DB) {
	h := &handlers{db: db}
	mux.HandleFunc("GET /healthz", h.healthz)
	mux.HandleFunc("GET /api/version", wrap(h.version))
	mux.HandleFunc("GET /api/projects", wrap(h.listProjects))
	mux.HandleFunc("POST /api/projects", wrap(h.createProject))
	mux.HandleFunc("GET /api/tickets", wrap(h.listTickets))
	mux.HandleFunc("POST /api/tickets", wrap(h.createTicket))
	mux.HandleFunc("GET /api/tickets/{key}", wrap(h.getTicket))
	mux.HandleFunc("PATCH /api/tickets/{key}", wrap(h.patchTicket))
	mux.HandleFunc("POST /api/tickets/{key}/comments", wrap(h.addComment))
	mux.HandleFunc("POST /api/tickets/{key}/proposals", wrap(h.proposeEdit))
	mux.HandleFunc("POST /api/tickets/{key}/decisions", wrap(h.recordDecision))
	mux.HandleFunc("POST /api/tickets/{key}/verdicts", wrap(h.recordVerdict))
	mux.HandleFunc("POST /api/tickets/{key}/usage", wrap(h.recordUsage))
	mux.HandleFunc("GET /api/decisions/uncited", wrap(h.uncitedDecisions))
	mux.HandleFunc("GET /api/needs-you", wrap(h.needsYou))
	mux.HandleFunc("GET /api/tripwires", wrap(h.listTripwires))
	mux.HandleFunc("POST /api/tripwires", wrap(h.registerTripwire))
	mux.HandleFunc("GET /api/batches", wrap(h.listBatches))
	mux.HandleFunc("POST /api/batches", wrap(h.createBatch))
	mux.HandleFunc("GET /api/batches/{id}", wrap(h.getBatch))
	mux.HandleFunc("PATCH /api/batches/{id}", wrap(h.patchBatch))
	mux.HandleFunc("PUT /api/batches/{id}/guidance", wrap(h.attachGuidance))
	mux.HandleFunc("POST /api/batches/plan", wrap(h.planBatch))
	mux.HandleFunc("GET /api/batches/{id}/reviews", wrap(h.listReviews))
	mux.HandleFunc("POST /api/batches/{id}/reviews", wrap(h.recordReview))
	mux.HandleFunc("POST /api/batches/{id}/size", wrap(h.reportBatchSize))
	mux.HandleFunc("GET /api/project-memory", wrap(h.projectMemory))
	mux.HandleFunc("PUT /api/batches/{id}/order", wrap(h.reorderBatch))
	mux.HandleFunc("POST /api/batches/{id}/tickets", wrap(h.addToBatch))
	mux.HandleFunc("DELETE /api/batches/{id}/tickets/{key}", wrap(h.removeFromBatch))
	mux.HandleFunc("GET /api/roadmaps", wrap(h.listRoadmaps))
	mux.HandleFunc("POST /api/roadmaps", wrap(h.createRoadmap))
	mux.HandleFunc("GET /api/roadmaps/{id}", wrap(h.getRoadmap))
	mux.HandleFunc("GET /api/tags", wrap(h.listTags))
	mux.HandleFunc("GET /api/guidance", wrap(h.listGuidance))
	mux.HandleFunc("PUT /api/guidance/{slug}", wrap(h.putGuidance))
	mux.HandleFunc("GET /api/guidance/{slug}", wrap(h.getGuidance))
	mux.HandleFunc("POST /api/guidance/{slug}/review", wrap(h.reviewGuidance))
	// The claim was MCP-only, so an agent working a ticket found through GET /api/tickets received
	// no guidance and left no record of what it was given. That made a guided run silently a control.
	mux.HandleFunc("POST /api/claim", wrap(h.claim))
	mux.HandleFunc("GET /api/areas", wrap(h.listAreas))
	// Areas are listed here but managed over GraphQL only: they are a list the human curates, and
	// auto_accept_ai on an area would let an agent route its own findings past triage (rule 17).
}

type handlers struct{ db service.DB }

// result is what a handler returns: a status and a body, or an error to render.
type result struct {
	status int
	body   any
}

func wrap(fn func(*http.Request) (result, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out, err := fn(r)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, out.status, out.body)
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if body != nil {
		_ = json.NewEncoder(w).Encode(body)
	}
}

// writeError renders {"error": "..."} with the matching status, the shape docs/api.md promises.
func writeError(w http.ResponseWriter, err error) {
	e := apperr.As(err)
	writeJSON(w, e.Status(), map[string]string{"error": e.Message})
}

// actorOf is rule 10: the actor the middleware settled, defaulting to claude-code for REST.
func actorOf(r *http.Request) string {
	return service.ActorOr(service.ActorFrom(r.Context()).Name, "claude-code")
}

func decode[T any](r *http.Request) (T, error) {
	var out T
	// An absent body is an empty one. POST /api/claim takes only optional filters, so requiring
	// `{}` there would be a papercut with no meaning behind it; a malformed body still fails.
	dec := json.NewDecoder(r.Body)
	// The consumer of this API is a model that guesses field names, so an unknown one is almost
	// always a mistake worth naming. Accepting it silently returns a success the caller cannot
	// question: a POST that said "area_id" instead of "area" created an unrouted ticket and
	// reported 201. encoding/json names the offending field in the error.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil && !errors.Is(err, io.EOF) {
		return out, apperr.BadRequestf("%v", err)
	}
	return out, nil
}

func (h *handlers) healthz(w http.ResponseWriter, r *http.Request) {
	if err := service.Health(r.Context(), h.db); err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// version is the JSON sibling of healthz, which keeps its plain "ok" for the scripts that probe it.
func (h *handlers) version(*http.Request) (result, error) {
	return result{http.StatusOK, service.CurrentVersion()}, nil
}

// --- projects ---------------------------------------------------------------

func (h *handlers) listProjects(r *http.Request) (result, error) {
	projects, err := service.ListProjects(r.Context(), h.db)
	if err != nil {
		return result{}, err
	}
	return result{http.StatusOK, projects}, nil
}

type createProjectBody struct {
	Key      string  `json:"key"`
	Name     string  `json:"name"`
	RepoPath *string `json:"repo_path"`
}

func (h *handlers) createProject(r *http.Request) (result, error) {
	payload, err := decode[createProjectBody](r)
	if err != nil {
		return result{}, err
	}
	project, err := service.CreateProject(r.Context(), h.db, payload.Key, payload.Name, payload.RepoPath)
	if err != nil {
		return result{}, err
	}
	return result{http.StatusCreated, project}, nil
}

// --- tickets ----------------------------------------------------------------

type locationBody struct {
	FilePath  string  `json:"file_path"`
	LineStart *int32  `json:"line_start"`
	LineEnd   *int32  `json:"line_end"`
	Snippet   *string `json:"snippet"`
}

type createTicketBody struct {
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Type        string         `json:"type"`
	Priority    *string        `json:"priority"`
	Source      *string        `json:"source"`
	Area        *string        `json:"area"` // area *name*
	Branch      *string        `json:"branch"`
	Fingerprint *string        `json:"fingerprint"`
	Locations   []locationBody `json:"locations"`
	BatchID     *uuid.UUID     `json:"batch_id"`
	Tags        []string       `json:"tags"`
	Goals       []int32        `json:"goals"`
	Project     *string        `json:"project"`
}

func (h *handlers) createTicket(r *http.Request) (result, error) {
	ctx := r.Context()
	payload, err := decode[createTicketBody](r)
	if err != nil {
		return result{}, err
	}
	ticketType, priority, err := service.ParseTypeAndPriority(payload.Type, payload.Priority)
	if err != nil {
		return result{}, err
	}
	source := model.SourceHuman
	if payload.Source != nil {
		if source, err = model.ParseSource(*payload.Source); err != nil {
			return result{}, err
		}
	}
	project, err := h.writeProject(r, payload.Project)
	if err != nil {
		return result{}, err
	}

	locations := make([]service.NewLocation, 0, len(payload.Locations))
	for _, l := range payload.Locations {
		locations = append(locations, service.NewLocation{
			FilePath: l.FilePath, LineStart: l.LineStart, LineEnd: l.LineEnd, Snippet: l.Snippet,
		})
	}
	var area *service.AreaSelector
	if payload.Area != nil {
		area = service.AreaByName(*payload.Area)
	}

	outcome, err := service.CreateTicket(ctx, h.db, project, service.NewTicket{
		Title:       payload.Title,
		Description: payload.Description,
		Type:        ticketType,
		Priority:    priority,
		Source:      source,
		Area:        area,
		Branch:      payload.Branch,
		Fingerprint: payload.Fingerprint,
		Locations:   locations,
		BatchID:     payload.BatchID,
		Tags:        payload.Tags,
		Goals:       payload.Goals,
		Actor:       actorOf(r),
	})
	if err != nil {
		return result{}, err
	}
	status := http.StatusOK
	if outcome.Created {
		status = http.StatusCreated
	}
	return result{status, ticketBody(&outcome.View, &outcome.Created)}, nil
}

// ticketBody is the ticket JSON, optionally with the `created` flag the create endpoint adds.
func ticketBody(view *model.TicketView, created *bool) any {
	body := view.ToJSON()
	if created == nil {
		return body
	}
	// Re-encode through a map so `created` sits alongside the flattened row rather than nested.
	raw, err := json.Marshal(body)
	if err != nil {
		return body
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return body
	}
	out["created"] = *created
	return out
}

func (h *handlers) listTickets(r *http.Request) (result, error) {
	ctx := r.Context()
	q := r.URL.Query()
	get := func(key string) *string {
		v := strings.TrimSpace(q.Get(key))
		if v == "" {
			return nil
		}
		return &v
	}

	project, err := service.ResolveProject(ctx, h.db, get("project"))
	if err != nil {
		return result{}, err
	}

	// `status` may repeat or be comma-separated; a query string cannot nest a list.
	var statuses []string
	for _, raw := range q["status"] {
		for _, part := range strings.Split(raw, ",") {
			if part = strings.TrimSpace(part); part != "" {
				statuses = append(statuses, part)
			}
		}
	}
	filter, err := service.ParseTicketFilter(service.TicketFilterInput{Status: statuses,
		Source: get("source"), Type: get("type"), Priority: get("priority"), Area: get("area"),
		Tag: get("tag"), CreatedAfter: get("created_after"), UpdatedAfter: get("updated_after")})
	if err != nil {
		return result{}, err
	}

	limit, err := numberParam(get("limit"), service.DefaultLimit)
	if err != nil {
		return result{}, err
	}
	offset, err := numberParam(get("offset"), 0)
	if err != nil {
		return result{}, err
	}

	views, total, err := service.ListTickets(ctx, h.db, project.ID, filter,
		model.Deref(get("search")), limit, offset)
	if err != nil {
		return result{}, err
	}
	items := make([]model.TicketJSON, 0, len(views))
	for i := range views {
		items = append(items, views[i].ToJSON())
	}
	return result{http.StatusOK, model.TicketPageJSON{Items: items, Total: total}}, nil
}

func numberParam(raw *string, fallback int64) (int64, error) {
	if raw == nil {
		return fallback, nil
	}
	n, err := strconv.ParseInt(*raw, 10, 64)
	if err != nil {
		return 0, apperr.BadRequestf("must be a number")
	}
	return n, nil
}

func (h *handlers) getTicket(r *http.Request) (result, error) {
	ctx := r.Context()
	key := r.PathValue("key")
	project, err := service.ResolveProjectForTicket(ctx, h.db, key)
	if err != nil {
		return result{}, err
	}
	view, err := service.RequireTicket(ctx, h.db, project.ID, key)
	if err != nil {
		return result{}, err
	}
	criteria, err := service.ListCriteria(ctx, h.db, view.Ticket.ID)
	if err != nil {
		return result{}, err
	}
	checks, err := service.ListPublicChecks(ctx, h.db, view.Ticket.ID)
	if err != nil {
		return result{}, err
	}
	decisions, err := service.ListDecisions(ctx, h.db, view.Ticket.ID)
	if err != nil {
		return result{}, err
	}
	verdicts, err := service.ListVerdicts(ctx, h.db, view.Ticket.ID)
	if err != nil {
		return result{}, err
	}
	return result{http.StatusOK, ticketWithCriteria{view.ToJSON(), model.NonNil(criteria), model.NonNil(checks), model.NonNil(decisions), model.NonNil(verdicts)}}, nil
}

// ticketWithCriteria is one ticket with what done means: the human's criteria and the public checks.
type ticketWithCriteria struct {
	model.TicketJSON
	Criteria []model.TicketCriterion `json:"criteria"`
	Checks   []model.TicketCheck     `json:"checks"`
	// What has been decided about it, newest first (rule 19).
	Decisions []model.Decision `json:"decisions"`
	// Judgements on the criteria, newest first; each names its criterion_id.
	Verdicts []model.CriterionVerdict `json:"verdicts"`
}

type patchTicketBody struct {
	Version     int32   `json:"version"`
	Title       *string `json:"title"`
	Description *string `json:"description"`
	Type        *string `json:"type"`
	Priority    *string `json:"priority"`
	// Area is raw so an absent key can be told from an explicit null, which clears it.
	Area       json.RawMessage `json:"area"`
	Branch     *string         `json:"branch"`
	PRURL      *string         `json:"pr_url"`
	Status     *string         `json:"status"`
	Resolution *string         `json:"resolution"`
	// Tags replaces the set when present; [] clears it.
	Tags *[]string `json:"tags"`
	// Goals replaces the key goals the ticket serves.
	Goals *[]int32 `json:"goals"`
	// Section B and C, the agent's to write. Section A has no field here, so it cannot be written.
	Approach         *string   `json:"approach"`
	RiskAndRollback  *string   `json:"risk_and_rollback"`
	WorkedExample    *string   `json:"worked_example"`
	VerificationPlan *string   `json:"verification_plan"`
	EvidenceRefs     *[]string `json:"evidence_refs"`
	EstimatedTokens  *int32    `json:"estimated_tokens"`
	EstimatedCostUSD *float64  `json:"estimated_cost_usd"`
	VerifiedBy       *string   `json:"verified_by"`
	MeasuredDelta    *string   `json:"measured_delta"`
	SummaryForHuman  *string   `json:"summary_for_human"`
}

func (h *handlers) patchTicket(r *http.Request) (result, error) {
	ctx := r.Context()
	key := r.PathValue("key")
	payload, err := decode[patchTicketBody](r)
	if err != nil {
		return result{}, err
	}

	patch := service.TicketPatch{
		Title:       payload.Title,
		Description: payload.Description,
		Branch:      payload.Branch,
		PRURL:       payload.PRURL,
		Tags:        payload.Tags,
		Goals:       payload.Goals,

		Approach: payload.Approach, RiskAndRollback: payload.RiskAndRollback, WorkedExample: payload.WorkedExample,
		VerificationPlan: payload.VerificationPlan, EvidenceRefs: payload.EvidenceRefs,
		EstimatedTokens: payload.EstimatedTokens, EstimatedCostUSD: payload.EstimatedCostUSD,
		VerifiedBy: payload.VerifiedBy, MeasuredDelta: payload.MeasuredDelta, SummaryForHuman: payload.SummaryForHuman,
		Resolution: payload.Resolution,
	}
	if err := service.ParsePatchEnums(&patch, payload.Type, payload.Priority, payload.Status); err != nil {
		return result{}, err
	}
	if len(payload.Area) > 0 {
		var name *string
		if err := json.Unmarshal(payload.Area, &name); err != nil {
			return result{}, apperr.BadRequestf("area must be an area name or null")
		}
		if name == nil {
			patch.Area = service.ClearArea()
		} else {
			patch.Area = service.SetArea(service.AreaByName(*name))
		}
	}

	project, err := service.ResolveProjectForTicket(ctx, h.db, key)
	if err != nil {
		return result{}, err
	}
	current, err := service.RequireTicket(ctx, h.db, project.ID, key)
	if err != nil {
		return result{}, err
	}
	view, err := service.UpdateTicket(ctx, h.db, current.Ticket.ID, payload.Version, patch, actorOf(r))
	if err != nil {
		return result{}, err
	}
	return result{http.StatusOK, ticketBody(view, nil)}, nil
}

type commentBody struct {
	Body string `json:"body"`
}

func (h *handlers) addComment(r *http.Request) (result, error) {
	ctx := r.Context()
	key := r.PathValue("key")
	payload, err := decode[commentBody](r)
	if err != nil {
		return result{}, err
	}
	project, err := service.ResolveProjectForTicket(ctx, h.db, key)
	if err != nil {
		return result{}, err
	}
	ticket, err := service.RequireTicket(ctx, h.db, project.ID, key)
	if err != nil {
		return result{}, err
	}
	comment, err := service.AddComment(ctx, h.db, ticket.Ticket.ID, actorOf(r), payload.Body)
	if err != nil {
		return result{}, err
	}
	return result{http.StatusCreated, comment}, nil
}

// --- batches ----------------------------------------------------------------

func (h *handlers) listBatches(r *http.Request) (result, error) {
	ctx := r.Context()
	project, err := h.project(r)
	if err != nil {
		return result{}, err
	}
	batches, err := service.ListBatches(ctx, h.db, project.ID)
	if err != nil {
		return result{}, err
	}
	return result{http.StatusOK, batches}, nil
}

type createBatchBody struct {
	Kind    string  `json:"kind"`
	Title   string  `json:"title"`
	Brief   string  `json:"brief"`
	Summary *string `json:"summary"`
	Project *string `json:"project"`
	// Guidance names the documents this batch's tickets should arrive with, by slug.
	Guidance []string `json:"guidance"`
	// Pin freezes each document at its current revision, which an experiment needs and ordinary
	// work does not: pinned guidance cannot be improved underneath in-flight tickets.
	Pin bool `json:"pin"`
}

func (h *handlers) createBatch(r *http.Request) (result, error) {
	ctx := r.Context()
	payload, err := decode[createBatchBody](r)
	if err != nil {
		return result{}, err
	}
	kind, err := model.ParseBatchKind(payload.Kind)
	if err != nil {
		return result{}, err
	}
	project, err := h.writeProject(r, payload.Project)
	if err != nil {
		return result{}, err
	}
	batch, err := service.StartBatch(ctx, h.db, project.ID, service.NewBatch{
		Kind: kind, Title: payload.Title, Brief: payload.Brief,
		Summary: payload.Summary, Actor: actorOf(r),
	})
	if err != nil {
		return result{}, err
	}
	if len(payload.Guidance) > 0 {
		if err := service.AttachGuidance(ctx, h.db, project.ID, batch.ID, payload.Guidance, payload.Pin); err != nil {
			return result{}, err
		}
	}
	return result{http.StatusCreated, batch}, nil
}

func (h *handlers) getBatch(r *http.Request) (result, error) {
	ctx := r.Context()
	project, err := h.project(r)
	if err != nil {
		return result{}, err
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return result{}, apperr.BadRequestf("%q is not a valid id", r.PathValue("id"))
	}
	batch, err := service.GetBatch(ctx, h.db, project.ID, id)
	if err != nil {
		return result{}, err
	}
	views, err := service.ListBatchTickets(ctx, h.db, id)
	if err != nil {
		return result{}, err
	}
	return batchResponse(r, h.db, batch, views, http.StatusOK)
}

type patchBatchBody struct {
	Summary *string `json:"summary"`
	Finish  bool    `json:"finish"`
	Project *string `json:"project"`
}

func (h *handlers) patchBatch(r *http.Request) (result, error) {
	ctx := r.Context()
	payload, err := decode[patchBatchBody](r)
	if err != nil {
		return result{}, err
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return result{}, apperr.BadRequestf("%q is not a valid id", r.PathValue("id"))
	}
	if !payload.Finish {
		return result{}, apperr.BadRequestf("nothing to change: pass finish=true with a summary")
	}
	batch, err := service.FinishBatch(ctx, h.db, id, payload.Summary)
	if err != nil {
		return result{}, err
	}
	return result{http.StatusOK, struct {
		model.Batch
		NextSteps []string `json:"next_steps,omitempty"`
	}{*batch, service.FinishSteps(batch)}}, nil
}

type attachGuidanceBody struct {
	Guidance []string `json:"guidance"`
	Pin      bool     `json:"pin"`
	Project  *string  `json:"project"`
}

func (h *handlers) attachGuidance(r *http.Request) (result, error) {
	ctx := r.Context()
	payload, err := decode[attachGuidanceBody](r)
	if err != nil {
		return result{}, err
	}
	project, err := h.writeProject(r, payload.Project)
	if err != nil {
		return result{}, err
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return result{}, apperr.BadRequestf("%q is not a valid id", r.PathValue("id"))
	}
	if err := service.AttachGuidance(ctx, h.db, project.ID, id, payload.Guidance, payload.Pin); err != nil {
		return result{}, err
	}
	return result{http.StatusOK, map[string]any{"attached": payload.Guidance, "pinned": payload.Pin}}, nil
}

// --- guidance ---------------------------------------------------------------

func (h *handlers) listGuidance(r *http.Request) (result, error) {
	ctx := r.Context()
	project, err := h.project(r)
	if err != nil {
		return result{}, err
	}
	docs, err := service.ListGuidance(ctx, h.db, project.ID)
	if err != nil {
		return result{}, err
	}
	return result{http.StatusOK, docs}, nil
}

func (h *handlers) getGuidance(r *http.Request) (result, error) {
	ctx := r.Context()
	project, err := h.project(r)
	if err != nil {
		return result{}, err
	}
	doc, revision, err := service.GetGuidance(ctx, h.db, project.ID, r.PathValue("slug"))
	if err != nil {
		return result{}, err
	}
	// The banner goes inside the body here, as it does over MCP, because this endpoint's reader is
	// an agent parsing markdown. The web app reads `stale` and `staleReason` over GraphQL instead
	// and renders its own, so putting it in the body there would say it twice.
	if revision != nil {
		delivered := *revision
		delivered.Body = service.DeliverBody(*doc, delivered.Body)
		revision = &delivered
	}
	return result{http.StatusOK, map[string]any{"document": doc, "revision": revision}}, nil
}

type putGuidanceBody struct {
	Title       string   `json:"title"`
	Kind        string   `json:"kind"`
	Body        string   `json:"body"`
	Inline      *bool    `json:"inline"`
	Weight      *int32   `json:"weight"`
	SourcePaths []string `json:"source_paths"`
	Project     *string  `json:"project"`
}

func (h *handlers) putGuidance(r *http.Request) (result, error) {
	ctx := r.Context()
	payload, err := decode[putGuidanceBody](r)
	if err != nil {
		return result{}, err
	}
	kind, err := model.ParseGuidanceKind(payload.Kind)
	if err != nil {
		return result{}, err
	}
	project, err := h.writeProject(r, payload.Project)
	if err != nil {
		return result{}, err
	}
	doc, err := service.UpsertGuidance(ctx, h.db, project.ID, service.NewGuidance{
		Slug: r.PathValue("slug"), Title: payload.Title, Kind: kind, Body: payload.Body,
		Inline: payload.Inline, Weight: payload.Weight, SourcePaths: payload.SourcePaths,
		Author: actorOf(r),
	})
	if err != nil {
		return result{}, err
	}
	return result{http.StatusOK, doc}, nil
}

type reviewGuidanceBody struct {
	SourceCommit *string `json:"source_commit"`
	Stale        *bool   `json:"stale"`
	StaleReason  *string `json:"stale_reason"`
	Accepted     bool    `json:"accepted"`
	Project      *string `json:"project"`
}

func (h *handlers) reviewGuidance(r *http.Request) (result, error) {
	ctx := r.Context()
	payload, err := decode[reviewGuidanceBody](r)
	if err != nil {
		return result{}, err
	}
	project, err := h.writeProject(r, payload.Project)
	if err != nil {
		return result{}, err
	}
	doc, err := service.ReviewGuidance(ctx, h.db, project.ID, r.PathValue("slug"), service.ReviewMark{
		SourceCommit: payload.SourceCommit, Stale: payload.Stale, StaleReason: payload.StaleReason,
		Accepted: payload.Accepted, ReviewedBy: actorOf(r),
	})
	if err != nil {
		return result{}, err
	}
	return result{http.StatusOK, doc}, nil
}

// --- claiming ---------------------------------------------------------------

type claimBody struct {
	Area    *string    `json:"area"`
	BatchID *uuid.UUID `json:"batch_id"`
	Project *string    `json:"project"`
}

// claim mirrors the MCP tool, including the guidance envelope and the claim record. Without it, the
// REST path that CLAUDE.md tells agents about would hand out work with none of the context and none
// of the attribution.
func (h *handlers) claim(r *http.Request) (result, error) {
	ctx := r.Context()
	payload, err := decode[claimBody](r)
	if err != nil {
		return result{}, err
	}
	project, err := h.writeProject(r, payload.Project)
	if err != nil {
		return result{}, err
	}
	claimed, err := service.ClaimNextTicket(ctx, h.db, project, service.ClaimRequest{
		Area:   payload.Area,
		Batch:  payload.BatchID,
		Actor:  actorOf(r),
		RunTag: r.Header.Get("X-Eval-Run"),
		Arm:    r.Header.Get("X-Eval-Arm"),
		Model:  r.Header.Get("X-Eval-Model"),
	})
	if err != nil {
		return result{}, err
	}
	if claimed == nil {
		return result{http.StatusOK, nil}, nil
	}
	out := map[string]any{
		"ticket": claimed.View.ToJSON(), "area_detail": claimed.View.Area, "batch": claimed.Batch,
		"criteria": model.NonNil(claimed.Criteria), "checks": model.NonNil(claimed.Checks),
		"next_steps": service.ClaimSteps(claimed.Batch, &claimed.View.Ticket),
	}
	// Omitted rather than served empty, so that "guidance" present means guidance was served —
	// the same rule the MCP envelope follows, because the eval runner asserts on both.
	if len(claimed.Guidance.Docs) > 0 || len(claimed.Guidance.Omitted) > 0 {
		out["guidance"] = claimed.Guidance
	}
	return result{http.StatusOK, out}, nil
}

// writeProject resolves the project a write names: the body's "project", or ?project= when the body
// has none, refusing a request whose two disagree. Writes used to read the body alone, so a caller
// who put the project in the query wrote into the oldest project without a word: FS's guidance was
// saved into TP twice (TP-358), and an FS batch "did not exist" for add_to_batch.
func (h *handlers) writeProject(r *http.Request, bodyKey *string) (*model.Project, error) {
	queryKey := r.URL.Query().Get("project")
	switch {
	case bodyKey == nil && queryKey != "":
		bodyKey = &queryKey
	case bodyKey != nil && queryKey != "" && !strings.EqualFold(*bodyKey, queryKey):
		return nil, apperr.BadRequestf("the body names project %q but the query names %q", *bodyKey, queryKey)
	}
	return service.ResolveProject(r.Context(), h.db, bodyKey)
}

// project resolves ?project= for the read endpoints that take it as a query parameter.
func (h *handlers) project(r *http.Request) (*model.Project, error) {
	var key *string
	if v := r.URL.Query().Get("project"); v != "" {
		key = &v
	}
	return service.ResolveProject(r.Context(), h.db, key)
}

// --- areas ------------------------------------------------------------------

func (h *handlers) listAreas(r *http.Request) (result, error) {
	ctx := r.Context()
	q := r.URL.Query()
	includeArchived := false
	switch q.Get("include_archived") {
	case "1", "true", "yes":
		includeArchived = true
	}
	var projectKey *string
	if key := q.Get("project"); key != "" {
		projectKey = &key
	}
	project, err := h.writeProject(r, projectKey)
	if err != nil {
		return result{}, err
	}
	areas, err := service.ListAreas(ctx, h.db, project.ID, includeArchived)
	if err != nil {
		return result{}, err
	}
	return result{http.StatusOK, areas}, nil
}
