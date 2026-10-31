package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/joelshore/ticketing/api/internal/model"
	"github.com/joelshore/ticketing/api/internal/service"
)

type listAreasArgs struct {
	IncludeArchived *bool   `json:"include_archived,omitempty"`
	Project         *string `json:"project,omitempty"`
}

type listTagsArgs struct {
	Project *string `json:"project,omitempty"`
}

type proposeTicketEditArgs struct {
	Key       string `json:"key"`
	Field     string `json:"field" jsonschema:"intent | constraints | non_goals | criteria"`
	Proposed  string `json:"proposed" jsonschema:"the text you propose"`
	Rationale string `json:"rationale" jsonschema:"why, with evidence"`
}

type recordVerdictArgs struct {
	Key string `json:"key"`
	service.VerdictRequest
}

type recordDecisionArgs struct {
	Key string `json:"key"`
	service.DecisionRequest
}

// registerOwnership is the boundary between what an agent owns and what the human owns. The two
// managed lists are read-only here — an area or a tag is created over GraphQL — and Section A of a
// ticket can only be proposed, never written. What an agent does own is its own record: the
// decision it made and the verdict it reached.
func registerOwnership(s *mcp.Server, db service.DB) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "list_areas",
		Description: "The areas work is routed to, each with its description, path globs and open " +
			"ticket count. Use it before report_findings to pick the right area name.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args listAreasArgs) (*mcp.CallToolResult, any, error) {
		project, err := service.ResolveProject(ctx, db, args.Project)
		if err != nil {
			return fail(err)
		}
		areas, err := service.ListAreas(ctx, db, project.ID, model.Deref(args.IncludeArchived))
		return reply(areas, err)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "list_tags",
		Description: "The project's managed tag list. Tag tickets only with these; if work needs a tag " +
			"that is not here, ask the human to add it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args listTagsArgs) (*mcp.CallToolResult, any, error) {
		project, err := service.ResolveProject(ctx, db, args.Project)
		if err != nil {
			return fail(err)
		}
		tags, err := service.ListTags(ctx, db, project.ID, false)
		return reply(tags, err)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "propose_ticket_edit",
		Description: "Propose a change to a ticket's human-owned section: intent, constraints, non_goals or " +
			"criteria. You cannot edit those; a human reads the proposal and applies it or not. Nothing " +
			"changes until they do.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args proposeTicketEditArgs) (*mcp.CallToolResult, any, error) {
		project, err := service.ResolveProjectForTicket(ctx, db, args.Key)
		if err != nil {
			return fail(err)
		}
		view, err := service.RequireTicket(ctx, db, project.ID, args.Key)
		if err != nil {
			return fail(err)
		}
		comment, err := service.ProposeEdit(ctx, db, view.Ticket.ID, args.Field, args.Proposed, args.Rationale, actorOf(req))
		return reply(comment, err)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "record_verdict",
		Description: "Judge one success criterion of a finished ticket against the work: pass, fail, or " +
			"deviation (met differently on purpose). Fail and deviation need a reason. A fail files a " +
			"follow-up ticket, placed before the batch's eval when it still can be.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args recordVerdictArgs) (*mcp.CallToolResult, any, error) {
		v, err := args.VerdictRequest.Parse()
		if err != nil {
			return fail(err)
		}
		project, err := service.ResolveProjectForTicket(ctx, db, args.Key)
		if err != nil {
			return fail(err)
		}
		out, err := service.RecordVerdict(ctx, db, project, args.Key, args.Ordinal, v, actorOf(req))
		return reply(out, err)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "record_decision",
		Description: "Record your decision about a ticket: accepted, deferred (with the revisit_trigger that " +
			"would bring it back), rejected, or reframed (with a rationale). Cite evidence_refs; a decision " +
			"that cites nothing is flagged to the human as uncited. Never edited: a new decision supersedes " +
			"the current one.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args recordDecisionArgs) (*mcp.CallToolResult, any, error) {
		d, err := args.DecisionRequest.Parse()
		if err != nil {
			return fail(err)
		}
		project, err := service.ResolveProjectForTicket(ctx, db, args.Key)
		if err != nil {
			return fail(err)
		}
		view, err := service.RequireTicket(ctx, db, project.ID, args.Key)
		if err != nil {
			return fail(err)
		}
		dec, err := service.RecordDecision(ctx, db, view.Ticket.ID, service.MadeByAI, d, actorOf(req))
		return reply(dec, err)
	})
}
