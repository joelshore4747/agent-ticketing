package service_test

import (
	"testing"

	"github.com/joelshore/ticketing/api/internal/model"
	"github.com/joelshore/ticketing/api/internal/service"
)

// TP-89: one of each thing waiting on the human comes back, and nothing that is not.
func TestNeedsYouListsExactlyWhatWaitsOnTheHuman(t *testing.T) {
	ctx, pool, project := setup(t)

	empty, err := service.ListNeedsYou(ctx, pool, project.ID)
	if err != nil || empty.Total() != 0 {
		t.Fatalf("a fresh project needs %d things (%v)", empty.Total(), err)
	}

	// Triage: one AI finding. A human ticket is open, so it is not waiting.
	finding := mustCreate(t, ctx, pool, project, ticket("an AI finding", model.SourceAI, model.PriorityHigh)).View.Ticket
	open := mustCreate(t, ctx, pool, project, ticket("human work", model.SourceHuman, model.PriorityLow)).View.Ticket

	// Checkpoint: a finished roadmap batch nobody has signed off.
	roadmap, err := service.CreateRoadmap(ctx, pool, project.ID, "Evolution", "spec")
	if err != nil {
		t.Fatal(err)
	}
	in := planInput("Awaiting you", planned("work"))
	in.RoadmapID = &roadmap.ID
	plan := mustPlan(t, ctx, pool, project, in)
	accepted, err := service.AcceptBatch(ctx, pool, project.ID, plan.Batch.ID, "joel")
	if err != nil {
		t.Fatal(err)
	}
	finishAll(t, ctx, pool, project, accepted, plan.Batch.ID)

	// Proposal: pending on the open ticket; a settled one elsewhere does not count.
	if _, err := service.ProposeEdit(ctx, pool, open.ID, "constraints", "No new dependencies.", "brief", "claude-code"); err != nil {
		t.Fatal(err)
	}
	// Uncited: Claude's reframing of the open ticket, with nothing behind it; a cited one does not count.
	if _, err := service.RecordDecision(ctx, pool, open.ID, service.MadeByAI, service.NewDecision{Kind: model.DecisionReframed, Rationale: "smaller"}, "claude-code"); err != nil {
		t.Fatal(err)
	}
	cited := accepted[0].Ticket
	if _, err := service.RecordDecision(ctx, pool, cited.ID, service.MadeByAI, service.NewDecision{Kind: model.DecisionAccepted, EvidenceRefs: []string{"TP-1"}}, "claude-code"); err != nil {
		t.Fatal(err)
	}
	// Tripwires: one unconfirmed, one confirmed.
	if _, err := service.RegisterTripwire(ctx, pool, project.ID, "a_test.go:TestA", "A holds", "h"); err != nil {
		t.Fatal(err)
	}
	confirmed, err := service.RegisterTripwire(ctx, pool, project.ID, "b_test.go:TestB", "B holds", "h")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ConfirmTripwire(ctx, pool, project.ID, confirmed.ID, nil, "joel"); err != nil {
		t.Fatal(err)
	}

	got, err := service.ListNeedsYou(ctx, pool, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Triage) != 1 || got.Triage[0].Ticket.Key != finding.Key {
		t.Errorf("triage = %v", got.Triage)
	}
	if len(got.Checkpoints) != 1 || got.Checkpoints[0].ID != plan.Batch.ID {
		t.Errorf("checkpoints = %v", got.Checkpoints)
	}
	if len(got.Proposals) != 1 || got.Proposals[0].Key != open.Key || got.Proposals[0].Field != "constraints" {
		t.Errorf("proposals = %+v", got.Proposals)
	}
	if len(got.Uncited) != 1 || got.Uncited[0].Key != open.Key {
		t.Errorf("uncited = %+v", got.Uncited)
	}
	if len(got.Tripwires) != 1 || got.Tripwires[0].TestRef != "a_test.go:TestA" {
		t.Errorf("tripwires = %+v", got.Tripwires)
	}
	if got.Total() != 5 {
		t.Errorf("total = %d, want 5", got.Total())
	}

	// A placeholder batch later in the roadmap brings an eval ticket in triage; it is not a decision
	// until that batch is planned, so it stays off the queue.
	later := planInput("Later placeholder")
	later.RoadmapID = &roadmap.ID
	mustPlan(t, ctx, pool, project, later)
	if again, _ := service.ListNeedsYou(ctx, pool, project.ID); len(again.Triage) != 1 {
		t.Errorf("a locked placeholder's eval ticket reached the queue: %v", again.Triage)
	}

	// Signing the checkpoint takes it off the queue.
	if _, err := service.CheckpointBatch(ctx, pool, project.ID, plan.Batch.ID, model.CheckpointContinue, "", "joel"); err != nil {
		t.Fatal(err)
	}
	if after, _ := service.ListNeedsYou(ctx, pool, project.ID); len(after.Checkpoints) != 0 {
		t.Errorf("a signed checkpoint still waits: %v", after.Checkpoints)
	}
}
