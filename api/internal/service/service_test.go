// Service-layer rules from docs/api.md, straight against Postgres. Ported one for one from the
// Rust suite this replaced.
package service_test

import (
	"context"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/joelshore/ticketing/api/internal/apperr"
	"github.com/joelshore/ticketing/api/internal/model"
	"github.com/joelshore/ticketing/api/internal/service"
	"github.com/joelshore/ticketing/api/internal/testdb"
)

func ticket(title string, source model.Source, priority model.Priority) service.NewTicket {
	return service.NewTicket{
		Title:    title,
		Type:     model.TypeBug,
		Priority: priority,
		Source:   source,
		Actor:    "tester",
	}
}

func setup(t *testing.T) (context.Context, *pgxpool.Pool, *model.Project) {
	t.Helper()
	ctx := t.Context()
	pool := testdb.New(t)
	project, err := service.ResolveProject(ctx, pool, nil)
	if err != nil {
		t.Fatalf("seeded project: %v", err)
	}
	return ctx, pool, project
}

func mustCreate(t *testing.T, ctx context.Context, db service.DB, project *model.Project,
	input service.NewTicket) *service.CreateOutcome {
	t.Helper()
	outcome, err := service.CreateTicket(ctx, db, project, input)
	if err != nil {
		t.Fatalf("create ticket %q: %v", input.Title, err)
	}
	return outcome
}

func countTickets(t *testing.T, ctx context.Context, db service.DB, projectID uuid.UUID) (int, int64) {
	t.Helper()
	items, total, err := service.ListTickets(ctx, db, projectID, service.TicketFilter{}, "", 50, 0)
	if err != nil {
		t.Fatalf("list tickets: %v", err)
	}
	return len(items), total
}

