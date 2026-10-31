package mcpserver

import (
	"context"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/joelshore/ticketing/api/internal/apperr"
	"github.com/joelshore/ticketing/api/internal/model"
	"github.com/joelshore/ticketing/api/internal/service"
)

type findingLocation struct {
	FilePath  string  `json:"file_path"`
	LineStart *int32  `json:"line_start,omitempty"`
	LineEnd   *int32  `json:"line_end,omitempty"`
	Snippet   *string `json:"snippet,omitempty"`
}

type finding struct {
	Title       string            `json:"title"`
	Description string            `json:"description,omitempty"`
	Type        string            `json:"type" jsonschema:"bug | feature | improvement | chore | research | design | refactor | eval"`
	Priority    *string           `json:"priority,omitempty" jsonschema:"low | medium | high | urgent"`
	Area        *string           `json:"area,omitempty" jsonschema:"area name; omitted findings are routed by their locations"`
	Fingerprint *string           `json:"fingerprint,omitempty"`
	Locations   []findingLocation `json:"locations,omitempty"`
	Tags        []string          `json:"tags,omitempty" jsonschema:"names from list_tags"`
	Goals       []int32           `json:"goals,omitempty" jsonschema:"the key goals (1-9) fixing this would serve"`
}

type reportFindingsArgs struct {
	BatchID  *string   `json:"batch_id,omitempty" jsonschema:"batch uuid returned by start_batch"`
	Findings []finding `json:"findings"`
	Project  *string   `json:"project,omitempty"`
}

type claimNextTicketArgs struct {
	Area    *string `json:"area,omitempty" jsonschema:"restrict the claim to one area name"`
	Batch   *string `json:"batch,omitempty" jsonschema:"restrict the claim to one batch uuid; its tickets are handed out in order"`
	Project *string `json:"project,omitempty"`
}

type updateTicketArgs struct {
	Key         string    `json:"key" jsonschema:"ticket key, e.g. TP-42"`
	Version     int32     `json:"version"`
	Title       *string   `json:"title,omitempty"`
	Description *string   `json:"description,omitempty"`
	Type        *string   `json:"type,omitempty" jsonschema:"bug | feature | improvement | chore | research | design | refactor | eval"`
	Priority    *string   `json:"priority,omitempty" jsonschema:"low | medium | high | urgent"`
	Area        *string   `json:"area,omitempty" jsonschema:"area name"`
	ClearArea   *bool     `json:"clear_area,omitempty" jsonschema:"unset the area"`
	Branch      *string   `json:"branch,omitempty"`
	PRURL       *string   `json:"pr_url,omitempty"`
	Status      *string   `json:"status,omitempty" jsonschema:"triage | open | in_progress | done | closed"`
	Resolution  *string   `json:"resolution,omitempty"`
	Tags        *[]string `json:"tags,omitempty" jsonschema:"replaces the ticket's tags with these names from list_tags; [] clears them"`
	Goals       *[]int32  `json:"goals,omitempty" jsonschema:"replaces the key goals (1-9) the ticket serves"`
	// Section B: your proposal, which a human approves. Editing an approved one clears the approval.
	Approach         *string   `json:"approach,omitempty" jsonschema:"the approach and its trade-offs, short; put citations in evidence_refs"`
	RiskAndRollback  *string   `json:"risk_and_rollback,omitempty"`
	WorkedExample    *string   `json:"worked_example,omitempty" jsonschema:"features: one concrete end-to-end request and response"`
	VerificationPlan *string   `json:"verification_plan,omitempty" jsonschema:"the exact commands you will run, written before the work"`
	EvidenceRefs     *[]string `json:"evidence_refs,omitempty" jsonschema:"file:line, measurements, ticket keys backing each claim"`
	EstimatedTokens  *int32    `json:"estimated_tokens,omitempty"`
	EstimatedCostUSD *float64  `json:"estimated_cost_usd,omitempty"`
	// Section C: what you report.
	VerifiedBy      *string `json:"verified_by,omitempty" jsonschema:"the command you ran and its output, naming the test"`
	MeasuredDelta   *string `json:"measured_delta,omitempty" jsonschema:"required when the ticket claimed an improvement"`
	SummaryForHuman *string `json:"summary_for_human,omitempty" jsonschema:"one plain-English line, 280 characters at most"`
}

