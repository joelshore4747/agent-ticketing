package app_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/joelshore/ticketing/api/internal/app"
	"github.com/joelshore/ticketing/api/internal/testdb"
)

// The batch commands over REST and GraphQL, and the line between them: planning is the agent's,
// accepting and checkpointing are the human's.

func TestAPlanRunsEndToEndOverRESTAndGraphQL(t *testing.T) {
	h := router(t)

	code, roadmap := send(t, h, "POST", "/api/roadmaps", body{"title": "Evolution", "spec": "the brief"})
	if code != http.StatusCreated {
		t.Fatalf("POST roadmaps = %d: %v", code, roadmap)
	}
	roadmapID := str(t, roadmap, "id")

	code, plan := send(t, h, "POST", "/api/batches/plan", body{
		"title": "Ordering", "what": "an order", "how": "a column", "why": "so work is not skipped",
		"demo": "try to claim ticket 2 first", "roadmap_id": roadmapID,
		"tickets": []body{
			{"title": "Schema for order", "type": "feature", "tags": []string{"architecture"}, "goals": []int{1}},
			{"title": "Rules for order", "type": "feature", "goals": []int{1, 5}},
		},
	})
	if code != http.StatusCreated {
		t.Fatalf("POST plan = %d: %v", code, plan)
	}
	batchID := str(t, plan, "batch", "id")
	first, second, eval := str(t, plan, "tickets", 0, "key"), str(t, plan, "tickets", 1, "key"), str(t, plan, "tickets", 2, "key")
	if str(t, plan, "tickets", 2, "type") != "eval" || str(t, plan, "tickets", 0, "tags", 0) != "architecture" {
		t.Errorf("plan tickets = %v", at(t, plan, "tickets"))
	}
	// Goal 2: a new plan waits on its before-review, and says so.
	if !strings.Contains(str(t, plan, "progress", "locked_by"), "before-review") {
		t.Errorf("a fresh plan's progress = %v", at(t, plan, "progress"))
	}
	reviewed(t, h, batchID)
	_, plan = send(t, h, "GET", "/api/batches/"+batchID, nil)
	if str(t, plan, "progress", "next") != first || str(t, plan, "progress", "blocked_by", second) != first {
		t.Errorf("progress = %v", at(t, plan, "progress"))
	}

	code, reordered := send(t, h, "PUT", "/api/batches/"+batchID+"/order", body{"keys": []string{second, first, eval}})
	if code != http.StatusOK || str(t, reordered, "tickets", 0, "key") != second {
		t.Fatalf("reorder = %d: %v", code, reordered)
	}

	code, loose := send(t, h, "POST", "/api/tickets", body{"title": "Found on the way", "type": "chore"})
	if code != http.StatusCreated {
		t.Fatalf("POST ticket = %d", code)
	}
	code, added := send(t, h, "POST", "/api/batches/"+batchID+"/tickets", body{"key": str(t, loose, "key")})
	if code != http.StatusOK || str(t, added, "tickets", 2, "key") != str(t, loose, "key") {
		t.Fatalf("add = %d: %v", code, added)
	}
	code, removed := send(t, h, "DELETE", "/api/batches/"+batchID+"/tickets/"+str(t, loose, "key"), nil)
	if code != http.StatusOK || len(at(t, removed, "tickets").([]any)) != 3 {
		t.Fatalf("remove = %d: %v", code, removed)
	}

	// The human accepts the batch in one move, over GraphQL.
	out := gql(t, h, `mutation { acceptBatch(id: "`+batchID+`") { what how brief progress { next blockedBy { key behind } } tickets { key status batchPosition tags } } }`)
	if at(t, out, "errors") != nil {
		t.Fatalf("acceptBatch: %v", out)
	}
	for _, tk := range at(t, out, "data", "acceptBatch", "tickets").([]any) {
		if str(t, tk, "status") != "open" {
			t.Errorf("after acceptBatch %v", tk)
		}
	}
	if str(t, out, "data", "acceptBatch", "progress", "next") != second {
		t.Errorf("progress after accept = %v", at(t, out, "data", "acceptBatch", "progress"))
	}

	code, got := send(t, h, "GET", "/api/roadmaps/"+roadmapID, nil)
	if code != http.StatusOK || str(t, got, "batches", 0, "state") != "open" {
		t.Errorf("roadmap = %d: %v", code, got)
	}
	code, tagged := send(t, h, "GET", "/api/tickets?tag=architecture", nil)
	if code != http.StatusOK || num(t, tagged, "total") != 1 {
		t.Errorf("tag filter = %d: %v", code, tagged)
	}

	// A checkpoint comes after the batch finishes, not before.
	out = gql(t, h, `mutation { checkpointBatch(id: "`+batchID+`", decision: continue) { checkpointDecision } }`)
	if at(t, out, "errors") == nil {
		t.Errorf("checkpointing an unfinished batch succeeded: %v", out)
	}
}

