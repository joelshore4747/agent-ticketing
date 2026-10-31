package app_test

import (
	"net/http"
	"strings"
	"testing"
)

// TP-88: the digest finish_batch computes is read back on every transport, and none of them lets a
// caller write it.
func TestTheDigestIsReadOnEveryTransportAndWrittenByNone(t *testing.T) {
	h := router(t)
	code, plan := send(t, h, "POST", "/api/batches/plan", body{
		"title": "Digest", "what": "a digest", "how": "at finish", "why": "Joel reads it first",
		"demo": "finish the batch and read it", "tickets": []body{{"title": "Compose it", "type": "feature", "goals": []int{6}}},
	})
	if code != http.StatusCreated {
		t.Fatalf("plan = %d %v", code, plan)
	}
	batchID := str(t, plan, "batch", "id")
	reviewed(t, h, batchID)
	if out := gql(t, h, `mutation { acceptBatch(id: "`+batchID+`") { id } }`); at(t, out, "errors") != nil {
		t.Fatalf("acceptBatch: %v", out)
	}
	work, eval := str(t, plan, "tickets", 0, "key"), str(t, plan, "tickets", 1, "key")
	send(t, h, "POST", "/api/tickets/"+work+"/decisions", body{"kind": "reframed", "rationale": "compose, then store"})
	for _, key := range []string{work, eval} {
		for _, status := range []string{"in_progress", "done"} {
			_, cur := send(t, h, "GET", "/api/tickets/"+key, nil)
			patch := body{"version": num(t, cur, "version"), "status": status}
			if status == "done" {
				patch["resolution"] = key + " finished"
				patch["verified_by"] = "go test ./... — ok"
				patch["worked_example"] = "finish the batch; its digest names this ticket"
			}
			if code, r := send(t, h, "PATCH", "/api/tickets/"+key, patch); code != http.StatusOK {
				t.Fatalf("move %s to %s = %d %v", key, status, code, r)
			}
		}
	}

	code, refused := send(t, h, "PATCH", "/api/batches/"+batchID, body{"finish": true, "digest": "all fine, trust me"})
	if code != http.StatusBadRequest || !strings.Contains(str(t, refused, "error"), "digest") {
		t.Errorf("REST let a caller write the digest: %d %v", code, refused)
	}

	call := mcpSession(t, h)
	out := call(mcpTool("finish_batch", `{"id":"`+batchID+`","summary":"`+reportJSON+`"}`))
	if strings.Contains(out, `"isError":true`) || !strings.Contains(out, `## What`) || !strings.Contains(out, "compose, then store") {
		t.Fatalf("MCP finish_batch reply lacks the digest: %s", out)
	}
	if got := call(mcpTool("get_batch", `{"id":"`+batchID+`"}`)); !strings.Contains(got, "Joel reads it first") {
		t.Errorf("MCP get_batch lacks the digest: %s", got)
	}

	_, got := send(t, h, "GET", "/api/batches/"+batchID, nil)
	digest := str(t, got, "batch", "digest")
	if !strings.Contains(digest, "2 of 2 tickets finished.") || !strings.Contains(digest, work+" finished") {
		t.Errorf("REST digest = %q", digest)
	}
	res := gql(t, h, `{ batch(id: "`+batchID+`") { digest digestAt } }`)
	if str(t, res, "data", "batch", "digest") != digest || str(t, res, "data", "batch", "digestAt") == "" {
		t.Errorf("GraphQL digest = %v", res)
	}
}

// TP-89: the human's queue over REST and GraphQL.
func TestNeedsYouIsServedOverRESTAndGraphQL(t *testing.T) {
	h := router(t)
	_, finding := send(t, h, "POST", "/api/tickets", body{"title": "An AI finding", "type": "bug", "source": "ai"})
	_, work := send(t, h, "POST", "/api/tickets", body{"title": "Work", "type": "bug"})
	send(t, h, "POST", "/api/tickets/"+str(t, work, "key")+"/decisions", body{"kind": "rejected", "rationale": "no"})

	code, got := send(t, h, "GET", "/api/needs-you", nil)
	if code != http.StatusOK || num(t, got, "total") != 2 || str(t, got, "triage", 0, "key") != str(t, finding, "key") ||
		str(t, got, "uncited", 0, "key") != str(t, work, "key") {
		t.Errorf("REST needs-you = %d %v", code, got)
	}
	res := gql(t, h, `{ needsYou { total triage { key } uncited { ticketKey decision { kind } } proposals { ticketKey } checkpoints { id } tripwires { testRef } } }`)
	if num(t, res, "data", "needsYou", "total") != 2 || str(t, res, "data", "needsYou", "uncited", 0, "decision", "kind") != "rejected" {
		t.Errorf("GraphQL needsYou = %v", res)
	}
}

