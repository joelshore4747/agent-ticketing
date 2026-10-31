package service_test

import (
	"strings"
	"testing"

	"github.com/joelshore/ticketing/api/internal/apperr"
	"github.com/joelshore/ticketing/api/internal/model"
	"github.com/joelshore/ticketing/api/internal/service"
)

// Rule 19: the human's accept and dismiss are decisions, filed with the move and made current.
func TestAcceptAndDismissFileTheHumansDecision(t *testing.T) {
	ctx, pool, project := setup(t)
	kept := mustCreate(t, ctx, pool, project, ticket("worth doing", model.SourceAI, model.PriorityHigh)).View.Ticket
	dropped := mustCreate(t, ctx, pool, project, ticket("not worth it", model.SourceAI, model.PriorityLow)).View.Ticket

	accepted, err := service.TransitionTicket(ctx, pool, kept.ID, kept.Version, model.StatusOpen, nil, "joel")
	if err != nil {
		t.Fatal(err)
	}
	dismissed, err := service.TransitionTicket(ctx, pool, dropped.ID, dropped.Version, model.StatusClosed,
		model.Ptr("duplicate of the ordering work"), "joel")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		view *model.TicketView
		kind model.DecisionKind
	}{{accepted, model.DecisionAccepted}, {dismissed, model.DecisionRejected}} {
		decisions, err := service.ListDecisions(ctx, pool, c.view.Ticket.ID)
		if err != nil || len(decisions) != 1 {
			t.Fatalf("%s: %d decisions (%v)", c.view.Ticket.Key, len(decisions), err)
		}
		d := decisions[0]
		if d.Kind != c.kind || d.MadeBy != service.MadeByHuman || d.Actor != "joel" {
			t.Errorf("%s decision = %s by %s (%s)", c.view.Ticket.Key, d.Kind, d.MadeBy, d.Actor)
		}
		if c.view.Ticket.DecisionID == nil || *c.view.Ticket.DecisionID != d.ID {
			t.Errorf("%s does not point at its decision", c.view.Ticket.Key)
		}
	}
	if d, _ := service.ListDecisions(ctx, pool, dropped.ID); d[0].Rationale != "duplicate of the ordering work" {
		t.Errorf("the dismissal's rationale = %q, want its resolution", d[0].Rationale)
	}

	// Moving a ticket that was never in triage is not a decision.
	started := model.StatusInProgress
	if _, err := service.UpdateTicket(ctx, pool, kept.ID, accepted.Ticket.Version, service.TicketPatch{Status: &started}, "claude-code"); err != nil {
		t.Fatal(err)
	}
	if d, _ := service.ListDecisions(ctx, pool, kept.ID); len(d) != 1 {
		t.Errorf("starting work filed a decision: %d rows", len(d))
	}
}

func TestAcceptBatchFilesADecisionPerTicket(t *testing.T) {
	ctx, pool, project := setup(t)
	plan := mustPlan(t, ctx, pool, project, planInput("Decided together", planned("one"), planned("two")))
	accepted, err := service.AcceptBatch(ctx, pool, project.ID, plan.Batch.ID, "joel")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range accepted {
		d, err := service.ListDecisions(ctx, pool, v.Ticket.ID)
		if err != nil || len(d) != 1 || d[0].Kind != model.DecisionAccepted || d[0].MadeBy != service.MadeByHuman {
			t.Errorf("%s: %+v (%v)", v.Ticket.Key, d, err)
		}
	}
}

