package app_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joelshore/ticketing/api/internal/app"
	"github.com/joelshore/ticketing/api/internal/testdb"
)

// Rule 10 with login on (TP-317). The point of the lot: an actor stops being something a caller can
// type. Until TP-315 issues real credentials every one of these is a throwaway in a temp file.

const mcpInitialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18",` +
	`"capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`

// ticketNamed is a create payload. Each one needs its own title: two with the same title have the
// same fingerprint, and the second is deduped into the first rather than created (rule 7).
func ticketNamed(title string) string {
	return fmt.Sprintf(`{"title":%q,"description":"d","type":"bug","source":"ai"}`, title)
}

const newTicket = `{"title":"from a credential","description":"d","type":"bug","source":"ai"}`

// loggedIn builds the router over a credentials file holding one line per token: issued["t1"] =
// "strategist" is the `<sha256(t1)> strategist` line scripts/issue-token.sh would have appended.
func loggedIn(t *testing.T, issued map[string]string) http.Handler {
	t.Helper()
	pool := testdb.New(t)
	h, err := app.New(pool, "http://localhost:5173", credentialsFile(t, issued))
	if err != nil {
		t.Fatalf("build the app with credentials: %v", err)
	}
	return h
}

func credentialsFile(t *testing.T, issued map[string]string) string {
	t.Helper()
	lines := []string{"# issued by scripts/issue-token.sh", ""}
	for token, actor := range issued {
		sum := sha256.Sum256([]byte(token))
		lines = append(lines, fmt.Sprintf("%s %s", hex.EncodeToString(sum[:]), actor))
	}
	path := filepath.Join(t.TempDir(), "tokens")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// hit is one request with the headers this file cares about. The body is a raw string because MCP's
// is JSON-RPC rather than anything send() would marshal.
func hit(t *testing.T, h http.Handler, method, path, payload string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func asJSON(t *testing.T, rec *httptest.ResponseRecorder) any {
	t.Helper()
	var out any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("body %q is not JSON: %v", rec.Body.String(), err)
	}
	return out
}

// eventActors is who a ticket's events are recorded under, read back over GraphQL.
func eventActors(t *testing.T, h http.Handler, token, key string) []string {
	t.Helper()
	query := fmt.Sprintf(`{"query":"{ ticket(key: \"%s\") { events { actor field } } }"}`, key)
	rec := hit(t, h, "POST", "/graphql", query, map[string]string{"Authorization": "Bearer " + token})
	if rec.Code != http.StatusOK {
		t.Fatalf("reading %s's events = %d, body %q", key, rec.Code, rec.Body.String())
	}
	events, _ := at(t, asJSON(t, rec), "data", "ticket", "events").([]any)
	if len(events) == 0 {
		t.Fatalf("%s has no events to attribute: %q", key, rec.Body.String())
	}
	out := make([]string, 0, len(events))
	for i := range events {
		out = append(out, str(t, events, i, "actor"))
	}
	return out
}

func TestEveryTransportRefusesARequestWithoutACredential(t *testing.T) {
	h := loggedIn(t, map[string]string{"t1": "strategist"})
	for _, tc := range []struct{ name, method, path, payload string }{
		{"rest write", "POST", "/api/tickets", newTicket},
		// A read too: a tracker whose tickets anyone may list is one a second project cannot share.
		{"rest read", "GET", "/api/tickets", ""},
		{"graphql", "POST", "/graphql", `{"query":"{ tickets(limit: 1) { total } }"}`},
		{"mcp", "POST", "/mcp", mcpInitialize},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := hit(t, h, tc.method, tc.path, tc.payload, nil)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("no credential = %d, want 401; body %q", rec.Code, rec.Body.String())
			}
			if got := str(t, asJSON(t, rec), "error"); !strings.Contains(got, "Authorization: Bearer") {
				t.Errorf("401 says %q, want it to name how to authenticate", got)
			}
			// An unknown token is no better than none, and the credential it does know is not refused.
			if rec := hit(t, h, tc.method, tc.path, tc.payload, map[string]string{"Authorization": "Bearer guessed"}); rec.Code != http.StatusUnauthorized {
				t.Errorf("an unknown token = %d, want 401", rec.Code)
			}
			if rec := hit(t, h, tc.method, tc.path, tc.payload, map[string]string{"Authorization": "Bearer t1"}); rec.Code == http.StatusUnauthorized {
				t.Errorf("the issued credential was refused: %q", rec.Body.String())
			}
		})
	}
}

