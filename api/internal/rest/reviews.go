package rest

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/joelshore/ticketing/api/internal/apperr"
	"github.com/joelshore/ticketing/api/internal/model"
	"github.com/joelshore/ticketing/api/internal/service"
)

// Project reviews (goal 2): the reviewer reads the repository and records what it found; the API
// only keeps the memory.

type reviewBody struct {
	Phase   string  `json:"phase"`
	Commit  string  `json:"commit"`
	Body    string  `json:"body"`
	Project *string `json:"project"`
}

func (h *handlers) recordReview(r *http.Request) (result, error) {
	ctx := r.Context()
	payload, err := decode[reviewBody](r)
	if err != nil {
		return result{}, err
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return result{}, apperr.BadRequestf("%q is not a valid id", r.PathValue("id"))
	}
	project, err := h.writeProject(r, payload.Project)
	if err != nil {
		return result{}, err
	}
	review, err := service.RecordReview(ctx, h.db, project.ID, id, payload.Phase, payload.Commit, payload.Body, actorOf(r))
	if err != nil {
		return result{}, err
	}
	return result{http.StatusCreated, review}, nil
}

// reportBatchSize is where scripts/batch-size.sh posts how big a batch's change was. REST only, for
// the same reason as recordUsage: a script measures it, because the API never runs git (rule 22).
type sizeBody struct {
	service.SizeRequest
	Project *string `json:"project"`
}

func (h *handlers) reportBatchSize(r *http.Request) (result, error) {
	ctx := r.Context()
	payload, err := decode[sizeBody](r)
	if err != nil {
		return result{}, err
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return result{}, apperr.BadRequestf("%q is not a valid id", r.PathValue("id"))
	}
	project, err := h.writeProject(r, payload.Project)
	if err != nil {
		return result{}, err
	}
	batch, err := service.ReportBatchSize(ctx, h.db, project.ID, id, payload.SizeRequest)
	if err != nil {
		return result{}, err
	}
	return result{http.StatusOK, batch}, nil
}

func (h *handlers) listReviews(r *http.Request) (result, error) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return result{}, apperr.BadRequestf("%q is not a valid id", r.PathValue("id"))
	}
	reviews, err := service.ListReviews(r.Context(), h.db, id)
	if err != nil {
		return result{}, err
	}
	return result{http.StatusOK, model.NonNil(reviews)}, nil
}

func (h *handlers) projectMemory(r *http.Request) (result, error) {
	project, err := h.project(r)
	if err != nil {
		return result{}, err
	}
	memory, err := service.GetProjectMemory(r.Context(), h.db, project.ID)
	if err != nil {
		return result{}, err
	}
	return result{http.StatusOK, memory}, nil
}
