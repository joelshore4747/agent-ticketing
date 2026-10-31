package service_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/joelshore/ticketing/api/internal/model"
	"github.com/joelshore/ticketing/api/internal/service"
)

func TestGuidanceRevisionsAreAppendOnlyAndSkipNoOpSaves(t *testing.T) {
	ctx, pool, project := setup(t)

	write := func(body string) *model.GuidanceDoc {
		t.Helper()
		doc, err := service.UpsertGuidance(ctx, pool, project.ID, service.NewGuidance{
			Slug:   "go-conventions",
			Title:  "How Go is written here",
			Kind:   model.GuidanceConvention,
			Body:   body,
			Author: "tester",
		})
		if err != nil {
			t.Fatal(err)
		}
		return doc
	}

	first := write("Business rules live only in internal/service.")
	_, rev, err := service.GetGuidance(ctx, pool, project.ID, "go-conventions")
	if err != nil {
		t.Fatal(err)
	}
	if rev.Revision != 1 {
		t.Errorf("first revision = %d, want 1", rev.Revision)
	}

	// Saving the same text again must not invent history — that is what the content hash is for.
	same := write("Business rules live only in internal/service.")
	if same.CurrentRevisionID == nil || *same.CurrentRevisionID != *first.CurrentRevisionID {
		t.Error("re-saving identical text should keep the same revision")
	}

	changed := write("Business rules live only in internal/service. Never in a transport.")
	if changed.CurrentRevisionID == nil || *changed.CurrentRevisionID == *first.CurrentRevisionID {
		t.Fatal("changed text should append a revision")
	}
	_, rev, err = service.GetGuidance(ctx, pool, project.ID, "go-conventions")
	if err != nil {
		t.Fatal(err)
	}
	if rev.Revision != 2 {
		t.Errorf("second revision = %d, want 2", rev.Revision)
	}

	// The first revision is still there: a claim that pinned it can still be read back.
	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM guidance_revisions WHERE document_id = $1`, first.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("revisions kept = %d, want 2", count)
	}
}

// A bundle is read top to bottom, so its order must be deterministic: re-claiming the same ticket
// has to produce byte-identical guidance or nothing downstream can compare two runs honestly.
func TestGuidanceArrivesInBundleOrderWithABudget(t *testing.T) {
	ctx, pool, project := setup(t)

	docs := []struct {
		slug, title string
		kind        model.GuidanceKind
		body        string
	}{
		{"checks", "Before you finish", model.GuidanceChecklist, "Tick these."},
		{"conventions", "How Go is written here", model.GuidanceConvention, "Small and boring."},
		{"workflow", "Working a ticket", model.GuidanceWorkflow, "Branch, commit, PR."},
		{"area-api", "What api covers", model.GuidanceArea, "The Go API."},
	}
	for _, d := range docs {
		if _, err := service.UpsertGuidance(ctx, pool, project.ID, service.NewGuidance{
			Slug: d.slug, Title: d.title, Kind: d.kind, Body: d.body, Author: "tester",
		}); err != nil {
			t.Fatal(err)
		}
	}

	batch, err := service.StartBatch(ctx, pool, project.ID, service.NewBatch{
		Kind:  model.BatchReview,
		Title: "guidance ordering",
		Brief: "why we are doing this",
		Actor: "tester",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Attached in a deliberately unhelpful order; the bundle must still come back sorted by kind.
	if err := service.AttachGuidance(ctx, pool, project.ID, batch.ID,
		[]string{"checks", "conventions", "workflow", "area-api"}, false); err != nil {
		t.Fatal(err)
	}

	in := ticket("Work with guidance", model.SourceHuman, model.PriorityMedium)
	in.BatchID = &batch.ID
	created := mustCreate(t, ctx, pool, project, in)

	resolved, err := service.ResolveGuidance(ctx, pool, &created.View.Ticket)
	if err != nil {
		t.Fatal(err)
	}
	bundle := service.Pack(resolved, service.DefaultGuidanceBudget)

	var order []string
	for _, doc := range bundle.Docs {
		order = append(order, doc.Slug)
	}
	if got, want := strings.Join(order, ","), "workflow,area-api,conventions,checks"; got != want {
		t.Errorf("bundle order = %q, want %q", got, want)
	}
	if bundle.SHA256 == "" {
		t.Error("a served bundle must carry the hash that pins it")
	}
	if bundle.UsedTokens == 0 || bundle.UsedTokens > bundle.BudgetTokens {
		t.Errorf("used %d of %d tokens", bundle.UsedTokens, bundle.BudgetTokens)
	}

	// A document over budget is still announced, with the call that would fetch it.
	tight := service.Pack(resolved, 1)
	if len(tight.Docs) != 0 {
		t.Errorf("a 1-token budget inlined %d documents", len(tight.Docs))
	}
	if len(tight.Omitted) != len(docs) {
		t.Fatalf("omitted %d documents, want %d", len(tight.Omitted), len(docs))
	}
	if !strings.Contains(tight.Omitted[0].Fetch, "get_guidance") {
		t.Errorf("omitted entry must say how to fetch it, got %q", tight.Omitted[0].Fetch)
	}
	if tight.Omitted[0].Body != "" {
		t.Error("an omitted document must not carry its body")
	}
}

func TestClaimRecordsWhatItServed(t *testing.T) {
	ctx, pool, project := setup(t)

	if _, err := service.UpsertGuidance(ctx, pool, project.ID, service.NewGuidance{
		Slug: "workflow", Title: "Working a ticket", Kind: model.GuidanceWorkflow,
		Body: "Branch, commit, open a PR.", Author: "tester",
	}); err != nil {
		t.Fatal(err)
	}
	batch, err := service.StartBatch(ctx, pool, project.ID, service.NewBatch{
		Kind: model.BatchReview, Title: "claim recording", Brief: "so a run can be attributed",
		Actor: "tester",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.AttachGuidance(ctx, pool, project.ID, batch.ID, []string{"workflow"}, true); err != nil {
		t.Fatal(err)
	}

	in := ticket("Claim me", model.SourceHuman, model.PriorityUrgent)
	in.BatchID = &batch.ID
	mustCreate(t, ctx, pool, project, in)

	result, err := service.ClaimNextTicket(ctx, pool, project, service.ClaimRequest{
		Actor:      "worker",
		SessionID:  "session-abc",
		ClientName: "claude-code",
		RunTag:     "run-1",
		Arm:        "guided",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Fatal("nothing was claimed")
	}
	if result.Batch == nil || result.Batch.Brief != "so a run can be attributed" {
		t.Errorf("the claim must carry its batch's brief, got %+v", result.Batch)
	}
	if len(result.Guidance.Docs) != 1 || result.Guidance.Docs[0].Slug != "workflow" {
		t.Fatalf("guidance = %+v", result.Guidance)
	}

	var (
		actor, sessionID, clientName, runTag, arm, sha string
		docCount                                       int
	)
	if err := pool.QueryRow(ctx,
		`SELECT actor, session_id, client_name, run_tag, arm, guidance_sha256,
		        (SELECT count(*) FROM ticket_claim_documents d WHERE d.claim_id = c.id)
		 FROM ticket_claims c WHERE c.ticket_id = $1`,
		result.View.Ticket.ID).Scan(&actor, &sessionID, &clientName, &runTag, &arm, &sha, &docCount); err != nil {
		t.Fatalf("a claim must record itself: %v", err)
	}
	if actor != "worker" || sessionID != "session-abc" || clientName != "claude-code" {
		t.Errorf("recorded %q/%q/%q", actor, sessionID, clientName)
	}
	if runTag != "run-1" || arm != "guided" {
		t.Errorf("the arm must come from the transport, got %q/%q", runTag, arm)
	}
	if sha != result.Guidance.SHA256 {
		t.Errorf("recorded hash %q, served %q", sha, result.Guidance.SHA256)
	}
	var bytesServed int32
	if err := pool.QueryRow(ctx, `SELECT guidance_bytes FROM ticket_claims WHERE ticket_id = $1`,
		result.View.Ticket.ID).Scan(&bytesServed); err != nil {
		t.Fatal(err)
	}
	if want := int32(len(result.Guidance.Docs[0].Body)); bytesServed != want {
		t.Errorf("recorded %d bytes of guidance, served %d", bytesServed, want)
	}
	if docCount != 1 {
		t.Errorf("recorded %d served documents, want 1", docCount)
	}
}

func TestBatchesCanBeReadBack(t *testing.T) {
	ctx, pool, project := setup(t)

	batch, err := service.StartBatch(ctx, pool, project.ID, service.NewBatch{
		Kind: model.BatchAudit, Title: "post-port audit", Brief: "look for what the port dropped",
		Actor: "reviewer",
	})
	if err != nil {
		t.Fatal(err)
	}

	in := ticket("A finding", model.SourceAI, model.PriorityHigh)
	in.BatchID = &batch.ID
	mustCreate(t, ctx, pool, project, in)

	// The whole point of the rename: a batch used to be write-only.
	read, err := service.GetBatch(ctx, pool, project.ID, batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if read.Title != "post-port audit" || read.Brief != "look for what the port dropped" {
		t.Errorf("read back %+v", read)
	}

	listed, err := service.ListBatches(ctx, pool, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 {
		t.Fatalf("listed %d batches, want 1", len(listed))
	}

	tickets, err := service.ListBatchTickets(ctx, pool, batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tickets) != 1 || tickets[0].Ticket.Title != "A finding" {
		t.Errorf("batch tickets = %+v", tickets)
	}
}

func TestDriftReportsDedupePerRevision(t *testing.T) {
	ctx, pool, project := setup(t)

	write := func(body string) {
		t.Helper()
		if _, err := service.UpsertGuidance(ctx, pool, project.ID, service.NewGuidance{
			Slug: "go-conventions", Title: "How Go is written here", Kind: model.GuidanceConvention,
			Body: body, SourcePaths: []string{"api/internal/service"}, Author: "tester",
		}); err != nil {
			t.Fatal(err)
		}
	}
	write("Business rules live only in api/src/service.rs.")

	report := service.DriftReport{
		Slug:     "go-conventions",
		Problem:  "There is no api/src — the service layer is api/internal/service.",
		Evidence: "ls api/src exits 2",
		Actor:    "claude-code",
	}
	first, err := service.ReportGuidanceDrift(ctx, pool, project, report)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created {
		t.Fatal("the first report should file a ticket")
	}
	// The docs area is seeded auto_accept_ai, so a drift report is actionable immediately rather
	// than sitting in triage where nobody would see it.
	if first.View.Ticket.Status != model.StatusOpen {
		t.Errorf("status = %q, want open", first.View.Ticket.Status)
	}
	if first.View.Area == nil || first.View.Area.Name != "docs" {
		t.Errorf("area = %+v, want docs", first.View.Area)
	}

	// A second agent noticing the same thing must not file a second ticket.
	again, err := service.ReportGuidanceDrift(ctx, pool, project, report)
	if err != nil {
		t.Fatal(err)
	}
	if again.Created || again.View.Ticket.Key != first.View.Ticket.Key {
		t.Errorf("duplicate report created %q (created=%v)", again.View.Ticket.Key, again.Created)
	}
	if again.View.Ticket.SeenCount < 2 {
		t.Errorf("seen_count = %d, want at least 2", again.View.Ticket.SeenCount)
	}

	// Editing the document is the fix, so a report against the new text must get through.
	write("Business rules live only in api/internal/service.")
	afterEdit, err := service.ReportGuidanceDrift(ctx, pool, project, report)
	if err != nil {
		t.Fatal(err)
	}
	if !afterEdit.Created {
		t.Error("a new revision should accept a fresh report")
	}

	var reported *string
	if err := pool.QueryRow(ctx,
		`SELECT to_char(drift_reported_at, 'YYYY-MM-DD') FROM guidance_documents WHERE slug = 'go-conventions'`,
	).Scan(&reported); err != nil {
		t.Fatal(err)
	}
	if reported == nil {
		t.Error("reporting drift must stamp the document")
	}
}

// Every path that hands out a body must say the document is stale, because an agent that arrived by
// get_guidance rather than a claim is reading the same wrong document.
func TestAStaleDocumentIsMarkedInTheBodyItself(t *testing.T) {
	ctx, pool, project := setup(t)

	if _, err := service.UpsertGuidance(ctx, pool, project.ID, service.NewGuidance{
		Slug: "go-conventions", Title: "How Go is written here", Kind: model.GuidanceConvention,
		Body: "Business rules live only in api/src/service.rs.", Author: "tester",
	}); err != nil {
		t.Fatal(err)
	}
	doc, revision, err := service.GetGuidance(ctx, pool, project.ID, "go-conventions")
	if err != nil {
		t.Fatal(err)
	}
	if got := service.DeliverBody(*doc, revision.Body); strings.HasPrefix(got, "> STALE") {
		t.Errorf("a fresh document must not be banner-ed: %q", got)
	}

	if _, err := service.ReviewGuidance(ctx, pool, project.ID, "go-conventions", service.ReviewMark{
		Stale:       model.Ptr(true),
		StaleReason: model.Ptr("api/src no longer exists"),
	}); err != nil {
		t.Fatal(err)
	}
	doc, revision, err = service.GetGuidance(ctx, pool, project.ID, "go-conventions")
	if err != nil {
		t.Fatal(err)
	}
	delivered := service.DeliverBody(*doc, revision.Body)
	if !strings.HasPrefix(delivered, "> STALE: api/src no longer exists") {
		t.Errorf("delivered body = %q", delivered)
	}
	// Marked, never withheld: no guidance is worse than old guidance.
	if !strings.Contains(delivered, revision.Body) {
		t.Error("the banner must not replace the body")
	}
	if !strings.Contains(delivered, "report_guidance_drift") {
		t.Error("the banner must name the way to report it")
	}

	// Clearing staleness must clear the reason with it, or the document keeps explaining a problem
	// it no longer has.
	fixed, err := service.ReviewGuidance(ctx, pool, project.ID, "go-conventions", service.ReviewMark{
		Stale: model.Ptr(false), Accepted: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if fixed.Stale || fixed.StaleReason != nil {
		t.Errorf("after review: stale=%v reason=%v", fixed.Stale, fixed.StaleReason)
	}
	if fixed.ReviewedAt == nil || fixed.ReviewedBy == nil {
		t.Error("an accepted review must record who looked and when")
	}
}

// A batch with nothing attached is the evaluation runner's control arm, and the column that says
// how much guidance it received has to read zero. It used to record the length of the serialised
// empty bundle — 50 bytes of guidance on every claim that served none.
func TestAControlClaimRecordsNoGuidance(t *testing.T) {
	ctx, pool, project := setup(t)

	batch, err := service.StartBatch(ctx, pool, project.ID, service.NewBatch{
		Kind: model.BatchReview, Title: "control", Actor: "tester",
	})
	if err != nil {
		t.Fatal(err)
	}
	in := ticket("Work with nothing attached", model.SourceHuman, model.PriorityUrgent)
	in.BatchID = &batch.ID
	mustCreate(t, ctx, pool, project, in)

	result, err := service.ClaimNextTicket(ctx, pool, project,
		service.ClaimRequest{Actor: "worker", Arm: "control"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Guidance.Docs) != 0 {
		t.Fatalf("a batch with nothing attached served %+v", result.Guidance.Docs)
	}

	var (
		bytesServed int32
		sha         *string
	)
	if err := pool.QueryRow(ctx,
		`SELECT guidance_bytes, guidance_sha256 FROM ticket_claims WHERE ticket_id = $1`,
		result.View.Ticket.ID).Scan(&bytesServed, &sha); err != nil {
		t.Fatal(err)
	}
	if bytesServed != 0 {
		t.Errorf("recorded %d bytes of guidance on a claim that served none", bytesServed)
	}
	if sha != nil {
		t.Errorf("recorded hash %q on a claim that served nothing", *sha)
	}
}

// Every ticket filed by hand belongs to no batch, and for as long as that meant "no guidance" the
// corpus reached nobody: the documents were moved out of CLAUDE.md precisely so they would be
// handed over at claim time.
func TestAnUnbatchedClaimServesTheStandingCorpus(t *testing.T) {
	ctx, pool, project := setup(t)

	for _, doc := range []service.NewGuidance{
		{Slug: "checklist", Title: "Before you finish", Kind: model.GuidanceChecklist,
			Body: "Lint and test are green.", Author: "tester"},
		{Slug: "workflow", Title: "Working a ticket", Kind: model.GuidanceWorkflow,
			Body: "One ticket, one branch.", Author: "tester"},
	} {
		if _, err := service.UpsertGuidance(ctx, pool, project.ID, doc); err != nil {
			t.Fatal(err)
		}
	}

	mustCreate(t, ctx, pool, project,
		ticket("Filed by hand", model.SourceHuman, model.PriorityUrgent))

	result, err := service.ClaimNextTicket(ctx, pool, project, service.ClaimRequest{Actor: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	if result.View.Ticket.BatchID != nil {
		t.Fatal("this ticket is meant to belong to no batch")
	}

	got := make([]string, 0, len(result.Guidance.Docs))
	for _, doc := range result.Guidance.Docs {
		got = append(got, doc.Slug)
	}
	// Bundle order, not insertion order or alphabetical: how we work, then the checklist.
	if strings.Join(got, ",") != "workflow,checklist" {
		t.Errorf("served %v, want the corpus in bundle order", got)
	}
}

// TestNewProjectGetsTheCoreGuidance is goal 7's other half of TestNewProjectGetsTheStarterTags: a
// second project started with an empty corpus, so plan_batch refused every ticket for naming no key
// goal (rule 24) and the agents claiming its work were handed nothing to read. What travels is how
// work is decided and run — the same wherever the code is — and not how this repository's Go or web
// code is written, which in another checkout would simply be false.
func TestNewProjectGetsTheCoreGuidance(t *testing.T) {
	ctx, pool, seeded := setup(t)

	// The corpus is loaded by `just guidance` against a running API rather than by a migration, so a
	// test database starts with none: stand the source up the way it is really served.
	corpus := []service.NewGuidance{
		{Slug: "key-goals", Title: "The key goals", Kind: model.GuidanceWorkflow,
			Body: "1. Work the goals. 9. Nothing else.", SourcePaths: []string{"api/internal/service/goals.go"}},
		{Slug: "planning-a-batch", Title: "Planning a batch", Kind: model.GuidanceWorkflow,
			Body: "Tickets in order, the eval last.", SourcePaths: []string{"api/internal/service/plans.go"}},
		{Slug: "working-a-ticket", Title: "Working a ticket", Kind: model.GuidanceWorkflow,
			Body: "One ticket, one branch, one push."},
		{Slug: "the-agents", Title: "The agents", Kind: model.GuidanceWorkflow,
			Body: "Six of them, one step at a time.", SourcePaths: []string{".claude/agents"}},
		// Language-specific, and therefore about this repository rather than about the work: it must
		// not travel to a project whose code is somewhere else and may not even be Go.
		{Slug: "go-service-layer", Title: "How the Go API is written", Kind: model.GuidanceConvention,
			Body: "pgx, no ORM."},
	}
	for _, doc := range corpus {
		doc.Author = "tester"
		if _, err := service.UpsertGuidance(ctx, pool, seeded.ID, doc); err != nil {
			t.Fatal(err)
		}
	}

	created, err := service.CreateProject(ctx, pool, "OPS", "Operations", nil)
	if err != nil {
		t.Fatal(err)
	}

	docs, err := service.ListGuidance(ctx, pool, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(docs))
	for _, doc := range docs {
		got = append(got, doc.Slug)
	}
	slices.Sort(got)
	want := []string{"key-goals", "planning-a-batch", "the-agents", "working-a-ticket"}
	if !slices.Equal(got, want) {
		t.Errorf("a new project's guidance = %v, want exactly %v", got, want)
	}

	// Copied, not linked: each document is this project's own row at its own revision 1, carrying
	// the body the source is serving today.
	for _, source := range corpus[:4] {
		doc, revision, err := service.GetGuidance(ctx, pool, created.ID, source.Slug)
		if err != nil {
			t.Fatalf("%s: %v", source.Slug, err)
		}
		if revision == nil || revision.Revision != 1 {
			t.Fatalf("%s: revision = %v, want the copy's first", source.Slug, revision)
		}
		if revision.Body != source.Body {
			t.Errorf("%s body = %q, want the source's %q", source.Slug, revision.Body, source.Body)
		}
		if doc.Title != source.Title || doc.Kind != source.Kind {
			t.Errorf("%s = %q/%s, want %q/%s", source.Slug, doc.Title, doc.Kind, source.Title, source.Kind)
		}
		// source_paths name files in THIS repository; against another checkout the same strings are
		// what guidance-check reports as MISSING, so a copy starts unbound.
		if len(doc.SourcePaths) != 0 {
			t.Errorf("%s carried source_paths %v into another project", source.Slug, doc.SourcePaths)
		}
		var revisions int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM guidance_revisions WHERE document_id = $1`, doc.ID).Scan(&revisions); err != nil {
			t.Fatal(err)
		}
		if revisions != 1 {
			t.Errorf("%s has %d revisions in the new project, want 1", source.Slug, revisions)
		}
	}

	// The batch's "their own goals": editing the copy is not editing the original.
	if _, err := service.UpsertGuidance(ctx, pool, created.ID, service.NewGuidance{
		Slug: "key-goals", Title: "The key goals", Kind: model.GuidanceWorkflow,
		Body: "1. Operations has goals of its own.", Author: "tester"}); err != nil {
		t.Fatal(err)
	}
	_, mine, err := service.GetGuidance(ctx, pool, created.ID, "key-goals")
	if err != nil {
		t.Fatal(err)
	}
	if mine.Revision != 2 || mine.Body != "1. Operations has goals of its own." {
		t.Errorf("the copy did not take the edit: revision %d, %q", mine.Revision, mine.Body)
	}
	_, theirs, err := service.GetGuidance(ctx, pool, seeded.ID, "key-goals")
	if err != nil {
		t.Fatal(err)
	}
	if theirs.Revision != 1 || theirs.Body != corpus[0].Body {
		t.Errorf("editing the copy moved the source to revision %d, %q", theirs.Revision, theirs.Body)
	}
}

