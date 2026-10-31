package mcpserver

import (
	"context"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/joelshore/ticketing/api/internal/apperr"
	"github.com/joelshore/ticketing/api/internal/model"
	"github.com/joelshore/ticketing/api/internal/service"
)

type startBatchArgs struct {
	Kind    string  `json:"kind" jsonschema:"audit | review | prompt; work decided in advance is plan_batch, not a plan batch opened here"`
	Title   *string `json:"title,omitempty" jsonschema:"a short name for this batch of work"`
	Brief   *string `json:"brief,omitempty" jsonschema:"why this work is being done; served to whoever picks up its tickets"`
	Summary *string `json:"summary,omitempty"`
	Project *string `json:"project,omitempty" jsonschema:"project key; defaults to the oldest project"`
}

type finishBatchArgs struct {
	ID      string  `json:"id" jsonschema:"batch uuid returned by start_batch"`
	Summary *string `json:"summary,omitempty"`
}

type listBatchesArgs struct {
	Project *string `json:"project,omitempty"`
}

type getBatchArgs struct {
	ID      string  `json:"id" jsonschema:"batch uuid"`
	Project *string `json:"project,omitempty"`
}

type planBatchArgs struct {
	Title    string                         `json:"title"`
	What     string                         `json:"what" jsonschema:"what the batch builds, in plain English"`
	How      string                         `json:"how" jsonschema:"how it goes about it"`
	Why      string                         `json:"why" jsonschema:"why it is being done; served to whoever claims its tickets"`
	Demo     string                         `json:"demo" jsonschema:"how a human checks the batch worked in under five minutes, without reading the diff"`
	Tickets  []service.PlannedTicketRequest `json:"tickets" jsonschema:"in the order they must be worked; may be empty for a roadmap placeholder"`
	Batch    *string                        `json:"batch,omitempty" jsonschema:"fill this existing roadmap placeholder instead of creating a batch; its uuid from get_roadmap. Omit roadmap and sequence with it"`
	Roadmap  *string                        `json:"roadmap,omitempty" jsonschema:"roadmap uuid from create_roadmap"`
	Sequence *int32                         `json:"sequence,omitempty" jsonschema:"place in the roadmap; defaults to after the last batch"`
	GitRef   *string                        `json:"git_ref,omitempty" jsonschema:"the batch's integration branch; named for you when omitted"`
	Project  *string                        `json:"project,omitempty"`
}

type batchOrderArgs struct {
	ID      string   `json:"id" jsonschema:"batch uuid"`
	Keys    []string `json:"keys" jsonschema:"every ticket key in the batch, once, in the new order"`
	Project *string  `json:"project,omitempty"`
}

type batchMemberArgs struct {
	ID       string  `json:"id" jsonschema:"batch uuid"`
	Key      string  `json:"key" jsonschema:"ticket key, e.g. TP-42"`
	Position *int32  `json:"position,omitempty" jsonschema:"add_to_batch only; defaults to just before the eval ticket"`
	Project  *string `json:"project,omitempty"`
}

type createRoadmapArgs struct {
	Title   string  `json:"title"`
	Spec    *string `json:"spec,omitempty" jsonschema:"the specification the roadmap follows"`
	Project *string `json:"project,omitempty"`
}

type getRoadmapArgs struct {
	ID      *string `json:"id,omitempty" jsonschema:"roadmap uuid; omit to list every roadmap"`
	Project *string `json:"project,omitempty"`
}