func TestTagsAreListedToAgentsAndManagedByTheHuman(t *testing.T) {
	h := router(t)
	code, tags := send(t, h, "GET", "/api/tags", nil)
	if code != http.StatusOK || len(tags.([]any)) != 9 {
		t.Fatalf("GET tags = %d: %v", code, tags)
	}
	out := gql(t, h, `mutation { createTag(name: "ux", description: "How it feels to use") { id name } }`)
	if str(t, out, "data", "createTag", "name") != "ux" {
		t.Fatalf("createTag: %v", out)
	}
	id := str(t, out, "data", "createTag", "id")
	out = gql(t, h, `mutation { archiveTag(id: "`+id+`") { archivedAt } }`)
	if at(t, out, "data", "archiveTag", "archivedAt") == nil {
		t.Errorf("archiveTag: %v", out)
	}
	out = gql(t, h, `{ tags { name } }`)
	if strings.Contains(stringify(out), `"ux"`) {
		t.Errorf("an archived tag is still offered: %v", out)
	}
}

// Planning is the agent's; accepting a batch and signing off its checkpoint are not. MCP must list
// the one and not the other.
func TestMCPOffersPlanningButNotTheHumansMoves(t *testing.T) {
	h := router(t)
	listed := mcpSession(t, h)(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)

	for _, name := range []string{"plan_batch", "reorder_batch", "add_to_batch", "remove_from_batch",
		"create_roadmap", "get_roadmap", "list_tags"} {
		if !strings.Contains(listed, `"`+name+`"`) {
			t.Errorf("MCP does not offer %s", name)
		}
	}
	for _, name := range []string{"accept_batch", "acceptBatch", "checkpoint_batch", "checkpointBatch", "create_tag"} {
		if strings.Contains(listed, `"name":"`+name+`"`) {
			t.Errorf("MCP offers the human's move %s", name)
		}
	}
}

