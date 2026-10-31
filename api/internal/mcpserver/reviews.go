package mcpserver

import (
	"context"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/joelshore/ticketing/api/internal/apperr"
	"github.com/joelshore/ticketing/api/internal/service"
)

type recordReviewArgs struct {
	BatchID string  `json:"batch_id" jsonschema:"the batch this review opens (before) or closes (after)"`
	Phase   string  `json:"phase" jsonschema:"before | after"`
	Commit  string  `json:"commit" jsonschema:"the git sha you reviewed (git rev-parse HEAD)"`
	Body    string  `json:"body" jsonschema:"markdown: structure, architecture, hotspots, debts and stale code, what should change and why"`
	Project *string `json:"project,omitempty"`
}

type projectMemoryArgs struct {
	Project *string `json:"project,omitempty"`
}

// registerReviews is the project's memory: the review that opens or closes a batch, and the last
// one read back. The pair is what keeps a review reading a diff rather than the whole codebase.
func registerReviews(s *mcp.Server, db service.DB) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "get_project_memory",
		Description: "The project memory: the latest after-review, the commit it describes, and what to read " +
			"beside it. Start every review here and read only the diff since that commit, not the whole codebase.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args projectMemoryArgs) (*mcp.CallToolResult, any, error) {
		project, err := service.ResolveProject(ctx, db, args.Project)
		if err != nil {
			return fail(err)
		}
		memory, err := service.GetProjectMemory(ctx, db, project.ID)
		return reply(memory, err)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "record_review",
		Description: "Record the project review that opens (phase before) or closes (phase after) a batch. " +
			"A batch planned with reviews does not start without a before-review and cannot finish without an " +
			"after-review. File what should change as tickets too; the review is the memory, tickets are the work.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args recordReviewArgs) (*mcp.CallToolResult, any, error) {
		project, err := service.ResolveProject(ctx, db, args.Project)
		if err != nil {
			return fail(err)
		}
		id, err := uuid.Parse(args.BatchID)
		if err != nil {
			return fail(apperr.BadRequestf("%q is not a valid batch id", args.BatchID))
		}
		review, err := service.RecordReview(ctx, db, project.ID, id, args.Phase, args.Commit, args.Body, actorOf(req))
		return reply(review, err)
	})
}
