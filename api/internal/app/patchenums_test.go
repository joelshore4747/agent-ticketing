package app_test

import (
	"encoding/json"
	"strings"
	"testing"
)

// REST PATCH /api/tickets/:key and MCP update_ticket each turned a patch's raw type, priority and
// status into a service.TicketPatch with their own copy of the same three blocks (TP-311). This
// pins the only behaviour those blocks have that a caller can see — which value is refused, and
// exactly what the refusal says — so folding them into one service function can be measured
// against it. Run green on the parent commit before the fold.

// mcpToolResult runs one tool call and returns its text, and whether the call was refused.
func mcpToolResult(t *testing.T, call func(string) string, payload string) (string, bool) {
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
	if envelope.Error != nil {
		return envelope.Error.Message, true
	}
	if len(envelope.Result.Content) == 0 {
		t.Fatalf("the tool call answered with no content: %s", raw)
	}
	return envelope.Result.Content[0].Text, envelope.Result.IsError
}

// mcpToolError returns the text a tool call failed with, and fails the test if it succeeded.
func mcpToolError(t *testing.T, call func(string) string, payload string) string {
	t.Helper()
	text, refused := mcpToolResult(t, call, payload)
	if !refused {
		t.Fatalf("the tool call was meant to be refused, and was not: %s", text)
	}
	return text
}

func TestPatchRefusesABadEnumTheSameOnRESTAndMCP(t *testing.T) {
	h := router(t)
	call := mcpSession(t, h)

	for _, tc := range []struct {
		field string
		bad   string
		want  string
	}{
		{"type", "epic", `invalid TicketType: "epic"`},
		{"priority", "critical", `invalid Priority: "critical"`},
		{"status", "in progress", `invalid Status: "in progress"`},
	} {
		t.Run(tc.field, func(t *testing.T) {
			// A fresh ticket per row, so a refusal that wrongly went through cannot hide in the next.
			_, created := send(t, h, "POST", "/api/tickets", body{"title": "Patch me", "type": "bug"})
			key := str(t, created, "key")
			version := int(num(t, created, "version"))

			code, refused := send(t, h, "PATCH", "/api/tickets/"+key,
				body{"version": version, tc.field: tc.bad})
			if code != 400 {
				t.Fatalf("REST patch with %s=%q answered %d; a bad enum is a 400", tc.field, tc.bad, code)
			}
			overREST := str(t, refused, "error")

			args, err := json.Marshal(map[string]any{"key": key, "version": version, tc.field: tc.bad})
			if err != nil {
				t.Fatal(err)
			}
			overMCP := mcpToolError(t, call, mcpTool("update_ticket", string(args)))

			for transport, got := range map[string]string{"REST": overREST, "MCP": overMCP} {
				if !strings.Contains(got, tc.want) {
					t.Errorf("%s refused %s=%q with %q; wanted it to name %s", transport, tc.field, tc.bad, got, tc.want)
				}
			}
			if overREST != overMCP {
				t.Errorf("the two transports refuse %s=%q differently:\n REST: %s\n  MCP: %s",
					tc.field, tc.bad, overREST, overMCP)
			}

			// The refusal is the whole call: nothing was written.
			_, after := send(t, h, "GET", "/api/tickets/"+key, nil)
			if got := int(num(t, after, "version")); got != version {
				t.Errorf("a refused patch bumped the version to %d from %d", got, version)
			}
		})
	}
}