func stringify(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

// REST and MCP parsed a planned ticket's type, priority and area separately, so a field added to
// one was missed by the other (TP-117); they now share service.PlannedTicketRequest. This is the
// tripwire: the same payload must get the same default and the same refusal from both.
func TestAPlannedTicketIsParsedTheSameOverRESTAndMCP(t *testing.T) {
	h := router(t)
	call := mcpSession(t, h)
	// A live ticket of the same title is moved in rather than planned again, so each plan here names
	// its ticket after itself; what is being compared is the parse, not the dedupe.
	plan := func(title, ticketType string) body {
		return body{
			"title": title, "what": "one parse", "how": "a shared request type",
			"why": "a field is added once", "demo": "plan the same batch over both transports",
			"tickets": []body{
				{"title": "Priority left out " + title, "type": ticketType, "goals": []int{3}},
			},
		}
	}

	// The tool's reply quotes its JSON inside JSON; unescaping it once keeps the matches readable.
	overMCP := func(args body) string {
		return strings.ReplaceAll(call(mcpTool("plan_batch", stringify(args))), `\"`, `"`)
	}

	// No priority given: both default it to medium rather than refusing.
	code, rest := send(t, h, "POST", "/api/batches/plan", plan("Over REST", "refactor"))
	if code != http.StatusCreated || str(t, rest, "tickets", 0, "priority") != "medium" {
		t.Fatalf("REST plan = %d: %v", code, rest)
	}
	if out := overMCP(plan("Over MCP", "refactor")); !strings.Contains(out, `"priority": "medium"`) {
		t.Fatalf("MCP plan = %s", out)
	}

	// And the same bad type is refused with the same message.
	code, refused := send(t, h, "POST", "/api/batches/plan", plan("Bad over REST", "epic"))
	if code != http.StatusBadRequest || !strings.Contains(str(t, refused, "error"), `invalid TicketType: "epic"`) {
		t.Fatalf("REST bad type = %d: %v", code, refused)
	}
	if out := overMCP(plan("Bad over MCP", "epic")); !strings.Contains(out, `invalid TicketType: "epic"`) {
		t.Fatalf("MCP bad type = %s", out)
	}
}

// TP-149: a roadmap's later batches are laid out as placeholders, and the Strategist fills the next
// one at the checkpoint. It used to take report_findings plus one add_to_batch per ticket, so the
// work arrived as findings and the plan was split across calls. One plan_batch call now does it.
func TestMCPPlanBatchFillsARoadmapPlaceholder(t *testing.T) {
	h := router(t)
	call := mcpSession(t, h)

	code, roadmap := send(t, h, "POST", "/api/roadmaps", body{"title": "Evolution", "spec": "the brief"})
	if code != http.StatusCreated {
		t.Fatalf("POST roadmaps = %d: %v", code, roadmap)
	}
	code, held := send(t, h, "POST", "/api/batches/plan", body{
		"title": "Batch two, one day", "what": "decided at the checkpoint", "how": "decided at the checkpoint",
		"why": "the roadmap says it comes second", "demo": "to be decided",
		"roadmap_id": str(t, roadmap, "id"), "tickets": []body{},
	})
	if code != http.StatusCreated {
		t.Fatalf("placeholder = %d: %v", code, held)
	}
	batchID, evalKey := str(t, held, "batch", "id"), str(t, held, "tickets", 0, "key")

	out := strings.ReplaceAll(call(mcpTool("plan_batch", stringify(body{
		"batch": batchID, "title": "Batch two, decided", "what": "the wiring",
		"how": "a schema ticket then a rules ticket", "why": "the checkpoint said continue",
		"demo": "run just demo-two and read the count",
		"tickets": []body{
			{"title": "Schema for two", "type": "feature", "goals": []int{1}},
			{"title": "Rules for two", "type": "feature", "goals": []int{1, 5}},
		},
	}))), `\"`, `"`)
	if !strings.Contains(out, `"id": "`+batchID+`"`) {
		t.Fatalf("fill did not answer with the placeholder: %s", out)
	}

	code, filled := send(t, h, "GET", "/api/batches/"+batchID, nil)
	if code != http.StatusOK {
		t.Fatalf("GET batch = %d: %v", code, filled)
	}
	if num(t, filled, "batch", "sequence") != 1 || str(t, filled, "batch", "title") != "Batch two, decided" {
		t.Errorf("the batch moved or kept the placeholder's title: %v", at(t, filled, "batch"))
	}
	tickets, ok := at(t, filled, "tickets").([]any)
	if !ok || len(tickets) != 3 {
		t.Fatalf("tickets = %v, want two planned plus the one eval ticket", at(t, filled, "tickets"))
	}
	if str(t, filled, "tickets", 0, "title") != "Schema for two" || str(t, filled, "tickets", 1, "title") != "Rules for two" {
		t.Errorf("tickets out of order: %v", tickets)
	}
	// The placeholder's own eval ticket is kept, and rewritten for the demo that was just decided.
	if str(t, filled, "tickets", 2, "key") != evalKey || str(t, filled, "tickets", 2, "type") != "eval" {
		t.Errorf("last ticket = %v, want the placeholder's eval ticket", at(t, filled, "tickets", 2))
	}
	if desc := str(t, filled, "tickets", 2, "description"); !strings.Contains(desc, "run just demo-two and read the count") {
		t.Errorf("the eval ticket still carries the placeholder's demo:\n%s", desc)
	}
	// Nothing arrived on the way as a finding outside the plan: the project holds these three only.
	code, all := send(t, h, "GET", "/api/tickets", nil)
	if code != http.StatusOK || num(t, all, "total") != 3 {
		t.Errorf("tickets in the project = %d/%v, want the batch's three", code, at(t, all, "total"))
	}
}

// The instructions sit in every agent's system prompt, so they must describe the queue as it now
// works and stay inside the budget their own comment sets. Phase A changed the queue and missed
// them (TP-69); this is the tripwire for the next change.
func TestMCPInstructionsDescribeTheOrderAndStayInBudget(t *testing.T) {
	h := router(t)
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18",`+
			`"capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	raw := rec.Body.String()
	start := strings.Index(raw, "{")
	var msg struct {
		Result struct {
			Instructions string `json:"instructions"`
		} `json:"result"`
	}
	if start < 0 || json.Unmarshal([]byte(raw[start:strings.LastIndex(raw, "}")+1]), &msg) != nil {
		t.Fatalf("initialize body = %q", raw)
	}
	text := msg.Result.Instructions
	for _, want := range []string{"in order", "get_batch", "plan_batch"} {
		if !strings.Contains(text, want) {
			t.Errorf("instructions do not mention %q", want)
		}
	}
	if tokens := len(text)/4 + 1; tokens > 500 {
		t.Errorf("instructions are ~%d tokens, over their own 500-token budget", tokens)
	}
}

// Rule 17 over the transports: REST and MCP cannot take a ticket out of triage; GraphQL can.
func TestTriageIsLeftOnlyOverGraphQL(t *testing.T) {
	h := router(t)
	code, finding := send(t, h, "POST", "/api/tickets", body{"title": "An AI finding", "type": "bug", "source": "ai"})
	if code != http.StatusCreated || str(t, finding, "status") != "triage" {
		t.Fatalf("POST = %d %v", code, finding)
	}
	key, version := str(t, finding, "key"), num(t, finding, "version")

	code, refused := send(t, h, "PATCH", "/api/tickets/"+key, body{"version": version, "status": "open"})
	if code != http.StatusConflict || !strings.Contains(str(t, refused, "error"), "rule 17") {
		t.Errorf("REST accept = %d %v", code, refused)
	}

	out := mcpSession(t, h)(mcpTool("update_ticket",
		`{"key":"`+key+`","version":`+itoa(version)+`,"status":"open"}`))
	if !strings.Contains(out, "rule 17") || !strings.Contains(out, `"isError":true`) {
		t.Errorf("MCP accept was not refused: %s", out)
	}

	_, ticket := send(t, h, "GET", "/api/tickets/"+key, nil)
	res := gql(t, h, `mutation { acceptTicket(id: "`+str(t, ticket, "id")+`", version: `+itoa(num(t, ticket, "version"))+`) { status } }`)
	if str(t, res, "data", "acceptTicket", "status") != "open" {
		t.Errorf("GraphQL accept: %v", res)
	}
}

func TestTagsAreWrittenOverRESTAndGraphQL(t *testing.T) {
	h := router(t)
	code, created := send(t, h, "POST", "/api/tickets", body{"title": "Tag me", "type": "chore", "tags": []string{"eval"}})
	if code != http.StatusCreated || str(t, created, "tags", 0) != "eval" {
		t.Fatalf("REST create = %d %v", code, created)
	}
	code, patched := send(t, h, "PATCH", "/api/tickets/"+str(t, created, "key"),
		body{"version": num(t, created, "version"), "tags": []string{"consistency", "docs-drift"}})
	if code != http.StatusOK || len(at(t, patched, "tags").([]any)) != 2 {
		t.Fatalf("REST retag = %d %v", code, patched)
	}
	code, refused := send(t, h, "PATCH", "/api/tickets/"+str(t, created, "key"),
		body{"version": num(t, patched, "version"), "tags": []string{"nope"}})
	if code != http.StatusBadRequest || !strings.Contains(str(t, refused, "error"), "managed list") {
		t.Errorf("unknown tag = %d %v", code, refused)
	}
	out := gql(t, h, `mutation { updateTicket(id: "`+str(t, created, "id")+`", version: `+itoa(num(t, patched, "version"))+`, patch: {tags: []}) { tags } }`)
	if tags, _ := at(t, out, "data", "updateTicket", "tags").([]any); tags == nil || len(tags) != 0 {
		t.Errorf("GraphQL clear = %v", out)
	}
}

// TP-302: a plan batch's tickets were the only ones a claim served nothing, because ResolveGuidance
// reads a batch's attachments and plan_batch wrote none — so no claim in a plan batch recorded a
// document and the digest's per-guidance scores had nothing to score.
func TestAClaimInAPlanBatchIsServedGuidance(t *testing.T) {
	pool := testdb.New(t)
	h, err := app.New(pool, "http://localhost:5173", "")
	if err != nil {
		t.Fatalf("build the app: %v", err)
	}
	if code, doc := send(t, h, "PUT", "/api/guidance/go-conventions", body{
		"title": "How Go is written here", "kind": "convention",
		"body": "Business rules live only in internal/service.",
	}); code != http.StatusOK {
		t.Fatalf("PUT guidance = %d: %v", code, doc)
	}

	code, plan := send(t, h, "POST", "/api/batches/plan", body{
		"title": "Guided work", "what": "a batch", "how": "one ticket", "why": "so a claim is guided",
		"demo":    "claim it and read the bundle",
		"tickets": []body{{"title": "The guided ticket", "type": "chore", "goals": []int{4}}},
	})
	if code != http.StatusCreated {
		t.Fatalf("POST plan = %d: %v", code, plan)
	}
	batchID, key := str(t, plan, "batch", "id"), str(t, plan, "tickets", 0, "key")
	reviewed(t, h, batchID)
	if out := gql(t, h, `mutation { acceptBatch(id: "`+batchID+`") { id } }`); at(t, out, "errors") != nil {
		t.Fatalf("acceptBatch: %v", out)
	}

	code, claimed := send(t, h, "POST", "/api/claim", body{"batch_id": batchID})
	if code != http.StatusOK || str(t, claimed, "ticket", "key") != key {
		t.Fatalf("POST claim = %d: %v", code, claimed)
	}
	if got := strings.Join(servedSlugs(t, claimed), ","); got != "go-conventions" {
		t.Errorf("a claim in a plan batch served %q, want the project's standing corpus", got)
	}

	var documents int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM ticket_claim_documents d JOIN ticket_claims c ON c.id = d.claim_id
		 JOIN tickets t ON t.id = c.ticket_id WHERE t.key = $1`, key).Scan(&documents); err != nil {
		t.Fatalf("count the documents the claim recorded: %v", err)
	}
	if documents == 0 {
		t.Error("the claim recorded no served documents, so TP-188's scores stay empty")
	}
}
