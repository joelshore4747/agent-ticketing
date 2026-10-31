package app_test

import (
	"encoding/json"
	"strings"
	"testing"
)

// REST createTicket, MCP report_findings and service.PlannedTicketRequest.Parse each turned a new
// ticket's required type and optional priority into typed values with their own copy of the same
// block (TP-312). This pins everything a caller can see of all three — which value is refused, in
// the same words, and that an omitted priority becomes medium — so folding them into one service
// function can be measured against it. Run green on the parent commit before the fold.
//
// The three doors are the three copies, not three transports: the plan copy lives in the service
// and is reached by REST and MCP alike, which tripwire 737e4e40 pins as a pair.

// createDoor is one place a new ticket's type and priority are parsed. Each door files one ticket
// and answers with its key, or with the text the door refused it with; rawPriority is "" to leave
// the field out altogether, which is the case that has to default.
type createDoor struct {
	name   string
	create func(t *testing.T, title, rawType, rawPriority string) (key, refusal string)
}

func TestCreateRefusesABadTypeOrPriorityTheSameOnEveryDoor(t *testing.T) {
	h := router(t)
	call := mcpSession(t, h)

	doors := []createDoor{
		{"REST createTicket", func(t *testing.T, title, rawType, rawPriority string) (string, string) {
			t.Helper()
			payload := body{"title": title, "type": rawType}
			if rawPriority != "" {
				payload["priority"] = rawPriority
			}
			code, out := send(t, h, "POST", "/api/tickets", payload)
			if code == 201 {
				return str(t, out, "key"), ""
			}
			return "", str(t, out, "error")
		}},
		{"MCP report_findings", func(t *testing.T, title, rawType, rawPriority string) (string, string) {
			t.Helper()
			finding := body{"title": title, "type": rawType, "goals": []int{3}}
			if rawPriority != "" {
				finding["priority"] = rawPriority
			}
			text, refused := mcpToolResult(t, call,
				mcpTool("report_findings", stringify(body{"findings": []body{finding}})))
			if refused {
				return "", text
			}
			// The tool answers with the outcome per finding, which carries the key but not the
			// priority, so the caller reads that back off the filed ticket.
			var outcomes []struct {
				Key string `json:"key"`
			}
			if err := json.Unmarshal([]byte(text), &outcomes); err != nil || len(outcomes) != 1 {
				t.Fatalf("report_findings answered %s", text)
			}
			return outcomes[0].Key, ""
		}},
		{"plan_batch", func(t *testing.T, title, rawType, rawPriority string) (string, string) {
			t.Helper()
			ticket := body{"title": title, "type": rawType, "goals": []int{3}}
			if rawPriority != "" {
				ticket["priority"] = rawPriority
			}
			code, out := send(t, h, "POST", "/api/batches/plan", body{
				"title": title, "what": "one parse", "how": "one service function",
				"why": "every door refuses a bad type alike", "demo": "file a ticket on each door",
				// A planned ticket of a title already live is moved in rather than parsed again, so
				// each plan here names its ticket after itself.
				"tickets": []body{ticket},
			})
			if code == 201 {
				return str(t, out, "tickets", 0, "key"), ""
			}
			return "", str(t, out, "error")
		}},
	}

	for _, tc := range []struct {
		name        string
		rawType     string
		rawPriority string
		want        string
	}{
		{"a bad type", "epic", "", `invalid TicketType: "epic"`},
		{"a bad priority", "bug", "critical", `invalid Priority: "critical"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refusals := make([]string, 0, len(doors))
			for _, d := range doors {
				key, refusal := d.create(t, tc.name+" over "+d.name, tc.rawType, tc.rawPriority)
				if key != "" {
					t.Fatalf("%s took %s and filed %s; it is meant to refuse it", d.name, tc.name, key)
				}
				if !strings.Contains(refusal, tc.want) {
					t.Errorf("%s refused %s with %q; wanted it to name %s", d.name, tc.name, refusal, tc.want)
				}
				refusals = append(refusals, refusal)
			}
			// And in the same words on every door, which is what one shared parse buys.
			for i, refusal := range refusals[1:] {
				if refusal != refusals[0] {
					t.Errorf("%s and %s refuse %s differently:\n  %s: %s\n  %s: %s", doors[0].name,
						doors[i+1].name, tc.name, doors[0].name, refusals[0], doors[i+1].name, refusal)
				}
			}
		})
	}

	t.Run("an omitted priority is medium", func(t *testing.T) {
		for _, d := range doors {
			key, refusal := d.create(t, "No priority over "+d.name, "chore", "")
			if refusal != "" {
				t.Fatalf("%s refused a ticket that left the priority out: %s", d.name, refusal)
			}
			_, filed := send(t, h, "GET", "/api/tickets/"+key, nil)
			if got := str(t, filed, "priority"); got != "medium" {
				t.Errorf("%s filed %s at priority %q; an omitted priority defaults to medium",
					d.name, key, got)
			}
		}
	})
}
