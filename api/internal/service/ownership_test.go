package service_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/joelshore/ticketing/api/internal/apperr"
	"github.com/joelshore/ticketing/api/internal/model"
	"github.com/joelshore/ticketing/api/internal/service"
)

// The ownership model (TP-78): Section A is the human's, set as one change; the AI can only propose.

func TestTheHumanSetsTheFoundationAsOneChange(t *testing.T) {
	ctx, pool, project := setup(t)
	tk := mustCreate(t, ctx, pool, project, ticket("owned", model.SourceHuman, model.PriorityLow)).View.Ticket
	criteria := []service.CriterionInput{
		{Tier: model.TierBehavioural, Text: "POST with an unknown field gets a 400 naming it"},
		{Tier: model.TierGate, Text: "just test"},
	}
	got, err := service.SetTicketFoundation(ctx, pool, tk.ID, tk.Version, service.Foundation{
		Intent: model.Ptr("An agent learns at once when it guesses a field name."), NonGoals: model.Ptr("GraphQL"),
		Criteria: &criteria,
	}, "joel")
	if err != nil {
		t.Fatal(err)
	}
	if got.Ticket.Version != tk.Version+1 || got.Ticket.NonGoals != "GraphQL" {
		t.Errorf("foundation = %+v", got.Ticket)
	}
	rows, err := service.ListCriteria(ctx, pool, tk.ID)
	if err != nil || len(rows) != 2 || rows[1].Tier != model.TierGate || rows[1].Ordinal != 2 {
		t.Fatalf("criteria = %+v (%v)", rows, err)
	}
	if _, err := service.SetTicketFoundation(ctx, pool, tk.ID, tk.Version, service.Foundation{Intent: model.Ptr("stale")}, "joel"); !apperr.Is(err, apperr.Conflict) {
		t.Errorf("a stale version set the foundation: %v", err)
	}
	empty := []service.CriterionInput{{Tier: model.TierGate, Text: "  "}}
	if _, err := service.SetTicketFoundation(ctx, pool, tk.ID, got.Ticket.Version, service.Foundation{Criteria: &empty}, "joel"); !apperr.Is(err, apperr.BadRequest) {
		t.Errorf("an empty criterion was stored: %v", err)
	}
}

func TestTheAIProposesButNeverWritesTheFoundation(t *testing.T) {
	ctx, pool, project := setup(t)
	tk := mustCreate(t, ctx, pool, project, ticket("owned", model.SourceHuman, model.PriorityLow)).View.Ticket
	comment, err := service.ProposeEdit(ctx, pool, tk.ID, "intent", "Faster claims.", "the brief says speed matters", "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	if comment.Kind != "proposal" || !strings.Contains(comment.Body, "Faster claims.") || !strings.Contains(comment.Body, "nothing has changed") {
		t.Errorf("proposal = %+v", comment)
	}
	after, err := service.GetTicket(ctx, pool, project.ID, tk.Key)
	if err != nil || after.Ticket.Intent != "" || after.Ticket.Version != tk.Version {
		t.Errorf("a proposal changed the ticket: %+v (%v)", after.Ticket, err)
	}
	if _, err := service.ProposeEdit(ctx, pool, tk.ID, "status", "done", "why not", "claude-code"); !apperr.Is(err, apperr.BadRequest) {
		t.Errorf("status was proposable: %v", err)
	}
}