// The three that stay open, and the ordering they depend on: the preflight is answered by CORS
// before any credential check, and a 401 carries CORS headers — a 401 the browser cannot read is
// one the web app cannot offer a login for.
func TestLoginLeavesThePreflightAndTheProbesOpen(t *testing.T) {
	h := loggedIn(t, map[string]string{"t1": "strategist"})
	for _, tc := range []struct {
		name, method, path string
		want               int
	}{
		{"preflight", "OPTIONS", "/api/tickets", http.StatusNoContent},
		{"version probe", "GET", "/api/version", http.StatusOK},
		{"playground", "GET", "/graphql", http.StatusOK},
	} {
		if rec := hit(t, h, tc.method, tc.path, "", nil); rec.Code != tc.want {
			t.Errorf("%s = %d, want %d: %q", tc.name, rec.Code, tc.want, rec.Body.String())
		}
	}
	rec := hit(t, h, "GET", "/api/tickets", "", nil)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Errorf("401 allow-origin = %q, want the configured origin so the browser can read it", got)
	}
}

func TestTheActorComesFromTheCredentialNotTheHeader(t *testing.T) {
	h := loggedIn(t, map[string]string{"t1": "strategist", "t2": "approver"})

	// Both calls claim to be the human in X-Actor. With login on that header decides nothing.
	rest := hit(t, h, "POST", "/api/tickets", newTicket,
		map[string]string{"Authorization": "Bearer t1", "X-Actor": "human"})
	if rest.Code != http.StatusCreated {
		t.Fatalf("create over REST = %d, body %q", rest.Code, rest.Body.String())
	}
	graphql := hit(t, h, "POST", "/graphql", `{"query":"mutation { createTicket(input: {title: \"from GraphQL\", type: bug}) { key } }"}`,
		map[string]string{"Authorization": "Bearer t2", "X-Actor": "human"})
	if graphql.Code != http.StatusOK {
		t.Fatalf("create over GraphQL = %d, body %q", graphql.Code, graphql.Body.String())
	}

	for _, tc := range []struct{ key, want string }{
		{str(t, asJSON(t, rest), "key"), "strategist"},
		{str(t, asJSON(t, graphql), "data", "createTicket", "key"), "approver"},
	} {
		if got := eventActors(t, h, "t1", tc.key); got[0] != tc.want {
			t.Errorf("%s's created event names %q, want the token's actor %q", tc.key, got[0], tc.want)
		}
	}
}

func TestLocalModeStillReadsXActor(t *testing.T) {
	h := router(t) // TICKETING_TOKENS unset, as every other test in this package runs it

	// No credential anywhere, and X-Actor is believed again.
	named := hit(t, h, "POST", "/api/tickets", ticketNamed("local mode, named"), map[string]string{"X-Actor": "cleanup-scout"})
	if named.Code != http.StatusCreated {
		t.Fatalf("create in local mode = %d, body %q", named.Code, named.Body.String())
	}
	if got := eventActors(t, h, "", str(t, asJSON(t, named), "key")); got[0] != "cleanup-scout" {
		t.Errorf("created event names %q, want the X-Actor header", got[0])
	}

	// And the per-transport defaults are still the ones rule 10 has always promised.
	unnamed := hit(t, h, "POST", "/api/tickets", ticketNamed("local mode, unnamed"), nil)
	if unnamed.Code != http.StatusCreated {
		t.Fatalf("create with no actor at all = %d, body %q", unnamed.Code, unnamed.Body.String())
	}
	if got := eventActors(t, h, "", str(t, asJSON(t, unnamed), "key")); got[0] != "claude-code" {
		t.Errorf("REST with no X-Actor names %q, want claude-code", got[0])
	}
	overGraphQL := hit(t, h, "POST", "/graphql", `{"query":"mutation { createTicket(input: {title: \"local mode, GraphQL\", type: bug}) { key } }"}`, nil)
	if overGraphQL.Code != http.StatusOK {
		t.Fatalf("create over GraphQL = %d, body %q", overGraphQL.Code, overGraphQL.Body.String())
	}
	key := str(t, asJSON(t, overGraphQL), "data", "createTicket", "key")
	if got := eventActors(t, h, "", key); got[0] != "human" {
		t.Errorf("GraphQL with no X-Actor names %q, want human", got[0])
	}
}

// MCP settles its actor the same way, but off its own HTTP header rather than the context, because a
// tool handler's context descends from the session's initialize request (TP-318).

// filedOverMCP calls report_findings and returns the key of the ticket it created. extra is spliced
// into the arguments object, which is how the compatibility test smuggles in a stale `actor`.
func filedOverMCP(t *testing.T, call func(string) string, title, extra string) string {
	t.Helper()
	args := fmt.Sprintf(`{"findings":[{"title":%q,"description":"d","type":"bug"}]%s}`, title, extra)
	text := mcpResultText(t, call, mcpTool("report_findings", args))
	var outcomes []struct {
		Key     string `json:"key"`
		Created bool   `json:"created"`
	}
	if err := json.Unmarshal([]byte(text), &outcomes); err != nil || len(outcomes) != 1 {
		t.Fatalf("report_findings answered %s", text)
	}
	if !outcomes[0].Created {
		t.Fatalf("%s was deduped into a ticket this call did not file, so its actor is not ours", outcomes[0].Key)
	}
	return outcomes[0].Key
}