type linkPullRequestArgs struct {
	Key    string  `json:"key"`
	URL    string  `json:"url"`
	Number *int32  `json:"number,omitempty"`
	Branch *string `json:"branch,omitempty"`
	State  *string `json:"state,omitempty" jsonschema:"open | merged | closed"`
}

type addCommentArgs struct {
	Key  string `json:"key"`
	Body string `json:"body"`
}

type listTicketsArgs struct {
	Source       *string  `json:"source,omitempty" jsonschema:"ai | human"`
	Status       []string `json:"status,omitempty" jsonschema:"one or more of triage | open | in_progress | done | closed"`
	Type         *string  `json:"type,omitempty" jsonschema:"bug | feature | improvement | chore | research | design | refactor | eval"`
	Area         *string  `json:"area,omitempty" jsonschema:"area name"`
	Priority     *string  `json:"priority,omitempty" jsonschema:"low | medium | high | urgent"`
	CreatedAfter *string  `json:"created_after,omitempty" jsonschema:"RFC 3339 instant; only tickets filed at or after it"`
	UpdatedAfter *string  `json:"updated_after,omitempty" jsonschema:"RFC 3339 instant; only tickets touched at or after it"`
	Search       *string  `json:"search,omitempty"`
	Tag          *string  `json:"tag,omitempty" jsonschema:"one tag name from list_tags"`
	Limit        *int64   `json:"limit,omitempty"`
	Offset       *int64   `json:"offset,omitempty"`
	Project      *string  `json:"project,omitempty"`
}

type getTicketArgs struct {
	Key     string  `json:"key"`
	Project *string `json:"project,omitempty"`
}