func TestSeededProjectAndAreas(t *testing.T) {
	ctx, pool, project := setup(t)
	if project.Key != "TP" {
		t.Errorf("project key = %q, want TP", project.Key)
	}
	areas, err := service.ListAreas(ctx, pool, project.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	autoAccept := map[string]bool{}
	for _, a := range areas {
		names = append(names, a.Name)
		autoAccept[a.Name] = a.AutoAcceptAI
	}
	if got, want := strings.Join(names, ","), "api,docs,infra,web"; got != want {
		t.Errorf("areas = %q, want %q", got, want)
	}
	if !autoAccept["docs"] {
		t.Error("docs should skip the inbox")
	}
}

func TestHumanTicketOpensAsTP1(t *testing.T) {
	ctx, pool, project := setup(t)
	outcome := mustCreate(t, ctx, pool, project, ticket("Add a keyboard shortcut", model.SourceHuman, model.PriorityMedium))

	if !outcome.Created {
		t.Error("want created")
	}
	got := outcome.View.Ticket
	if got.Key != "TP-1" || got.Number != 1 || got.Status != model.StatusOpen || got.Version != 1 {
		t.Errorf("got %+v, want TP-1/1/open/v1", got)
	}

	events, err := service.ListEvents(ctx, pool, got.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Field != "created" || events[0].Actor != "tester" {
		t.Errorf("events = %+v, want one `created` by tester", events)
	}

	// Numbers keep counting up.
	second := mustCreate(t, ctx, pool, project, ticket("Second", model.SourceHuman, model.PriorityLow))
	if second.View.Ticket.Key != "TP-2" {
		t.Errorf("second key = %q, want TP-2", second.View.Ticket.Key)
	}
}

func TestAITicketStartsInTriageUnlessAreaAutoAccepts(t *testing.T) {
	ctx, pool, project := setup(t)

	triaged := mustCreate(t, ctx, pool, project, ticket("Unrouted finding", model.SourceAI, model.PriorityHigh))
	if triaged.View.Ticket.Status != model.StatusTriage {
		t.Errorf("status = %s, want triage", triaged.View.Ticket.Status)
	}
	if triaged.View.Area != nil {
		t.Error("an unrouted finding should have no area")
	}

	input := ticket("Docs typo", model.SourceAI, model.PriorityLow)
	input.Area = service.AreaByName("docs")
	auto := mustCreate(t, ctx, pool, project, input)
	if auto.View.Ticket.Status != model.StatusOpen {
		t.Errorf("status = %s, want open", auto.View.Ticket.Status)
	}
	if auto.View.Area == nil || auto.View.Area.Name != "docs" {
		t.Errorf("area = %+v, want docs", auto.View.Area)
	}
}

func TestSameFingerprintDedupesAndCounts(t *testing.T) {
	ctx, pool, project := setup(t)
	finding := func() service.NewTicket {
		in := ticket("Null deref in parser", model.SourceAI, model.PriorityHigh)
		in.Fingerprint = model.Ptr("fp-abc")
		return in
	}

	first := mustCreate(t, ctx, pool, project, finding())
	if !first.Created || first.View.Ticket.SeenCount != 1 {
		t.Fatalf("first = created:%v seen:%d, want true/1", first.Created, first.View.Ticket.SeenCount)
	}

	second := mustCreate(t, ctx, pool, project, finding())
	if second.Created {
		t.Error("the second report must merge, not create")
	}
	if second.View.Ticket.Key != first.View.Ticket.Key || second.View.Ticket.SeenCount != 2 {
		t.Errorf("second = %s seen:%d, want %s/2", second.View.Ticket.Key,
			second.View.Ticket.SeenCount, first.View.Ticket.Key)
	}

	comments, err := service.ListComments(ctx, pool, first.View.Ticket.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 || comments[0].Kind != "system" ||
		comments[0].Body != "Seen again by tester (2 times)" {
		t.Errorf("comments = %+v", comments)
	}

	events, err := service.ListEvents(ctx, pool, first.View.Ticket.ID)
	if err != nil {
		t.Fatal(err)
	}
	seenAgain := 0
	for _, e := range events {
		if e.Field == "seen_again" {
			seenAgain++
		}
	}
	if seenAgain != 1 {
		t.Errorf("seen_again events = %d, want 1", seenAgain)
	}

	if items, total := countTickets(t, ctx, pool, project.ID); items != 1 || total != 1 {
		t.Errorf("tickets = %d/%d, want one", items, total)
	}
}

func TestAIFindingsGetAComputedFingerprint(t *testing.T) {
	ctx, pool, project := setup(t)
	finding := func() service.NewTicket {
		in := ticket("Unused import", model.SourceAI, model.PriorityLow)
		in.Locations = []service.NewLocation{{FilePath: "api/src/x.go", LineStart: model.Ptr(int32(10))}}
		return in
	}

	first := mustCreate(t, ctx, pool, project, finding())
	if first.View.Ticket.Fingerprint == nil {
		t.Fatal("an AI finding must carry a fingerprint")
	}
	second := mustCreate(t, ctx, pool, project, finding())
	if second.Created || second.View.Ticket.SeenCount != 2 {
		t.Errorf("second = created:%v seen:%d, want false/2", second.Created, second.View.Ticket.SeenCount)
	}
}

func TestFindingIsRoutedByPathGlob(t *testing.T) {
	ctx, pool, project := setup(t)
	in := ticket("Shadowed binding", model.SourceAI, model.PriorityMedium)
	in.Locations = []service.NewLocation{{
		FilePath:  "api/internal/service/x.go",
		LineStart: model.Ptr(int32(3)),
		LineEnd:   model.Ptr(int32(9)),
		Snippet:   model.Ptr("x := 1"),
	}}
	outcome := mustCreate(t, ctx, pool, project, in)

	if outcome.View.Area == nil || outcome.View.Area.Name != "api" {
		t.Fatalf("area = %+v, want api", outcome.View.Area)
	}
	// `api` does not auto-accept, so the finding waits in triage.
	if outcome.View.Ticket.Status != model.StatusTriage {
		t.Errorf("status = %s, want triage", outcome.View.Ticket.Status)
	}

	locations, err := service.ListLocations(ctx, pool, outcome.View.Ticket.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(locations) != 1 || locations[0].FilePath != "api/internal/service/x.go" {
		t.Errorf("locations = %+v", locations)
	}
}

func TestRoutingPrefersTheMostSpecificGlob(t *testing.T) {
	ctx, pool, project := setup(t)
	if _, err := service.CreateArea(ctx, pool, project.ID, service.NewArea{
		Name:      "api-service",
		PathGlobs: []string{"api/internal/service/service.go"},
	}); err != nil {
		t.Fatal(err)
	}

	in := ticket("Long function", model.SourceAI, model.PriorityLow)
	in.Locations = []service.NewLocation{{FilePath: "api/internal/service/service.go"}}
	outcome := mustCreate(t, ctx, pool, project, in)

	if outcome.View.Area == nil || outcome.View.Area.Name != "api-service" {
		t.Errorf("area = %+v, want api-service", outcome.View.Area)
	}
}

func TestStaleVersionConflictsAndUpdatesWriteEvents(t *testing.T) {
	ctx, pool, project := setup(t)
	created := mustCreate(t, ctx, pool, project, ticket("Rename button", model.SourceHuman, model.PriorityLow))
	id := created.View.Ticket.ID

	_, err := service.UpdateTicket(ctx, pool, id, created.View.Ticket.Version+5,
		service.TicketPatch{Title: model.Ptr("Nope")}, "tester")
	if !apperr.Is(err, apperr.Conflict) {
		t.Fatalf("stale version error = %v, want a conflict", err)
	}

	updated, err := service.UpdateTicket(ctx, pool, id, created.View.Ticket.Version,
		service.TicketPatch{
			Title:    model.Ptr("Rename the submit button"),
			Priority: model.Ptr(model.PriorityHigh),
		}, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Ticket.Title != "Rename the submit button" || updated.Ticket.Priority != model.PriorityHigh {
		t.Errorf("updated = %+v", updated.Ticket)
	}
	if updated.Ticket.Version != created.View.Ticket.Version+1 {
		t.Errorf("version = %d, want %d", updated.Ticket.Version, created.View.Ticket.Version+1)
	}

	events, err := service.ListEvents(ctx, pool, id)
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]bool{}
	for _, e := range events {
		fields[e.Field] = true
	}
	// Rule 6: one row per changed field, and nothing for fields that did not change.
	for _, want := range []string{"created", "title", "priority"} {
		if !fields[want] {
			t.Errorf("missing event for %q; got %v", want, fields)
		}
	}
	if fields["description"] {
		t.Error("an unchanged field must not write an event")
	}
}

func TestDoneNeedsAResolutionAndSetsCompletedAt(t *testing.T) {
	ctx, pool, project := setup(t)
	created := mustCreate(t, ctx, pool, project, ticket("Fix the crash", model.SourceHuman, model.PriorityHigh))
	id := created.View.Ticket.ID
	version := created.View.Ticket.Version

	// open -> done is not in the table.
	_, err := service.TransitionTicket(ctx, pool, id, version, model.StatusDone, model.Ptr("done"), "tester")
	if !apperr.Is(err, apperr.Conflict) {
		t.Fatalf("open -> done error = %v, want a conflict", err)
	}

	started, err := service.TransitionTicket(ctx, pool, id, version, model.StatusInProgress, nil, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if started.Ticket.Status != model.StatusInProgress {
		t.Errorf("status = %s, want in_progress", started.Ticket.Status)
	}

	// done without a resolution is a 400.
	_, err = service.TransitionTicket(ctx, pool, id, started.Ticket.Version, model.StatusDone, nil, "tester")
	if !apperr.Is(err, apperr.BadRequest) {
		t.Fatalf("done without a resolution = %v, want a bad request", err)
	}

	done, err := service.TransitionTicket(ctx, pool, id, started.Ticket.Version, model.StatusDone,
		model.Ptr("Fixed the off-by-one"), "tester")
	if err != nil {
		t.Fatal(err)
	}
	if done.Ticket.Status != model.StatusDone || done.Ticket.CompletedAt == nil {
		t.Errorf("done = %+v, want done with completed_at", done.Ticket)
	}
	if model.Deref(done.Ticket.Resolution) != "Fixed the off-by-one" {
		t.Errorf("resolution = %v", done.Ticket.Resolution)
	}

	// Reopening clears the completion bookkeeping.
	reopened, err := service.TransitionTicket(ctx, pool, id, done.Ticket.Version, model.StatusOpen, nil, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Ticket.Status != model.StatusOpen || reopened.Ticket.CompletedAt != nil ||
		reopened.Ticket.Resolution != nil {
		t.Errorf("reopened = %+v, want open with no completed_at or resolution", reopened.Ticket)
	}
}

func TestTransitionTableMatchesTheDocumentedRules(t *testing.T) {
	// Rule 4, as a table so the service and docs/api.md cannot drift apart quietly.
	allowed := map[model.Status][]model.Status{
		model.StatusTriage:     {model.StatusOpen, model.StatusClosed},
		model.StatusOpen:       {model.StatusInProgress, model.StatusClosed},
		model.StatusInProgress: {model.StatusOpen, model.StatusDone, model.StatusClosed},
		model.StatusDone:       {model.StatusOpen},
		model.StatusClosed:     {model.StatusOpen},
	}
	for _, from := range model.Statuses {
		for _, to := range model.Statuses {
			want := false
			for _, candidate := range allowed[from] {
				if candidate == to {
					want = true
				}
			}
			if got := service.TransitionAllowed(from, to); got != want {
				t.Errorf("%s -> %s = %v, want %v", from, to, got, want)
			}
		}
	}
}

func TestFiltersAndSearch(t *testing.T) {
	ctx, pool, project := setup(t)

	bug := ticket("Crash on empty input", model.SourceHuman, model.PriorityUrgent)
	bug.Description = "The parser panics."
	mustCreate(t, ctx, pool, project, bug)

	chore := ticket("Tidy the imports", model.SourceHuman, model.PriorityLow)
	chore.Type = model.TypeChore
	mustCreate(t, ctx, pool, project, chore)

	finding := ticket("Unchecked error", model.SourceAI, model.PriorityHigh)
	finding.Area = service.AreaByName("docs")
	mustCreate(t, ctx, pool, project, finding)

	count := func(filter service.TicketFilter, search string) int64 {
		t.Helper()
		_, total, err := service.ListTickets(ctx, pool, project.ID, filter, search, 50, 0)
		if err != nil {
			t.Fatal(err)
		}
		return total
	}

	if got := count(service.TicketFilter{}, ""); got != 3 {
		t.Errorf("all = %d, want 3", got)
	}
	if got := count(service.TicketFilter{Source: model.Ptr(model.SourceAI)}, ""); got != 1 {
		t.Errorf("ai = %d, want 1", got)
	}
	if got := count(service.TicketFilter{Type: model.Ptr(model.TypeChore)}, ""); got != 1 {
		t.Errorf("chore = %d, want 1", got)
	}
	if got := count(service.TicketFilter{Priority: model.Ptr(model.PriorityUrgent)}, ""); got != 1 {
		t.Errorf("urgent = %d, want 1", got)
	}
	if got := count(service.TicketFilter{Area: service.AreaByName("docs")}, ""); got != 1 {
		t.Errorf("docs = %d, want 1", got)
	}
	if got := count(service.TicketFilter{Status: []model.Status{model.StatusOpen}}, ""); got != 3 {
		t.Errorf("open = %d, want 3 (docs auto-accepts)", got)
	}
	if got := count(service.TicketFilter{}, "parser"); got != 1 {
		t.Errorf("search body = %d, want 1", got)
	}
	if got := count(service.TicketFilter{}, "TP-2"); got != 1 {
		t.Errorf("search by key = %d, want 1", got)
	}
	if got := count(service.TicketFilter{}, "nothingmatchesthis"); got != 0 {
		t.Errorf("search miss = %d, want 0", got)
	}

	// Rule 11: priority first, then newest.
	items, _, err := service.ListTickets(ctx, pool, project.ID, service.TicketFilter{}, "", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if items[0].Ticket.Priority != model.PriorityUrgent || items[len(items)-1].Ticket.Priority != model.PriorityLow {
		t.Errorf("order = %s..%s, want urgent..low", items[0].Ticket.Priority,
			items[len(items)-1].Ticket.Priority)
	}

	// The limit is clamped rather than trusted.
	page, _, err := service.ListTickets(ctx, pool, project.ID, service.TicketFilter{}, "", 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 {
		t.Errorf("limit 1 returned %d", len(page))
	}
}

func TestTicketsCanBeFilteredByWhenTheyArrived(t *testing.T) {
	ctx, pool, project := setup(t)
	mustCreate(t, ctx, pool, project, ticket("today", model.SourceHuman, model.PriorityMedium))

	count := func(filter service.TicketFilter) int64 {
		t.Helper()
		_, total, err := service.ListTickets(ctx, pool, project.ID, filter, "", 50, 0)
		if err != nil {
			t.Fatal(err)
		}
		return total
	}

	if got := count(service.TicketFilter{CreatedAfter: model.Ptr(time.Now().Add(-5 * time.Minute))}); got != 1 {
		t.Errorf("filed a moment ago = %d, want 1", got)
	}
	if got := count(service.TicketFilter{CreatedAfter: model.Ptr(time.Now().Add(5 * time.Minute))}); got != 0 {
		t.Errorf("not filed in the next five minutes = %d, want 0", got)
	}
	if got := count(service.TicketFilter{UpdatedAfter: model.Ptr(time.Now().Add(5 * time.Minute))}); got != 0 {
		t.Errorf("updated_after bounds separately = %d, want 0", got)
	}
}

func TestClaimNextTakesTheHighestPriorityOpenTicket(t *testing.T) {
	ctx, pool, project := setup(t)
	mustCreate(t, ctx, pool, project, ticket("Low job", model.SourceHuman, model.PriorityLow))
	mustCreate(t, ctx, pool, project, ticket("Urgent job", model.SourceHuman, model.PriorityUrgent))

	claimed, err := service.ClaimNextTicket(ctx, pool, project, service.ClaimRequest{Actor: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil || claimed.View.Ticket.Title != "Urgent job" {
		t.Fatalf("claimed = %+v, want the urgent one", claimed)
	}
	if claimed.View.Ticket.Status != model.StatusInProgress || model.Deref(claimed.View.Ticket.ClaimedBy) != "worker" {
		t.Errorf("claimed = %+v, want in_progress by worker", claimed.View.Ticket)
	}

	comments, err := service.ListComments(ctx, pool, claimed.View.Ticket.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 || comments[0].Body != "Claimed by worker" {
		t.Errorf("comments = %+v", comments)
	}

	// The next claim takes the only one left.
	next, err := service.ClaimNextTicket(ctx, pool, project, service.ClaimRequest{Actor: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	if next == nil || next.View.Ticket.Title != "Low job" {
		t.Fatalf("next = %+v, want the low one", next)
	}

	// And then there is nothing claimable.
	none, err := service.ClaimNextTicket(ctx, pool, project, service.ClaimRequest{Actor: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	if none != nil {
		t.Errorf("claim = %+v, want nil", none)
	}
}

func TestClaimNextCanBeScopedToAnArea(t *testing.T) {
	ctx, pool, project := setup(t)

	elsewhere := ticket("Urgent elsewhere", model.SourceHuman, model.PriorityUrgent)
	elsewhere.Area = service.AreaByName("web")
	mustCreate(t, ctx, pool, project, elsewhere)

	here := ticket("Low but mine", model.SourceHuman, model.PriorityLow)
	here.Area = service.AreaByName("api")
	mustCreate(t, ctx, pool, project, here)

	claimed, err := service.ClaimNextTicket(ctx, pool, project, service.ClaimRequest{Area: model.Ptr("api"), Actor: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil || claimed.View.Ticket.Title != "Low but mine" {
		t.Fatalf("claimed = %+v, want the api one despite its lower priority", claimed)
	}
}

func TestClaimNextWithAnUnknownAreaIsNotFound(t *testing.T) {
	ctx, pool, project := setup(t)
	_, err := service.ClaimNextTicket(ctx, pool, project, service.ClaimRequest{Area: model.Ptr("nosucharea"), Actor: "worker"})
	if !apperr.Is(err, apperr.NotFound) {
		t.Errorf("error = %v, want a not found", err)
	}
}

func TestAreasRenameArchiveAndCount(t *testing.T) {
	ctx, pool, project := setup(t)

	created, err := service.CreateArea(ctx, pool, project.ID, service.NewArea{
		Name:        "cli",
		Description: "command line",
		PathGlobs:   []string{"cli/**"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.OpenCount != 0 {
		t.Errorf("open count = %d, want 0", created.OpenCount)
	}

	in := ticket("Flag parsing", model.SourceHuman, model.PriorityMedium)
	in.Area = service.AreaByID(created.ID)
	mustCreate(t, ctx, pool, project, in)

	reread, err := service.GetArea(ctx, pool, project.ID, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reread.OpenCount != 1 {
		t.Errorf("open count = %d, want 1", reread.OpenCount)
	}

	renamed, err := service.UpdateArea(ctx, pool, project.ID, created.ID, service.AreaPatch{
		Name:         model.Ptr("cli-tools"),
		AutoAcceptAI: model.Ptr(true),
	})
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Name != "cli-tools" || !renamed.AutoAcceptAI {
		t.Errorf("renamed = %+v", renamed)
	}

	// Rule 12: renaming onto an existing name is a conflict.
	_, err = service.UpdateArea(ctx, pool, project.ID, created.ID, service.AreaPatch{Name: model.Ptr("api")})
	if !apperr.Is(err, apperr.Conflict) {
		t.Errorf("duplicate rename = %v, want a conflict", err)
	}

	archived, err := service.SetAreaArchived(ctx, pool, project.ID, created.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if archived.ArchivedAt == nil {
		t.Error("want archived_at set")
	}

	// Archived areas are hidden by default and cannot take new tickets.
	visible, err := service.ListAreas(ctx, pool, project.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range visible {
		if a.ID == created.ID {
			t.Error("an archived area must be hidden by default")
		}
	}
	all, err := service.ListAreas(ctx, pool, project.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != len(visible)+1 {
		t.Errorf("include_archived returned %d, want %d", len(all), len(visible)+1)
	}

	rejected := ticket("Into an archived area", model.SourceHuman, model.PriorityLow)
	rejected.Area = service.AreaByID(created.ID)
	if _, err := service.CreateTicket(ctx, pool, project, rejected); !apperr.Is(err, apperr.BadRequest) {
		t.Errorf("assigning an archived area = %v, want a bad request", err)
	}
}

func TestAreaLookupsAreScopedToTheirProject(t *testing.T) {
	ctx, pool, project := setup(t)
	other, err := service.CreateProject(ctx, pool, "OPS", "Operations", nil)
	if err != nil {
		t.Fatal(err)
	}
	// The seeded areas belong to TP, so OPS starts with none and cannot see them.
	areas, err := service.ListAreas(ctx, pool, other.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(areas) != 0 {
		t.Errorf("a new project has %d areas, want 0", len(areas))
	}

	tpAreas, err := service.ListAreas(ctx, pool, project.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetArea(ctx, pool, other.ID, tpAreas[0].ID); !apperr.Is(err, apperr.NotFound) {
		t.Errorf("cross-project area lookup = %v, want a not found", err)
	}
}

func TestBatchCollectsFindings(t *testing.T) {
	ctx, pool, project := setup(t)
	run, err := service.StartBatch(ctx, pool, project.ID, service.NewBatch{
		Kind:  model.BatchReview,
		Title: "post-port review",
		Brief: "the API just moved from Rust to Go; look for what the port dropped",
		Actor: "reviewer",
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.FindingsCount != 0 || run.FinishedAt != nil {
		t.Errorf("new run = %+v", run)
	}

	findings := []service.NewTicket{
		{Title: "First finding", Type: model.TypeBug, Priority: model.PriorityHigh,
			Locations: []service.NewLocation{{FilePath: "api/internal/x.go"}}, BatchID: &run.ID, Actor: "reviewer"},
		{Title: "Second finding", Type: model.TypeImprovement, Priority: model.PriorityLow,
			Locations: []service.NewLocation{{FilePath: "web/src/x.tsx"}}, BatchID: &run.ID, Actor: "reviewer"},
	}
	outcomes, err := service.ReportFindings(ctx, pool, project, findings)
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 2 || !outcomes[0].Created || !outcomes[1].Created {
		t.Errorf("outcomes = %+v", outcomes)
	}
	// ReportFindings forces source ai, so both land in triage.
	for _, o := range outcomes {
		if o.Status != model.StatusTriage {
			t.Errorf("%s status = %s, want triage", o.Key, o.Status)
		}
	}

	finished, err := service.FinishBatch(ctx, pool, run.ID, model.Ptr("two findings"))
	if err != nil {
		t.Fatal(err)
	}
	if finished.FindingsCount != 2 {
		t.Errorf("findings count = %d, want 2", finished.FindingsCount)
	}
	if finished.FinishedAt == nil || model.Deref(finished.Summary) != "two findings" {
		t.Errorf("finished = %+v", finished)
	}

	if _, err := service.FinishBatch(ctx, pool, uuid.New(), nil); !apperr.Is(err, apperr.NotFound) {
		t.Errorf("finishing an unknown run = %v, want a not found", err)
	}
}

func TestPullRequestsSurfaceAsPRURL(t *testing.T) {
	ctx, pool, project := setup(t)
	created := mustCreate(t, ctx, pool, project, ticket("Wire it up", model.SourceHuman, model.PriorityMedium))
	id := created.View.Ticket.ID

	linked, err := service.LinkPullRequest(ctx, pool, id, "https://github.com/o/r/pull/7",
		model.Ptr(int32(7)), model.Ptr("ticket/tp-1-wire-it-up"), model.PRStateOpen, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if model.Deref(linked.PRURL) != "https://github.com/o/r/pull/7" {
		t.Errorf("pr_url = %v", linked.PRURL)
	}
	if model.Deref(linked.Ticket.Branch) != "ticket/tp-1-wire-it-up" {
		t.Errorf("branch = %v, want the one the PR carried", linked.Ticket.Branch)
	}

	// The newest pull request wins.
	again, err := service.LinkPullRequest(ctx, pool, id, "https://github.com/o/r/pull/8",
		nil, nil, model.PRStateOpen, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if model.Deref(again.PRURL) != "https://github.com/o/r/pull/8" {
		t.Errorf("pr_url = %v, want the newer one", again.PRURL)
	}

	prs, err := service.ListPullRequests(ctx, pool, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 2 {
		t.Errorf("pull requests = %d, want 2", len(prs))
	}
}

func TestBadInputIsRejected(t *testing.T) {
	ctx, pool, project := setup(t)

	empty := ticket("   ", model.SourceHuman, model.PriorityLow)
	if _, err := service.CreateTicket(ctx, pool, project, empty); !apperr.Is(err, apperr.BadRequest) {
		t.Errorf("empty title = %v, want a bad request", err)
	}

	long := ticket(strings.Repeat("x", 201), model.SourceHuman, model.PriorityLow)
	if _, err := service.CreateTicket(ctx, pool, project, long); !apperr.Is(err, apperr.BadRequest) {
		t.Errorf("201-character title = %v, want a bad request", err)
	}

	created := mustCreate(t, ctx, pool, project, ticket("Real one", model.SourceHuman, model.PriorityLow))
	if _, err := service.AddComment(ctx, pool, created.View.Ticket.ID, "tester", "  "); !apperr.Is(err, apperr.BadRequest) {
		t.Errorf("empty comment = %v, want a bad request", err)
	}
	if _, err := service.AddComment(ctx, pool, uuid.New(), "tester", "hi"); !apperr.Is(err, apperr.NotFound) {
		t.Errorf("comment on a missing ticket = %v, want a not found", err)
	}

	for _, name := range []string{"Api", "-api", "api space", strings.Repeat("a", 65)} {
		if _, err := service.CreateArea(ctx, pool, project.ID, service.NewArea{Name: name}); !apperr.Is(err, apperr.BadRequest) {
			t.Errorf("area name %q = %v, want a bad request", name, err)
		}
	}
}

func TestPathGlobsMustCompile(t *testing.T) {
	ctx, pool, project := setup(t)
	_, err := service.CreateArea(ctx, pool, project.ID, service.NewArea{
		Name:      "broken",
		PathGlobs: []string{"api/[unclosed"},
	})
	if !apperr.Is(err, apperr.BadRequest) {
		t.Errorf("uncompilable glob = %v, want a bad request", err)
	}

	good, err := service.CreateArea(ctx, pool, project.ID, service.NewArea{
		Name:      "fine",
		PathGlobs: []string{"fine/**"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.UpdateArea(ctx, pool, project.ID, good.ID, service.AreaPatch{
		PathGlobs: &[]string{"also/[unclosed"},
	})
	if !apperr.Is(err, apperr.BadRequest) {
		t.Errorf("uncompilable glob on update = %v, want a bad request", err)
	}
}

func TestPRURLsAndFilePathsAreValidated(t *testing.T) {
	ctx, pool, project := setup(t)
	created := mustCreate(t, ctx, pool, project, ticket("Validate me", model.SourceHuman, model.PriorityLow))
	id := created.View.Ticket.ID

	for _, url := range []string{"ftp://x/y", "notaurl", "https://", "https://x/ y", "http://" + strings.Repeat("x", 2048)} {
		if _, err := service.LinkPullRequest(ctx, pool, id, url, nil, nil, model.PRStateOpen, "tester"); !apperr.Is(err, apperr.BadRequest) {
			t.Errorf("pr url %q = %v, want a bad request", url, err)
		}
	}

	bad := ticket("Bad path", model.SourceAI, model.PriorityLow)
	bad.Locations = []service.NewLocation{{FilePath: "  "}}
	if _, err := service.CreateTicket(ctx, pool, project, bad); !apperr.Is(err, apperr.BadRequest) {
		t.Errorf("empty file path = %v, want a bad request", err)
	}
	nul := ticket("NUL path", model.SourceAI, model.PriorityLow)
	nul.Locations = []service.NewLocation{{FilePath: "api/x\x00.go"}}
	if _, err := service.CreateTicket(ctx, pool, project, nul); !apperr.Is(err, apperr.BadRequest) {
		t.Errorf("NUL in a file path = %v, want a bad request", err)
	}
}

// --- projects ---------------------------------------------------------------

func TestASecondProjectNumbersItsOwnTickets(t *testing.T) {
	ctx, pool, seeded := setup(t)
	other, err := service.CreateProject(ctx, pool, "ops", "Operations", nil)
	if err != nil {
		t.Fatal(err)
	}
	if other.Key != "OPS" {
		t.Errorf("key = %q, want OPS (upper-cased)", other.Key)
	}

	listed, err := service.ListProjects(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].Key != "TP" || listed[1].Key != "OPS" {
		t.Errorf("projects = %+v, want TP then OPS (oldest first)", listed)
	}

	a := mustCreate(t, ctx, pool, seeded, ticket("first here", model.SourceHuman, model.PriorityMedium))
	b := mustCreate(t, ctx, pool, other, ticket("first there", model.SourceHuman, model.PriorityMedium))
	if a.View.Ticket.Key != "TP-1" || b.View.Ticket.Key != "OPS-1" {
		t.Errorf("keys = %s / %s, want TP-1 / OPS-1", a.View.Ticket.Key, b.View.Ticket.Key)
	}

	// And a project's board never shows another's tickets.
	theirs, total, err := service.ListTickets(ctx, pool, other.ID, service.TicketFilter{}, "", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || theirs[0].Ticket.Key != "OPS-1" {
		t.Errorf("OPS board = %d tickets %+v", total, theirs)
	}
}

func TestATicketKeyResolvesItsOwnProject(t *testing.T) {
	ctx, pool, _ := setup(t)
	other, err := service.CreateProject(ctx, pool, "OPS", "Operations", nil)
	if err != nil {
		t.Fatal(err)
	}
	created := mustCreate(t, ctx, pool, other, ticket("theirs", model.SourceHuman, model.PriorityMedium))

	// No project argument anywhere: OPS-1 says which project it is in.
	resolved, err := service.ResolveProjectForTicket(ctx, pool, created.View.Ticket.Key)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ID != other.ID {
		t.Errorf("resolved %s, want OPS", resolved.Key)
	}

	// A uuid has no project prefix, so it falls back to the default rather than erroring.
	fallback, err := service.ResolveProjectForTicket(ctx, pool, created.View.Ticket.ID.String())
	if err != nil {
		t.Fatal(err)
	}
	if fallback.Key != "TP" {
		t.Errorf("uuid fell back to %s, want TP", fallback.Key)
	}
}

func TestProjectKeysAreValidatedAndUnique(t *testing.T) {
	ctx, pool, _ := setup(t)
	for _, bad := range []string{"a", "", "TOOLONGAKEY", "1ST", "OP-S", "ops ops"} {
		if _, err := service.CreateProject(ctx, pool, bad, "Nope", nil); err == nil {
			t.Errorf("key %q was accepted", bad)
		}
	}
	if _, err := service.CreateProject(ctx, pool, "OPS", "Operations", nil); err != nil {
		t.Fatal(err)
	}
	_, err := service.CreateProject(ctx, pool, "ops", "Duplicate", nil)
	if !apperr.Is(err, apperr.Conflict) || !strings.Contains(apperr.As(err).Message, "already exists") {
		t.Errorf("duplicate key = %v, want a conflict saying it exists", err)
	}
	if _, err := service.CreateProject(ctx, pool, "OPS2", "", nil); !apperr.Is(err, apperr.BadRequest) {
		t.Errorf("empty name = %v, want a bad request", err)
	}
}

// TestNewProjectGetsTheStarterTags is also the guard that keeps service.starterTags and the
// migrations that seeded the same list saying the same thing: the migrated project and a created
// one are compared tag for tag, so adding to one list without the other fails here.
func TestNewProjectGetsTheStarterTags(t *testing.T) {
	ctx, pool, seeded := setup(t)
	created, err := service.CreateProject(ctx, pool, "OPS", "Operations", nil)
	if err != nil {
		t.Fatal(err)
	}
	names := func(projectID uuid.UUID) []string {
		t.Helper()
		tags, err := service.ListTags(ctx, pool, projectID, false)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, tag := range tags {
			out = append(out, tag.Name+": "+tag.Description)
		}
		return out
	}
	want, got := names(seeded.ID), names(created.ID)
	if len(got) == 0 {
		t.Fatal("a new project got no tags at all")
	}
	if !slices.Equal(want, got) {
		t.Errorf("new project tags\n got %v\nwant %v (the migrated project's)", got, want)
	}

	// The point of having them: a ticket in the new project can name one.
	input := ticket("Tagged in a brand new project", model.SourceHuman, model.PriorityMedium)
	input.Tags = []string{"needs-joel"}
	input.Description = whatYouDo // rule 27: the tag that asks for Joel needs its steps
	outcome, err := service.CreateTicket(ctx, pool, created, input)
	if err != nil {
		t.Fatalf("tagging a ticket in a new project: %v", err)
	}
	if !slices.Contains(outcome.View.Tags, "needs-joel") {
		t.Errorf("ticket tags = %v, want needs-joel", outcome.View.Tags)
	}
}

// --- the fingerprint race ---------------------------------------------------

func insertRawTicket(t *testing.T, ctx context.Context, db service.DB, projectID uuid.UUID, key, fingerprint string) {
	t.Helper()
	number := key[strings.LastIndex(key, "-")+1:]
	_, err := db.Exec(ctx,
		`INSERT INTO tickets (project_id, number, key, title, type, status, source, fingerprint) `+
			`VALUES ($1, $2, $3, 'Planted finding', 'bug', 'triage', 'ai', $4)`,
		projectID, number, key, fingerprint)
	if err != nil {
		t.Fatalf("raw insert: %v", err)
	}
}

func TestCreateMergesIntoATicketInsertedOutsideTheService(t *testing.T) {
	ctx, pool, project := setup(t)
	insertRawTicket(t, ctx, pool, project.ID, "TP-77", "fp-planted")

	in := ticket("Same finding, reported again", model.SourceAI, model.PriorityHigh)
	in.Fingerprint = model.Ptr("fp-planted")
	outcome := mustCreate(t, ctx, pool, project, in)

	if outcome.Created {
		t.Error("want a merge, not a create")
	}
	if outcome.View.Ticket.Key != "TP-77" || outcome.View.Ticket.SeenCount != 2 {
		t.Errorf("merged into %s seen:%d", outcome.View.Ticket.Key, outcome.View.Ticket.SeenCount)
	}
	// The planted title wins: a merge never overwrites the existing ticket.
	if outcome.View.Ticket.Title != "Planted finding" {
		t.Errorf("title = %q, want the planted one", outcome.View.Ticket.Title)
	}

	events, err := service.ListEvents(ctx, pool, outcome.View.Ticket.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Field != "seen_again" ||
		model.Deref(events[0].OldValue) != "1" || model.Deref(events[0].NewValue) != "2" {
		t.Errorf("events = %+v", events)
	}

	if items, total := countTickets(t, ctx, pool, project.ID); items != 1 || total != 1 {
		t.Errorf("tickets = %d/%d, want one", items, total)
	}
}

// TestCreateRecoversWhenTheDuplicateLandsMidFlight is the losing side of the race: the duplicate
// only becomes visible after CreateTicket has passed its pre-check, so the insert is what collides.
func TestCreateRecoversWhenTheDuplicateLandsMidFlight(t *testing.T) {
	ctx, pool, project := setup(t)
	var before int32
	if err := pool.QueryRow(ctx, `SELECT next_ticket_number FROM projects WHERE id = $1`,
		project.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}

	// Hold the duplicate in an open transaction: invisible to everyone else, but it already owns
	// the fingerprint in the unique index.
	planted, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	insertRawTicket(t, ctx, planted, project.ID, "TP-77", "fp-race")

	type createResult struct {
		outcome *service.CreateOutcome
		err     error
	}
	done := make(chan createResult, 1)
	go func() {
		in := ticket("Racing finding", model.SourceAI, model.PriorityHigh)
		in.Fingerprint = model.Ptr("fp-race")
		outcome, err := service.CreateTicket(context.Background(), pool, project, in)
		done <- createResult{outcome, err}
	}()

	// Let it get past the pre-check and block on the unique index, then release.
	time.Sleep(250 * time.Millisecond)
	if err := planted.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	got := <-done
	if got.err != nil {
		t.Fatalf("the loser must merge, not fail: %v", got.err)
	}
	if got.outcome.Created {
		t.Error("the loser must merge, not create")
	}
	if got.outcome.View.Ticket.Key != "TP-77" || got.outcome.View.Ticket.SeenCount != 2 {
		t.Errorf("merged into %s seen:%d", got.outcome.View.Ticket.Key, got.outcome.View.Ticket.SeenCount)
	}

	// Exactly one ticket, and the rolled-back savepoint left no gap in the number sequence.
	if _, total := countTickets(t, ctx, pool, project.ID); total != 1 {
		t.Errorf("tickets = %d, want 1", total)
	}
	var after int32
	if err := pool.QueryRow(ctx, `SELECT next_ticket_number FROM projects WHERE id = $1`,
		project.ID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("next_ticket_number went %d -> %d; a merged create must not burn a number", before, after)
	}
}

func TestTwoConcurrentCreatesWithOneFingerprintProduceOneTicket(t *testing.T) {
	ctx, pool, project := setup(t)

	var wg sync.WaitGroup
	results := make([]*service.CreateOutcome, 2)
	errs := make([]error, 2)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			in := ticket("Concurrent finding", model.SourceAI, model.PriorityUrgent)
			in.Fingerprint = model.Ptr("fp-concurrent")
			results[i], errs[i] = service.CreateTicket(context.Background(), pool, project, in)
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("create %d failed: %v", i, err)
		}
	}
	if results[0].View.Ticket.Key != results[1].View.Ticket.Key {
		t.Errorf("keys = %s / %s, want one ticket not two",
			results[0].View.Ticket.Key, results[1].View.Ticket.Key)
	}
	created := 0
	for _, r := range results {
		if r.Created {
			created++
		}
	}
	if created != 1 {
		t.Errorf("%d of the two created the ticket, want exactly 1", created)
	}

	items, total, err := service.ListTickets(ctx, pool, project.ID, service.TicketFilter{}, "", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || items[0].Ticket.SeenCount != 2 {
		t.Errorf("tickets = %d, seen = %d; want 1 and 2", total, items[0].Ticket.SeenCount)
	}
	comments, err := service.ListComments(ctx, pool, items[0].Ticket.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 || comments[0].Body != "Seen again by tester (2 times)" {
		t.Errorf("comments = %+v", comments)
	}
}

// --- order and categories (migration 0003) --------------------------------------

func TestTheWiderTicketTypesAreAccepted(t *testing.T) {
	ctx, pool, project := setup(t)
	for _, typ := range []model.TicketType{model.TypeResearch, model.TypeDesign, model.TypeRefactor, model.TypeEval} {
		input := ticket("a "+string(typ)+" ticket", model.SourceHuman, model.PriorityMedium)
		input.Type = typ
		if got := mustCreate(t, ctx, pool, project, input); got.View.Ticket.Type != typ {
			t.Errorf("type = %q, want %q", got.View.Ticket.Type, typ)
		}
	}
}

func TestTagsAreAManagedListSeededPerProject(t *testing.T) {
	ctx, pool, project := setup(t)
	var names []string
	rows, err := pool.Query(ctx, `SELECT name FROM tags WHERE project_id = $1 ORDER BY name`, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	want := "architecture bug-check consistency docs-drift eval needs-joel performance security tech-debt"
	if got := strings.Join(names, " "); got != want {
		t.Errorf("seeded tags = %q, want %q", got, want)
	}
}

func TestABatchCarriesItsRoadmapPlaceAndHumanRead(t *testing.T) {
	ctx, pool, project := setup(t)
	var roadmapID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO roadmaps (project_id, title) VALUES ($1, 'Evolution') RETURNING id`,
		project.ID).Scan(&roadmapID); err != nil {
		t.Fatal(err)
	}
	batch, _ := planBatch(t, ctx, pool, project, false)
	if _, err := pool.Exec(ctx,
		`UPDATE batches SET roadmap_id = $2, sequence = 1, what = 'order', how = 'a column', checkpoint_decision = 'continue' WHERE id = $1`,
		batch.ID, roadmapID); err != nil {
		t.Fatal(err)
	}
	got, err := service.GetBatch(ctx, pool, project.ID, batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if model.Deref(got.RoadmapID) != roadmapID || model.Deref(got.Sequence) != 1 || got.What != "order" ||
		got.How != "a column" || model.Deref(got.CheckpointDecision) != model.CheckpointContinue {
		t.Errorf("batch read back as %+v", got)
	}

	// A sequence means nothing outside a roadmap, and two batches cannot share a place in one.
	if _, err := pool.Exec(ctx, `UPDATE batches SET roadmap_id = NULL WHERE id = $1`, batch.ID); err == nil {
		t.Error("a batch kept its sequence after leaving the roadmap")
	}
	second, _ := planBatch(t, ctx, pool, project, false)
	if _, err := pool.Exec(ctx, `UPDATE batches SET roadmap_id = $2, sequence = 1 WHERE id = $1`,
		second.ID, roadmapID); err == nil {
		t.Error("two batches took sequence 1 in the same roadmap")
	}
}

// --- rule 15: order, rule 16: finishing a plan -------------------------------------

// planBatch files one ticket per title into a new plan batch, positioned in the order given. The
// last title is the eval ticket when withEval is set. plan_batch does this for real (TP-56); the
// row and the positions are written directly here so the rules can be tested on their own — with no
// titles it is a bare plan, which plan_batch cannot make since TP-171 gave it its one door.
func planBatch(t *testing.T, ctx context.Context, db service.DB, project *model.Project, withEval bool,
	titles ...string) (*model.Batch, []*model.Ticket) {
	t.Helper()
	var id uuid.UUID
	if err := db.QueryRow(ctx, `INSERT INTO batches (project_id, kind, title) VALUES ($1, 'plan', 'plan') RETURNING id`,
		project.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	batch, err := service.GetBatch(ctx, db, project.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]*model.Ticket, 0, len(titles))
	for i, title := range titles {
		input := ticket(title, model.SourceHuman, model.PriorityMedium)
		input.BatchID = &batch.ID
		if withEval && i == len(titles)-1 {
			input.Type = model.TypeEval
		}
		created := mustCreate(t, ctx, db, project, input)
		if _, err := db.Exec(ctx, `UPDATE tickets SET batch_position = $2 WHERE id = $1`,
			created.View.Ticket.ID, i+1); err != nil {
			t.Fatal(err)
		}
		tk := created.View.Ticket
		out = append(out, &tk)
	}
	return batch, out
}

// move walks a ticket through the given statuses, keeping its version current.
// wants fails unless subject contains every substring given. Ten tests spelled this loop out; label
// names what was searched so a failure still reads the way it used to (TP-177).
func wants(t *testing.T, subject, label string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(subject, w) {
			t.Errorf("%s lacks %q:\n%s", label, w, subject)
		}
	}
}

// finishAll moves every ticket in an accepted plan to done and finishes its batch. Five test files
// wanted a finished batch and each wrote this loop out again, so it lives beside move (TP-177).
func finishAll(t *testing.T, ctx context.Context, pool *pgxpool.Pool, project *model.Project,
	accepted []model.TicketView, batchID uuid.UUID) *model.Batch {
	t.Helper()
	for _, v := range accepted {
		cur, err := service.GetTicket(ctx, pool, project.ID, v.Ticket.Key)
		if err != nil {
			t.Fatal(err)
		}
		tk := cur.Ticket
		move(t, ctx, pool, &tk, model.StatusInProgress, model.StatusDone)
	}
	batch, err := service.FinishBatch(ctx, pool, batchID, nil)
	if err != nil {
		t.Fatal(err)
	}
	return batch
}

func move(t *testing.T, ctx context.Context, db service.DB, tk *model.Ticket, to ...model.Status) {
	t.Helper()
	for _, status := range to {
		patch := service.TicketPatch{Status: &status}
		if status.IsTerminal() {
			patch.Resolution = model.Ptr("done in the test")
		}
		// Rule 21: done carries the evidence its type asks for.
		if status == model.StatusDone {
			patch.VerifiedBy = model.Ptr("go test ./... — ok")
			if tk.Type == model.TypeFeature && tk.WorkedExample == "" {
				patch.WorkedExample = model.Ptr("one input and what it now produces")
			}
		}
		view, err := service.UpdateTicket(ctx, db, tk.ID, tk.Version, patch, "tester")
		if err != nil {
			t.Fatalf("move %s to %s: %v", tk.Key, status, err)
		}
		*tk = view.Ticket
	}
}

func claimKey(t *testing.T, ctx context.Context, db service.DB, project *model.Project) string {
	t.Helper()
	result, err := service.ClaimNextTicket(ctx, db, project, service.ClaimRequest{Actor: "tester"})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if result == nil {
		return ""
	}
	return result.View.Ticket.Key
}

func TestABatchIsClaimedInOrderWhateverThePriority(t *testing.T) {
	ctx, pool, project := setup(t)
	_, tickets := planBatch(t, ctx, pool, project, false, "first", "second", "third")
	if _, err := pool.Exec(ctx, `UPDATE tickets SET priority = 'urgent' WHERE id = $1`, tickets[2].ID); err != nil {
		t.Fatal(err)
	}

	if got := claimKey(t, ctx, pool, project); got != tickets[0].Key {
		t.Fatalf("first claim = %q, want %s despite the urgent ticket 3", got, tickets[0].Key)
	}
	if got := claimKey(t, ctx, pool, project); got != "" {
		t.Fatalf("claimed %s while ticket 1 was still in progress", got)
	}
	first, err := service.GetTicket(ctx, pool, project.ID, tickets[0].Key)
	if err != nil {
		t.Fatal(err)
	}
	tk := first.Ticket
	move(t, ctx, pool, &tk, model.StatusDone)
	if got := claimKey(t, ctx, pool, project); got != tickets[1].Key {
		t.Errorf("after ticket 1 is done the claim = %q, want %s", got, tickets[1].Key)
	}
}

func TestATriageTicketHoldsUpTheOnesAfterItUntilItIsDecided(t *testing.T) {
	ctx, pool, project := setup(t)
	_, tickets := planBatch(t, ctx, pool, project, false, "not accepted yet", "waiting")
	if _, err := pool.Exec(ctx, `UPDATE tickets SET status = 'triage' WHERE id = $1`, tickets[0].ID); err != nil {
		t.Fatal(err)
	}
	if got := claimKey(t, ctx, pool, project); got != "" {
		t.Fatalf("claimed %s past a ticket still in triage", got)
	}
	// Dismissed through the human's entry point (rule 17): a decision, so it unblocks.
	if _, err := service.TransitionTicket(ctx, pool, tickets[0].ID, tickets[0].Version, model.StatusClosed,
		model.Ptr("not needed"), "joel"); err != nil {
		t.Fatal(err)
	}
	if got := claimKey(t, ctx, pool, project); got != tickets[1].Key {
		t.Errorf("after dismissing ticket 1 the claim = %q, want %s", got, tickets[1].Key)
	}
}

func TestAMoveToInProgressCannotSkipTheOrder(t *testing.T) {
	ctx, pool, project := setup(t)
	_, tickets := planBatch(t, ctx, pool, project, false, "foundation", "built on it")
	status := model.StatusInProgress
	_, err := service.UpdateTicket(ctx, pool, tickets[1].ID, tickets[1].Version,
		service.TicketPatch{Status: &status}, "tester")
	if !apperr.Is(err, apperr.Conflict) {
		t.Fatalf("err = %v, want a conflict", err)
	}
	if !strings.Contains(err.Error(), tickets[0].Key) || !strings.Contains(err.Error(), "in order") {
		t.Errorf("refusal %q should name %s and say the batch is worked in order", err, tickets[0].Key)
	}
}

func TestALaterRoadmapBatchWaitsForTheEarlierOnesCheckpoint(t *testing.T) {
	ctx, pool, project := setup(t)
	var roadmapID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO roadmaps (project_id, title) VALUES ($1, 'Evolution') RETURNING id`,
		project.ID).Scan(&roadmapID); err != nil {
		t.Fatal(err)
	}
	first, _ := planBatch(t, ctx, pool, project, false, "batch one's work")
	second, later := planBatch(t, ctx, pool, project, false, "batch two's work")
	for seq, id := range []uuid.UUID{first.ID, second.ID} {
		if _, err := pool.Exec(ctx, `UPDATE batches SET roadmap_id = $2, sequence = $3 WHERE id = $1`,
			id, roadmapID, seq+1); err != nil {
			t.Fatal(err)
		}
	}
	// Leave batch one's ticket alone: only batch two's is a candidate, so a claim that returns
	// nothing is the roadmap rule at work.
	if _, err := pool.Exec(ctx, `UPDATE tickets SET status = 'closed', resolution = 'x' WHERE batch_id = $1`, first.ID); err != nil {
		t.Fatal(err)
	}

	refusal := func() string {
		status := model.StatusInProgress
		_, err := service.UpdateTicket(ctx, pool, later[0].ID, later[0].Version,
			service.TicketPatch{Status: &status}, "tester")
		if err == nil {
			t.Fatal("batch two's ticket started early")
		}
		return err.Error()
	}
	steps := []struct {
		sql, want string
	}{
		{``, "has not finished"},
		{`UPDATE batches SET finished_at = now() WHERE id = $1`, "waiting for its checkpoint"},
		{`UPDATE batches SET checkpoint_decision = 'stop' WHERE id = $1`, "checkpointed stop"},
	}
	for _, step := range steps {
		if step.sql != "" {
			if _, err := pool.Exec(ctx, step.sql, first.ID); err != nil {
				t.Fatal(err)
			}
		}
		if got := claimKey(t, ctx, pool, project); got != "" {
			t.Fatalf("claimed %s with batch one at %q", got, step.want)
		}
		if got := refusal(); !strings.Contains(got, step.want) {
			t.Errorf("refusal %q, want it to say %q", got, step.want)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE batches SET checkpoint_decision = 'continue' WHERE id = $1`, first.ID); err != nil {
		t.Fatal(err)
	}
	if got := claimKey(t, ctx, pool, project); got != later[0].Key {
		t.Errorf("after the checkpoint the claim = %q, want %s", got, later[0].Key)
	}
}

func TestAPlanBatchCannotFinishUntilItsEvalTicketIsDone(t *testing.T) {
	ctx, pool, project := setup(t)
	batch, tickets := planBatch(t, ctx, pool, project, true, "the work", "the eval")

	_, err := service.FinishBatch(ctx, pool, batch.ID, nil)
	if err == nil || !strings.Contains(err.Error(), tickets[0].Key+" is open") ||
		!strings.Contains(err.Error(), "no eval ticket is done") {
		t.Fatalf("finish with everything open: %v", err)
	}

	move(t, ctx, pool, tickets[0], model.StatusInProgress, model.StatusDone)
	closed := *tickets[1]
	move(t, ctx, pool, &closed, model.StatusClosed)
	if _, err := service.FinishBatch(ctx, pool, batch.ID, nil); err == nil ||
		!strings.Contains(err.Error(), "no eval ticket is done") {
		t.Fatalf("closing the eval ticket skipped the gate: %v", err)
	}

	move(t, ctx, pool, &closed, model.StatusOpen, model.StatusInProgress, model.StatusDone)
	finished, err := service.FinishBatch(ctx, pool, batch.ID, model.Ptr("all done"))
	if err != nil || finished.FinishedAt == nil {
		t.Fatalf("finish after the eval: %+v, %v", finished, err)
	}
}

func TestAFindingsBatchStillFinishesWithItsTicketsOpen(t *testing.T) {
	ctx, pool, project := setup(t)
	batch, err := service.StartBatch(ctx, pool, project.ID, service.NewBatch{Kind: model.BatchReview, Title: "review"})
	if err != nil {
		t.Fatal(err)
	}
	input := ticket("a finding", model.SourceAI, model.PriorityMedium)
	input.BatchID = &batch.ID
	mustCreate(t, ctx, pool, project, input)
	if _, err := service.FinishBatch(ctx, pool, batch.ID, nil); err != nil {
		t.Errorf("a review batch should finish once its findings are filed: %v", err)
	}
}

// Rule 17: an agent cannot accept or dismiss a finding; only the human's entry point can.
func TestOnlyTheHumanMovesATicketOutOfTriage(t *testing.T) {
	ctx, pool, project := setup(t)
	finding := mustCreate(t, ctx, pool, project, ticket("an AI finding", model.SourceAI, model.PriorityHigh)).View.Ticket
	if finding.Status != model.StatusTriage {
		t.Fatalf("status = %s, want triage", finding.Status)
	}
	for _, to := range []model.Status{model.StatusOpen, model.StatusClosed} {
		status := to
		_, err := service.UpdateTicket(ctx, pool, finding.ID, finding.Version,
			service.TicketPatch{Status: &status, Resolution: model.Ptr("self-served")}, "claude-code")
		if !apperr.Is(err, apperr.Conflict) || !strings.Contains(err.Error(), "rule 17") {
			t.Errorf("agent move to %s: %v", to, err)
		}
	}
	accepted, err := service.TransitionTicket(ctx, pool, finding.ID, finding.Version, model.StatusOpen, nil, "joel")
	if err != nil || accepted.Ticket.Status != model.StatusOpen {
		t.Fatalf("the human's accept: %+v, %v", accepted, err)
	}
}

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

// TP-20: an open ticket claimed within the TTL is skipped; once the claim is older, it is claimable
// again and the new claim replaces the old one. (Every transition clears a claim, so an open ticket
// only carries a claim stamp when something wrote it directly; the window guards exactly that.)
func TestAnExpiredClaimCanBeClaimedAgain(t *testing.T) {
	ctx, pool, project := setup(t)
	tk := mustCreate(t, ctx, pool, project, ticket("held a while ago", model.SourceHuman, model.PriorityHigh)).View.Ticket
	stamp := func(ago string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `UPDATE tickets SET claimed_by = 'someone-else', claimed_at = now() - $2::interval WHERE id = $1`, tk.ID, ago); err != nil {
			t.Fatal(err)
		}
	}
	stamp("29 minutes")
	if key := claimKey(t, ctx, pool, project); key != "" {
		t.Fatalf("a claim 29 minutes old was taken over: got %s", key)
	}
	stamp("31 minutes")
	if key := claimKey(t, ctx, pool, project); key != tk.Key {
		t.Fatalf("an expired claim was not reclaimable: got %q", key)
	}
	got, err := service.GetTicket(ctx, pool, project.ID, tk.Key)
	if err != nil {
		t.Fatal(err)
	}
	if model.Deref(got.Ticket.ClaimedBy) != "tester" || got.Ticket.Status != model.StatusInProgress {
		t.Errorf("after reclaiming: claimed_by %v, status %s", got.Ticket.ClaimedBy, got.Ticket.Status)
	}
}