// TP-95: verdicts over REST and MCP, read back on every ticket read.
func TestVerdictsAreRecordedAndReadOnEveryTransport(t *testing.T) {
	h := router(t)
	_, created := send(t, h, "POST", "/api/tickets", body{"title": "Judge me", "type": "bug"})
	id, key := str(t, created, "id"), str(t, created, "key")
	out := gql(t, h, `mutation { setTicketFoundation(id: "`+id+`", version: `+itoa(num(t, created, "version"))+`, input: {
		criteria: [{tier: behavioural, text: "it is refused"}, {tier: gate, text: "just test"}] }) { version } }`)
	if at(t, out, "errors") != nil {
		t.Fatalf("foundation: %v", out)
	}
	code, early := send(t, h, "POST", "/api/tickets/"+key+"/verdicts", body{"ordinal": 1, "verdict": "pass"})
	if code != http.StatusConflict {
		t.Errorf("judging an open ticket = %d %v", code, early)
	}
	for _, status := range []string{"in_progress", "done"} {
		_, cur := send(t, h, "GET", "/api/tickets/"+key, nil)
		patch := body{"version": num(t, cur, "version"), "status": status}
		if status == "done" {
			patch["resolution"], patch["verified_by"] = "refused now", "go test — ok"
		}
		if code, r := send(t, h, "PATCH", "/api/tickets/"+key, patch); code != http.StatusOK {
			t.Fatalf("move to %s = %d %v", status, code, r)
		}
	}
	code, pass := send(t, h, "POST", "/api/tickets/"+key+"/verdicts", body{"ordinal": 1, "verdict": "pass", "evidence": []string{"go test"}})
	if code != http.StatusCreated || str(t, pass, "verdict", "verdict") != "pass" || at(t, pass, "follow_up") != nil {
		t.Errorf("REST pass = %d %v", code, pass)
	}
	call := mcpSession(t, h)
	failed := call(mcpTool("record_verdict", `{"key":"`+key+`","ordinal":2,"verdict":"fail","reason":"web suite red"}`))
	if strings.Contains(failed, `"isError":true`) || !strings.Contains(failed, "missed: just test") {
		t.Errorf("MCP fail = %s", failed)
	}
	if detail := call(mcpTool("get_ticket", `{"key":"`+key+`"}`)); !strings.Contains(detail, "web suite red") {
		t.Errorf("get_ticket lacks verdicts: %s", detail)
	}
	_, ticket := send(t, h, "GET", "/api/tickets/"+key, nil)
	if list, _ := at(t, ticket, "verdicts").([]any); len(list) != 2 {
		t.Errorf("REST ticket verdicts = %v", at(t, ticket, "verdicts"))
	}
	res := gql(t, h, `{ ticket(key: "`+key+`") { criteria { ordinal verdicts { verdict reason judgedBy } } } }`)
	if str(t, res, "data", "ticket", "criteria", 1, "verdicts", 0, "verdict") != "fail" ||
		str(t, res, "data", "ticket", "criteria", 0, "verdicts", 0, "verdict") != "pass" {
		t.Errorf("GraphQL Criterion.verdicts = %v", res)
	}
}

