package service_test

import (
	"testing"

	"github.com/joelshore/ticketing/api/internal/apperr"
	"github.com/joelshore/ticketing/api/internal/model"
	"github.com/joelshore/ticketing/api/internal/service"
)

// TP-107: tickets name the key goals they serve; the digest says which were served and which not.
func TestTicketsNameTheirGoalsAndTheDigestSaysWhichWereServed(t *testing.T) {
	ctx, pool, project := setup(t)
	in := ticket("serves two", model.SourceHuman, model.PriorityLow)
	in.Goals = []int32{3, 2, 3}
	tk := mustCreate(t, ctx, pool, project, in).View.Ticket
	if got := tk.Goals; len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Errorf("goals = %v, want [2 3] (sorted, deduped)", got)
	}
	bad := ticket("off the map", model.SourceHuman, model.PriorityLow)
	bad.Goals = []int32{0}
	if _, err := service.CreateTicket(ctx, pool, project, bad); !apperr.Is(err, apperr.BadRequest) {
		t.Errorf("goal 0: %v", err)
	}
	v, err := service.UpdateTicket(ctx, pool, tk.ID, tk.Version, service.TicketPatch{Goals: &[]int32{9}}, "claude-code")
	if err != nil || len(v.Ticket.Goals) != 1 || v.Ticket.Goals[0] != 9 {
		t.Errorf("update goals: %v %v", v, err)
	}

	two := planned("clean up")
	two.Goals = []int32{3, 2}
	plan := mustPlan(t, ctx, pool, project, planInput("Goal batch", planned("first"), two))
	if g := plan.Tickets[2].Ticket.Goals; plan.Tickets[2].Ticket.Type != model.TypeEval || len(g) != 1 || g[0] != 1 {
		t.Errorf("the eval ticket's goals = %v, want [1]", g)
	}
	accepted, err := service.AcceptBatch(ctx, pool, project.ID, plan.Batch.ID, "joel")
	if err != nil {
		t.Fatal(err)
	}
	b := finishAll(t, ctx, pool, project, accepted, plan.Batch.ID)
	wants(t, *b.Digest, "digest", "## Goals served",
		"Served goal 1 (2 tickets), goal 2 (1 ticket), goal 3 (1 ticket).", "Not touched: goals 4, 5, 6, 7, 8, 9.")
}
