package service_test

import (
	"strings"
	"testing"

	"github.com/joelshore/ticketing/api/internal/apperr"
	"github.com/joelshore/ticketing/api/internal/model"
	"github.com/joelshore/ticketing/api/internal/service"
)

// TP-94: a finished ticket's criteria are judged; a fail becomes work before the batch's eval.
func TestVerdictsJudgeCriteriaAndAFailBecomesWork(t *testing.T) {
	ctx, pool, project := setup(t)
	plan := mustPlan(t, ctx, pool, project, planInput("Judged", planned("Refuse unknown fields"), planned("Say which field")))
	accepted, err := service.AcceptBatch(ctx, pool, project.ID, plan.Batch.ID, "joel")
	if err != nil {
		t.Fatal(err)
	}
	work, second, eval := accepted[0].Ticket, accepted[1].Ticket, accepted[2].Ticket
	criteria := []service.CriterionInput{
		{Tier: model.TierBehavioural, Text: "an unknown field gets a 400"},
		{Tier: model.TierMeasurement, Text: "the error names the field"},
		{Tier: model.TierGate, Text: "just test"},
	}
	v, err := service.SetTicketFoundation(ctx, pool, work.ID, work.Version, service.Foundation{Criteria: &criteria}, "joel")
	if err != nil {
		t.Fatal(err)
	}
	work = v.Ticket

	judge := func(ordinal int32, verdict model.Verdict, reason string) (*service.VerdictResult, error) {
		return service.RecordVerdict(ctx, pool, project, work.Key, ordinal, service.NewVerdict{Verdict: verdict, Reason: reason, Evidence: []string{"go test"}}, "converger")
	}
	if _, err := judge(1, model.VerdictPass, ""); !apperr.Is(err, apperr.Conflict) {
		t.Errorf("judging an unfinished ticket: %v", err)
	}
	move(t, ctx, pool, &work, model.StatusInProgress, model.StatusDone)

	if r, err := judge(1, model.VerdictPass, ""); err != nil || r.Verdict.Verdict != model.VerdictPass || r.FollowUp != nil {
		t.Errorf("pass: %+v %v", r, err)
	}
	if _, err := judge(2, model.VerdictDeviation, ""); !apperr.Is(err, apperr.BadRequest) {
		t.Errorf("a deviation without a reason: %v", err)
	}
	if r, err := judge(2, model.VerdictDeviation, "names the JSON path, not the field"); err != nil || r.FollowUp != nil {
		t.Errorf("deviation: %+v %v", r, err)
	}
	if _, err := judge(9, model.VerdictPass, ""); !apperr.Is(err, apperr.NotFound) {
		t.Errorf("an unknown criterion: %v", err)
	}

	fail, err := judge(3, model.VerdictFail, "just test is red on the web suite")
	if err != nil || fail.FollowUp == nil || !fail.JoinedBatch {
		t.Fatalf("fail: %+v %v", fail, err)
	}
	if fail.FollowUp.Status != model.StatusTriage || !strings.Contains(fail.FollowUp.Title, work.Key+" missed: just test") {
		t.Errorf("follow-up = %s %q", fail.FollowUp.Status, fail.FollowUp.Title)
	}
	order, err := service.ListBatchTickets(ctx, pool, plan.Batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := keys(order), strings.Join([]string{work.Key, second.Key, fail.FollowUp.Key, eval.Key}, " "); got != want {
		t.Errorf("batch order = %s, want %s (the follow-up just before the eval)", got, want)
	}
	again, err := judge(3, model.VerdictFail, "still red")
	if err != nil || again.FollowUp.Key != fail.FollowUp.Key || again.JoinedBatch {
		t.Errorf("a second fail on the same criterion filed new work: %+v %v", again, err)
	}
	all, err := service.ListVerdicts(ctx, pool, work.ID)
	if err != nil || len(all) != 4 {
		t.Errorf("verdicts = %d (%v)", len(all), err)
	}

	// The digest counts the newest verdict per criterion and lists each miss.
	followUp, _ := service.GetTicket(ctx, pool, project.ID, fail.FollowUp.Key)
	if _, err := service.TransitionTicket(ctx, pool, followUp.Ticket.ID, followUp.Ticket.Version, model.StatusOpen, nil, "joel"); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{second.Key, fail.FollowUp.Key, eval.Key} {
		cur, _ := service.GetTicket(ctx, pool, project.ID, key)
		tk := cur.Ticket
		move(t, ctx, pool, &tk, model.StatusInProgress, model.StatusDone)
	}
	batch, err := service.FinishBatch(ctx, pool, plan.Batch.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	wants(t, *batch.Digest, "digest",
		"Convergence verdicts on 3 criteria: 1 pass, 1 deviation, 1 fail.",
		"**"+work.Key+"** criterion 3 missed (just test): still red",
	)
}

// Past the eval, a fail's follow-up is left for the human to place rather than breaking the order.
func TestAFailAfterTheEvalStartedLeavesItsFollowUpUnbatched(t *testing.T) {
	ctx, pool, project := setup(t)
	plan := mustPlan(t, ctx, pool, project, planInput("Late", planned("work")))
	accepted, err := service.AcceptBatch(ctx, pool, project.ID, plan.Batch.ID, "joel")
	if err != nil {
		t.Fatal(err)
	}
	work, eval := accepted[0].Ticket, accepted[1].Ticket
	criteria := []service.CriterionInput{{Tier: model.TierGate, Text: "just test"}}
	v, err := service.SetTicketFoundation(ctx, pool, work.ID, work.Version, service.Foundation{Criteria: &criteria}, "joel")
	if err != nil {
		t.Fatal(err)
	}
	work = v.Ticket
	move(t, ctx, pool, &work, model.StatusInProgress, model.StatusDone)
	move(t, ctx, pool, &eval, model.StatusInProgress)

	r, err := service.RecordVerdict(ctx, pool, project, work.Key, 1, service.NewVerdict{Verdict: model.VerdictFail, Reason: "red"}, "converger")
	if err != nil || r.FollowUp == nil || r.JoinedBatch || r.FollowUp.BatchID != nil {
		t.Errorf("late fail: %+v %v", r, err)
	}
}
