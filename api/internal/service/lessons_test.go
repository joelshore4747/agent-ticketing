// TP-186: a lesson is an ordinary finding whose whole value is that it is counted. The after-review
// files one per lesson with the fingerprint `lesson:<kebab-pattern>` and the `lesson` tag, so the
// third time a pattern bites, the Strategist reads a number instead of three reviews' prose (goal 5)
// and learning is measurable (goal 9).
//
// Nothing in the service knows the word "lesson", and that is the design: the counting is the rule 3
// fingerprint dedupe that already exists, so there is no new table and no second code path to keep
// true. What these tests pin is the convention — that a pattern key, not a wording, is what two
// reviews have to agree on — and the one behaviour the convention rests on that is easy to break.
package service_test

import (
	"context"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/joelshore/ticketing/api/internal/model"
	"github.com/joelshore/ticketing/api/internal/service"
)

// The project's tag list is the human's — creating a tag is on the Only Joel list — and `lesson` is
// on it in the real database already. The seed every test database is built from does not carry it
// yet, and putting it there would be creating a tag, so this fixture adds the row to one throwaway
// clone instead and TP-278 asks Joel for the seed. When that lands, delete this and the calls.
func seedLessonTag(t *testing.T, ctx context.Context, pool *pgxpool.Pool, project *model.Project) {
	t.Helper()
	_, err := pool.Exec(ctx,
		`INSERT INTO tags (project_id, name, description) VALUES ($1, 'lesson', $2) ON CONFLICT DO NOTHING`,
		project.ID, "A pattern an after-review drew, counted by its lesson: fingerprint")
	if err != nil {
		t.Fatalf("seed the lesson tag: %v", err)
	}
}

// lessonFinding is what architecture-scout.md tells the scout to file. The docs area has
// auto_accept_ai, so a lesson lands open rather than in triage. The title is free prose and differs
// every time on purpose: the fingerprint is the pattern, so two reviews that describe the same
// pattern in different words still land on one ticket, which a computed fingerprint would not do.
func lessonFinding(pattern, title string) service.NewTicket {
	in := ticket(title, model.SourceAI, model.PriorityMedium)
	in.Type = model.TypeChore
	in.Area = service.AreaByName("docs")
	in.Fingerprint = model.Ptr("lesson:" + pattern)
	in.Tags = []string{"lesson"}
	return in
}

func TestALessonSeenThreeTimesIsOneTicketCountedThree(t *testing.T) {
	ctx, pool, project := setup(t)
	seedLessonTag(t, ctx, pool, project)
	var key string

	for _, tc := range []struct {
		name    string
		title   string
		created bool
		seen    int32
	}{
		{"the first review draws the lesson", "The eval ran against a stale API", true, 1},
		{"a later batch trips over the same thing", "Eval hit a stale API again after a migration", false, 2},
		{"a third time, which is what makes it a pattern", "Stale API at eval time, third batch running", false, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := mustCreate(t, ctx, pool, project, lessonFinding("stale-api-at-eval", tc.title))
			if out.Created != tc.created || out.View.Ticket.SeenCount != tc.seen {
				t.Fatalf("created:%v seen:%d, want %v/%d",
					out.Created, out.View.Ticket.SeenCount, tc.created, tc.seen)
			}
			if key == "" {
				key = out.View.Ticket.Key
			} else if out.View.Ticket.Key != key {
				t.Fatalf("counted onto %s, want the count to stay on %s", out.View.Ticket.Key, key)
			}
			if !slices.Contains(out.View.Tags, "lesson") {
				t.Errorf("tags = %v, want `lesson` among them", out.View.Tags)
			}
		})
	}

	// The number is the deliverable: three reviews, one ticket, and a count somebody can read.
	if items, total := countTickets(t, ctx, pool, project.ID); items != 1 || total != 1 {
		t.Errorf("tickets = %d/%d, want the single ticket that was counted three times", items, total)
	}
}

// The decision recorded on TP-186: a lesson that recurs after its ticket was finished files a NEW
// ticket and starts a fresh count. findOpenByFingerprint (tickets_create.go) deliberately excludes
// done and closed tickets, and the partial unique index covers live ones only, so counting into a
// finished ticket would mean reopening it — and moving a ticket out of a terminal status is the
// human's move (rule 17), not a side effect of a scout filing a finding. The cost is that a pattern
// closed and recurring reads as two tickets of 1 rather than one of 4; the count is of occurrences
// since somebody last decided the lesson was handled, which is the more useful question anyway.
// Pinned here so this cannot change without someone choosing to change it.
func TestALessonThatRecursAfterItIsFinishedStartsANewCount(t *testing.T) {
	ctx, pool, project := setup(t)
	seedLessonTag(t, ctx, pool, project)

	for _, tc := range []struct {
		name    string
		pattern string
		// route is how the ticket reaches its terminal status, since open cannot go straight to done.
		route []model.Status
	}{
		{"closed as handled, then it happens again", "merge-blocked-by-commit-signing",
			[]model.Status{model.StatusClosed}},
		{"done, then it happens again", "sdl-edit-silently-drops-a-field",
			[]model.Status{model.StatusInProgress, model.StatusDone}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first := mustCreate(t, ctx, pool, project, lessonFinding(tc.pattern, "A lesson worth writing down"))
			version := first.View.Ticket.Version
			for _, status := range tc.route {
				patch := service.TicketPatch{Status: &status}
				if status.IsTerminal() {
					patch.Resolution = model.Ptr("Written into the guidance.")
					patch.VerifiedBy = model.Ptr("just gate exited 0")
				}
				view, err := service.UpdateTicket(ctx, pool, first.View.Ticket.ID, version, patch, "tester")
				if err != nil {
					t.Fatalf("move the lesson to %s: %v", status, err)
				}
				version = view.Ticket.Version
			}

			again := mustCreate(t, ctx, pool, project, lessonFinding(tc.pattern, "The same lesson, months later"))
			if !again.Created {
				t.Fatal("a recurrence after the ticket was finished merged into it instead of filing a new one")
			}
			if again.View.Ticket.Key == first.View.Ticket.Key {
				t.Fatalf("filed onto %s, the finished ticket itself", first.View.Ticket.Key)
			}
			if again.View.Ticket.SeenCount != 1 {
				t.Errorf("seen = %d, want the new ticket to start its own count at 1", again.View.Ticket.SeenCount)
			}
		})
	}
}
