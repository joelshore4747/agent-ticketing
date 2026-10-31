package app_test

import (
	"encoding/json"
	"strings"
	"testing"
)

// A ticket with nothing attached still reads as `[]`, never null (TP-293). REST's copy of the
// nil-to-[] helper is pinned by ownership_test.go's TestATicketReadsWithItsCriteriaAndOnlyPublicChecks;
// MCP's three copies had nothing pinning them, which is why folding them into one model.NonNil
// needed this test written first and run on the parent commit.

// mcpResultJSON returns the JSON text a tool call answered with, as raw bytes, so a caller can tell
// `[]` from `null` — which is the whole point here and is what an unmarshal into []T would erase.
func mcpResultJSON(t *testing.T, call func(string) string, payload string) map[string]json.RawMessage {
	t.Helper()
	text := mcpResultText(t, call, payload)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &fields); err != nil {
		t.Fatalf("tool result is not a JSON object: %s", text)
	}
	return fields
}

// mcpResultText unwraps a successful tool call down to the JSON it answered with: the SSE frame, the
// JSON-RPC envelope, then the one text content block. A tool error fails the test here.
func mcpResultText(t *testing.T, call func(string) string, payload string) string {
	t.Helper()
	var envelope struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	// The streamable-HTTP transport frames the reply as one SSE event, so the JSON is the `data:` line.
	raw := call(payload)
	for _, line := range strings.Split(raw, "\n") {
		if after, found := strings.CutPrefix(strings.TrimSpace(line), "data: "); found {
			raw = after
		}
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		t.Fatalf("not JSON-RPC: %s", raw)
	}
	if envelope.Error != nil || envelope.Result.IsError || len(envelope.Result.Content) == 0 {
		t.Fatalf("tool call failed: %s", raw)
	}
	return envelope.Result.Content[0].Text
}

func TestMCPReadsAnEmptyTicketsCriteriaAndChecksAsEmptyArrays(t *testing.T) {
	h := router(t)
	_, created := send(t, h, "POST", "/api/tickets", body{"title": "Nothing attached", "type": "bug"})
	key := str(t, created, "key")

	call := mcpSession(t, h)
	for _, tool := range []struct {
		name string
		args string
	}{
		{"get_ticket", `{"key":"` + key + `"}`},
		{"claim_next_ticket", `{}`},
	} {
		fields := mcpResultJSON(t, call, mcpTool(tool.name, tool.args))
		if got := string(fields["key"]); got != `"`+key+`"` {
			t.Fatalf("%s answered about %s, not %s", tool.name, got, key)
		}
		for _, field := range []string{"criteria", "checks"} {
			if got := string(fields[field]); got != "[]" {
				t.Errorf("%s %s = %s; a ticket with none reads as [], not null", tool.name, field, got)
			}
		}
	}
}

// These two pinned graph/convert.go's nonNil and service/goals.go's nonNilGoals while those copies
// waited on Joel confirming them as tripwires (c6775393, 03c68047), which he did on 2026-10-01; TP-308
// then folded both into model.NonNil against them, unedited. graph/convert.go's goalsOrEmpty was a
// third copy of the same idea, and TP-308 left it alone because at the time nothing read goals over
// GraphQL: the REST case below reads the service's copy, and the tags case reads a different field.
// TestGraphQLReadsATicketWithNoGoalsAsAnEmptyArray covers that GraphQL read, but as a contract test and
// not as a tripwire for goalsOrEmpty, which TP-341 found is unobservable here: gqlgen's marshaller for a
// non-null list builds make(graphql.Array, len(v)) with no nil check (generated.go:15319), so a nil Goals
// renders as [] whether goalsOrEmpty runs or not — gutting it to `return g` left this test green
// (2026-10-03, TP-342). What it does pin is the consumer-visible contract that goals reads [] and never
// null, which an SDL change to [Int!] paired with a nil would break. TP-340 therefore folded the call
// site into model.NonNil and deleted the helper on the marshaller's proof, not behind a guard that
// cannot fail.

func TestGraphQLReadsATicketWithNoTagsAsAnEmptyArray(t *testing.T) {
	h := router(t)
	_, created := send(t, h, "POST", "/api/tickets", body{"title": "Untagged", "type": "bug"})

	res := gql(t, h, `{ ticket(key: "`+str(t, created, "key")+`") { tags } }`)
	tags, ok := at(t, res, "data", "ticket", "tags").([]any)
	if !ok || len(tags) != 0 {
		t.Errorf("tags = %v; a ticket with none reads as [], not null", at(t, res, "data", "ticket", "tags"))
	}
}

func TestGraphQLReadsATicketWithNoGoalsAsAnEmptyArray(t *testing.T) {
	h := router(t)
	_, created := send(t, h, "POST", "/api/tickets", body{"title": "Goalless over GraphQL", "type": "bug"})

	res := gql(t, h, `{ ticket(key: "`+str(t, created, "key")+`") { goals } }`)
	goals, ok := at(t, res, "data", "ticket", "goals").([]any)
	if !ok || len(goals) != 0 {
		t.Errorf("goals = %v; a ticket with none reads as [], not null", at(t, res, "data", "ticket", "goals"))
	}
}

func TestATicketCreatedWithNoGoalsReadsGoalsAsAnEmptyArray(t *testing.T) {
	h := router(t)
	_, created := send(t, h, "POST", "/api/tickets", body{"title": "Goalless", "type": "bug"})

	_, got := send(t, h, "GET", "/api/tickets/"+str(t, created, "key"), nil)
	goals, ok := at(t, got, "goals").([]any)
	if !ok || len(goals) != 0 {
		t.Errorf("goals = %v; a ticket created with none reads as [], not null", at(t, got, "goals"))
	}
}