// The agent's decisions carry evidence; a changed mind supersedes rather than edits.
func TestTheAgentRecordsAndSupersedesItsDecisions(t *testing.T) {
	ctx, pool, project := setup(t)
	tk := mustCreate(t, ctx, pool, project, ticket("a question", model.SourceHuman, model.PriorityMedium)).View.Ticket

	_, err := service.RecordDecision(ctx, pool, tk.ID, service.MadeByAI, service.NewDecision{Kind: model.DecisionDeferred}, "claude-code")
	if !apperr.Is(err, apperr.BadRequest) || !strings.Contains(err.Error(), "revisit_trigger") {
		t.Errorf("a deferral without a trigger: %v", err)
	}
	first, err := service.RecordDecision(ctx, pool, tk.ID, service.MadeByAI, service.NewDecision{
		Kind: model.DecisionDeferred, RevisitTrigger: model.Ptr("when the harness runs nightly"),
	}, "claude-code")
	if err != nil || first.MadeBy != service.MadeByAI || first.Supersedes != nil {
		t.Fatalf("first: %+v, %v", first, err)
	}
	second, err := service.RecordDecision(ctx, pool, tk.ID, service.MadeByAI, service.NewDecision{
		Kind: model.DecisionAccepted, Rationale: "the harness now runs nightly",
		EvidenceRefs: []string{"  ", "api/internal/service/decisions.go:40"},
	}, "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	if second.Supersedes == nil || *second.Supersedes != first.ID {
		t.Errorf("second supersedes %v, want %v", second.Supersedes, first.ID)
	}
	if len(second.EvidenceRefs) != 1 {
		t.Errorf("blank evidence refs kept: %q", second.EvidenceRefs)
	}
	got, err := service.GetTicket(ctx, pool, project.ID, tk.Key)
	if err != nil || got.Ticket.DecisionID == nil || *got.Ticket.DecisionID != second.ID {
		t.Errorf("current decision = %v, want the second (%v)", got.Ticket.DecisionID, err)
	}
	if got.Ticket.Version != tk.Version {
		t.Errorf("recording a decision bumped the version %d -> %d", tk.Version, got.Ticket.Version)
	}

	other := mustCreate(t, ctx, pool, project, ticket("another", model.SourceHuman, model.PriorityLow)).View.Ticket
	_, err = service.RecordDecision(ctx, pool, other.ID, service.MadeByAI, service.NewDecision{
		Kind: model.DecisionRejected, Rationale: "x", Supersedes: &first.ID,
	}, "claude-code")
	if !apperr.Is(err, apperr.BadRequest) {
		t.Errorf("superseding another ticket's decision: %v", err)
	}
}

// Uncited means the agent's current decision rests on nothing; deferrals and the human are exempt.
func TestUncitedDecisionsAreTheAgentsCurrentOnesWithoutEvidence(t *testing.T) {
	ctx, pool, project := setup(t)
	make := func(title string) model.Ticket {
		return mustCreate(t, ctx, pool, project, ticket(title, model.SourceHuman, model.PriorityLow)).View.Ticket
	}
	record := func(tk model.Ticket, d service.NewDecision) {
		t.Helper()
		if _, err := service.RecordDecision(ctx, pool, tk.ID, service.MadeByAI, d, "claude-code"); err != nil {
			t.Fatal(err)
		}
	}
	bare, cited, deferred, fixed := make("bare"), make("cited"), make("deferred"), make("fixed later")
	record(bare, service.NewDecision{Kind: model.DecisionRejected, Rationale: "felt wrong"})
	record(cited, service.NewDecision{Kind: model.DecisionAccepted, EvidenceRefs: []string{"TP-1"}})
	record(deferred, service.NewDecision{Kind: model.DecisionDeferred, RevisitTrigger: model.Ptr("after batch 6")})
	record(fixed, service.NewDecision{Kind: model.DecisionAccepted})
	record(fixed, service.NewDecision{Kind: model.DecisionAccepted, EvidenceRefs: []string{"eval/run-12.json"}})

	uncited, err := service.ListUncitedDecisions(ctx, pool, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(uncited) != 1 || uncited[0].Key != bare.Key {
		keys := []string{}
		for _, u := range uncited {
			keys = append(keys, u.Key)
		}
		t.Errorf("uncited = %v, want only %s", keys, bare.Key)
	}
}
