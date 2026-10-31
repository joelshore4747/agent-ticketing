package app_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The ticket-list filters are parsed twice today — by hand in rest.listTickets and again in the MCP
// list_tickets tool — so a filter fixed on one transport can stay broken on the other. TP-275 replaces
// both with one service parse. This is the guard it will be measured by (TP-284): every row sends the
// same filter over both and asserts the same keys, or the same refusal, come back. It is written
// against the unrefactored code and must pass unedited before and after the cut, so where the two
// transports genuinely differ today the row pins the difference rather than hiding it.

// mcpList runs list_tickets and returns the keys it answered with, or the tool error's text.
func mcpList(t *testing.T, call func(string) string, args string) ([]string, string) {
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
	raw := call(mcpTool("list_tickets", args))
	for _, line := range strings.Split(raw, "\n") {
		if after, found := strings.CutPrefix(strings.TrimSpace(line), "data: "); found {
			raw = after
		}
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		t.Fatalf("list_tickets(%s) is not JSON-RPC: %s", args, raw)
	}
	var text string
	if len(envelope.Result.Content) > 0 {
		text = envelope.Result.Content[0].Text
	}
	// A schema refusal comes back as a protocol error rather than a tool error; both are "it said no".
	if envelope.Error != nil {
		return nil, envelope.Error.Message
	}
	if envelope.Result.IsError {
		return nil, text
	}
	var page struct {
		Items []struct {
			Key string `json:"key"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(text), &page); err != nil {
		t.Fatalf("list_tickets(%s) answered %s", args, raw)
	}
	keys := make([]string, 0, len(page.Items))
	for _, item := range page.Items {
		keys = append(keys, item.Key)
	}
	return keys, ""
}

// restList runs GET /api/tickets and returns the keys it answered with, or the 400's message.
func restList(t *testing.T, h http.Handler, query string) ([]string, string) {
	t.Helper()
	code, page := send(t, h, "GET", "/api/tickets"+query, nil)
	if code != http.StatusOK {
		return nil, str(t, page, "error")
	}
	items, ok := at(t, page, "items").([]any)
	if !ok {
		t.Fatalf("GET /api/tickets%s = %#v", query, page)
	}
	keys := make([]string, 0, len(items))
	for i := range items {
		keys = append(keys, str(t, page, "items", i, "key"))
	}
	return keys, ""
}

func sameKeys(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestListFiltersAnswerTheSameOverRESTAndMCP(t *testing.T) {
	h := router(t)
	call := mcpSession(t, h)

	// Four tickets, no two alike in any filtered field, so every row below can tell them apart.
	// `docs` auto-accepts AI findings and `api` does not, which is how TP-3 lands in triage.
	for _, seed := range []body{
		{"title": "Alpha over REST", "type": "bug", "priority": "high", "source": "human", "area": "api", "tags": []string{"tech-debt"}},
		{"title": "Beta the finding", "type": "chore", "priority": "low", "source": "ai", "area": "docs", "tags": []string{"eval"}},
		{"title": "Gamma in triage", "type": "feature", "priority": "medium", "source": "ai", "area": "api"},
		{"title": "Delta on the web", "type": "refactor", "priority": "urgent", "source": "human", "area": "web"},
	} {
		if code, created := send(t, h, "POST", "/api/tickets", seed); code != http.StatusCreated {
			t.Fatalf("seed %v = %d: %v", seed["title"], code, created)
		}
	}
	// TP-1 Alpha, TP-2 Beta, TP-3 Gamma, TP-4 Delta. Gamma is the only one in triage.
	if keys, _ := restList(t, h, "?status=triage"); !sameKeys(keys, []string{"TP-3"}) {
		t.Fatalf("the seed is not what the rows below assume: triage = %v", keys)
	}

	for _, row := range []struct {
		name  string
		query string // what REST is sent, after /api/tickets
		args  string // what list_tickets is sent
		want  []string
		// refusal is the text both transports' "no" must contain; "" means the row expects an answer.
		refusal string
	}{
		// The order is the service's, not a transport's: priority first, urgent down to low.
		{name: "no filter at all", query: "", args: `{}`,
			want: []string{"TP-4", "TP-1", "TP-3", "TP-2"}},
		{name: "one status", query: "?status=triage", args: `{"status":["triage"]}`,
			want: []string{"TP-3"}},
		{name: "two statuses, repeated over REST and a list over MCP", query: "?status=open&status=triage",
			args: `{"status":["open","triage"]}`, want: []string{"TP-4", "TP-1", "TP-3", "TP-2"}},
		{name: "source", query: "?source=ai", args: `{"source":"ai"}`, want: []string{"TP-3", "TP-2"}},
		{name: "type", query: "?type=refactor", args: `{"type":"refactor"}`, want: []string{"TP-4"}},
		{name: "priority", query: "?priority=high", args: `{"priority":"high"}`, want: []string{"TP-1"}},
		{name: "area", query: "?area=api", args: `{"area":"api"}`, want: []string{"TP-1", "TP-3"}},
		{name: "tag", query: "?tag=tech-debt", args: `{"tag":"tech-debt"}`, want: []string{"TP-1"}},
		{name: "search", query: "?search=Gamma", args: `{"search":"Gamma"}`, want: []string{"TP-3"}},
		{name: "created_after lets everything through", query: "?created_after=2000-01-01T00:00:00Z",
			args: `{"created_after":"2000-01-01T00:00:00Z"}`, want: []string{"TP-4", "TP-1", "TP-3", "TP-2"}},
		{name: "created_after in the future lets nothing through", query: "?created_after=2999-01-01T00:00:00Z",
			args: `{"created_after":"2999-01-01T00:00:00Z"}`, want: []string{}},
		{name: "updated_after in the future lets nothing through", query: "?updated_after=2999-01-01T00:00:00Z",
			args: `{"updated_after":"2999-01-01T00:00:00Z"}`, want: []string{}},
		{name: "limit", query: "?limit=2", args: `{"limit":2}`, want: []string{"TP-4", "TP-1"}},
		{name: "offset", query: "?limit=2&offset=2", args: `{"limit":2,"offset":2}`, want: []string{"TP-3", "TP-2"}},
		{name: "two filters together", query: "?source=ai&area=api", args: `{"source":"ai","area":"api"}`,
			want: []string{"TP-3"}},
		// A tag that does not exist matches nothing on both; an area that does not exist is refused on
		// both, because an area is a managed list and a typo there would silently answer "no tickets".
		{name: "an unknown tag matches nothing", query: "?tag=nowhere", args: `{"tag":"nowhere"}`, want: []string{}},

		// The refusals. Each names the enum or the parameter it could not read, on both transports.
		{name: "an unknown area", query: "?area=nowhere", args: `{"area":"nowhere"}`, refusal: `no area named "nowhere"`},
		{name: "a bad status", query: "?status=nope", args: `{"status":["nope"]}`, refusal: `invalid Status: "nope"`},
		{name: "a bad source", query: "?source=nope", args: `{"source":"nope"}`, refusal: `invalid Source: "nope"`},
		{name: "a bad type", query: "?type=nope", args: `{"type":"nope"}`, refusal: `invalid TicketType: "nope"`},
		{name: "a bad priority", query: "?priority=nope", args: `{"priority":"nope"}`, refusal: `invalid Priority: "nope"`},
		{name: "a bad created_after", query: "?created_after=yesterday", args: `{"created_after":"yesterday"}`,
			refusal: "created_after must be an RFC 3339 timestamp"},
		{name: "a bad updated_after", query: "?updated_after=yesterday", args: `{"updated_after":"yesterday"}`,
			refusal: "updated_after must be an RFC 3339 timestamp"},
	} {
		t.Run(row.name, func(t *testing.T) {
			restKeys, restErr := restList(t, h, row.query)
			mcpKeys, mcpErr := mcpList(t, call, row.args)
			if row.refusal != "" {
				if !strings.Contains(restErr, row.refusal) {
					t.Errorf("REST%s answered %v / %q, want a refusal naming %q", row.query, restKeys, restErr, row.refusal)
				}
				if !strings.Contains(mcpErr, row.refusal) {
					t.Errorf("list_tickets%s answered %v / %q, want a refusal naming %q", row.args, mcpKeys, mcpErr, row.refusal)
				}
				return
			}
			if restErr != "" || mcpErr != "" {
				t.Fatalf("REST%s = %q, list_tickets%s = %q, want both to answer", row.query, restErr, row.args, mcpErr)
			}
			if !sameKeys(restKeys, mcpKeys) {
				t.Errorf("the transports disagree: REST%s = %v, list_tickets%s = %v", row.query, restKeys, row.args, mcpKeys)
			}
			if !sameKeys(restKeys, row.want) {
				t.Errorf("REST%s = %v, want %v", row.query, restKeys, row.want)
			}
		})
	}
}

// Where the two do not agree today. TP-275 must keep each of these answering exactly as it does here:
// the refactor is a cut, not a behaviour change, and a row that started passing would mean it made one
// silently. Any of them is worth closing on purpose afterwards, as its own ticket.
func TestListFiltersWhereRESTAndMCPStillDifferToday(t *testing.T) {
	h := router(t)
	call := mcpSession(t, h)
	for _, seed := range []body{
		{"title": "Alpha over REST", "type": "bug", "priority": "high", "source": "human", "area": "api"},
		{"title": "Gamma in triage", "type": "feature", "priority": "medium", "source": "ai", "area": "api"},
	} {
		if code, created := send(t, h, "POST", "/api/tickets", seed); code != http.StatusCreated {
			t.Fatalf("seed %v = %d: %v", seed["title"], code, created)
		}
	}

	// 1. A comma-separated status. REST splits it, because a query string cannot nest; MCP is handed a
	// list and parses each element whole, so the comma is part of the value and the enum refuses it.
	if keys, err := restList(t, h, "?status=open,triage"); err != "" || !sameKeys(keys, []string{"TP-1", "TP-2"}) {
		t.Errorf("REST splits a comma-separated status: got %v / %q", keys, err)
	}
	if keys, err := mcpList(t, call, `{"status":["open,triage"]}`); !strings.Contains(err, `invalid Status: "open,triage"`) {
		t.Errorf("MCP takes a status element whole: got %v / %q", keys, err)
	}

	// 2. An empty value. REST trims and drops it, so `?type=` is no filter at all; MCP is given the
	// empty string as a real argument and the enum refuses it.
	if keys, err := restList(t, h, "?type=&priority=%20"); err != "" || len(keys) != 2 {
		t.Errorf("REST drops an empty filter: got %v / %q", keys, err)
	}
	if keys, err := mcpList(t, call, `{"type":""}`); !strings.Contains(err, `invalid TicketType: ""`) {
		t.Errorf("MCP refuses an empty type: got %v / %q", keys, err)
	}

	// 3. A limit that is not a number. REST reads every parameter out of the query string and refuses
	// it itself; over MCP the tool schema types it as an integer, so the refusal comes from the
	// protocol before the handler runs. Both say no, in different words — which is why the rows above
	// pin a shared message and this one only pins that neither accepts it.
	if keys, err := restList(t, h, "?limit=ten"); !strings.Contains(err, "must be a number") {
		t.Errorf("REST refuses a non-numeric limit: got %v / %q", keys, err)
	}
	if keys, err := mcpList(t, call, `{"limit":"ten"}`); err == "" {
		t.Errorf("MCP refuses a non-numeric limit: got %v / %q", keys, err)
	}
}