// registerTickets is the work itself: filing it, claiming it, reporting on it and reading it back.
// The human's own moves on a ticket — leaving triage, editing Section A — are GraphQL's and are not
// here.
func registerTickets(s *mcp.Server, db service.DB) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "report_findings",
		Description: "Report AI findings as tickets, in one atomic batch. Each finding is routed to an " +
			"area by its file paths and deduped by fingerprint.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args reportFindingsArgs) (*mcp.CallToolResult, any, error) {
		project, err := service.ResolveProject(ctx, db, args.Project)
		if err != nil {
			return fail(err)
		}
		var batchID *uuid.UUID
		if args.BatchID != nil {
			id, err := uuid.Parse(*args.BatchID)
			if err != nil {
				return fail(apperr.BadRequestf("%q is not a valid batch id", *args.BatchID))
			}
			batchID = &id
		}
		actor := actorOf(req)

		tickets := make([]service.NewTicket, 0, len(args.Findings))
		for _, f := range args.Findings {
			ticketType, priority, err := service.ParseTypeAndPriority(f.Type, f.Priority)
			if err != nil {
				return fail(err)
			}
			locations := make([]service.NewLocation, 0, len(f.Locations))
			for _, l := range f.Locations {
				locations = append(locations, service.NewLocation{
					FilePath: l.FilePath, LineStart: l.LineStart, LineEnd: l.LineEnd, Snippet: l.Snippet,
				})
			}
			var area *service.AreaSelector
			if f.Area != nil {
				area = service.AreaByName(*f.Area)
			}
			tickets = append(tickets, service.NewTicket{
				Title:       f.Title,
				Description: f.Description,
				Type:        ticketType,
				Priority:    priority,
				Source:      model.SourceAI,
				Area:        area,
				Fingerprint: f.Fingerprint,
				Locations:   locations,
				Tags:        f.Tags,
				Goals:       f.Goals,
				BatchID:     batchID,
				Actor:       actor,
			})
		}
		outcomes, err := service.ReportFindings(ctx, db, project, tickets)
		if err != nil {
			return fail(err)
		}
		type outcomeJSON struct {
			Key     string       `json:"key"`
			Created bool         `json:"created"`
			Status  model.Status `json:"status"`
		}
		out := make([]outcomeJSON, 0, len(outcomes))
		for _, o := range outcomes {
			out = append(out, outcomeJSON{o.Key, o.Created, o.Status})
		}
		return reply(out, nil)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "claim_next_ticket",
		Description: "Claim the highest-priority open ticket and move it to in_progress. The response " +
			"carries the batch's brief and its guidance documents: read them before writing code, " +
			"because they, not your defaults, are how this project is written. A planned batch is " +
			"handed out in order: a ticket waits until the ones before it are done or closed, and a " +
			"roadmap's next batch waits for the previous one's checkpoint. Returns null when nothing " +
			"is claimable.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args claimNextTicketArgs) (*mcp.CallToolResult, any, error) {
		project, err := service.ResolveProject(ctx, db, args.Project)
		if err != nil {
			return fail(err)
		}
		var batch *uuid.UUID
		if args.Batch != nil {
			id, err := uuid.Parse(*args.Batch)
			if err != nil {
				return fail(apperr.BadRequestf("%q is not a valid batch id", *args.Batch))
			}
			batch = &id
		}
		result, err := service.ClaimNextTicket(ctx, db, project, claimRequest(req, args, batch))
		if err != nil {
			return fail(err)
		}
		if result == nil {
			return reply(nil, nil)
		}
		return reply(claimEnvelope(result), nil)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "update_ticket",
		Description: "Update a ticket by key. Requires the current version; status changes go through the transition table. " +
			"Moving to done needs the evidence the ticket's type asks for, in the same call if you like: verified_by " +
			"(the command you ran and its output) for most types, plus worked_example for a feature; evidence_refs " +
			"for research; an approved approach for design. The refusal names anything missing.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args updateTicketArgs) (*mcp.CallToolResult, any, error) {
		// A key names its project, so this tool needs no project argument.
		project, err := service.ResolveProjectForTicket(ctx, db, args.Key)
		if err != nil {
			return fail(err)
		}
		current, err := service.RequireTicket(ctx, db, project.ID, args.Key)
		if err != nil {
			return fail(err)
		}

		patch := service.TicketPatch{
			Title:       args.Title,
			Description: args.Description,
			Branch:      args.Branch,
			PRURL:       args.PRURL,
			Resolution:  args.Resolution,
			Tags:        args.Tags,
			Goals:       args.Goals,

			Approach: args.Approach, RiskAndRollback: args.RiskAndRollback, WorkedExample: args.WorkedExample,
			VerificationPlan: args.VerificationPlan, EvidenceRefs: args.EvidenceRefs,
			EstimatedTokens: args.EstimatedTokens, EstimatedCostUSD: args.EstimatedCostUSD,
			VerifiedBy: args.VerifiedBy, MeasuredDelta: args.MeasuredDelta, SummaryForHuman: args.SummaryForHuman,
		}
		if err := service.ParsePatchEnums(&patch, args.Type, args.Priority, args.Status); err != nil {
			return fail(err)
		}
		switch {
		case args.ClearArea != nil && *args.ClearArea:
			patch.Area = service.ClearArea()
		case args.Area != nil:
			patch.Area = service.SetArea(service.AreaByName(*args.Area))
		}

		view, err := service.UpdateTicket(ctx, db, current.Ticket.ID, args.Version, patch, actorOf(req))
		if err != nil {
			return fail(err)
		}
		return reply(view.ToJSON(), nil)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "link_pull_request",
		Description: "Attach a pull request URL to a ticket.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args linkPullRequestArgs) (*mcp.CallToolResult, any, error) {
		project, err := service.ResolveProjectForTicket(ctx, db, args.Key)
		if err != nil {
			return fail(err)
		}
		current, err := service.RequireTicket(ctx, db, project.ID, args.Key)
		if err != nil {
			return fail(err)
		}
		state := model.PRStateOpen
		if args.State != nil {
			if state, err = model.ParsePullRequestState(*args.State); err != nil {
				return fail(err)
			}
		}
		view, err := service.LinkPullRequest(ctx, db, current.Ticket.ID, args.URL, args.Number,
			args.Branch, state, actorOf(req))
		if err != nil {
			return fail(err)
		}
		return reply(view.ToJSON(), nil)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "add_comment",
		Description: "Add a markdown comment to a ticket.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args addCommentArgs) (*mcp.CallToolResult, any, error) {
		project, err := service.ResolveProjectForTicket(ctx, db, args.Key)
		if err != nil {
			return fail(err)
		}
		current, err := service.RequireTicket(ctx, db, project.ID, args.Key)
		if err != nil {
			return fail(err)
		}
		comment, err := service.AddComment(ctx, db, current.Ticket.ID, actorOf(req), args.Body)
		return reply(comment, err)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_tickets",
		Description: "List tickets with the same filters as GET /api/tickets.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args listTicketsArgs) (*mcp.CallToolResult, any, error) {
		project, err := service.ResolveProject(ctx, db, args.Project)
		if err != nil {
			return fail(err)
		}
		// The status list arrives already nested, so unlike REST nothing is split here.
		filter, err := service.ParseTicketFilter(service.TicketFilterInput{Status: args.Status,
			Source: args.Source, Type: args.Type, Priority: args.Priority, Area: args.Area,
			Tag: args.Tag, CreatedAfter: args.CreatedAfter, UpdatedAfter: args.UpdatedAfter})
		if err != nil {
			return fail(err)
		}

		limit, offset := service.DefaultLimit, model.Deref(args.Offset)
		if args.Limit != nil {
			limit = *args.Limit
		}
		views, total, err := service.ListTickets(ctx, db, project.ID, filter,
			model.Deref(args.Search), limit, offset)
		if err != nil {
			return fail(err)
		}
		items := make([]model.TicketJSON, 0, len(views))
		for i := range views {
			items = append(items, views[i].ToJSON())
		}
		return reply(model.TicketPageJSON{Items: items, Total: total}, nil)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_ticket",
		Description: "Fetch one ticket with its comments, events, locations and pull requests.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args getTicketArgs) (*mcp.CallToolResult, any, error) {
		project, err := service.ResolveProject(ctx, db, args.Project)
		if err != nil {
			return fail(err)
		}
		view, err := service.RequireTicket(ctx, db, project.ID, args.Key)
		if err != nil {
			return fail(err)
		}
		id := view.Ticket.ID
		comments, err := service.ListComments(ctx, db, id)
		if err != nil {
			return fail(err)
		}
		events, err := service.ListEvents(ctx, db, id)
		if err != nil {
			return fail(err)
		}
		locations, err := service.ListLocations(ctx, db, id)
		if err != nil {
			return fail(err)
		}
		prs, err := service.ListPullRequests(ctx, db, id)
		if err != nil {
			return fail(err)
		}
		criteria, err := service.ListCriteria(ctx, db, id)
		if err != nil {
			return fail(err)
		}
		checks, err := service.ListPublicChecks(ctx, db, id)
		if err != nil {
			return fail(err)
		}
		decisions, err := service.ListDecisions(ctx, db, id)
		if err != nil {
			return fail(err)
		}
		verdicts, err := service.ListVerdicts(ctx, db, id)
		if err != nil {
			return fail(err)
		}
		return reply(ticketDetailJSON{
			Verdicts:     model.NonNil(verdicts),
			TicketJSON:   view.ToJSON(),
			Criteria:     model.NonNil(criteria),
			Checks:       model.NonNil(checks),
			Decisions:    model.NonNil(decisions),
			Comments:     comments,
			Events:       events,
			Locations:    locations,
			PullRequests: prs,
		}, nil)
	})
}