// registerPlanning is the batch commands: opening and closing a batch, planning ordered work,
// changing the order, and reading a roadmap. Accepting a batch and signing off its checkpoint are
// the human's and are not here.
func registerPlanning(s *mcp.Server, db service.DB) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "start_batch",
		Description: "Open a batch to report findings into: a group of work that belongs together. Its " +
			"brief and its guidance documents are served to whoever later claims one of its tickets. " +
			"Work decided in advance is plan_batch, which files the tickets in order and appends the " +
			"eval ticket that gates the batch; kind=plan here is refused.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args startBatchArgs) (*mcp.CallToolResult, any, error) {
		kind, err := model.ParseBatchKind(args.Kind)
		if err != nil {
			return fail(err)
		}
		project, err := service.ResolveProject(ctx, db, args.Project)
		if err != nil {
			return fail(err)
		}
		batch, err := service.StartBatch(ctx, db, project.ID, service.NewBatch{
			Kind:    kind,
			Title:   model.Deref(args.Title),
			Brief:   model.Deref(args.Brief),
			Summary: args.Summary,
			Actor:   actorOf(req),
		})
		return reply(batch, err)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "finish_batch",
		Description: "Close a batch, recording a summary of what it actually did. Write the summary " +
			"for a human reading it later, not for a machine. The reply carries the batch's digest, " +
			"computed from its tickets, decisions and open questions: read it back, because it is what " +
			"the human reads first.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args finishBatchArgs) (*mcp.CallToolResult, any, error) {
		id, err := uuid.Parse(args.ID)
		if err != nil {
			return fail(apperr.BadRequestf("%q is not a valid batch id", args.ID))
		}
		run, err := service.FinishBatch(ctx, db, id, args.Summary)
		if err != nil {
			return fail(err)
		}
		return reply(finishedBatch{Batch: *run, NextSteps: service.FinishSteps(run)}, nil)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_batches",
		Description: "The batches of work in this project, newest first.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args listBatchesArgs) (*mcp.CallToolResult, any, error) {
		project, err := service.ResolveProject(ctx, db, args.Project)
		if err != nil {
			return fail(err)
		}
		batches, err := service.ListBatches(ctx, db, project.ID)
		return reply(batches, err)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "get_batch",
		Description: "One batch with its what/how/why and its tickets in order. For a planned batch it " +
			"also says which ticket is next, why the whole batch may be locked (an earlier roadmap " +
			"batch), and which earlier ticket each waiting one is behind.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args getBatchArgs) (*mcp.CallToolResult, any, error) {
		project, err := service.ResolveProject(ctx, db, args.Project)
		if err != nil {
			return fail(err)
		}
		id, err := uuid.Parse(args.ID)
		if err != nil {
			return fail(apperr.BadRequestf("%q is not a valid batch id", args.ID))
		}
		return batchResult(ctx, db, project.ID, id, nil)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "plan_batch",
		Description: "Plan a batch of work in advance: its what/how/why, the tickets in the order they " +
			"must be worked, and a demo a human can run to check it. An eval ticket is added last " +
			"automatically, and the batch cannot finish until it is done. Tickets land in triage for " +
			"a human to accept. Put the batch in a roadmap to order it after the batches before it, or " +
			"pass `batch` to fill a roadmap placeholder in place: it keeps its id, its place in the " +
			"roadmap and its eval ticket, and the whole plan still lands in one call.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args planBatchArgs) (*mcp.CallToolResult, any, error) {
		project, err := service.ResolveProject(ctx, db, args.Project)
		if err != nil {
			return fail(err)
		}
		in := service.PlanInput{Title: args.Title, What: args.What, How: args.How, Why: args.Why,
			Demo: args.Demo, Sequence: args.Sequence, GitRef: args.GitRef, Actor: actorOf(req)}
		if args.Roadmap != nil {
			id, err := uuid.Parse(*args.Roadmap)
			if err != nil {
				return fail(apperr.BadRequestf("%q is not a valid roadmap id", *args.Roadmap))
			}
			in.RoadmapID = &id
		}
		if args.Batch != nil {
			id, err := uuid.Parse(*args.Batch)
			if err != nil {
				return fail(apperr.BadRequestf("%q is not a valid batch id", *args.Batch))
			}
			in.BatchID = &id
		}
		if in.Tickets, err = service.ParsePlannedTickets(args.Tickets); err != nil {
			return fail(err)
		}
		plan, err := service.PlanBatch(ctx, db, project, in)
		if err != nil {
			return fail(err)
		}
		return reply(batchReply(ctx, db, &plan.Batch, plan.Tickets))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "reorder_batch",
		Description: "Change the order of a planned batch. Give every ticket key once, in the new order. " +
			"Started tickets keep their place and the eval ticket stays last.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args batchOrderArgs) (*mcp.CallToolResult, any, error) {
		project, id, err := batchTarget(ctx, db, args.Project, args.ID)
		if err != nil {
			return fail(err)
		}
		_, err = service.ReorderBatch(ctx, db, project.ID, id, args.Keys, actorOf(req))
		return batchResult(ctx, db, project.ID, id, err)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "add_to_batch",
		Description: "Move a ticket that belongs to no batch into a planned batch, by default just before " +
			"its eval ticket.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args batchMemberArgs) (*mcp.CallToolResult, any, error) {
		project, id, err := batchTarget(ctx, db, args.Project, args.ID)
		if err != nil {
			return fail(err)
		}
		_, err = service.AddToBatch(ctx, db, project.ID, id, args.Key, args.Position, actorOf(req))
		return batchResult(ctx, db, project.ID, id, err)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "remove_from_batch",
		Description: "Take a ticket that has not started out of a planned batch. The eval ticket cannot be removed.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args batchMemberArgs) (*mcp.CallToolResult, any, error) {
		project, id, err := batchTarget(ctx, db, args.Project, args.ID)
		if err != nil {
			return fail(err)
		}
		_, err = service.RemoveFromBatch(ctx, db, project.ID, id, args.Key, actorOf(req))
		return batchResult(ctx, db, project.ID, id, err)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "create_roadmap",
		Description: "Start a roadmap: an ordered list of batches following one specification. Add " +
			"batches to it with plan_batch; each opens only after the one before it has finished and " +
			"a human has signed off its checkpoint.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args createRoadmapArgs) (*mcp.CallToolResult, any, error) {
		project, err := service.ResolveProject(ctx, db, args.Project)
		if err != nil {
			return fail(err)
		}
		roadmap, err := service.CreateRoadmap(ctx, db, project.ID, args.Title, model.Deref(args.Spec))
		return reply(roadmap, err)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "get_roadmap",
		Description: "A roadmap with its batches in sequence, each marked locked, unplanned, open, at_eval, " +
			"awaiting_checkpoint, finished or stopped. Omit id to list the roadmaps.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args getRoadmapArgs) (*mcp.CallToolResult, any, error) {
		project, err := service.ResolveProject(ctx, db, args.Project)
		if err != nil {
			return fail(err)
		}
		if args.ID == nil {
			roadmaps, err := service.ListRoadmaps(ctx, db, project.ID)
			return reply(roadmaps, err)
		}
		id, err := uuid.Parse(*args.ID)
		if err != nil {
			return fail(apperr.BadRequestf("%q is not a valid roadmap id", *args.ID))
		}
		roadmap, err := service.GetRoadmap(ctx, db, project.ID, id)
		if err != nil {
			return fail(err)
		}
		batches, err := service.RoadmapBatches(ctx, db, id)
		return reply(map[string]any{"roadmap": roadmap, "batches": batches}, err)
	})
}

