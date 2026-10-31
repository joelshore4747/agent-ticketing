package graph

import (
	"context"

	"github.com/google/uuid"

	"github.com/joelshore/ticketing/api/internal/graph/gqlmodel"
	"github.com/joelshore/ticketing/api/internal/model"
	"github.com/joelshore/ticketing/api/internal/service"
)

// Resolver holds what every resolver needs. gqlgen regenerates schema.resolvers.go but never this
// file, so dependencies belong here.
type Resolver struct {
	DB service.DB
}

// batchTarget is on *Resolver, which both the query and the mutation resolver embed, because the
// query side wants the same project-then-id resolution and used to spell it out again (TP-177).
func (r *Resolver) batchTarget(ctx context.Context, project *string, id string) (*model.Project, uuid.UUID, error) {
	proj, err := service.ResolveProject(ctx, r.DB, project)
	if err != nil {
		return nil, uuid.Nil, gqlErr(err)
	}
	batchID, err := parseID(id)
	if err != nil {
		return nil, uuid.Nil, err
	}
	return proj, batchID, nil
}

func (r *mutationResolver) setTagArchived(ctx context.Context, project *string, id string, archived bool) (*gqlmodel.Tag, error) {
	proj, err := service.ResolveProject(ctx, r.DB, project)
	if err != nil {
		return nil, gqlErr(err)
	}
	tagID, err := parseID(id)
	if err != nil {
		return nil, err
	}
	tag, err := service.SetTagArchived(ctx, r.DB, proj.ID, tagID, archived)
	if err != nil {
		return nil, gqlErr(err)
	}
	return toTag(tag), nil
}
