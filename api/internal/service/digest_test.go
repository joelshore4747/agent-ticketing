package service_test

import (
	"strings"
	"testing"

	"github.com/joelshore/ticketing/api/internal/model"
	"github.com/joelshore/ticketing/api/internal/service"
)

// TP-87: finish_batch writes a digest a human can read in a minute, from what the record says.
func TestFinishingABatchWritesItsDigest(t *testing.T) {
	ctx, pool, project := setup(t)
	plan := mustPlan(t, ctx, pool, project, planInput("Digest me", planned("Guard the order"), planned("Show the order")))
	accepted, err := service.AcceptBatch(ctx, pool, project.ID, plan.Batch.ID, "joel")
	if err != nil {
		t.Fatal(err)
	}
	guard, show, eval := accepted[0].Ticket, accepted[1].Ticket, accepted[2].Ticket

	// Something for each "waiting on you" source.
	tw, err := service.RegisterTripwire(ctx, pool, project.ID, "api/order_test.go:TestOrder", "the order holds", "h1")
	if err != nil {
		t.Fatal(err)
	}
	criteria := []service.CriterionInput{{Tier: model.TierGate, Text: "just test", TripwireID: &tw.ID}}
	if _, err := service.SetTicketFoundation(ctx, pool, guard.ID, guard.Version, service.Foundation{Criteria: &criteria}, "joel"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ProposeEdit(ctx, pool, guard.ID, "intent", "Claims follow the batch order.", "the brief", "claude-code"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordDecision(ctx, pool, show.ID, service.MadeByAI, service.NewDecision{
		Kind: model.DecisionReframed, Rationale: "It is a badge, not a page.\nMore detail here.",
	}, "claude-code"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordDecision(ctx, pool, eval.ID, service.MadeByAI, service.NewDecision{
		Kind: model.DecisionDeferred, RevisitTrigger: model.Ptr("when the harness runs nightly"),
	}, "claude-code"); err != nil {
		t.Fatal(err)
	}

	for _, key := range []string{guard.Key, show.Key, eval.Key} {
		v, err := service.GetTicket(ctx, pool, project.ID, key)
		if err != nil {
			t.Fatal(err)
		}
		tk := v.Ticket
		move(t, ctx, pool, &tk, model.StatusInProgress, model.StatusDone)
	}
	batch, err := service.FinishBatch(ctx, pool, plan.Batch.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if batch.Digest == nil || batch.DigestAt == nil {
		t.Fatalf("no digest on the finished batch")
	}
	digest := *batch.Digest
	wants(t, digest, "digest",
		"## What\n\nwhat it builds", "## How\n\nhow it goes about it", "## Why\n\nwhy it matters",
		"3 of 3 tickets finished.", "**"+guard.Key+"** Guard the order — done: done in the test",
		"No convergence verdicts were recorded",
		"You accepted 1 ticket from triage as filed.", // guard; show and eval now carry Claude's decisions
		"**"+show.Key+"** reframed by Claude Code: It is a badge, not a page. …",
		"## Deferred", "revisit when the harness runs nightly",
		`Claude proposes a change to intent: "Claims follow the batch order."`,
		"**"+show.Key+"** Claude's decision (reframed) cites no evidence",
		"Tripwire `api/order_test.go:TestOrder` needs you to confirm",
		"## Cost\n\nNot measured: no claim in this batch reported usage",
	)
	if strings.Contains(digest, "reframed by Claude Code: It is a badge, not a page.\nMore") {
		t.Error("a multi-line rationale broke the digest's lines")
	}
}

func TestADigestLeavesOutWhatIsEmpty(t *testing.T) {
	ctx, pool, project := setup(t)
	plan := mustPlan(t, ctx, pool, project, planInput("Quiet batch", planned("only work")))
	// Planning requires how; a batch filed before that rule may have none.
	if _, err := pool.Exec(ctx, `UPDATE batches SET how = '' WHERE id = $1`, plan.Batch.ID); err != nil {
		t.Fatal(err)
	}
	accepted, err := service.AcceptBatch(ctx, pool, project.ID, plan.Batch.ID, "joel")
	if err != nil {
		t.Fatal(err)
	}
	// A proposal the human has since acted on is settled, so it is not an open question.
	work := accepted[0].Ticket
	if _, err := service.ProposeEdit(ctx, pool, work.ID, "intent", "Say it plainly.", "brief", "claude-code"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetTicketFoundation(ctx, pool, work.ID, work.Version, service.Foundation{Intent: model.Ptr("Plainly.")}, "joel"); err != nil {
		t.Fatal(err)
	}
	batch := finishAll(t, ctx, pool, project, accepted, plan.Batch.ID)
	for _, absent := range []string{"## How", "## Deferred", "## Open questions", "none"} {
		if strings.Contains(*batch.Digest, absent) {
			t.Errorf("digest has %q:\n%s", absent, *batch.Digest)
		}
	}
	if !strings.Contains(*batch.Digest, "You accepted 2 tickets from triage as filed.") {
		t.Errorf("digest:\n%s", *batch.Digest)
	}

	again, err := service.FinishBatch(ctx, pool, plan.Batch.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !again.FinishedAt.Equal(*batch.FinishedAt) || !again.DigestAt.After(*batch.DigestAt) {
		t.Errorf("re-finish moved finished_at (%v -> %v) or kept the old digest (%v -> %v)",
			batch.FinishedAt, again.FinishedAt, batch.DigestAt, again.DigestAt)
	}
}
