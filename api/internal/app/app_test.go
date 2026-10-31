// REST, GraphQL and MCP over the real router. Ported from the Rust suite this replaced.
package app_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/joelshore/ticketing/api/internal/app"
	"github.com/joelshore/ticketing/api/internal/testdb"
)

func router(t *testing.T) http.Handler {
	t.Helper()
	pool := testdb.New(t)
	handler, err := app.New(pool, "http://localhost:5173", "")
	if err != nil {
		t.Fatalf("build the app: %v", err)
	}
	return handler
}

type body map[string]any

// send runs one request against the router and decodes whatever came back.
func send(t *testing.T, h http.Handler, method, path string, payload any) (int, any) {
	t.Helper()
	var reader io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, reader)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Actor", "curl-tester")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var decoded any
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &decoded)
	}
	return rec.Code, decoded
}

// at walks a decoded JSON tree: at(v, "items", 0, "key").
func at(t *testing.T, value any, path ...any) any {
	t.Helper()
	for _, step := range path {
		switch key := step.(type) {
		case string:
			m, ok := value.(map[string]any)
			if !ok {
				t.Fatalf("wanted an object at %v, got %T", path, value)
			}
			value = m[key]
		case int:
			list, ok := value.([]any)
			if !ok || key >= len(list) {
				t.Fatalf("wanted index %d at %v, got %T", key, path, value)
			}
			value = list[key]
		}
	}
	return value
}

func str(t *testing.T, value any, path ...any) string {
	t.Helper()
	got, ok := at(t, value, path...).(string)
	if !ok {
		t.Fatalf("wanted a string at %v, got %#v", path, at(t, value, path...))
	}
	return got
}

func num(t *testing.T, value any, path ...any) float64 {
	t.Helper()
	got, ok := at(t, value, path...).(float64)
	if !ok {
		t.Fatalf("wanted a number at %v, got %#v", path, at(t, value, path...))
	}
	return got
}