// finishedBatch is finish_batch's reply: the batch, with its digest, and what to run next.
type finishedBatch struct {
	model.Batch
	NextSteps []string `json:"next_steps,omitempty"`
}

func batchTarget(ctx context.Context, db service.DB, projectKey *string, rawID string) (*model.Project, uuid.UUID, error) {
	project, err := service.ResolveProject(ctx, db, projectKey)
	if err != nil {
		return nil, uuid.Nil, err
	}
	id, err := uuid.Parse(rawID)
	if err != nil {
		return nil, uuid.Nil, apperr.BadRequestf("%q is not a valid batch id", rawID)
	}
	return project, id, nil
}

// batchResult answers a change to a batch with the batch as it now stands.
func batchResult(ctx context.Context, db service.DB, projectID, id uuid.UUID, err error) (*mcp.CallToolResult, any, error) {
	if err != nil {
		return fail(err)
	}
	batch, err := service.GetBatch(ctx, db, projectID, id)
	if err != nil {
		return fail(err)
	}
	views, err := service.ListBatchTickets(ctx, db, id)
	if err != nil {
		return fail(err)
	}
	return reply(batchReply(ctx, db, batch, views))
}

// batchReply is the one shape a batch is returned in: the batch, its tickets in order, and for a
// plan its progress.
func batchReply(ctx context.Context, db service.DB, batch *model.Batch, views []model.TicketView) (map[string]any, error) {
	tickets := make([]model.TicketJSON, 0, len(views))
	for i := range views {
		tickets = append(tickets, views[i].ToJSON())
	}
	out := map[string]any{"batch": batch, "tickets": tickets}
	if batch.Kind == model.BatchPlan {
		progress, err := service.BatchProgress(ctx, db, batch, views)
		if err != nil {
			return nil, err
		}
		out["progress"] = progress
	}
	return out, nil
}