func TestMCPTakesTheActorFromTheCredential(t *testing.T) {
	h := loggedIn(t, map[string]string{"t3": "architecture-scout"})

	// The call claims to be the human in X-Actor, exactly as the REST and GraphQL cases above do.
	call := mcpSessionAs(t, h, map[string]string{"Authorization": "Bearer t3", "X-Actor": "human"})
	key := filedOverMCP(t, call, "filed over MCP with login on", "")
	if got := eventActors(t, h, "t3", key); got[0] != "architecture-scout" {
		t.Errorf("%s's created event names %q, want the credential's actor", key, got[0])
	}
}

func TestMCPInLocalModeStillReadsXActor(t *testing.T) {
	h := router(t) // TICKETING_TOKENS unset

	named := mcpSessionAs(t, h, map[string]string{"X-Actor": "cleanup-scout"})
	key := filedOverMCP(t, named, "filed over MCP in local mode", "")
	if got := eventActors(t, h, "", key); got[0] != "cleanup-scout" {
		t.Errorf("created event names %q, want the X-Actor header", got[0])
	}

	// And MCP's own default from rule 10, which no header and no credential still falls back to.
	key = filedOverMCP(t, mcpSessionAs(t, h, nil), "filed over MCP with no actor at all", "")
	if got := eventActors(t, h, "", key); got[0] != "claude-code" {
		t.Errorf("MCP with no X-Actor names %q, want claude-code", got[0])
	}
}

// The compatibility half: every tool's `actor` argument is gone, and jsonschema-go marks each
// argument struct additionalProperties:false, so without the receiving middleware that deletes the
// key an old caller's call would not be misattributed — it would be refused outright.
func TestAnMCPCallStillCarryingActorSucceeds(t *testing.T) {
	const stale = `,"actor":"someone-else"`

	h := loggedIn(t, map[string]string{"t4": "evaluator"})
	call := mcpSessionAs(t, h, map[string]string{"Authorization": "Bearer t4"})
	key := filedOverMCP(t, call, "an old caller, login on", stale)
	if got := eventActors(t, h, "t4", key); got[0] != "evaluator" {
		t.Errorf("%s's created event names %q, want the credential's actor and not the argument", key, got[0])
	}

	local := router(t)
	localCall := mcpSessionAs(t, local, map[string]string{"X-Actor": "narrator"})
	key = filedOverMCP(t, localCall, "an old caller, local mode", stale)
	if got := eventActors(t, local, "", key); got[0] != "narrator" {
		t.Errorf("%s's created event names %q, want the X-Actor header and not the argument", key, got[0])
	}
}

// Rule 10's last turn of the screw (TP-319): the two Only Joel moves that have an API door stop being
// the human's by being the mutations REST and MCP leave out, and become his by credential. The list
// itself lives in the `the-agents` guidance, and these two are the only part of it the API can police.

// asHuman is the line `scripts/issue-token.sh <actor> --human` writes. credentialsFile passes the
// actor through verbatim, so one map value carrying the third field is one human credential.
const asHuman = "joel human"

// over runs one GraphQL document as the holder of a token. A resolver's refusal is 200 with an errors
// array, so this reads the body either way and leaves the judging to the caller.
func over(t *testing.T, h http.Handler, token, query string) any {
	t.Helper()
	rec := hit(t, h, "POST", "/graphql", fmt.Sprintf(`{"query":%q}`, query),
		map[string]string{"Authorization": "Bearer " + token})
	if rec.Code != http.StatusOK {
		t.Fatalf("%s = %d, body %q", query, rec.Code, rec.Body.String())
	}
	return asJSON(t, rec)
}

// wentThrough fails with the refusal itself, rather than leaving the caller to read a null field.
func wentThrough(t *testing.T, out any) any {
	t.Helper()
	if errs := at(t, out, "errors"); errs != nil {
		t.Fatalf("refused, want it allowed: %v", errs)
	}
	return out
}

