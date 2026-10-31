package service_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/joelshore/ticketing/api/internal/model"
	"github.com/joelshore/ticketing/api/internal/service"
)

// usedBatch plans a one-ticket batch, claims and reports usage for its ticket, runs between() while
// the batch is open, then finishes it and returns the digest.
func usedBatch(t *testing.T, ctx context.Context, pool *pgxpool.Pool, project *model.Project, title string,
	u service.UsageRequest, between func()) string {
	t.Helper()
	plan := mustPlan(t, ctx, pool, project, planInput(title, planned("the work")))
	if _, err := service.AcceptBatch(ctx, pool, project.ID, plan.Batch.ID, "joel"); err != nil {
		t.Fatal(err)
	}
	claimed, err := service.ClaimNextTicket(ctx, pool, project, service.ClaimRequest{Actor: "claude-code", Batch: &plan.Batch.ID})
	if err != nil || claimed == nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := service.RecordUsage(ctx, pool, project.ID, claimed.View.Ticket.Key, u); err != nil {
		t.Fatal(err)
	}
	if between != nil {
		between()
	}
	tickets, _ := service.ListBatchTickets(ctx, pool, plan.Batch.ID)
	for _, v := range tickets {
		tk := v.Ticket
		if tk.Status == model.StatusOpen {
			move(t, ctx, pool, &tk, model.StatusInProgress)
		}
		move(t, ctx, pool, &tk, model.StatusDone)
	}
	b, err := service.FinishBatch(ctx, pool, plan.Batch.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	return *b.Digest
}

// TP-103: tokens lead, the dollar figure is labelled an estimate.
func TestTheDigestCostLeadsWithTokens(t *testing.T) {
	ctx, pool, project := setup(t)
	digest := usedBatch(t, ctx, pool, project, "Measured", service.UsageRequest{
		InputTokens: 1000, OutputTokens: 50, CacheReadInputTokens: 9000, CacheCreationInputTokens: 2000,
		CostUSD: model.Ptr(0.25), WallTimeSeconds: 754}, nil)
	wants(t, digest, "digest",
		"## Cost\n\nTokens: 1,000 input · 50 output · 9,000 cache read · 2,000 cache written, from 1 of 1 claim.",
		"Cache-hit rate 90.0% · wall time 12m34s.",
		"About $0.25 by the CLI's own figure — an estimate, not a charge.",
	)
	if strings.Contains(digest, "Cache-hit rate dropped") {
		t.Errorf("a first batch has nothing to drop against:\n%s", digest)
	}
}

// TP-296 (lesson TP-282): a claim that reported nothing is named by key, and a measured one is not.
// The fallback the loop takes when its own account is spent — a step run from a supervising session —
// posts no usage at all, so slot 13's digest measured 0 of 6 claims and said nothing about which.
func TestTheCostLineNamesTheClaimsThatReportedNothing(t *testing.T) {
	ctx, pool, project := setup(t)
	plan := mustPlan(t, ctx, pool, project, planInput("Half measured", planned("measured"), planned("unmetered")))
	if _, err := service.AcceptBatch(ctx, pool, project.ID, plan.Batch.ID, "joel"); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for i := 0; i < 2; i++ {
		claimed, err := service.ClaimNextTicket(ctx, pool, project, service.ClaimRequest{Actor: "claude-code", Batch: &plan.Batch.ID})
		if err != nil || claimed == nil {
			t.Fatalf("claim %d: %v", i, err)
		}
		tk := claimed.View.Ticket
		keys = append(keys, tk.Key)
		if i == 0 { // only the first step reports; the second is the in-session one
			if _, err := service.RecordUsage(ctx, pool, project.ID, tk.Key, service.UsageRequest{
				InputTokens: 1000, OutputTokens: 50, CacheReadInputTokens: 9000}); err != nil {
				t.Fatal(err)
			}
		}
		move(t, ctx, pool, &tk, model.StatusDone)
	}
	tickets, _ := service.ListBatchTickets(ctx, pool, plan.Batch.ID) // the eval ticket nobody claimed
	for _, v := range tickets {
		if tk := v.Ticket; tk.Status == model.StatusOpen {
			move(t, ctx, pool, &tk, model.StatusInProgress)
			move(t, ctx, pool, &tk, model.StatusDone)
		}
	}
	b, err := service.FinishBatch(ctx, pool, plan.Batch.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	wants(t, *b.Digest, "digest", "from 1 of 2 claims. No usage reported for "+keys[1]+".")
	for _, line := range strings.Split(*b.Digest, "\n") { // the flag names the unmetered claim and no other
		if strings.Contains(line, "No usage reported for") && strings.Contains(line, keys[0]) {
			t.Errorf("the measured claim %s was named as unmetered:\n%s", keys[0], line)
		}
	}
}

// A hit rate well below recent batches is flagged, naming guidance revised while the batch ran.
func TestACacheHitDropIsFlaggedWithWhatChanged(t *testing.T) {
	ctx, pool, project := setup(t)
	good := service.UsageRequest{InputTokens: 100, CacheReadInputTokens: 9900} // 99%
	for _, title := range []string{"one", "two", "three"} {
		usedBatch(t, ctx, pool, project, title, good, nil)
	}
	steady := usedBatch(t, ctx, pool, project, "steady", service.UsageRequest{InputTokens: 300, CacheReadInputTokens: 9700}, nil) // 97%
	if strings.Contains(steady, "dropped") {
		t.Errorf("a 2-point dip was flagged:\n%s", steady)
	}

	revise := func() {
		if _, err := service.UpsertGuidance(ctx, pool, project.ID, service.NewGuidance{
			Slug: "working-a-ticket", Title: "Working a ticket", Kind: model.GuidanceWorkflow, Body: "a new first line", Author: "tester",
		}); err != nil {
			t.Fatal(err)
		}
	}
	broken := usedBatch(t, ctx, pool, project, "broken", service.UsageRequest{InputTokens: 5000, CacheReadInputTokens: 5000}, revise) // 50%
	wants(t, broken, "digest", "**Cache-hit rate dropped**: 50.0% is", "points below the last 4 batches'",
		"Guidance changed while it ran (working-a-ticket r1)")
	quiet := usedBatch(t, ctx, pool, project, "quiet", service.UsageRequest{InputTokens: 6000, CacheReadInputTokens: 4000}, nil) // 40%
	if !strings.Contains(quiet, "No guidance changed while it ran") {
		t.Errorf("a drop with no guidance change should say so:\n%s", quiet)
	}
}