// --- the claim envelope -----------------------------------------------------

// claimResultJSON is the ticket a claim returns, plus the context for doing the work. The embedded
// TicketJSON is anonymous, which is Go's serde(flatten): every field the tool returned before is
// still at the top level, so this is additive for anything already reading it.
//
// It lives here rather than in model because it is MCP's shape alone. Widening model.TicketJSON
// would change GET /api/tickets for every REST and GraphQL consumer to buy one field.
type claimResultJSON struct {
	model.TicketJSON
	// The human's success criteria and the public checks: what done means, read before the work.
	Criteria   []model.TicketCriterion  `json:"criteria"`
	Checks     []model.TicketCheck      `json:"checks"`
	Decisions  []model.Decision         `json:"decisions"`
	Verdicts   []model.CriterionVerdict `json:"verdicts"`
	AreaDetail *model.Area              `json:"area_detail,omitempty"`
	Batch      *batchJSON               `json:"batch,omitempty"`
	Guidance   *model.GuidanceBundle    `json:"guidance,omitempty"`
	NextSteps  []string                 `json:"next_steps,omitempty"`
}

// ticketDetailJSON is get_ticket's shape. The ticket's own fields sit at the top level, exactly
// where claim_next_ticket puts them: the instructions send an agent from one tool to the other on a
// version conflict, and `.version` quietly becoming `.ticket.version` halfway through a recovery is
// a trap the agent has no reason to expect.
type ticketDetailJSON struct {
	model.TicketJSON
	Criteria     []model.TicketCriterion  `json:"criteria"`
	Checks       []model.TicketCheck      `json:"checks"`
	Decisions    []model.Decision         `json:"decisions"`
	Verdicts     []model.CriterionVerdict `json:"verdicts"`
	Comments     []model.Comment          `json:"comments"`
	Events       []model.TicketEvent      `json:"events"`
	Locations    []model.TicketLocation   `json:"locations"`
	PullRequests []model.PullRequest      `json:"pull_requests"`
}