// TP-98: the branch reaches the agent through the plan, the claim and the finish.
func TestBatchBranchesReachTheAgentOnEveryTransport(t *testing.T) {
	h := router(t)
	_, plan := send(t, h, "POST", "/api/batches/plan", body{
		"title": "Branches", "what": "a branch", "how": "per batch", "why": "one merge", "demo": "look",
		"tickets": []body{{"title": "Name it", "type": "chore", "goals": []int{1}}},
	})
	ref, batchID := str(t, plan, "batch", "git_ref"), str(t, plan, "batch", "id")
	reviewed(t, h, batchID)
	if !strings.HasPrefix(ref, "batch/") || !strings.HasSuffix(ref, "-branches") {
		t.Fatalf("git_ref = %q", ref)
	}
	gql(t, h, `mutation { acceptBatch(id: "`+batchID+`") { id } }`)
	_, claimed := send(t, h, "POST", "/api/claim", body{"batch_id": batchID})
	steps := stringify(at(t, claimed, "next_steps"))
	if !strings.Contains(steps, "git switch "+ref) || !strings.Contains(steps, "verified_by") {
		t.Errorf("REST claim next_steps = %s", steps)
	}
	for _, key := range []string{str(t, claimed, "ticket", "key"), str(t, plan, "tickets", 1, "key")} {
		for _, status := range []string{"in_progress", "done"} {
			_, cur := send(t, h, "GET", "/api/tickets/"+key, nil)
			if str(t, cur, "status") == status {
				continue
			}
			patch := body{"version": num(t, cur, "version"), "status": status}
			if status == "done" {
				patch["resolution"], patch["verified_by"] = "done", "go test — ok"
			}
			if code, r := send(t, h, "PATCH", "/api/tickets/"+key, patch); code != http.StatusOK {
				t.Fatalf("move %s to %s = %d %v", key, status, code, r)
			}
		}
	}
	out := mcpSession(t, h)(mcpTool("finish_batch", `{"id":"`+batchID+`","summary":"`+reportJSON+`"}`))
	if !strings.Contains(out, "batch-merge.sh "+batchID) || !strings.Contains(out, "gh pr create") {
		t.Errorf("MCP finish_batch next_steps missing: %s", out)
	}
}

// TP-27: every ticket read carries its row of the transition table, so clients need no copy of it.
func TestTicketsCarryTheirNextStatuses(t *testing.T) {
	h := router(t)
	_, created := send(t, h, "POST", "/api/tickets", body{"title": "Where can I go", "type": "bug"})
	if got := stringify(at(t, created, "next_statuses")); got != `["in_progress","closed"]` {
		t.Errorf("REST next_statuses for open = %s", got)
	}
	res := gql(t, h, `{ ticket(key: "`+str(t, created, "key")+`") { status nextStatuses } }`)
	if got := stringify(at(t, res, "data", "ticket", "nextStatuses")); got != `["in_progress","closed"]` {
		t.Errorf("GraphQL nextStatuses = %s", got)
	}
	if out := mcpSession(t, h)(mcpTool("get_ticket", `{"key":"`+str(t, created, "key")+`"}`)); !strings.Contains(out, "next_statuses") {
		t.Errorf("MCP get_ticket lacks next_statuses: %s", out)
	}
}

// TP-21: defaulted arguments are nullable, as docs/api.md says. The Rust server printed `Int! = 50`
// and refused an explicit null; the SDL-generated schema must keep accepting one as the default.
func TestADefaultedArgumentAcceptsAnExplicitNull(t *testing.T) {
	h := router(t)
	send(t, h, "POST", "/api/tickets", body{"title": "one", "type": "bug"})
	for _, q := range []string{
		`{ tickets(limit: null, offset: null) { total items { key } } }`,
		`{ areas(includeArchived: null) { name } }`,
	} {
		if res := gql(t, h, q); at(t, res, "errors") != nil {
			t.Errorf("%s → %v", q, res)
		}
	}
	res := gql(t, h, `{ __type(name: "Query") { fields { name args { name defaultValue type { kind } } } } }`)
	for _, f := range at(t, res, "data", "__type", "fields").([]any) {
		for _, a := range f.(map[string]any)["args"].([]any) {
			arg := a.(map[string]any)
			if arg["defaultValue"] != nil && arg["type"].(map[string]any)["kind"] == "NON_NULL" {
				t.Errorf("%s(%s) has a default but is non-null", f.(map[string]any)["name"], arg["name"])
			}
		}
	}
}