// registeredTripwire is something for confirmTripwire to be tried on. Registering is the harness's
// move and needs no human; body_sha256 is scripts/tripwire-hash.sh's to compute (rule 26) and the
// API never checks it, so any non-empty string stands in here.
func registeredTripwire(t *testing.T, h http.Handler, token string) string {
	t.Helper()
	rec := hit(t, h, "POST", "/api/tripwires",
		`{"test_ref":"internal/app/auth_test.go:OnlyJoel","meaning":"the gate itself","body_sha256":"not-hashed-here"}`,
		map[string]string{"Authorization": "Bearer " + token})
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("registering a tripwire = %d, body %q", rec.Code, rec.Body.String())
	}
	return str(t, asJSON(t, rec), "id")
}

// triaged files an AI ticket over REST and hands back what the Approver's moves take: an id and the
// version they have to match.
func triaged(t *testing.T, h http.Handler, token, title string) (string, string) {
	t.Helper()
	rec := hit(t, h, "POST", "/api/tickets", ticketNamed(title), map[string]string{"Authorization": "Bearer " + token})
	if rec.Code != http.StatusCreated {
		t.Fatalf("filing %q = %d, body %q", title, rec.Code, rec.Body.String())
	}
	out := asJSON(t, rec)
	return str(t, out, "id"), itoa(num(t, out, "version"))
}

func TestOnlyAHumanCredentialMakesAnOnlyJoelMove(t *testing.T) {
	h := loggedIn(t, map[string]string{"agent": "strategist", "joel": asHuman})
	tripwire := registeredTripwire(t, h, "agent")
	const newTag = `mutation { createTag(name: "only-joel") { name } }`
	confirm := `mutation { confirmTripwire(id: "` + tripwire + `") { confirmedBy } }`

	// Registering that tripwire was an agent's to do. Confirming it is not, and nor is a new tag.
	for _, tc := range []struct{ name, mutation string }{
		{"createTag", newTag},
		{"confirmTripwire", confirm},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := over(t, h, "agent", tc.mutation)
			if code := str(t, out, "errors", 0, "extensions", "code"); code != "FORBIDDEN" {
				t.Errorf("an agent credential's %s answered %q, want FORBIDDEN", tc.name, code)
			}
			// The message is the whole point: an agent that reads it knows to file needs-joel instead.
			message := str(t, out, "errors", 0, "message")
			for _, want := range []string{tc.name, "Only Joel", "the-agents", "human", "strategist"} {
				if !strings.Contains(message, want) {
					t.Errorf("%s was refused with %q, want it to name %q", tc.name, message, want)
				}
			}
		})
	}

	// The same two moves on Joel's own credential, which is the half that proves the refusal above is
	// the human check and not something else saying no.
	tag := wentThrough(t, over(t, h, "joel", newTag))
	if got := str(t, tag, "data", "createTag", "name"); got != "only-joel" {
		t.Errorf("Joel's createTag made %q", got)
	}
	confirmed := wentThrough(t, over(t, h, "joel", confirm))
	if got := str(t, confirmed, "data", "confirmTripwire", "confirmedBy"); got != "joel" {
		t.Errorf("confirmTripwire recorded %q, want the credential's actor", got)
	}

	// And what the Approver does on its delegation still goes through on an agent credential: the gate
	// is those two moves, not every mutation REST and MCP leave out (rule 17).
	for _, tc := range []struct{ mutation, want string }{
		{"acceptTicket", "open"},
		{"dismissTicket", "closed"},
	} {
		id, version := triaged(t, h, "agent", "a triage ticket the Approver "+tc.mutation+"s")
		out := wentThrough(t, over(t, h, "agent",
			fmt.Sprintf(`mutation { %s(id: %q, version: %s) { status } }`, tc.mutation, id, version)))
		if got := str(t, out, "data", tc.mutation, "status"); got != tc.want {
			t.Errorf("the Approver's %s left the ticket %q, want %q", tc.mutation, got, tc.want)
		}
	}
}

// In local mode there is no credential to mark human, so the gate judges nobody: those moves are the
// human's by transport alone again, exactly as they were before login existed.
func TestLocalModeMakesNoOnlyJoelJudgement(t *testing.T) {
	h := router(t)
	out := wentThrough(t, over(t, h, "", `mutation { createTag(name: "local-mode") { name } }`))
	if got := str(t, out, "data", "createTag", "name"); got != "local-mode" {
		t.Errorf("createTag in local mode made %q", got)
	}
}

// Refusing to start is better than running open: a process that was told to check credentials and
// could not read them would authenticate nobody, and nothing downstream would ever say so.
func TestANamedCredentialsFileThatCannotBeUsedIsFatal(t *testing.T) {
	pool := testdb.New(t)
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(empty, []byte("# issued nothing yet\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(t.TempDir(), "absent"), empty} {
		if _, err := app.New(pool, "http://localhost:5173", path); err == nil {
			t.Errorf("TICKETING_TOKENS=%q booted anyway", path)
		}
	}
}
