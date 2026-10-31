package rest

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/joelshore/ticketing/api/internal/apperr"
	"github.com/joelshore/ticketing/api/internal/model"
	"github.com/joelshore/ticketing/api/internal/service"
)

// The batch commands over REST, mirroring the MCP tools of the same names. Accepting a batch and
// signing off its checkpoint are human decisions and live only in GraphQL.

type planBatchBody struct {
	Title     string                         `json:"title"`
	What      string                         `json:"what"`
	How       string                         `json:"how"`
	Why       string                         `json:"why"`
	Demo      string                         `json:"demo"`
	Tickets   []service.PlannedTicketRequest `json:"tickets"`
	BatchID   *uuid.UUID                     `json:"batch_id"`
	RoadmapID *uuid.UUID                     `json:"roadmap_id"`
	Sequence  *int32                         `json:"sequence"`
	GitRef    *string                        `json:"git_ref"`
	Project   *string                        `json:"project"`
}

func (h *handlers) planBatch(r *http.Request) (result, error) {
	ctx := r.Context()
	payload, err := decode[planBatchBody](r)
	if err != nil {
		return result{}, err
	}
	project, err := h.writeProject(r, payload.Project)
	if err != nil {
		return result{}, err
	}
	in := service.PlanInput{Title: payload.Title, What: payload.What, How: payload.How, Why: payload.Why,
		Demo: payload.Demo, BatchID: payload.BatchID, RoadmapID: payload.RoadmapID, Sequence: payload.Sequence,
		GitRef: payload.GitRef, Actor: actorOf(r)}
	if in.Tickets, err = service.ParsePlannedTickets(payload.Tickets); err != nil {
		return result{}, err
	}
	plan, err := service.PlanBatch(ctx, h.db, project, in)
	if err != nil {
		return result{}, err
	}
	// Filling a placeholder changes a batch that was already there, so it answers 200, not 201.
	status := http.StatusCreated
	if payload.BatchID != nil {
		status = http.StatusOK
	}
	return batchResponse(r, h.db, &plan.Batch, plan.Tickets, status)
}

type reorderBody struct {
	Keys    []string `json:"keys"`
	Project *string  `json:"project"`
}

func (h *handlers) reorderBatch(r *http.Request) (result, error) {
	payload, err := decode[reorderBody](r)
	if err != nil {
		return result{}, err
	}
	return h.changeBatch(r, payload.Project, func(ctx context.Context, projectID, id uuid.UUID) error {
		_, err := service.ReorderBatch(ctx, h.db, projectID, id, payload.Keys, actorOf(r))
		return err
	})
}

type addToBatchBody struct {
	Key      string  `json:"key"`
	Position *int32  `json:"position"`
	Project  *string `json:"project"`
}

func (h *handlers) addToBatch(r *http.Request) (result, error) {
	payload, err := decode[addToBatchBody](r)
	if err != nil {
		return result{}, err
	}
	return h.changeBatch(r, payload.Project, func(ctx context.Context, projectID, id uuid.UUID) error {
		_, err := service.AddToBatch(ctx, h.db, projectID, id, payload.Key, payload.Position, actorOf(r))
		return err
	})
}

func (h *handlers) removeFromBatch(r *http.Request) (result, error) {
	var projectKey *string
	if v := r.URL.Query().Get("project"); v != "" {
		projectKey = &v
	}
	return h.changeBatch(r, projectKey, func(ctx context.Context, projectID, id uuid.UUID) error {
		_, err := service.RemoveFromBatch(ctx, h.db, projectID, id, r.PathValue("key"), actorOf(r))
		return err
	})
}

// changeBatch runs one change to a batch and answers with the batch as it now stands.
func (h *handlers) changeBatch(r *http.Request, projectKey *string,
	change func(ctx context.Context, projectID, id uuid.UUID) error) (result, error) {

	ctx := r.Context()
	project, err := h.writeProject(r, projectKey)
	if err != nil {
		return result{}, err
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return result{}, apperr.BadRequestf("%q is not a valid id", r.PathValue("id"))
	}
	if err := change(ctx, project.ID, id); err != nil {
		return result{}, err
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

// batchResponse is the one shape a batch is returned in: the batch, its tickets in order, and for
// a plan its progress.
func batchResponse(r *http.Request, db service.DB, batch *model.Batch, views []model.TicketView, status int) (result, error) {
	tickets := make([]model.TicketJSON, 0, len(views))
	for i := range views {
		tickets = append(tickets, views[i].ToJSON())
	}
	out := map[string]any{"batch": batch, "tickets": tickets}
	if batch.Kind == model.BatchPlan {
		progress, err := service.BatchProgress(r.Context(), db, batch, views)
		if err != nil {
			return result{}, err
		}
		out["progress"] = progress
	}
	return result{status, out}, nil
}

// --- roadmaps and tags ---------------------------------------------------------

func (h *handlers) listRoadmaps(r *http.Request) (result, error) {
	project, err := h.project(r)
	if err != nil {
		return result{}, err
	}
	roadmaps, err := service.ListRoadmaps(r.Context(), h.db, project.ID)
	if err != nil {
		return result{}, err
	}
	return result{http.StatusOK, roadmaps}, nil
}

type createRoadmapBody struct {
	Title   string  `json:"title"`
	Spec    string  `json:"spec"`
	Project *string `json:"project"`
}

func (h *handlers) createRoadmap(r *http.Request) (result, error) {
	ctx := r.Context()
	payload, err := decode[createRoadmapBody](r)
	if err != nil {
		return result{}, err
	}
	project, err := h.writeProject(r, payload.Project)
	if err != nil {
		return result{}, err
	}
	roadmap, err := service.CreateRoadmap(ctx, h.db, project.ID, payload.Title, payload.Spec)
	if err != nil {
		return result{}, err
	}
	return result{http.StatusCreated, roadmap}, nil
}

func (h *handlers) getRoadmap(r *http.Request) (result, error) {
	ctx := r.Context()
	project, err := h.project(r)
	if err != nil {
		return result{}, err
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return result{}, apperr.BadRequestf("%q is not a valid id", r.PathValue("id"))
	}
	roadmap, err := service.GetRoadmap(ctx, h.db, project.ID, id)
	if err != nil {
		return result{}, err
	}
	batches, err := service.RoadmapBatches(ctx, h.db, id)
	if err != nil {
		return result{}, err
	}
	return result{http.StatusOK, map[string]any{"roadmap": roadmap, "batches": batches}}, nil
}

func (h *handlers) listTags(r *http.Request) (result, error) {
	project, err := h.project(r)
	if err != nil {
		return result{}, err
	}
	tags, err := service.ListTags(r.Context(), h.db, project.ID, r.URL.Query().Get("include_archived") == "true")
	if err != nil {
		return result{}, err
	}
	return result{http.StatusOK, tags}, nil
}
