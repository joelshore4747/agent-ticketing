package rest

import (
	"net/http"

	"github.com/joelshore/ticketing/api/internal/model"
	"github.com/joelshore/ticketing/api/internal/service"
)

// The agent's side of the ownership model over REST. Setting the human-owned section, approving an
// approach and confirming a tripwire are GraphQL only.

type proposalBody struct {
	Field     string `json:"field"`
	Proposed  string `json:"proposed"`
	Rationale string `json:"rationale"`
}

func (h *handlers) proposeEdit(r *http.Request) (result, error) {
	ctx := r.Context()
	payload, err := decode[proposalBody](r)
	if err != nil {
		return result{}, err
	}
	key := r.PathValue("key")
	project, err := service.ResolveProjectForTicket(ctx, h.db, key)
	if err != nil {
		return result{}, err
	}
	view, err := service.RequireTicket(ctx, h.db, project.ID, key)
	if err != nil {
		return result{}, err
	}
	comment, err := service.ProposeEdit(ctx, h.db, view.Ticket.ID, payload.Field, payload.Proposed, payload.Rationale, actorOf(r))
	if err != nil {
		return result{}, err
	}
	return result{http.StatusCreated, comment}, nil
}

type tripwireBody struct {
	TestRef    string  `json:"test_ref"`
	Meaning    string  `json:"meaning"`
	BodySHA256 string  `json:"body_sha256"`
	Project    *string `json:"project"`
}

// registerTripwire is how CI or the harness reports a test and its body hash. A changed hash clears
// the human's confirmation; confirming is GraphQL's.
func (h *handlers) registerTripwire(r *http.Request) (result, error) {
	ctx := r.Context()
	payload, err := decode[tripwireBody](r)
	if err != nil {
		return result{}, err
	}
	project, err := h.writeProject(r, payload.Project)
	if err != nil {
		return result{}, err
	}
	tw, err := service.RegisterTripwire(ctx, h.db, project.ID, payload.TestRef, payload.Meaning, payload.BodySHA256)
	if err != nil {
		return result{}, err
	}
	return result{http.StatusOK, tw}, nil
}

func (h *handlers) listTripwires(r *http.Request) (result, error) {
	project, err := h.project(r)
	if err != nil {
		return result{}, err
	}
	tws, err := service.ListTripwires(r.Context(), h.db, project.ID)
	if err != nil {
		return result{}, err
	}
	return result{http.StatusOK, model.NonNil(tws)}, nil
}

// recordDecision is the agent's decision over REST, so it is filed as made_by ai. The human's go
// through GraphQL's recordDecision, acceptTicket and dismissTicket.
func (h *handlers) recordDecision(r *http.Request) (result, error) {
	ctx := r.Context()
	payload, err := decode[service.DecisionRequest](r)
	if err != nil {
		return result{}, err
	}
	d, err := payload.Parse()
	if err != nil {
		return result{}, err
	}
	key := r.PathValue("key")
	project, err := service.ResolveProjectForTicket(ctx, h.db, key)
	if err != nil {
		return result{}, err
	}
	view, err := service.RequireTicket(ctx, h.db, project.ID, key)
	if err != nil {
		return result{}, err
	}
	dec, err := service.RecordDecision(ctx, h.db, view.Ticket.ID, service.MadeByAI, d, actorOf(r))
	if err != nil {
		return result{}, err
	}
	return result{http.StatusCreated, dec}, nil
}

func (h *handlers) uncitedDecisions(r *http.Request) (result, error) {
	project, err := h.project(r)
	if err != nil {
		return result{}, err
	}
	rows, err := service.ListUncitedDecisions(r.Context(), h.db, project.ID)
	if err != nil {
		return result{}, err
	}
	return result{http.StatusOK, model.NonNil(rows)}, nil
}

// needsYou is the human's queue. It is readable by anyone; every item on it is acted on through
// GraphQL, the human's transport.
func (h *handlers) needsYou(r *http.Request) (result, error) {
	project, err := h.project(r)
	if err != nil {
		return result{}, err
	}
	n, err := service.ListNeedsYou(r.Context(), h.db, project.ID)
	if err != nil {
		return result{}, err
	}
	triage := make([]model.TicketJSON, 0, len(n.Triage))
	for i := range n.Triage {
		triage = append(triage, n.Triage[i].ToJSON())
	}
	return result{http.StatusOK, map[string]any{
		"triage": triage, "checkpoints": model.NonNil(n.Checkpoints), "proposals": model.NonNil(n.Proposals),
		"uncited": model.NonNil(n.Uncited), "tripwires": model.NonNil(n.Tripwires), "total": n.Total(),
	}}, nil
}

// recordVerdict judges one criterion of a finished ticket. A fail files its follow-up.
func (h *handlers) recordVerdict(r *http.Request) (result, error) {
	ctx := r.Context()
	payload, err := decode[service.VerdictRequest](r)
	if err != nil {
		return result{}, err
	}
	v, err := payload.Parse()
	if err != nil {
		return result{}, err
	}
	key := r.PathValue("key")
	project, err := service.ResolveProjectForTicket(ctx, h.db, key)
	if err != nil {
		return result{}, err
	}
	out, err := service.RecordVerdict(ctx, h.db, project, key, payload.Ordinal, v, actorOf(r))
	if err != nil {
		return result{}, err
	}
	return result{http.StatusCreated, out}, nil
}

// recordUsage is where a session-end hook reports what a session cost. REST only: it is posted by a
// script, not chosen by the agent, and the API never measures the agent itself.
func (h *handlers) recordUsage(r *http.Request) (result, error) {
	ctx := r.Context()
	payload, err := decode[service.UsageRequest](r)
	if err != nil {
		return result{}, err
	}
	key := r.PathValue("key")
	project, err := service.ResolveProjectForTicket(ctx, h.db, key)
	if err != nil {
		return result{}, err
	}
	usage, err := service.RecordUsage(ctx, h.db, project.ID, key, payload)
	if err != nil {
		return result{}, err
	}
	return result{http.StatusOK, usage}, nil
}