// TP-107: goals travel on every transport, and planning refuses a ticket that names none.
func TestGoalsTravelOnEveryTransport(t *testing.T) {
	h := router(t)
	code, created := send(t, h, "POST", "/api/tickets", body{"title": "Aimed", "type": "bug", "goals": []int{4, 2}})
	if code != http.StatusCreated || stringify(at(t, created, "goals")) != "[2,4]" {
		t.Fatalf("REST create goals = %d %v", code, at(t, created, "goals"))
	}
	key := str(t, created, "key")
	if out := mcpSession(t, h)(mcpTool("update_ticket", `{"key":"`+key+`","version":`+itoa(num(t, created, "version"))+`,"goals":[7]}`)); !strings.Contains(strings.NewReplacer(" ", "", `\n`, "", `\"`, `"`).Replace(out), `"goals":[7]`) {
		t.Errorf("MCP update goals: %s", out)
	}
	res := gql(t, h, `{ ticket(key: "`+key+`") { goals } }`)
	if stringify(at(t, res, "data", "ticket", "goals")) != "[7]" {
		t.Errorf("GraphQL goals = %v", res)
	}
	code, refused := send(t, h, "POST", "/api/batches/plan", body{"title": "Aimless", "what": "w", "how": "h", "why": "y", "demo": "d",
		"tickets": []body{{"title": "no goal", "type": "chore"}}})
	if code != http.StatusBadRequest || !strings.Contains(str(t, refused, "error"), "key goal") {
		t.Errorf("a planned ticket with no goal = %d %v", code, refused)
	}
}

// reviewed records a stand-in before and after review, for tests about other rules (goal 2 gates a
// planned batch's start and finish on them).
func reviewed(t *testing.T, h http.Handler, batchID string) {
	t.Helper()
	for _, phase := range []string{"before", "after"} {
		if code, r := send(t, h, "POST", "/api/batches/"+batchID+"/reviews", body{"phase": phase, "commit": "abc1234", "body": "stand-in"}); code != http.StatusCreated {
			t.Fatalf("review %s = %d %v", phase, code, r)
		}
	}
}

// TP-108: reviews and the project memory over REST and MCP.
func TestReviewsAndTheProjectMemoryOverTheTransports(t *testing.T) {
	h := router(t)
	_, plan := send(t, h, "POST", "/api/batches/plan", body{"title": "Reviewed", "what": "w", "how": "h", "why": "y", "demo": "d",
		"tickets": []body{{"title": "work", "type": "chore", "goals": []int{2}}}})
	batchID := str(t, plan, "batch", "id")
	gql(t, h, `mutation { acceptBatch(id: "`+batchID+`") { id } }`)
	_, got := send(t, h, "GET", "/api/batches/"+batchID, nil)
	if !strings.Contains(stringify(at(t, got, "progress")), "before-review") {
		t.Errorf("progress does not say the batch waits on a before-review: %v", at(t, got, "progress"))
	}
	call := mcpSession(t, h)
	if out := call(mcpTool("get_project_memory", `{}`)); !strings.Contains(out, "reads the codebase itself") {
		t.Errorf("empty memory: %s", out)
	}
	if out := call(mcpTool("record_review", `{"batch_id":"`+batchID+`","phase":"before","commit":"abc1234","body":"Two packages."}`)); strings.Contains(out, `"isError":true`) {
		t.Fatalf("MCP record_review: %s", out)
	}
	code, after := send(t, h, "POST", "/api/batches/"+batchID+"/reviews", body{"phase": "after", "commit": "def5678", "body": "Split the digest."})
	if code != http.StatusCreated || str(t, after, "phase") != "after" {
		t.Fatalf("REST after-review = %d %v", code, after)
	}
	_, list := send(t, h, "GET", "/api/batches/"+batchID+"/reviews", nil)
	if l, _ := list.([]any); len(l) != 2 {
		t.Errorf("reviews = %v", list)
	}
	_, memory := send(t, h, "GET", "/api/project-memory", nil)
	if str(t, memory, "review", "commit") != "def5678" || !strings.Contains(str(t, memory, "read_next"), "def5678..HEAD") {
		t.Errorf("REST project memory = %v", memory)
	}
}

// reportJSON is a stand-in Narrator report with its five sections, escaped for a JSON string
// (goal 6: a reviewed batch finishes with one).
const reportJSON = `## What was added\nx\n## Why\nx\n## What it achieved\nx\n## Where in the code\nx\n## How it moves the goals\nx`
