package service_test

import (
	"strings"
	"testing"

	"github.com/joelshore/ticketing/api/internal/apperr"
	"github.com/joelshore/ticketing/api/internal/model"
	"github.com/joelshore/ticketing/api/internal/service"
)

// TP-108: a batch is reviewed before it starts and after it ends, and the after-review becomes the
// memory the next review starts from.
func TestABatchIsReviewedBeforeAndAfterIntoTheProjectMemory(t *testing.T) {
	ctx, pool, project := setup(t)
	memory, err := service.GetProjectMemory(ctx, pool, project.ID)
	if err != nil || memory.Review != nil || !strings.Contains(memory.ReadNext, "reads the codebase itself") {
		t.Fatalf("empty memory = %+v (%v)", memory, err)
	}

	plan, err := service.PlanBatch(ctx, pool, project, planInput("Reviewed", planned("work")))
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Batch.ReviewsRequired {
		t.Fatal("a newly planned batch does not require reviews")
	}
	id := plan.Batch.ID
	if _, err := service.AcceptBatch(ctx, pool, project.ID, id, "joel"); err != nil {
		t.Fatal(err)
	}
	if key := claimKey(t, ctx, pool, project); key != "" {
		t.Errorf("claimed %s before any before-review", key)
	}
	reason, err := service.BatchLockReason(ctx, pool, id)
	if err != nil || !strings.Contains(reason, "before-review") {
		t.Errorf("lock reason = %q (%v)", reason, err)
	}

	for _, bad := range []struct{ phase, commit, body string }{
		{"during", "abc1234", "x"}, {"before", "HEAD", "x"}, {"before", "abc1234", "  "},
	} {
		if _, err := service.RecordReview(ctx, pool, project.ID, id, bad.phase, bad.commit, bad.body, "scout"); !apperr.Is(err, apperr.BadRequest) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	if _, err := service.RecordReview(ctx, pool, project.ID, id, "after", "abc1234", "too soon", "scout"); !apperr.Is(err, apperr.Conflict) {
		t.Errorf("an after-review with no before-review: %v", err)
	}
	if _, err := service.RecordReview(ctx, pool, project.ID, id, "before", "ABC1234", "Two packages; the digest is getting long.", "scout"); err != nil {
		t.Fatal(err)
	}
	if key := claimKey(t, ctx, pool, project); key != plan.Tickets[0].Ticket.Key {
		t.Fatalf("after the before-review, claimed %q", key)
	}

	tickets, _ := service.ListBatchTickets(ctx, pool, id)
	for _, v := range tickets {
		tk := v.Ticket
		if tk.Status == model.StatusOpen {
			move(t, ctx, pool, &tk, model.StatusInProgress)
		}
		move(t, ctx, pool, &tk, model.StatusDone)
	}
	if _, err := service.FinishBatch(ctx, pool, id, nil); !apperr.Is(err, apperr.Conflict) || !strings.Contains(err.Error(), "after-review") {
		t.Errorf("finishing without an after-review: %v", err)
	}
	after, err := service.RecordReview(ctx, pool, project.ID, id, "after", "def5678", "The digest is split in two; nothing stale left.", "scout")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.FinishBatch(ctx, pool, id, model.Ptr(standInReport)); err != nil {
		t.Errorf("finishing with both reviews: %v", err)
	}
	memory, err = service.GetProjectMemory(ctx, pool, project.ID)
	if err != nil || memory.Review == nil || memory.Review.ID != after.ID || memory.BatchName != "Reviewed" ||
		!strings.Contains(memory.ReadNext, "git diff --stat def5678..HEAD") {
		t.Errorf("memory = %+v (%v)", memory, err)
	}
	reviews, _ := service.ListReviews(ctx, pool, id)
	if len(reviews) != 2 || reviews[0].Phase != "before" || reviews[0].Commit != "abc1234" {
		t.Errorf("reviews = %+v (the commit should be lower-cased)", reviews)
	}
}

// Batches planned before the rule keep working without reviews.
func TestABatchPlannedBeforeReviewsIsUnaffected(t *testing.T) {
	ctx, pool, project := setup(t)
	plan, err := service.PlanBatch(ctx, pool, project, planInput("Older", planned("work")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE batches SET reviews_required = false WHERE id = $1`, plan.Batch.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AcceptBatch(ctx, pool, project.ID, plan.Batch.ID, "joel"); err != nil {
		t.Fatal(err)
	}
	if key := claimKey(t, ctx, pool, project); key != plan.Tickets[0].Ticket.Key {
		t.Errorf("an older batch was held for a review: claimed %q", key)
	}
}

// TP-123: a reviewed batch finishes with the Narrator's report, in its five sections.
func TestAReviewedBatchFinishesWithItsReport(t *testing.T) {
	ctx, pool, project := setup(t)
	plan, err := service.PlanBatch(ctx, pool, project, planInput("Told", planned("work")))
	if err != nil {
		t.Fatal(err)
	}
	id := plan.Batch.ID
	for _, phase := range []string{"before", "after"} {
		if _, err := service.RecordReview(ctx, pool, project.ID, id, phase, "abc1234", "review", "scout"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.AcceptBatch(ctx, pool, project.ID, id, "joel"); err != nil {
		t.Fatal(err)
	}
	tickets, _ := service.ListBatchTickets(ctx, pool, id)
	for _, v := range tickets {
		tk := v.Ticket
		move(t, ctx, pool, &tk, model.StatusInProgress, model.StatusDone)
	}
	if _, err := service.FinishBatch(ctx, pool, id, nil); !apperr.Is(err, apperr.Conflict) ||
		!strings.Contains(err.Error(), "the report (summary) lacks: What was added, Why, What it achieved, Where in the code, How it moves the goals") {
		t.Errorf("finishing with no report: %v", err)
	}
	partial := "## What was added\nA thing.\n### why\nBecause.\n## What it achieved\nIt works."
	if _, err := service.FinishBatch(ctx, pool, id, &partial); !apperr.Is(err, apperr.Conflict) ||
		!strings.Contains(err.Error(), "lacks: Where in the code, How it moves the goals") {
		t.Errorf("finishing with a partial report (headings of any level and case count): %v", err)
	}
	b, err := service.FinishBatch(ctx, pool, id, model.Ptr(partial+"\n## Where in the code\napi/\n## How it moves the goals\nGoal 6."))
	if err != nil || !strings.Contains(model.Deref(b.Summary), "## Where in the code") {
		t.Errorf("finishing with the full report: %v", err)
	}
}
