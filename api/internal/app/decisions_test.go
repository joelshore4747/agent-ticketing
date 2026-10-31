package app_test

import (
	"net/http"
	"strings"
	"testing"
)

// Decisions over the transports (TP-84): made_by comes from the transport, never an argument, and
// every ticket read carries the history.

func TestDecisionsAreFiledByTheTransportTheyArriveOn(t *testing.T) {
	h := router(t)
	_, created := send(t, h, "POST", "/api/tickets", body{"title": "Worth a decision", "type": "bug"})
	id, key := str(t, created, "id"), str(t, created, "key")

	code, refused := send(t, h, "POST", "/api/tickets/"+key+"/decisions", body{"kind": "deferred"})
	if code != http.StatusBadRequest || !strings.Contains(str(t, refused, "error"), "revisit_trigger") {
		t.Errorf("a deferral without a trigger = %d %v", code, refused)
	}
	code, refused = send(t, h, "POST", "/api/tickets/"+key+"/decisions", body{"kind": "accepted", "made_by": "human"})
	if code != http.StatusBadRequest {
		t.Errorf("REST let the caller choose made_by: %d %v", code, refused)
	}
	code, rest := send(t, h, "POST", "/api/tickets/"+key+"/decisions", body{"kind": "rejected", "rationale": "rest says no"})
	if code != http.StatusCreated || str(t, rest, "made_by") != "ai" {
		t.Fatalf("REST decision = %d %v", code, rest)
	}
	_, uncited := send(t, h, "GET", "/api/decisions/uncited", nil)
	if list, _ := uncited.([]any); len(list) != 1 || str(t, uncited, 0, "key") != key {
		t.Errorf("uncited = %v", uncited)
	}

	call := mcpSession(t, h)
	out := call(mcpTool("record_decision", `{"key":"`+key+`","kind":"accepted","rationale":"mcp says yes","evidence_refs":["TP-1"]}`))
	if strings.Contains(out, `"isError":true`) || !strings.Contains(out, `mcp says yes`) {
		t.Fatalf("MCP record_decision = %s", out)
	}
	if compact := strings.NewReplacer(" ", "", `\"`, `"`).Replace(out); !strings.Contains(compact, `"made_by":"ai"`) {
		t.Errorf("MCP decision is not the agent's: %s", out)
	}
	if detail := call(mcpTool("get_ticket", `{"key":"`+key+`"}`)); !strings.Contains(detail, "rest says no") || !strings.Contains(detail, "mcp says yes") {
		t.Errorf("get_ticket lacks the decisions: %s", detail)
	}
	_, uncited = send(t, h, "GET", "/api/decisions/uncited", nil)
	if list, _ := uncited.([]any); len(list) != 0 {
		t.Errorf("a cited decision that supersedes the uncited one still flags: %v", uncited)
	}

	res := gql(t, h, `mutation { recordDecision(ticketId: "`+id+`", input: {kind: reframed, rationale: "graphql reframes"}) { madeBy supersedes } }`)
	if str(t, res, "data", "recordDecision", "madeBy") != "human" || at(t, res, "data", "recordDecision", "supersedes") == nil {
		t.Errorf("GraphQL decision = %v", res)
	}

	_, ticket := send(t, h, "GET", "/api/tickets/"+key, nil)
	decisions, _ := at(t, ticket, "decisions").([]any)
	if len(decisions) != 3 || str(t, ticket, "decisions", 0, "rationale") != "graphql reframes" {
		t.Errorf("REST ticket decisions = %v", decisions)
	}
	if str(t, ticket, "decision_id") != str(t, ticket, "decisions", 0, "id") {
		t.Errorf("decision_id %v is not the newest decision", at(t, ticket, "decision_id"))
	}
	res = gql(t, h, `{ ticket(key: "`+key+`") { decisionId decisions { kind madeBy } } }`)
	if str(t, res, "data", "ticket", "decisions", 2, "kind") != "rejected" || str(t, res, "data", "ticket", "decisionId") == "" {
		t.Errorf("GraphQL Ticket.decisions = %v", res)
	}

	// The claim envelope carries them too, so the agent sees what was decided before it starts.
	claimed := call(mcpTool("claim_next_ticket", `{}`))
	if !strings.Contains(claimed, key) || !strings.Contains(claimed, "graphql reframes") {
		t.Errorf("claim envelope lacks the decisions: %s", claimed)
	}
}

func TestAcceptingOverGraphQLFilesTheHumansDecision(t *testing.T) {
	h := router(t)
	_, finding := send(t, h, "POST", "/api/tickets", body{"title": "An AI finding", "type": "bug", "source": "ai"})
	res := gql(t, h, `mutation { dismissTicket(id: "`+str(t, finding, "id")+`", version: `+itoa(num(t, finding, "version"))+`,
		resolution: "not now") { decisions { kind madeBy rationale } } }`)
	if str(t, res, "data", "dismissTicket", "decisions", 0, "kind") != "rejected" ||
		str(t, res, "data", "dismissTicket", "decisions", 0, "madeBy") != "human" {
		t.Errorf("dismiss decision = %v", res)
	}
	call := mcpSession(t, h)
	if listed := call(`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`); !strings.Contains(listed, `"name":"record_decision"`) {
		t.Error("MCP does not offer record_decision")
	}
}