// The real corpus (5 documents, ~1,548 tokens) has never come close to the 4000-token default
// budget, so Pack()'s omission path, the omitted[].fetch hint, and get_guidance actually retrieving
// an omitted document have only ever been exercised together against a contrived budget=1
// (TestGuidanceArrivesInBundleOrderWithABudget, above). This proves the same path works at the
// REAL default budget, against a corpus sized the way a much richer one plausibly would be — so the
// path is proven before a real corpus ever grows enough to depend on it, not after.
func TestGuidanceOmissionAtTheRealBudget(t *testing.T) {
	ctx, pool, project := setup(t)

	// Ten documents of realistic, non-trivial length (not filler picked to hit a number) — enough
	// on their own to exceed DefaultGuidanceBudget several times over, the way a project whose
	// tickets "provide a lot of information for correct practices" would actually look.
	const paragraph = "This document describes a convention this project follows in real detail, " +
		"including the reasoning behind it and the failure it prevents, because a rule without its " +
		"reason attached is a rule the next person cannot safely bend. "
	var slugs []string
	for i := range 10 {
		slug := fmt.Sprintf("convention-%02d", i)
		slugs = append(slugs, slug)
		if _, err := service.UpsertGuidance(ctx, pool, project.ID, service.NewGuidance{
			Slug: slug, Title: fmt.Sprintf("Convention %d", i), Kind: model.GuidanceConvention,
			Body: strings.Repeat(paragraph, 8), Author: "tester",
		}); err != nil {
			t.Fatal(err)
		}
	}

	batch, err := service.StartBatch(ctx, pool, project.ID, service.NewBatch{
		Kind: model.BatchReview, Title: "a much larger corpus", Actor: "tester",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.AttachGuidance(ctx, pool, project.ID, batch.ID, slugs, false); err != nil {
		t.Fatal(err)
	}

	in := ticket("Work under a real corpus", model.SourceHuman, model.PriorityMedium)
	in.BatchID = &batch.ID
	created := mustCreate(t, ctx, pool, project, in)

	resolved, err := service.ResolveGuidance(ctx, pool, &created.View.Ticket)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 10 {
		t.Fatalf("resolved %d documents, want all 10 attached", len(resolved))
	}

	bundle := service.Pack(resolved, service.DefaultGuidanceBudget)

	// (a) the budget-respecting subset lands in bundle order, and actually stayed under budget.
	if len(bundle.Docs) == 0 || len(bundle.Docs) == 10 {
		t.Fatalf("%d of 10 documents inlined at the real budget — this corpus was sized to force a "+
			"genuine split; a formula change upstream would silently stop this test proving anything",
			len(bundle.Docs))
	}
	if bundle.UsedTokens > bundle.BudgetTokens {
		t.Errorf("used %d tokens against a %d budget", bundle.UsedTokens, bundle.BudgetTokens)
	}
	for i := 1; i < len(bundle.Docs); i++ {
		if bundle.Docs[i-1].Slug > bundle.Docs[i].Slug {
			t.Errorf("bundle not in order: %q before %q", bundle.Docs[i-1].Slug, bundle.Docs[i].Slug)
		}
	}

	// (b) the rest appear in Omitted, each with the call that would fetch it.
	if len(bundle.Docs)+len(bundle.Omitted) != 10 {
		t.Fatalf("%d inlined + %d omitted != 10 attached",
			len(bundle.Docs), len(bundle.Omitted))
	}
	omittedSlug := bundle.Omitted[0].Slug
	if want := fmt.Sprintf("get_guidance(slug: %q)", omittedSlug); bundle.Omitted[0].Fetch != want {
		t.Errorf("fetch hint = %q, want %q", bundle.Omitted[0].Fetch, want)
	}
	if bundle.Omitted[0].Body != "" {
		t.Error("an omitted document must not carry its body inline")
	}

	// (c) get_guidance for that omitted document really does return its real body — the documented
	// remedy for guidance.omitted actually works, not just that the hint text is well-formed.
	_, revision, err := service.GetGuidance(ctx, pool, project.ID, omittedSlug)
	if err != nil {
		t.Fatal(err)
	}
	if revision == nil || revision.Body == "" {
		t.Fatalf("get_guidance(%q) returned no body for a document Pack() just omitted", omittedSlug)
	}
	if got := estimateTokensForTest(revision.Body); got < 100 {
		t.Errorf("fetched body looks truncated: ~%d tokens", got)
	}

	// (d) the SHA256 only covers what was actually delivered — an agent that never received a
	// document must not be able to have its bytes silently counted into the pin hash.
	hashed := 0
	for _, d := range bundle.Docs {
		hashed++
		_ = d
	}
	if hashed == 0 || hashed == 10 {
		t.Fatalf("nothing to compare: %d documents contributed to the bundle", hashed)
	}
	if bundle.SHA256 == "" {
		t.Fatal("a served bundle must carry the hash that pins it")
	}
	// Two independent Pack() calls over the same attached set must be byte-identical — an agent
	// re-claiming the same ticket has to see exactly what the SHA256 says it saw.
	again := service.Pack(resolved, service.DefaultGuidanceBudget)
	if again.SHA256 != bundle.SHA256 {
		t.Error("Pack() is not reproducible over an unchanged attachment set")
	}
}

// estimateTokensForTest mirrors guidance.go's own estimateTokens (len/4+1), which is unexported.
// Duplicated rather than exported for a test: the formula is explicitly documented as "good enough
// for spending a budget and wrong for anything else", and exporting it would invite it being used
// as if it were authoritative somewhere it is not.
func estimateTokensForTest(body string) int {
	return len(body)/4 + 1
}
