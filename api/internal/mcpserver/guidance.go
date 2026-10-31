package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/joelshore/ticketing/api/internal/model"
	"github.com/joelshore/ticketing/api/internal/service"
)

type getGuidanceArgs struct {
	Slug    *string `json:"slug,omitempty" jsonschema:"one document by slug; omit to get every document for the project"`
	Project *string `json:"project,omitempty"`
}

type reportGuidanceDriftArgs struct {
	Slug     string  `json:"slug" jsonschema:"the guidance document that is wrong"`
	Problem  string  `json:"problem" jsonschema:"what the document says that the code no longer does"`
	Evidence *string `json:"evidence,omitempty" jsonschema:"the file, symbol or command that proves it"`
	Project  *string `json:"project,omitempty"`
}

// registerGuidance is the corpus an agent reads and the one way it can answer back. Writing a
// document is the human's and lives in REST and GraphQL; all an agent can do here is read one and
// say it is wrong.
func registerGuidance(s *mcp.Server, db service.DB) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "get_guidance",
		Description: "The standing guidance for this project: how it is written and why. " +
			"claim_next_ticket already attaches whatever applies to the ticket it hands you — use " +
			"this to read anything it listed under guidance.omitted, or to learn the conventions " +
			"before you have a ticket at all.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args getGuidanceArgs) (*mcp.CallToolResult, any, error) {
		project, err := service.ResolveProject(ctx, db, args.Project)
		if err != nil {
			return fail(err)
		}
		if args.Slug != nil {
			doc, revision, err := service.GetGuidance(ctx, db, project.ID, *args.Slug)
			if err != nil {
				return fail(err)
			}
			body := ""
			var number int32
			if revision != nil {
				body, number = service.DeliverBody(*doc, revision.Body), revision.Revision
			}
			return reply(map[string]any{
				"slug": doc.Slug, "title": doc.Title, "kind": doc.Kind,
				"revision": number, "stale": doc.Stale, "body": body,
			}, nil)
		}
		docs, err := service.ListGuidance(ctx, db, project.ID)
		return reply(docs, err)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "report_guidance_drift",
		Description: "File a ticket saying a guidance document no longer matches the code. Call it " +
			"the moment you notice the gap, while you still have both open: nobody downstream can " +
			"see what you just saw. Reporting the same problem twice is free — it is deduped.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args reportGuidanceDriftArgs) (*mcp.CallToolResult, any, error) {
		project, err := service.ResolveProject(ctx, db, args.Project)
		if err != nil {
			return fail(err)
		}
		outcome, err := service.ReportGuidanceDrift(ctx, db, project, service.DriftReport{
			Slug:     args.Slug,
			Problem:  args.Problem,
			Evidence: model.Deref(args.Evidence),
			Actor:    actorOf(req),
		})
		if err != nil {
			return fail(err)
		}
		return reply(map[string]any{
			"key": outcome.View.Ticket.Key, "created": outcome.Created,
			"status": outcome.View.Ticket.Status, "seen_count": outcome.View.Ticket.SeenCount,
		}, nil)
	})
}