func TestSectionsBAndCTravelInThePatchAndEditingClearsApproval(t *testing.T) {
	ctx, pool, project := setup(t)
	feature := ticket("a feature", model.SourceHuman, model.PriorityLow)
	feature.Type = model.TypeFeature
	tk := mustCreate(t, ctx, pool, project, feature).View.Ticket

	refs := []string{"api/internal/rest/rest.go:95"}
	v, err := service.UpdateTicket(ctx, pool, tk.ID, tk.Version, service.TicketPatch{
		Approach: model.Ptr("Disallow unknown fields in the decoder."), EvidenceRefs: &refs,
		EstimatedTokens: model.Ptr[int32](9000), EstimatedCostUSD: model.Ptr(0.42),
	}, "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApproveApproach(ctx, pool, tk.ID, v.Ticket.Version, "joel"); !apperr.Is(err, apperr.Conflict) ||
		!strings.Contains(err.Error(), "worked example") {
		t.Errorf("a feature was approved with no worked example: %v", err)
	}
	v, err = service.UpdateTicket(ctx, pool, tk.ID, v.Ticket.Version,
		service.TicketPatch{WorkedExample: model.Ptr("POST {nonsense_field} -> 400 naming it")}, "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	approved, err := service.ApproveApproach(ctx, pool, tk.ID, v.Ticket.Version, "joel")
	if err != nil || model.Deref(approved.Ticket.ApproachApprovedBy) != "joel" {
		t.Fatalf("approve = %+v, %v", approved, err)
	}

	edited, err := service.UpdateTicket(ctx, pool, tk.ID, approved.Ticket.Version,
		service.TicketPatch{Approach: model.Ptr("A different approach.")}, "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	if edited.Ticket.ApproachApprovedAt != nil {
		t.Error("changing an approved approach kept the approval")
	}
	reported, err := service.UpdateTicket(ctx, pool, tk.ID, edited.Ticket.Version,
		service.TicketPatch{VerifiedBy: model.Ptr("go test ./... ok"), SummaryForHuman: model.Ptr("Unknown fields now fail loudly.")}, "claude-code")
	if err != nil || model.Deref(reported.Ticket.VerifiedBy) == "" {
		t.Fatalf("section C = %+v, %v", reported, err)
	}
	long := strings.Repeat("x", 281)
	if _, err := service.UpdateTicket(ctx, pool, tk.ID, reported.Ticket.Version,
		service.TicketPatch{SummaryForHuman: &long}, "claude-code"); !apperr.Is(err, apperr.BadRequest) {
		t.Errorf("a 281-character summary was stored: %v", err)
	}
}

func TestATripwireLosesItsConfirmationWhenItsTestChanges(t *testing.T) {
	ctx, pool, project := setup(t)
	tw, err := service.RegisterTripwire(ctx, pool, project.ID, "api/internal/app/app_test.go:TestUnknownFieldsAreRefused",
		"Unknown fields are refused", "hash-1")
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := service.ConfirmTripwire(ctx, pool, project.ID, tw.ID, nil, "joel")
	if err != nil || confirmed.ConfirmedAt == nil {
		t.Fatalf("confirm = %+v, %v", confirmed, err)
	}
	same, err := service.RegisterTripwire(ctx, pool, project.ID, tw.TestRef, tw.Meaning, "hash-1")
	if err != nil || same.ConfirmedAt == nil {
		t.Errorf("re-registering an unchanged test lost its confirmation: %+v, %v", same, err)
	}
	changed, err := service.RegisterTripwire(ctx, pool, project.ID, tw.TestRef, tw.Meaning, "hash-2")
	if err != nil || changed.ConfirmedAt != nil || changed.ConfirmedBy != nil {
		t.Errorf("a changed test kept its confirmation: %+v, %v", changed, err)
	}
}

func TestOnlyPublicChecksCanBeRead(t *testing.T) {
	ctx, pool, project := setup(t)
	tk := mustCreate(t, ctx, pool, project, ticket("checked", model.SourceHuman, model.PriorityLow)).View.Ticket
	if _, err := pool.Exec(ctx,
		`INSERT INTO ticket_checks (ticket_id, ordinal, description, kind, command, visibility) VALUES
		   ($1, 1, 'public gate', 'command', 'just test', 'public'),
		   ($1, 2, 'held-out probe', 'command', 'secret.sh', 'held_out')`, tk.ID); err != nil {
		t.Fatal(err)
	}
	checks, err := service.ListPublicChecks(ctx, pool, tk.ID)
	if err != nil || len(checks) != 1 || checks[0].Description != "public gate" {
		t.Errorf("public checks = %+v (%v)", checks, err)
	}
}

// Section A is protected by absence: TicketPatch has no field for it, so no transport that decodes a
// patch can write it. This fails the day someone adds one.
func TestTicketPatchCannotCarrySectionA(t *testing.T) {
	patch := reflect.TypeOf(service.TicketPatch{})
	for _, name := range []string{"Intent", "Constraints", "NonGoals", "Criteria", "GatePassed", "ApproachApprovedBy", "ApproachApprovedAt"} {
		if _, ok := patch.FieldByName(name); ok {
			t.Errorf("TicketPatch has %s; it belongs to the human's or the runner's path only", name)
		}
	}
}
