package service_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/joelshore/ticketing/api/internal/apperr"
	"github.com/joelshore/ticketing/api/internal/model"
	"github.com/joelshore/ticketing/api/internal/service"
)

// TP-160 (goal 9): the digest measures each batch — lines added against deleted, and the pass rate
// beside the batches before it.

// A batch whose size was reported reads back exactly the counts that were posted, not a rounding or
// a net figure the human has to work out.
func TestTheDigestReportsTheSizeThatWasMeasured(t *testing.T) {
	ctx, pool, project := setup(t)
	plan := mustPlan(t, ctx, pool, project, planInput("Measure me", planned("do the work")))
	accepted, err := service.AcceptBatch(ctx, pool, project.ID, plan.Batch.ID, "joel")
	if err != nil {
		t.Fatal(err)
	}
	base := "d4b5b34e3c99566326ab5c30bd5b3150c15d239e"
	if _, err := service.ReportBatchSize(ctx, pool, project.ID, plan.Batch.ID,
		service.SizeRequest{LinesAdded: 1446, LinesDeleted: 829, BaseCommit: &base}); err != nil {
		t.Fatal(err)
	}
	batch := finishAll(t, ctx, pool, project, accepted, plan.Batch.ID)

	digest := model.Deref(batch.Digest)
	wants(t, digest, "digest",
		"## Measures",
		"Size: 1,446 added, 829 deleted, net +617.",
		"Counted on `"+model.Deref(plan.Batch.GitRef)+"` against `"+base+"`.",
	)
	if strings.Contains(digest, "Size unknown") {
		t.Errorf("a measured batch still says the size is unknown:\n%s", digest)
	}
	// A batch that served no guidance prints nothing extra (TP-188).
	if strings.Contains(digest, "Guidance served") {
		t.Errorf("a batch that served no guidance still has a guidance line:\n%s", digest)
	}

	// Reporting again replaces the figures rather than adding to them: the script re-runs.
	if _, err := service.ReportBatchSize(ctx, pool, project.ID, plan.Batch.ID,
		service.SizeRequest{LinesAdded: 1500, LinesDeleted: 900}); err != nil {
		t.Fatal(err)
	}
	again, err := service.FinishBatch(ctx, pool, plan.Batch.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(model.Deref(again.Digest), "Size: 1,500 added, 900 deleted, net +600.") {
		t.Errorf("a second report did not replace the first:\n%s", model.Deref(again.Digest))
	}
}

// The ceiling counts code, not tests (TP-212). A batch that posts the split reads its code figure
// first, with the test figure beside it and uncharged: 63% of batch 3's growth was tests, so charging
// a test line what a code line costs paid a builder to thin the test that proved the change.
func TestTheDigestCountsCodeAndReportsTestLinesBeside(t *testing.T) {
	ctx, pool, project := setup(t)
	plan := mustPlan(t, ctx, pool, project, planInput("Split me", planned("do the work")))
	accepted, err := service.AcceptBatch(ctx, pool, project.ID, plan.Batch.ID, "joel")
	if err != nil {
		t.Fatal(err)
	}
	// Batch 3's own figures against 9a2134fe: +603/-113, of which +314/-3 was test files. So the code
	// the ceiling reads is +289/-110, net +179 — not the net +490 the rule used to charge it for.
	if _, err := service.ReportBatchSize(ctx, pool, project.ID, plan.Batch.ID, service.SizeRequest{
		LinesAdded: 603, LinesDeleted: 113,
		TestLinesAdded: model.Ptr(int32(314)), TestLinesDeleted: model.Ptr(int32(3)),
	}); err != nil {
		t.Fatal(err)
	}
	batch := finishAll(t, ctx, pool, project, accepted, plan.Batch.ID)

	digest := model.Deref(batch.Digest)
	wants(t, digest, "digest",
		"Size: code 289 added, 110 deleted, net +179",
		"tests 314 added, 3 deleted, net +311",
		"The ceiling counts code only",
	)
	if strings.Contains(digest, "net +490") {
		t.Errorf("the digest still charges test lines against the ceiling:\n%s", digest)
	}
}

// Nobody measured it: the digest says so and names the command, and the finish still succeeds. The
// API cannot measure it itself — it never runs git, and never opens the project's repo_path.
func TestAnUnmeasuredBatchSaysSoRatherThanFailing(t *testing.T) {
	ctx, pool, project := setup(t)
	plan := mustPlan(t, ctx, pool, project, planInput("Unmeasured", planned("do the work")))
	accepted, err := service.AcceptBatch(ctx, pool, project.ID, plan.Batch.ID, "joel")
	if err != nil {
		t.Fatal(err)
	}
	batch := finishAll(t, ctx, pool, project, accepted, plan.Batch.ID)
	digest := model.Deref(batch.Digest)
	wants(t, digest, "digest",
		"## Measures",
		"Size unknown: nobody reported it.",
		"scripts/batch-size.sh, run on the batch before it finishes",
	)
}

func TestReportingASizeRefusesNonsense(t *testing.T) {
	ctx, pool, project := setup(t)
	plan := mustPlan(t, ctx, pool, project, planInput("Fussy", planned("do the work")))
	if _, err := service.ReportBatchSize(ctx, pool, project.ID, plan.Batch.ID,
		service.SizeRequest{LinesAdded: -1, LinesDeleted: 0}); !apperr.Is(err, apperr.BadRequest) {
		t.Errorf("a negative count = %v, want a 400", err)
	}
	if _, err := service.ReportBatchSize(ctx, pool, project.ID, plan.Batch.ID,
		service.SizeRequest{LinesAdded: 1, LinesDeleted: 0, BaseCommit: model.Ptr("HEAD~3")}); !apperr.Is(err, apperr.BadRequest) {
		t.Errorf("a non-commit base = %v, want a 400", err)
	}
	if _, err := service.ReportBatchSize(ctx, pool, project.ID, uuid.New(),
		service.SizeRequest{LinesAdded: 1, LinesDeleted: 0}); !apperr.Is(err, apperr.NotFound) {
		t.Errorf("an unknown batch = %v, want a 404", err)
	}
	// Half a split would leave the digest unable to say whether the missing half is zero or unmeasured.
	if _, err := service.ReportBatchSize(ctx, pool, project.ID, plan.Batch.ID, service.SizeRequest{
		LinesAdded: 10, LinesDeleted: 2, TestLinesAdded: model.Ptr(int32(4)),
	}); !apperr.Is(err, apperr.BadRequest) {
		t.Errorf("one half of the split = %v, want a 400", err)
	}
	// Tests are part of the diff they were split out of, so a bigger half would render as code deleted
	// that nobody deleted — a batch reporting a cut it never made.
	if _, err := service.ReportBatchSize(ctx, pool, project.ID, plan.Batch.ID, service.SizeRequest{
		LinesAdded: 10, LinesDeleted: 2,
		TestLinesAdded: model.Ptr(int32(11)), TestLinesDeleted: model.Ptr(int32(0)),
	}); !apperr.Is(err, apperr.BadRequest) {
		t.Errorf("more test lines than lines = %v, want a 400", err)
	}
}

// The trend: a batch's own pass rate, and the rates of the finished batches before it in the
// roadmap, in order, so the digest shows whether judging is getting better or worse.
func TestTheDigestShowsThePassRatesOfTheBatchesBefore(t *testing.T) {
	ctx, pool, project := setup(t)
	roadmap, err := service.CreateRoadmap(ctx, pool, project.ID, "Evolution", "the spec")
	if err != nil {
		t.Fatal(err)
	}

	// Batch 1: two criteria, one missed.
	first := planInput("First", planned("first work"))
	first.RoadmapID = &roadmap.ID
	one := mustPlan(t, ctx, pool, project, first)
	acceptedOne, err := service.AcceptBatch(ctx, pool, project.ID, one.Batch.ID, "joel")
	if err != nil {
		t.Fatal(err)
	}
	work := acceptedOne[0].Ticket
	criteria := []service.CriterionInput{
		{Tier: model.TierBehavioural, Text: "it works"},
		{Tier: model.TierBehavioural, Text: "it is fast"},
	}
	if _, err := service.SetTicketFoundation(ctx, pool, work.ID, work.Version,
		service.Foundation{Criteria: &criteria}, "joel"); err != nil {
		t.Fatal(err)
	}
	finishAll(t, ctx, pool, project, acceptedOne, one.Batch.ID)
	for ordinal, verdict := range map[int32]model.Verdict{1: model.VerdictPass, 2: model.VerdictFail} {
		if _, err := service.RecordVerdict(ctx, pool, project, work.Key, ordinal,
			service.NewVerdict{Verdict: verdict, Reason: "judged in the test"}, "tester"); err != nil {
			t.Fatal(err)
		}
	}
	// Judging again, so the batch's own digest is recomputed with the verdicts in it.
	if _, err := service.FinishBatch(ctx, pool, one.Batch.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CheckpointBatch(ctx, pool, project.ID, one.Batch.ID, model.CheckpointContinue, "", "joel"); err != nil {
		t.Fatal(err)
	}

	// Batch 2, after it in the same roadmap.
	second := planInput("Second", planned("second work"))
	second.RoadmapID = &roadmap.ID
	two := mustPlan(t, ctx, pool, project, second)
	acceptedTwo, err := service.AcceptBatch(ctx, pool, project.ID, two.Batch.ID, "joel")
	if err != nil {
		t.Fatal(err)
	}
	later := acceptedTwo[0].Ticket
	oneCriterion := []service.CriterionInput{{Tier: model.TierBehavioural, Text: "it still works"}}
	if _, err := service.SetTicketFoundation(ctx, pool, later.ID, later.Version,
		service.Foundation{Criteria: &oneCriterion}, "joel"); err != nil {
		t.Fatal(err)
	}
	finishAll(t, ctx, pool, project, acceptedTwo, two.Batch.ID)
	if _, err := service.RecordVerdict(ctx, pool, project, later.Key, 1,
		service.NewVerdict{Verdict: model.VerdictPass}, "tester"); err != nil {
		t.Fatal(err)
	}
	batch, err := service.FinishBatch(ctx, pool, two.Batch.ID, nil)
	if err != nil {
		t.Fatal(err)
	}

	digest := model.Deref(batch.Digest)
	wants(t, digest, "digest",
		"Pass rate: 1 of 1 criterion passed (100%)",
		"Before it: batch 1 (First) 1 of 2 criteria passed (50%).",
	)
	// Batch 1 is the first in its roadmap, so it has nothing to compare against.
	head, err := service.GetBatch(ctx, pool, project.ID, one.Batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(model.Deref(head.Digest), "Before it:") {
		t.Errorf("the first batch in a roadmap has no batches before it:\n%s", model.Deref(head.Digest))
	}
}

// TP-188 (goals 2, 9): guidance is the harness this project keeps changing, and nothing said
// whether a revision helped. Each revision a claim carried is scored by what that ticket did.
func TestTheDigestScoresTheGuidanceRevisionsItServed(t *testing.T) {
	ctx, pool, project := setup(t)
	put := func(body string) {
		t.Helper()
		if _, err := service.UpsertGuidance(ctx, pool, project.ID, service.NewGuidance{
			Slug: "how", Title: "How it is done", Kind: model.GuidanceWorkflow, Body: body, Author: "tester",
		}); err != nil {
			t.Fatal(err)
		}
	}
	put("Branch, then commit.")
	batch, err := service.StartBatch(ctx, pool, project.ID, service.NewBatch{
		Kind: model.BatchReview, Title: "Score me", Brief: "does the guidance help", Actor: "tester"})
	if err != nil {
		t.Fatal(err)
	}
	// Unpinned, so the second claim is served the revision written between the two.
	if err := service.AttachGuidance(ctx, pool, project.ID, batch.ID, []string{"how"}, false); err != nil {
		t.Fatal(err)
	}
	work := func(clean bool) {
		t.Helper()
		in := ticket("Work me", model.SourceHuman, model.PriorityHigh)
		in.BatchID = &batch.ID
		mustCreate(t, ctx, pool, project, in)
		c, err := service.ClaimNextTicket(ctx, pool, project, service.ClaimRequest{Actor: "worker"})
		if err != nil || c == nil {
			t.Fatalf("claim: %v", err)
		}
		// Both tickets pass their gate, so the send-back is the only thing telling the two apart.
		if _, err := pool.Exec(ctx, `UPDATE tickets SET gate_passed = true WHERE id = $1`, c.View.Ticket.ID); err != nil {
			t.Fatal(err)
		}
		if !clean {
			if _, err := service.RecordDecision(ctx, pool, c.View.Ticket.ID, service.MadeByHuman,
				service.NewDecision{Kind: model.DecisionRejected, Rationale: "sent back"}, "joel"); err != nil {
				t.Fatal(err)
			}
		}
	}
	work(true)
	put("Branch, then commit, then merge.")
	work(false)

	digest, err := service.ComposeDigest(ctx, pool, batch)
	if err != nil {
		t.Fatal(err)
	}
	wants(t, digest, "digest",
		"- `how` r1: 1 of 1 claim clean, 0 reworked.",
		"- `how` r2: 0 of 1 claim clean, 1 reworked.")
}