// servedSlugs is the documents a claim actually delivered, in the order it delivered them.
func servedSlugs(t *testing.T, claimed any) []string {
	t.Helper()
	if at(t, claimed, "guidance") == nil {
		return nil
	}
	docs, ok := at(t, claimed, "guidance", "docs").([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(docs))
	for i := range docs {
		out = append(out, str(t, docs, i, "slug"))
	}
	return out
}

func gql(t *testing.T, h http.Handler, query string) any {
	t.Helper()
	status, out := send(t, h, "POST", "/graphql", body{"query": query})
	if status != http.StatusOK {
		t.Fatalf("graphql status = %d, body %#v", status, out)
	}
	return out
}

func TestHealthzIsOK(t *testing.T) {
	h := router(t)
	req := httptest.NewRequest("GET", "/healthz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Errorf("healthz = %d %q", rec.Code, rec.Body.String())
	}
}

// The endpoint exists so the loop can tell the running process from the checkout, so the migration
// it names is compared with the directory rather than with the helper that produced it: asking
// migrations.Latest() twice would agree with itself however wrong it was (TP-256).
func TestVersionReportsTheCommitAndNewestMigration(t *testing.T) {
	entries, err := os.ReadDir("../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	newest := ""
	for _, entry := range entries {
		if name := entry.Name(); strings.HasSuffix(name, ".sql") && name > newest {
			newest = name
		}
	}
	if newest == "" {
		t.Fatal("no migrations on disk to compare against")
	}

	t.Setenv("GIT_COMMIT", "0ddba11feedface")
	status, out := send(t, router(t), "GET", "/api/version", nil)
	if status != http.StatusOK {
		t.Fatalf("version status = %d, body %#v", status, out)
	}
	if got := at(t, out, "commit"); got != "0ddba11feedface" {
		t.Errorf("commit = %#v, want the GIT_COMMIT the process started with", got)
	}
	if got := at(t, out, "migration"); got != newest {
		t.Errorf("migration = %#v, want %q", got, newest)
	}
}

func TestVersionSaysUnknownWithoutACommit(t *testing.T) {
	t.Setenv("GIT_COMMIT", "")
	status, out := send(t, router(t), "GET", "/api/version", nil)
	if status != http.StatusOK {
		t.Fatalf("version status = %d, body %#v", status, out)
	}
	if got := at(t, out, "commit"); got != "unknown" {
		t.Errorf("commit = %#v, want \"unknown\" when GIT_COMMIT is unset", got)
	}
}

func TestPostThenListThenPatch(t *testing.T) {
	h := router(t)

	status, created := send(t, h, "POST", "/api/tickets", body{
		"title": "REST created ticket", "description": "from curl",
		"type": "bug", "priority": "high", "source": "human", "area": "api",
	})
	if status != http.StatusCreated {
		t.Fatalf("create status = %d, body %#v", status, created)
	}
	if at(t, created, "created") != true {
		t.Error("want created: true")
	}
	if got := str(t, created, "key"); got != "TP-1" {
		t.Errorf("key = %q", got)
	}
	if got := str(t, created, "status"); got != "open" {
		t.Errorf("status = %q, want open", got)
	}
	if got := str(t, created, "area", "name"); got != "api" {
		t.Errorf("area = %q", got)
	}
	if at(t, created, "pr_url") != nil {
		t.Error("want a null pr_url")
	}

	_, page := send(t, h, "GET", "/api/tickets?source=human", nil)
	if num(t, page, "total") != 1 || str(t, page, "items", 0, "key") != "TP-1" {
		t.Errorf("human list = %#v", page)
	}
	_, page = send(t, h, "GET", "/api/tickets?source=ai", nil)
	if num(t, page, "total") != 0 {
		t.Error("the other source has nothing")
	}

	// Comma-separated and repeated status filters.
	_, page = send(t, h, "GET", "/api/tickets?status=open,triage", nil)
	if num(t, page, "total") != 1 {
		t.Error("comma-separated status")
	}
	_, page = send(t, h, "GET", "/api/tickets?status=done&status=closed", nil)
	if num(t, page, "total") != 0 {
		t.Error("repeated status")
	}

	status, one := send(t, h, "GET", "/api/tickets/TP-1", nil)
	if status != http.StatusOK || str(t, one, "title") != "REST created ticket" {
		t.Errorf("get = %d %#v", status, one)
	}

	version := num(t, created, "version")
	status, patched := send(t, h, "PATCH", "/api/tickets/TP-1", body{
		"version": version, "status": "in_progress", "priority": "urgent",
	})
	if status != http.StatusOK {
		t.Fatalf("patch status = %d, body %#v", status, patched)
	}
	if str(t, patched, "status") != "in_progress" || str(t, patched, "priority") != "urgent" {
		t.Errorf("patched = %#v", patched)
	}
	if num(t, patched, "version") != version+1 {
		t.Error("the version must move")
	}

	// Replaying the same version is a 409.
	status, conflict := send(t, h, "PATCH", "/api/tickets/TP-1", body{"version": version, "status": "open"})
	if status != http.StatusConflict || !strings.Contains(str(t, conflict, "error"), "version") {
		t.Errorf("replay = %d %#v", status, conflict)
	}

	// Closing without a resolution is a 400.
	status, badReq := send(t, h, "PATCH", "/api/tickets/TP-1", body{"version": version + 1, "status": "done"})
	if status != http.StatusBadRequest || !strings.Contains(str(t, badReq, "error"), "resolution") {
		t.Errorf("done without a resolution = %d %#v", status, badReq)
	}

	// Rule 21: done needs the evidence too, and the refusal says which.
	status, unshown := send(t, h, "PATCH", "/api/tickets/TP-1", body{"version": version + 1, "status": "done", "resolution": "shipped"})
	if status != http.StatusBadRequest || !strings.Contains(str(t, unshown, "error"), "verified_by") {
		t.Errorf("done without evidence = %d %#v", status, unshown)
	}

	status, done := send(t, h, "PATCH", "/api/tickets/TP-1", body{
		"version": version + 1, "status": "done", "resolution": "shipped", "verified_by": "go test ./... — ok",
	})
	if status != http.StatusOK || str(t, done, "status") != "done" {
		t.Errorf("done = %d %#v", status, done)
	}
	if at(t, done, "completed_at") == nil {
		t.Error("want completed_at set")
	}

	// A missing key is a 404 with the JSON error shape.
	status, missing := send(t, h, "GET", "/api/tickets/TP-999", nil)
	if status != http.StatusNotFound || str(t, missing, "error") == "" {
		t.Errorf("missing = %d %#v", status, missing)
	}
}

func TestRESTDedupesByFingerprint(t *testing.T) {
	h := router(t)
	finding := body{
		"title": "Unchecked unwrap", "type": "bug", "source": "ai",
		"locations": []body{{"file_path": "api/internal/x.go", "line_start": 4}},
	}

	status, first := send(t, h, "POST", "/api/tickets", finding)
	if status != http.StatusCreated || at(t, first, "created") != true {
		t.Fatalf("first = %d %#v", status, first)
	}
	if str(t, first, "status") != "triage" || str(t, first, "area", "name") != "api" {
		t.Errorf("first = %#v", first)
	}

	status, second := send(t, h, "POST", "/api/tickets", finding)
	if status != http.StatusOK || at(t, second, "created") != false {
		t.Fatalf("second = %d %#v", status, second)
	}
	if str(t, second, "key") != str(t, first, "key") || num(t, second, "seen_count") != 2 {
		t.Errorf("second = %#v", second)
	}
}

// Areas are listed over REST but managed over GraphQL only: they are the human's list, and an agent
// that could set auto_accept_ai would route its own findings past triage (rule 17).
func TestAreasAreListedOverRESTAndManagedOverGraphQL(t *testing.T) {
	h := router(t)

	status, areas := send(t, h, "GET", "/api/areas", nil)
	if list, ok := areas.([]any); status != http.StatusOK || !ok || len(list) != 4 {
		t.Fatalf("areas = %d %#v", status, areas)
	}
	if status, _ := send(t, h, "POST", "/api/areas", body{"name": "cli"}); status != http.StatusMethodNotAllowed {
		t.Errorf("REST create = %d, want 405", status)
	}

	out := gql(t, h, `mutation { createArea(input: {name: "cli", description: "command line", pathGlobs: ["cli/**"]}) { id name openCount } }`)
	id := str(t, out, "data", "createArea", "id")
	if str(t, out, "data", "createArea", "name") != "cli" || num(t, out, "data", "createArea", "openCount") != 0 {
		t.Fatalf("create = %#v", out)
	}
	if status, _ := send(t, h, "PATCH", "/api/areas/"+id, body{"auto_accept_ai": true}); status < 400 {
		t.Errorf("REST patch = %d; editing an area over REST must be refused", status)
	}

	out = gql(t, h, `mutation { updateArea(id: "`+id+`", patch: {name: "cli-tools", autoAcceptAi: true}) { name autoAcceptAi } }`)
	if str(t, out, "data", "updateArea", "name") != "cli-tools" || at(t, out, "data", "updateArea", "autoAcceptAi") != true {
		t.Fatalf("rename = %#v", out)
	}
	out = gql(t, h, `mutation { updateArea(id: "`+id+`", patch: {name: "api"}) { name } }`)
	if !strings.Contains(stringify(out), "already exists") {
		t.Errorf("duplicate name = %#v", out)
	}
	out = gql(t, h, `mutation { archiveArea(id: "`+id+`") { archivedAt } }`)
	if at(t, out, "data", "archiveArea", "archivedAt") == nil {
		t.Errorf("archive = %#v", out)
	}
	_, visible := send(t, h, "GET", "/api/areas", nil)
	if list, _ := visible.([]any); len(list) != 4 {
		t.Errorf("an archived area must be hidden: %d areas", len(list))
	}
	_, all := send(t, h, "GET", "/api/areas?include_archived=true", nil)
	if list, _ := all.([]any); len(list) != 5 {
		t.Errorf("include_archived = %d areas, want 5", len(list))
	}
}

func TestRESTRejectsAJunkPRURL(t *testing.T) {
	h := router(t)
	_, created := send(t, h, "POST", "/api/tickets", body{"title": "Has a PR", "type": "bug"})
	version := num(t, created, "version")
	status, out := send(t, h, "PATCH", "/api/tickets/TP-1", body{"version": version, "pr_url": "notaurl"})
	if status != http.StatusBadRequest || !strings.Contains(str(t, out, "error"), "pull request url") {
		t.Errorf("junk pr_url = %d %#v", status, out)
	}
}

func TestABadPathGlobIsRefused(t *testing.T) {
	h := router(t)
	out := gql(t, h, `mutation { createArea(input: {name: "broken", pathGlobs: ["api/[unclosed"]}) { id } }`)
	if !strings.Contains(stringify(out), "path glob") {
		t.Errorf("bad glob = %#v", out)
	}
}

func TestRESTProjectsAreListedCreatedAndKeptApart(t *testing.T) {
	h := router(t)

	status, projects := send(t, h, "GET", "/api/projects", nil)
	if status != http.StatusOK {
		t.Fatal(status)
	}
	if list, _ := projects.([]any); len(list) != 1 {
		t.Fatalf("projects = %#v", projects)
	}
	if str(t, projects, 0, "key") != "TP" {
		t.Error("the seeded project is TP")
	}

	status, created := send(t, h, "POST", "/api/projects", body{"key": "ops", "name": "Operations"})
	if status != http.StatusCreated || str(t, created, "key") != "OPS" {
		t.Fatalf("create = %d %#v", status, created)
	}

	status, conflict := send(t, h, "POST", "/api/projects", body{"key": "OPS", "name": "Again"})
	if status != http.StatusConflict || !strings.Contains(str(t, conflict, "error"), "already exists") {
		t.Errorf("duplicate = %d %#v", status, conflict)
	}

	// Each project numbers from 1, and a board only ever sees its own.
	_, here := send(t, h, "POST", "/api/tickets", body{"title": "same title both sides", "type": "chore", "project": "TP"})
	_, there := send(t, h, "POST", "/api/tickets", body{"title": "same title both sides", "type": "chore", "project": "OPS"})
	if str(t, here, "key") != "TP-1" || str(t, there, "key") != "OPS-1" {
		t.Errorf("keys = %s / %s", str(t, here, "key"), str(t, there, "key"))
	}

	_, page := send(t, h, "GET", "/api/tickets?project=OPS", nil)
	if num(t, page, "total") != 1 || str(t, page, "items", 0, "key") != "OPS-1" {
		t.Errorf("OPS board = %#v", page)
	}

	// A key carries its project, so the by-key endpoints need no parameter.
	status, one := send(t, h, "GET", "/api/tickets/OPS-1", nil)
	if status != http.StatusOK || str(t, one, "title") != "same title both sides" {
		t.Errorf("by key = %d %#v", status, one)
	}
	status, patched := send(t, h, "PATCH", "/api/tickets/OPS-1", body{
		"version": num(t, there, "version"), "priority": "urgent",
	})
	if status != http.StatusOK || str(t, patched, "priority") != "urgent" {
		t.Errorf("patch reaches the other project by key alone: %d %#v", status, patched)
	}
}

// A second project's code has to be findable before the loop can be pointed at it (goal 7, TP-144).
func TestAProjectCarriesItsRepositoryPath(t *testing.T) {
	h := router(t)

	out := gql(t, h, `mutation { createProject(key: "demo", name: "Demo", repoPath: "/tmp/demo") { key repoPath } }`)
	if str(t, out, "data", "createProject", "repoPath") != "/tmp/demo" {
		t.Fatalf("createProject = %#v", out)
	}

	// The path survives the round trip to REST, and the project seeded before the column existed
	// still loads with nothing in it.
	status, projects := send(t, h, "GET", "/api/projects", nil)
	if status != http.StatusOK {
		t.Fatal(status)
	}
	byKey := map[string]any{}
	list, _ := projects.([]any)
	for _, p := range list {
		byKey[str(t, p, "key")] = p
	}
	if len(byKey) != 2 {
		t.Fatalf("projects = %#v", projects)
	}
	if got := at(t, byKey["TP"], "repo_path"); got != nil {
		t.Errorf("TP loads with no path yet, got %#v", got)
	}
	if got := str(t, byKey["DEMO"], "repo_path"); got != "/tmp/demo" {
		t.Errorf("DEMO keeps its path, got %q", got)
	}

	// Relative is refused rather than resolved: the API's working directory is not the caller's.
	status, refused := send(t, h, "POST", "/api/projects", body{"key": "rel", "name": "Relative", "repo_path": "demo"})
	if status != http.StatusBadRequest || !strings.Contains(str(t, refused, "error"), "absolute") {
		t.Errorf("relative repo_path = %d %#v", status, refused)
	}

	// Omitting it is how you say "not known yet", and blank means the same thing.
	_, blank := send(t, h, "POST", "/api/projects", body{"key": "ops", "name": "Operations", "repo_path": "  "})
	if at(t, blank, "repo_path") != nil {
		t.Errorf("a blank path is null, got %#v", at(t, blank, "repo_path"))
	}
}

func TestGraphQLQueryAndMutations(t *testing.T) {
	h := router(t)

	out := gql(t, h, `mutation { createTicket(input: {title: "From GraphQL", type: bug, priority: high}) { key status version } }`)
	if str(t, out, "data", "createTicket", "key") != "TP-1" {
		t.Fatalf("createTicket = %#v", out)
	}
	if str(t, out, "data", "createTicket", "status") != "open" {
		t.Error("a human ticket opens")
	}
	version := num(t, out, "data", "createTicket", "version")

	out = gql(t, h, `{ tickets(limit: 10) { total items { key title type priority area { name } } } }`)
	if num(t, out, "data", "tickets", "total") != 1 {
		t.Errorf("tickets = %#v", out)
	}
	if str(t, out, "data", "tickets", "items", 0, "type") != "bug" {
		t.Error("enums serialise snake_case")
	}

	out = gql(t, h, `{ ticket(key: "TP-1") { key comments { body } events { field } locations { filePath } pullRequests { url } } }`)
	if str(t, out, "data", "ticket", "key") != "TP-1" {
		t.Errorf("ticket = %#v", out)
	}
	events, _ := at(t, out, "data", "ticket", "events").([]any)
	if len(events) != 1 {
		t.Errorf("events = %#v, want the `created` one", events)
	}

	out = gql(t, h, `{ project { key name } projects { key } areas { name openCount } }`)
	if str(t, out, "data", "project", "key") != "TP" {
		t.Errorf("project = %#v", out)
	}
	if areas, _ := at(t, out, "data", "areas").([]any); len(areas) != 4 {
		t.Errorf("areas = %d, want 4", len(areas))
	}

	// An illegal transition is a conflict carrying extensions.code.
	id := str(t, gql(t, h, `{ ticket(key: "TP-1") { id } }`), "data", "ticket", "id")
	out = gql(t, h, `mutation { transitionTicket(id: "`+id+`", version: `+itoa(version)+`, status: done, resolution: "no") { key } }`)
	if str(t, out, "errors", 0, "extensions", "code") != "CONFLICT" {
		t.Errorf("illegal transition = %#v", out)
	}
}

func TestGraphQLInboxAcceptAndDismiss(t *testing.T) {
	h := router(t)

	// Two AI findings, both landing in triage.
	for _, title := range []string{"Finding one", "Finding two"} {
		status, _ := send(t, h, "POST", "/api/tickets", body{"title": title, "type": "bug", "source": "ai"})
		if status != http.StatusCreated {
			t.Fatalf("seed %q: %d", title, status)
		}
	}

	out := gql(t, h, `{ tickets(filter: {status: [triage]}) { total items { id key version } } }`)
	if num(t, out, "data", "tickets", "total") != 2 {
		t.Fatalf("inbox = %#v", out)
	}
	first := at(t, out, "data", "tickets", "items", 0)
	second := at(t, out, "data", "tickets", "items", 1)

	accepted := gql(t, h, `mutation { acceptTicket(id: "`+str(t, first, "id")+
		`", version: `+itoa(num(t, first, "version"))+`) { key status } }`)
	if str(t, accepted, "data", "acceptTicket", "status") != "open" {
		t.Errorf("accept = %#v", accepted)
	}

	dismissed := gql(t, h, `mutation { dismissTicket(id: "`+str(t, second, "id")+
		`", version: `+itoa(num(t, second, "version"))+`) { key status resolution } }`)
	if str(t, dismissed, "data", "dismissTicket", "status") != "closed" {
		t.Errorf("dismiss = %#v", dismissed)
	}
	if str(t, dismissed, "data", "dismissTicket", "resolution") != "Dismissed from inbox" {
		t.Error("dismiss carries its default resolution")
	}

	out = gql(t, h, `{ tickets(filter: {status: [triage]}) { total } }`)
	if num(t, out, "data", "tickets", "total") != 0 {
		t.Error("the inbox should be empty")
	}
}

func TestGraphiQLIsServedOnGet(t *testing.T) {
	h := router(t)
	req := httptest.NewRequest("GET", "/graphql", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<!DOCTYPE html>") {
		t.Errorf("graphiql = %d, %q", rec.Code, rec.Body.String()[:min(80, rec.Body.Len())])
	}
}

// TestServedSchemaMatchesTheDocumentedSDL is the check the Rust suite only approximated: it
// compared a handful of fragments, so the doc could drift from the schema between them. Here
// internal/graph/schema.graphqls is *copied* from docs/api.md, so the two can be compared whole.
func TestServedSchemaMatchesTheDocumentedSDL(t *testing.T) {
	doc, err := os.ReadFile("../../../docs/api.md")
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile("(?s)## GraphQL schema.*?```graphql\n(.*?)```").FindSubmatch(doc)
	if block == nil {
		t.Fatal("docs/api.md has no GraphQL SDL block")
	}
	documented := strings.ReplaceAll(string(block[1]), "scalar DateTime   # RFC 3339", "scalar DateTime")

	served, err := os.ReadFile("../graph/schema.graphqls")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(documented) != strings.TrimSpace(string(served)) {
		t.Errorf("docs/api.md and internal/graph/schema.graphqls have drifted apart.\n"+
			"Run `just schema` to copy the documented SDL across, then `just generate`.\n"+
			"documented:\n%s\n\nserved:\n%s", documented, served)
	}
}

func TestCorsOriginMustBeARealOriginOrStar(t *testing.T) {
	pool := testdb.New(t)
	for _, bad := range []string{"", "localhost:5173", "http://", "http://x/path", "http://a b", "not an origin"} {
		if _, err := app.New(pool, bad, ""); err == nil {
			t.Errorf("CORS_ORIGIN %q was accepted", bad)
		}
	}
	for _, good := range []string{"*", "http://localhost:5173", "https://tickets.example.com:8443"} {
		if _, err := app.New(pool, good, ""); err != nil {
			t.Errorf("CORS_ORIGIN %q was refused: %v", good, err)
		}
	}
}

func TestCorsHeadersAreSent(t *testing.T) {
	h := router(t)
	req := httptest.NewRequest("OPTIONS", "/api/tickets", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Errorf("allow-origin = %q", got)
	}
	// MCP clients read the session id off the response.
	if got := rec.Header().Get("Access-Control-Expose-Headers"); got != "mcp-session-id" {
		t.Errorf("expose-headers = %q", got)
	}
}

func TestMCPIsMountedAndListsItsTools(t *testing.T) {
	h := router(t)
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18",`+
			`"capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("mcp initialize = %d, body %q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"serverInfo"`) {
		t.Errorf("initialize body = %q", rec.Body.String())
	}
	if rec.Header().Get("Mcp-Session-Id") == "" {
		t.Error("want a session id header for the client to reuse")
	}
}

func itoa(v float64) string {
	out, _ := json.Marshal(v)
	return strings.TrimSuffix(strings.TrimRight(string(out), "0"), ".")
}

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

// The whole authoring surface over REST: a guidance document, a batch that carries it, and a claim
// that returns it. Before this existed, guidance could only be written by running SQL by hand.
func TestGuidanceAndBatchesOverREST(t *testing.T) {
	h := router(t)

	code, doc := send(t, h, "PUT", "/api/guidance/go-conventions", body{
		"title":        "How Go is written here",
		"kind":         "convention",
		"body":         "Business rules live only in internal/service.",
		"source_paths": []string{"api/internal/service"},
	})
	if code != http.StatusOK {
		t.Fatalf("PUT guidance = %d: %v", code, doc)
	}
	if got := str(t, doc, "slug"); got != "go-conventions" {
		t.Errorf("slug = %q", got)
	}

	code, listed := send(t, h, "GET", "/api/guidance", nil)
	if code != http.StatusOK {
		t.Fatalf("GET guidance = %d", code)
	}
	if items, ok := listed.([]any); !ok || len(items) != 1 {
		t.Fatalf("listed %v", listed)
	}

	code, created := send(t, h, "POST", "/api/batches", body{
		"kind": "review", "title": "the REST surface", "brief": "so guidance can be authored",
		"guidance": []string{"go-conventions"},
	})
	if code != http.StatusCreated {
		t.Fatalf("POST batches = %d: %v", code, created)
	}
	batchID := str(t, created, "id")

	code, ticket := send(t, h, "POST", "/api/tickets", body{
		"title": "Work that should arrive with guidance", "type": "chore", "priority": "high",
	})
	if code != http.StatusCreated {
		t.Fatalf("POST tickets = %d: %v", code, ticket)
	}

	// This ticket belongs to no batch, so it resolves the project's standing corpus. That is every
	// ticket filed by hand and every ticket that predates batches; while an unbatched claim served
	// nothing, the documents moved out of CLAUDE.md to be handed to an agent were handed to nobody.
	code, claimed := send(t, h, "POST", "/api/claim", nil)
	if code != http.StatusOK {
		t.Fatalf("POST claim = %d: %v", code, claimed)
	}
	if got := strings.Join(servedSlugs(t, claimed), ","); got != "go-conventions" {
		t.Errorf("an unbatched claim served %q, want the standing corpus", got)
	}

	// A second document on a second batch, to check the thing the old assertion was reaching for
	// and only ever caught by accident: one batch's attachments must not appear on another's work.
	if code, second := send(t, h, "PUT", "/api/guidance/web-conventions", body{
		"title": "How the web app is written", "kind": "convention",
		"body": "Solid 2, plain CSS, no component library.",
	}); code != http.StatusOK {
		t.Fatalf("PUT second guidance = %d: %v", code, second)
	}
	if code, other := send(t, h, "POST", "/api/batches", body{
		"kind": "review", "title": "the web surface", "guidance": []string{"web-conventions"},
	}); code != http.StatusCreated {
		t.Fatalf("POST second batch = %d: %v", code, other)
	}

	code, batched := send(t, h, "POST", "/api/tickets", body{
		"title": "Work inside the first batch", "type": "chore", "priority": "high",
		"batch_id": batchID,
	})
	if code != http.StatusCreated {
		t.Fatalf("POST batched ticket = %d: %v", code, batched)
	}
	code, claimed = send(t, h, "POST", "/api/claim", nil)
	if code != http.StatusOK {
		t.Fatalf("POST claim = %d: %v", code, claimed)
	}
	if got := strings.Join(servedSlugs(t, claimed), ","); got != "go-conventions" {
		t.Errorf("a batched claim served %q, want only its own batch's document", got)
	}

	// A batch with nothing attached serves nothing, which is exactly what the evaluation runner's
	// control arm is: the guided arm's batch, with no documents on it.
	code, bare := send(t, h, "POST", "/api/batches", body{"kind": "review", "title": "control"})
	if code != http.StatusCreated {
		t.Fatalf("POST bare batch = %d: %v", code, bare)
	}
	code, control := send(t, h, "POST", "/api/tickets", body{
		"title": "Work in a batch with nothing attached", "type": "chore", "priority": "high",
		"batch_id": str(t, bare, "id"),
	})
	if code != http.StatusCreated {
		t.Fatalf("POST control ticket = %d: %v", code, control)
	}
	code, claimed = send(t, h, "POST", "/api/claim", nil)
	if code != http.StatusOK {
		t.Fatalf("POST claim = %d: %v", code, claimed)
	}
	if at(t, claimed, "guidance") != nil {
		t.Errorf("a batch with nothing attached served %v", at(t, claimed, "guidance"))
	}

	code, batch := send(t, h, "GET", "/api/batches/"+batchID, nil)
	if code != http.StatusOK {
		t.Fatalf("GET batch = %d: %v", code, batch)
	}
	if got := str(t, batch, "batch", "brief"); got != "so guidance can be authored" {
		t.Errorf("brief = %q", got)
	}

	// A plan batch has one door, POST /api/batches/plan — the one that files the eval ticket that
	// gates it (TP-171); POST /api/batches refuses the kind and says where to go.
	code, wrongDoor := send(t, h, "POST", "/api/batches", body{"kind": "plan", "title": "by hand"})
	if code != http.StatusBadRequest || !strings.Contains(str(t, wrongDoor, "error"), "plan_batch") {
		t.Fatalf("POST a plan batch = %d: %v", code, wrongDoor)
	}
	// With no plan to open it has no branch to be given either: rule 22 gives a git_ref only to a plan
	// batch, so this door names the field instead of storing one nothing would read (TP-184).
	code, withRef := send(t, h, "POST", "/api/batches", body{"kind": "review", "title": "a ref", "git_ref": "batch/x"})
	if code != http.StatusBadRequest || !strings.Contains(str(t, withRef, "error"), "git_ref") {
		t.Fatalf("POST a batch with a git_ref = %d: %v", code, withRef)
	}
	code, planned := send(t, h, "POST", "/api/batches/plan", body{
		"title": "planned work", "what": "the work", "how": "in order", "why": "so rule 16 has a plan",
		"demo": "finish it and read the refusal", "tickets": []body{{"title": "the work", "type": "chore", "goals": []int{3}}},
	})
	if code != http.StatusCreated {
		t.Fatalf("POST plan = %d: %v", code, planned)
	}
	// Rule 16: a plan whose eval ticket is not done is not finished because someone says so.
	code, refused := send(t, h, "PATCH", "/api/batches/"+str(t, planned, "batch", "id"), body{
		"finish": true, "summary": "authored one document",
	})
	if code != http.StatusConflict || !strings.Contains(str(t, refused, "error"), "no eval ticket is done") {
		t.Fatalf("finishing an unfinished plan = %d: %v", code, refused)
	}

	// A findings batch finishes when its author says so; its tickets are worked later.
	code, review := send(t, h, "POST", "/api/batches", body{"kind": "review", "title": "a review"})
	if code != http.StatusCreated {
		t.Fatalf("POST review batch = %d: %v", code, review)
	}
	code, finished := send(t, h, "PATCH", "/api/batches/"+str(t, review, "id"), body{
		"finish": true, "summary": "filed nothing",
	})
	if code != http.StatusOK {
		t.Fatalf("PATCH batch = %d: %v", code, finished)
	}
	if at(t, finished, "finished_at") == nil {
		t.Error("finishing a batch must stamp finished_at")
	}
}

// An unknown field is almost always a model guessing at the contract, and a silent 201 is a success
// it has no way to question. The one that started this said "area_id" where the API documents
// "area", and quietly produced an unrouted ticket.
func TestUnknownFieldsAreRefused(t *testing.T) {
	h := router(t)

	code, out := send(t, h, "POST", "/api/tickets", body{
		"title": "Routed by a field that does not exist", "type": "chore", "area_id": "web",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("POST with an unknown field = %d: %v", code, out)
	}
	if msg := str(t, out, "error"); !strings.Contains(msg, "area_id") {
		t.Errorf("the error must name the field, got %q", msg)
	}

	if code, ok := send(t, h, "POST", "/api/tickets", body{
		"title": "Routed by the documented field", "type": "chore", "area": "api",
	}); code != http.StatusCreated {
		t.Fatalf("POST with the documented field = %d: %v", code, ok)
	}
}

// The server instructions promise that a refused move "names the legal ones". An error that says
// only what was refused leaves the caller guessing, which is the one thing a tool error must never
// make it do.
func TestARefusedTransitionNamesTheLegalMoves(t *testing.T) {
	h := router(t)

	code, created := send(t, h, "POST", "/api/tickets", body{
		"title": "Cannot jump straight to done", "type": "chore",
	})
	if code != http.StatusCreated {
		t.Fatalf("POST tickets = %d: %v", code, created)
	}

	code, refused := send(t, h, "PATCH", "/api/tickets/"+str(t, created, "key"), body{
		"version": num(t, created, "version"), "status": "done", "resolution": "nope",
	})
	if code != http.StatusConflict {
		t.Fatalf("PATCH open -> done = %d: %v", code, refused)
	}
	msg := str(t, refused, "error")
	for _, legal := range []string{"in_progress", "closed"} {
		if !strings.Contains(msg, legal) {
			t.Errorf("error %q does not name %q as a legal move from open", msg, legal)
		}
	}
}

// A write that names its project in the query lands in that project. Writes used to read only the
// body, so `PUT /api/guidance/<slug>?project=FS` saved FS's guidance into TP without an error, twice
// (TP-358); a body and a query that disagree are refused rather than one silently winning.
func TestRESTWritesHonourTheProjectInTheQuery(t *testing.T) {
	h := router(t)
	if status, _ := send(t, h, "POST", "/api/projects", body{"key": "ops", "name": "Operations"}); status != http.StatusCreated {
		t.Fatal(status)
	}
	doc := body{"title": "Ops conventions", "kind": "convention", "body": "Ops only."}

	if code, out := send(t, h, "PUT", "/api/guidance/ops-conventions?project=OPS", doc); code != http.StatusOK {
		t.Fatalf("PUT ?project=OPS = %d: %v", code, out)
	}
	_, ops := send(t, h, "GET", "/api/guidance?project=OPS", nil)
	_, tp := send(t, h, "GET", "/api/guidance?project=TP", nil)
	if !strings.Contains(stringify(ops), "ops-conventions") || strings.Contains(stringify(tp), "ops-conventions") {
		t.Errorf("the document went to the wrong project: OPS %v, TP %v", ops, tp)
	}

	mixed := body{"title": "Mixed", "kind": "convention", "body": "x", "project": "TP"}
	code, refused := send(t, h, "PUT", "/api/guidance/mixed?project=OPS", mixed)
	if code != http.StatusBadRequest || !strings.Contains(str(t, refused, "error"), `the body names project "TP" but the query names "OPS"`) {
		t.Errorf("a body and query that disagree = %d %v, want 400 naming both", code, refused)
	}
}
