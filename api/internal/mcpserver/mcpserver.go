// Package mcpserver is the MCP surface at /mcp, over streamable HTTP. Like rest and graph it is a
// thin shell: every rule lives in service.
//
// One tool group per file, named for the service it shells for, which is how internal/rest is laid
// out too: tickets.go, planning.go, guidance.go, ownership.go and reviews.go. This file holds only
// what all of them share — the instructions, the handler, and the reply and error helpers.
//
// Each group declares its own argument types beside its tools. The SDK infers each tool's JSON
// schema from those structs, so the `jsonschema` tags are the tool documentation an agent actually
// sees. Enums are plain strings there and validated in the handler, which keeps one definition of
// each enum (internal/model) rather than a schema-shaped copy.
package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/joelshore/ticketing/api/internal/apperr"
	"github.com/joelshore/ticketing/api/internal/service"
)

// instructions is sent once, at initialize, and lands in the connected agent's system prompt for
// the whole session. That makes it the most expensive real estate this server owns — paid on every
// turn whether or not a ticket is ever claimed.
//
// So the rule is: ROUTING AND INVARIANTS, NEVER CONTENT. Anything true only sometimes, or that an
// agent can look up, belongs in a guidance document instead. Conventions, branch naming and commit
// format are deliberately absent: they change, and this string cannot change without a server
// restart AND an agent reconnect. Keep it under 500 tokens.
const instructions = `Ticketing is this project's work queue and the source of truth for what needs
doing. Every rule below is enforced server-side: a tool error is the authoritative answer, not a
suggestion.

A work session:
1. claim_next_ticket takes the next workable ticket and moves it to in_progress. A planned batch is
   handed out in order, and a roadmap's next batch waits for a human checkpoint; when a claim
   returns null, get_batch says what is in the way. Read the response in full before writing code —
   it carries the conventions that apply to this ticket, and those, not your defaults, are how this
   project is written.
2. Do the work.
3. update_ticket with the ticket's current "version". Every write takes it; a version conflict means
   the ticket moved, so get_ticket and retry. Entering done or closed needs a non-empty "resolution"
   saying what changed. link_pull_request attaches the PR.

Filing rather than fixing: start_batch, then report_findings, then finish_batch. Findings are deduped
by fingerprint and routed to an area by their file paths. list_areas shows each area, what it covers
and which paths belong to it. Work decided in advance is plan_batch: tickets in order, an eval ticket
last.

Everything is scoped by project. Omit "project" and you get the oldest. A ticket key names its own
project, so tools taking "key" never need one.

Enum values are lowercase snake_case everywhere. Status moves through a fixed transition table; when
a move is refused the error names the legal ones.`

// Handler builds the MCP server and returns an http.Handler for it.
func Handler(db service.DB) http.Handler {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "ticketing",
		Version: "1.0.0",
	}, &mcp.ServerOptions{Instructions: instructions})
	server.AddReceivingMiddleware(dropActorArgument)
	register(server, db)
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
}

// register mounts every tool. The order the groups go in is not part of the surface — the SDK
// sorts tools/list by name — so it is the reading order, not a contract.
func register(s *mcp.Server, db service.DB) {
	registerTickets(s, db)
	registerPlanning(s, db)
	registerGuidance(s, db)
	registerOwnership(s, db)
	registerReviews(s, db)
}

// --- helpers ----------------------------------------------------------------

// actorOf is rule 10: the actor app's middleware settled, defaulting to claude-code for MCP. It
// comes off this call's HTTP header rather than its context, because a tool handler's context
// descends from the session's *initialize* request — a later call's credential is not in it
// (TP-318). With login on the middleware has already overwritten the header with the credential's
// actor, so this is never what a caller typed.
func actorOf(req *mcp.CallToolRequest) string {
	if req == nil || req.Extra == nil {
		return "claude-code"
	}
	return service.ActorOr(req.Extra.Header.Get("X-Actor"), "claude-code")
}

// dropActorArgument deletes an `actor` key from tools/call arguments before the SDK validates them.
// Every tool's Actor field is gone, and jsonschema-go marks each argument struct
// additionalProperties:false, so a caller still sending one — an agent, a script, another project's
// client — would otherwise be refused outright. The credential names the actor now, so a stale
// argument is ignored rather than fatal.
func dropActorArgument(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		var args map[string]json.RawMessage
		call, isCall := req.(*mcp.CallToolRequest)
		if isCall && call.Params != nil &&
			json.Unmarshal(call.Params.Arguments, &args) == nil && args["actor"] != nil {
			delete(args, "actor")
			// Cannot fail: every value came out of the Unmarshal above.
			call.Params.Arguments, _ = json.Marshal(args)
		}
		return next(ctx, method, req)
	}
}

// reply renders a value as the JSON text an agent reads, or turns an error into a tool error.
func reply(value any, err error) (*mcp.CallToolResult, any, error) {
	if err != nil {
		return fail(err)
	}
	body, marshalErr := json.MarshalIndent(value, "", "  ")
	if marshalErr != nil {
		return fail(apperr.Internalf("could not encode the result: %v", marshalErr))
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(body)}},
	}, nil, nil
}

// fail reports a caller error as a tool error rather than a protocol error, so the agent sees the
// message and can correct itself.
func fail(err error) (*mcp.CallToolResult, any, error) {
	e := apperr.As(err)
	if e.Kind == apperr.Internal {
		return nil, nil, e
	}
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: e.Message}},
	}, nil, nil
}