type batchJSON struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Title string `json:"title,omitempty"`
	What  string `json:"what,omitempty"`
	How   string `json:"how,omitempty"`
	Brief string `json:"brief,omitempty"`
}

func claimEnvelope(result *service.ClaimResult) claimResultJSON {
	out := claimResultJSON{TicketJSON: result.View.ToJSON(),
		Criteria: model.NonNil(result.Criteria), Checks: model.NonNil(result.Checks),
		Decisions: model.NonNil(result.Decisions), Verdicts: model.NonNil(result.Verdicts)}
	// The area's description and globs are already loaded and were being thrown away by the
	// projection to {id, name}. An agent that knows its ticket is in "api" still has no idea what
	// "api" covers.
	out.AreaDetail = result.View.Area
	if result.Batch != nil {
		out.Batch = &batchJSON{
			ID:    result.Batch.ID.String(),
			Kind:  string(result.Batch.Kind),
			Title: result.Batch.Title,
			What:  result.Batch.What,
			How:   result.Batch.How,
			Brief: result.Batch.Brief,
		}
	}
	if len(result.Guidance.Docs) > 0 || len(result.Guidance.Omitted) > 0 {
		out.Guidance = &result.Guidance
	}

	// Generated from the ticket's real state, so it names the real key and the version that is
	// current right now. The cheapest available correction for "the agent forgot the last step".
	ticket := result.View.Ticket
	steps := []string{}
	if out.Guidance != nil && len(result.Guidance.Docs) > 0 {
		steps = append(steps, "Read every body under guidance.docs before editing code.")
	}
	for _, omitted := range result.Guidance.Omitted {
		steps = append(steps, "Fetch the guidance you did not receive: "+omitted.Fetch)
	}
	// The git steps and a finish step that names the evidence rule 21 will ask for.
	steps = append(steps, service.ClaimSteps(result.Batch, &ticket)...)
	out.NextSteps = steps
	return out
}

// claimRequest reads the facts the transport knows rather than asking the agent for them. The
// session id and client come from the MCP handshake; the run tag and arm come from HTTP headers the
// evaluation runner sets in .mcp.json, so a run cannot misreport which arm it was in.
func claimRequest(req *mcp.CallToolRequest, args claimNextTicketArgs, batch *uuid.UUID) service.ClaimRequest {
	out := service.ClaimRequest{Area: args.Area, Batch: batch, Actor: actorOf(req)}
	if req == nil {
		return out
	}
	if req.Session != nil {
		out.SessionID = req.Session.ID()
		if params := req.Session.InitializeParams(); params != nil && params.ClientInfo != nil {
			out.ClientName = params.ClientInfo.Name
			out.ClientVersion = params.ClientInfo.Version
		}
	}
	if req.Extra != nil && req.Extra.Header != nil {
		out.RunTag = req.Extra.Header.Get("X-Eval-Run")
		out.Arm = req.Extra.Header.Get("X-Eval-Arm")
		out.Model = req.Extra.Header.Get("X-Eval-Model")
	}
	return out
}
