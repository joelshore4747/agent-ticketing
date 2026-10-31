package app_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The ownership model over the transports (TP-79): the human's moves are GraphQL only; the agent
// writes Sections B and C and proposes, never writes, Section A.

func mcpSession(t *testing.T, h http.Handler) func(payload string) string {
	t.Helper()
	return mcpSessionAs(t, h, nil)
}

// mcpSessionAs is one MCP session whose every POST carries headers — the credential and the X-Actor
// the actor is settled from (TP-318), which a session cannot be opened without when login is on.
func mcpSessionAs(t *testing.T, h http.Handler, headers map[string]string) func(payload string) string {
	t.Helper()
	post := func(session, payload string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/mcp", strings.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		for name, value := range headers {
			req.Header.Set(name, value)
		}
		if session != "" {
			req.Header.Set("Mcp-Session-Id", session)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	rec := post("", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18",`+
		`"capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`)
	session := rec.Header().Get("Mcp-Session-Id")
	post(session, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	return func(payload string) string { return post(session, payload).Body.String() }
}

func mcpTool(name, args string) string {
	return `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"` + name + `","arguments":` + args + `}}`
}

func TestTheHumanOwnsSectionAOverGraphQL(t *testing.T) {
	h := router(t)
	_, created := send(t, h, "POST", "/api/tickets", body{"title": "A feature", "type": "feature"})
	id, key := str(t, created, "id"), str(t, created, "key")

	_, tw := send(t, h, "POST", "/api/tripwires", body{"test_ref": "api/x_test.go:TestX", "meaning": "X holds", "body_sha256": "h1"})
	twID := str(t, tw, "id")
	out := gql(t, h, `mutation { confirmTripwire(id: "`+twID+`", meaning: "X holds, always") { meaning confirmedBy } }`)
	if str(t, out, "data", "confirmTripwire", "meaning") != "X holds, always" {
		t.Fatalf("confirmTripwire = %v", out)
	}

	out = gql(t, h, `mutation { setTicketFoundation(id: "`+id+`", version: `+itoa(num(t, created, "version"))+`, input: {
		intent: "Agents learn at once", nonGoals: "GraphQL",
		criteria: [{tier: behavioural, text: "unknown field gets a 400"}, {tier: gate, text: "just test", tripwireId: "`+twID+`"}]
	}) { version intent criteria { ordinal tier text tripwire { meaning } } } }`)
	if at(t, out, "errors") != nil {
		t.Fatalf("setTicketFoundation = %v", out)
	}
	if str(t, out, "data", "setTicketFoundation", "criteria", 1, "tripwire", "meaning") != "X holds, always" {
		t.Errorf("criteria = %v", at(t, out, "data", "setTicketFoundation", "criteria"))
	}
	version := num(t, out, "data", "setTicketFoundation", "version")

	code, patched := send(t, h, "PATCH", "/api/tickets/"+key, body{"version": version, "approach": "Decoder rejects unknowns",
		"worked_example": "POST {nonsense} -> 400", "evidence_refs": []string{"rest.go:95"}})
	if code != http.StatusOK {
		t.Fatalf("REST section B = %d %v", code, patched)
	}
	out = gql(t, h, `mutation { approveApproach(id: "`+id+`", version: `+itoa(num(t, patched, "version"))+`) { approachApprovedBy } }`)
	if at(t, out, "data", "approveApproach", "approachApprovedBy") == nil {
		t.Errorf("approveApproach = %v", out)
	}
}

func TestTheAgentWritesSectionsBAndCAndProposesTheRest(t *testing.T) {
	h := router(t)
	_, created := send(t, h, "POST", "/api/tickets", body{"title": "Work", "type": "bug"})
	key, version := str(t, created, "key"), num(t, created, "version")

	code, refused := send(t, h, "PATCH", "/api/tickets/"+key, body{"version": version, "intent": "mine now"})
	if code != http.StatusBadRequest || !strings.Contains(str(t, refused, "error"), "intent") {
		t.Errorf("REST wrote Section A: %d %v", code, refused)
	}
	code, proposal := send(t, h, "POST", "/api/tickets/"+key+"/proposals", body{"field": "intent", "proposed": "Faster", "rationale": "the brief"})
	if code != http.StatusCreated || str(t, proposal, "kind") != "proposal" {
		t.Errorf("REST proposal = %d %v", code, proposal)
	}

	call := mcpSession(t, h)
	if out := call(mcpTool("update_ticket", `{"key":"`+key+`","version":`+itoa(version)+`,"intent":"mine"}`)); !strings.Contains(out, `"isError":true`) {
		t.Errorf("MCP wrote Section A: %s", out)
	}
	out := call(mcpTool("update_ticket", `{"key":"`+key+`","version":`+itoa(version)+`,"verification_plan":"go test ./...","verified_by":"ok"}`))
	if strings.Contains(out, `"isError":true`) || !strings.Contains(out, "go test ./...") {
		t.Errorf("MCP section B/C = %s", out)
	}
	if out := call(mcpTool("propose_ticket_edit", `{"key":"`+key+`","field":"criteria","proposed":"1. [gate] just test","rationale":"gate"}`)); !strings.Contains(out, "proposal") {
		t.Errorf("MCP proposal = %s", out)
	}
	listed := call(`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)
	for _, human := range []string{"set_ticket_foundation", "setTicketFoundation", "approve_approach", "confirm_tripwire"} {
		if strings.Contains(listed, `"name":"`+human+`"`) {
			t.Errorf("MCP offers the human's %s", human)
		}
	}
}

func TestATicketReadsWithItsCriteriaAndOnlyPublicChecks(t *testing.T) {
	h := router(t)
	_, created := send(t, h, "POST", "/api/tickets", body{"title": "Checked", "type": "bug"})
	gql(t, h, `mutation { setTicketFoundation(id: "`+str(t, created, "id")+`", version: `+itoa(num(t, created, "version"))+`, input: {criteria: [{tier: gate, text: "just test"}]}) { id } }`)

	code, got := send(t, h, "GET", "/api/tickets/"+str(t, created, "key"), nil)
	if code != http.StatusOK || str(t, got, "criteria", 0, "text") != "just test" {
		t.Errorf("REST ticket = %d %v", code, got)
	}
	if checks, _ := at(t, got, "checks").([]any); checks == nil || len(checks) != 0 {
		t.Errorf("checks = %v; a ticket with no public checks reads as []", at(t, got, "checks"))
	}
	_, claimed := send(t, h, "POST", "/api/claim", nil)
	if str(t, claimed, "criteria", 0, "tier") != "gate" {
		t.Errorf("the claim does not carry the criteria: %v", at(t, claimed, "criteria"))
	}
}
