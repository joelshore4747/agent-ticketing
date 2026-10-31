package service_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/joelshore/ticketing/api/internal/apperr"
	"github.com/joelshore/ticketing/api/internal/model"
	"github.com/joelshore/ticketing/api/internal/service"
)

func planInput(title string, tickets ...service.PlannedTicket) service.PlanInput {
	return service.PlanInput{
		Title: title, What: "what it builds", How: "how it goes about it", Why: "why it matters",
		Demo: "open the page and see it", Tickets: tickets, Actor: "tester",
	}
}

func planned(title string, tags ...string) service.PlannedTicket {
	return service.PlannedTicket{Title: title, Type: model.TypeFeature, Priority: model.PriorityHigh, Tags: tags, Goals: []int32{1}}
}

func mustPlan(t *testing.T, ctx context.Context, pool *pgxpool.Pool, project *model.Project, in service.PlanInput) *service.Plan {
	t.Helper()
	plan, err := service.PlanBatch(ctx, pool, project, in)
	if err != nil {
		t.Fatalf("plan %q: %v", in.Title, err)
	}
	// Goal 2's reviews gate the start and the finish; tests about other rules take them as given.
	for _, phase := range []string{"before", "after"} {
		if _, err := service.RecordReview(ctx, pool, project.ID, plan.Batch.ID, phase, "abc1234", "stand-in review", "tester"); err != nil {
			t.Fatalf("review %s: %v", phase, err)
		}
	}
	// And goal 6's report, which finishing a reviewed batch needs.
	if _, err := pool.Exec(ctx, `UPDATE batches SET summary = $2 WHERE id = $1`, plan.Batch.ID, standInReport); err != nil {
		t.Fatal(err)
	}
	return plan
}

func keys(views []model.TicketView) string {
	out := make([]string, len(views))
	for i, v := range views {
		out[i] = v.Ticket.Key
	}
	return strings.Join(out, " ")
}

func TestPlanBatchFilesItsTicketsInOrderAndEndsInAnEvalTicket(t *testing.T) {
	ctx, pool, project := setup(t)
	infra := "infra"
	scripted := planned("then the script")
	scripted.Area = &infra
	plan := mustPlan(t, ctx, pool, project, planInput("Ordering", planned("first the schema", "architecture"), scripted))

	if plan.Batch.Kind != model.BatchPlan || plan.Batch.What != "what it builds" || plan.Batch.How != "how it goes about it" ||
		plan.Batch.Brief != "why it matters" {
		t.Errorf("batch = %+v", plan.Batch)
	}
	if len(plan.Tickets) != 3 {
		t.Fatalf("tickets = %d, want the two planned plus the eval ticket", len(plan.Tickets))
	}
	for i, v := range plan.Tickets {
		if model.Deref(v.Ticket.BatchPosition) != int32(i+1) {
			t.Errorf("%s at position %v, want %d", v.Ticket.Key, v.Ticket.BatchPosition, i+1)
		}
		if v.Ticket.Source != model.SourceAI || v.Ticket.Status != model.StatusTriage {
			t.Errorf("%s is %s/%s, want an AI ticket in triage", v.Ticket.Key, v.Ticket.Source, v.Ticket.Status)
		}
	}
	if got := strings.Join(plan.Tickets[0].Tags, ","); got != "architecture" {
		t.Errorf("tags = %q", got)
	}
	eval := plan.Tickets[2].Ticket
	if eval.Type != model.TypeEval || eval.Title != "Eval: Ordering" {
		t.Fatalf("last ticket = %s %q, want the eval ticket", eval.Type, eval.Title)
	}
	wants(t, eval.Description, "eval description",
		"just lint", "just test", "just guidance-check", "just selftest-quick", "open the page and see it")
}

// TP-173: goal 9 needs the gate to be a number, not a habit. A planned ticket had no command check
// at all, so `scripts/run-checks.sh <key>` printed "no command checks; nothing to gate on" and wrote
// nothing — batch 20c65c62 finished four tickets with gate_passed null while the gate had been run by
// hand, green, on every one. Both planning doors now write the check, so the runner has something to
// run and records the verdict itself.
func TestEveryPlannedTicketCarriesTheGateItIsClosedThrough(t *testing.T) {
	ctx, pool, project := setup(t)
	plan := mustPlan(t, ctx, pool, project, planInput("Gated", planned("first"), planned("second")))

	gate := func(key string, id uuid.UUID) []model.TicketCheck {
		t.Helper()
		checks, err := service.ListPublicChecks(ctx, pool, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(checks) != len(service.DefaultChecks) {
			t.Fatalf("%s carries %d public check(s), want the %d the service attaches", key, len(checks), len(service.DefaultChecks))
		}
		for i, c := range checks {
			want := service.DefaultChecks[i]
			// kind and expect_exit are what run-checks.sh runs on: a manual check is listed, not run.
			if c.Kind != "command" || model.Deref(c.Command) != want.Command || c.ExpectExit != 0 || c.Description != want.Description {
				t.Errorf("%s check %d = %s %q expecting %d (%q)", key, c.Ordinal, c.Kind, model.Deref(c.Command), c.ExpectExit, c.Description)
			}
		}
		return checks
	}
	// The default is public on purpose: `just gate` is in the Justfile, so there is nothing to hold
	// out, and a held-out check would make gate_passed a condition of done for every planned ticket.
	if got := model.Deref(gate(plan.Tickets[0].Ticket.Key, plan.Tickets[0].Ticket.ID)[0].Command); got != "just gate" {
		t.Errorf("the default gate command is %q; the builders' gate is `just gate`", got)
	}
	gate(plan.Tickets[1].Ticket.Key, plan.Tickets[1].Ticket.ID)

	// The eval ticket is left alone: it carries the gate commands in its description and is judged on
	// having run them all.
	eval := plan.Tickets[2].Ticket
	var onEval int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ticket_checks WHERE ticket_id = $1`, eval.ID).Scan(&onEval); err != nil {
		t.Fatal(err)
	}
	if onEval != 0 {
		t.Errorf("the eval ticket %s carries %d check(s); its gate is its description", eval.Key, onEval)
	}

	// Joining a plan mid-batch is joining its gate.
	loose := mustCreate(t, ctx, pool, project, ticket("found while gating", model.SourceHuman, model.PriorityLow)).View.Ticket
	if _, err := service.AddToBatch(ctx, pool, project.ID, plan.Batch.ID, loose.Key, nil, "tester"); err != nil {
		t.Fatal(err)
	}
	gate(loose.Key, loose.ID)

	// And a ticket that already carries checks keeps them: checks-seed.sh bootstraps held-out answer
	// keys rather than syncing them, so a default that displaced one would replace what is scored.
	seeded := mustCreate(t, ctx, pool, project, ticket("already scored", model.SourceHuman, model.PriorityLow)).View.Ticket
	if _, err := pool.Exec(ctx,
		`INSERT INTO ticket_checks (ticket_id, ordinal, description, kind, command) VALUES ($1, 1, 'the answer key', 'command', 'test -f secret')`,
		seeded.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddToBatch(ctx, pool, project.ID, plan.Batch.ID, seeded.Key, nil, "tester"); err != nil {
		t.Fatal(err)
	}
	var commands []string
	rows, err := pool.Query(ctx, `SELECT command FROM ticket_checks WHERE ticket_id = $1 ORDER BY ordinal`, seeded.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var command string
		if err := rows.Scan(&command); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, command)
	}
	if strings.Join(commands, " ") != "test -f secret" {
		t.Errorf("%s now carries %v; a seeded check is never displaced or added to", seeded.Key, commands)
	}
}

// Goal 3: the cleanup pass measures whether the batch shrank, not only whether it added dead code.
func TestTheEvalTicketAsksWhetherTheBatchShrank(t *testing.T) {
	ctx, pool, project := setup(t)
	plan := mustPlan(t, ctx, pool, project, planInput("Shrinking", planned("cut something")))

	eval := plan.Tickets[len(plan.Tickets)-1].Ticket
	wants(t, eval.Description, "eval description",
		"./scripts/batch-size.sh "+plan.Batch.ID.String(), // the command, with this batch's own id
		// TP-169: the dead-code pass names its command too, so nobody re-derives the go.sum workaround.
		"`just deadcode`",
		"Net above zero **fails this check**",
		"record_decision",
		"evidence_refs",
		// Joel, 2026-10-18: the Approver no longer has to accept growth (it was TP-245's rule). Reserving it
		// stalled FS batches 2-4 at their evals; the evaluator judges it now, against the brief.
		"The evaluator may record it itself",
		"growth the brief did not ask for is a finding",
		"a measurement at or above the final net",
	)
}

// TP-189: agent prompts and guidance are the harness every later batch runs on, so a batch that
// edits one has to show the evidence it was changed on. The eval ticket is where that is asked for,
// because it is the gate the batch cannot finish without.
func TestTheEvalTicketAsksForEvidenceBeforeAHarnessChange(t *testing.T) {
	ctx, pool, project := setup(t)
	plan := mustPlan(t, ctx, pool, project, planInput("Harness", planned("edit a prompt")))

	eval := plan.Tickets[len(plan.Tickets)-1].Ticket
	wants(t, eval.Description, "eval description",
		"Evidence before changing the harness",
		"`.claude/agents/*`",               // the agent prompts
		"`docs/guidance/*`",                // the guidance corpus, files and PUTs alike
		"/api/guidance",                    //
		"fails the batch**",                // it is a failure, not a footnote
		"guidance-score line",              // TP-188's per-revision scores are the first evidence path
		"`eval.sh` A/B run on 3",           // and an A/B run is the second
		"`record_decision` on this ticket", // cited where the eval is judged
		// Joel, 2026-10-16: a document corrected to match the code is not a prompt changed on a hunch.
		"only corrects a document to match the code",
		// Joel, 2026-10-18: FS's Go rules were a planned ticket, and no score line or past Go ticket could
		// exist for them, so the check could never pass. The plan's approval covers a planned edit.
		"an edit a planned ticket in this batch asks for was approved with the plan",
		"Only an unplanned edit needs the evidence above",
	)
}

func TestPlanBatchRefusesAPlanItCouldNotCheck(t *testing.T) {
	ctx, pool, project := setup(t)
	noDemo := planInput("No demo", planned("work"))
	noDemo.Demo = " "
	evalGiven := planInput("Eval given", service.PlannedTicket{Title: "my own eval", Type: model.TypeEval, Goals: []int32{1}})
	badTag := planInput("Bad tag", planned("work", "perf"))
	// Goal 5: a planned ticket names the key goals it serves.
	noGoal := planInput("No goal", service.PlannedTicket{Title: "aimless", Type: model.TypeChore})
	badGoal := planInput("Bad goal", service.PlannedTicket{Title: "off the map", Type: model.TypeChore, Goals: []int32{10}})
	for _, in := range []service.PlanInput{noDemo, evalGiven, badTag, noGoal, badGoal} {
		_, err := service.PlanBatch(ctx, pool, project, in)
		if !apperr.Is(err, apperr.BadRequest) {
			t.Errorf("%s: err = %v, want 400", in.Title, err)
		}
	}
	_, err := service.PlanBatch(ctx, pool, project, badTag)
	if err == nil || !strings.Contains(err.Error(), "performance") {
		t.Errorf("an unknown tag should list the managed ones: %v", err)
	}
	var batches int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM batches`).Scan(&batches); err != nil || batches != 0 {
		t.Errorf("a refused plan left %d batches behind (%v)", batches, err)
	}
}

func TestPlanBatchInARoadmapTakesTheNextPlace(t *testing.T) {
	ctx, pool, project := setup(t)
	roadmap, err := service.CreateRoadmap(ctx, pool, project.ID, "Evolution", "the brief")
	if err != nil {
		t.Fatal(err)
	}
	for want := int32(1); want <= 2; want++ {
		in := planInput("batch", planned("work"))
		in.Tickets[0].Title = "work " + string(rune('0'+want))
		in.RoadmapID = &roadmap.ID
		plan := mustPlan(t, ctx, pool, project, in)
		if model.Deref(plan.Batch.Sequence) != want {
			t.Errorf("sequence = %v, want %d", plan.Batch.Sequence, want)
		}
	}
}

// TP-149: a roadmap's later batches are laid out as placeholders, and the Strategist fills the next
// one at the checkpoint. One plan_batch call does it, atomically, without the batch moving.
func TestPlanBatchFillsARoadmapPlaceholderInPlace(t *testing.T) {
	ctx, pool, project := setup(t)
	roadmap, err := service.CreateRoadmap(ctx, pool, project.ID, "Evolution", "the brief")
	if err != nil {
		t.Fatal(err)
	}
	placeholder := planInput("Batch two, one day")
	placeholder.RoadmapID = &roadmap.ID
	placeholder.Demo = "to be decided"
	held := mustPlan(t, ctx, pool, project, placeholder)
	if len(held.Tickets) != 1 || held.Tickets[0].Ticket.Type != model.TypeEval {
		t.Fatalf("a placeholder should hold only its eval ticket, got %s", keys(held.Tickets))
	}
	evalKey, ref := held.Tickets[0].Ticket.Key, model.Deref(held.Batch.GitRef)

	fill := planInput("Batch two, decided", planned("first the schema"), planned("then the wiring"))
	fill.Demo = "run just demo-two and read the count"
	fill.BatchID = &held.Batch.ID
	plan, err := service.PlanBatch(ctx, pool, project, fill)
	if err != nil {
		t.Fatalf("fill: %v", err)
	}

	if plan.Batch.ID != held.Batch.ID || model.Deref(plan.Batch.Sequence) != 1 ||
		model.Deref(plan.Batch.RoadmapID) != roadmap.ID || model.Deref(plan.Batch.GitRef) != ref {
		t.Errorf("filling moved the batch: %+v", plan.Batch)
	}
	if plan.Batch.Title != "Batch two, decided" || plan.Batch.What != "what it builds" {
		t.Errorf("the human read was not overwritten: %+v", plan.Batch)
	}
	if got := len(plan.Tickets); got != 3 {
		t.Fatalf("tickets = %s, want two planned plus the eval ticket", keys(plan.Tickets))
	}
	for i, v := range plan.Tickets {
		if model.Deref(v.Ticket.BatchPosition) != int32(i+1) {
			t.Errorf("%s at position %v, want %d", v.Ticket.Key, v.Ticket.BatchPosition, i+1)
		}
	}
	// The placeholder's own eval ticket is kept and rewritten, not replaced: the batch has one gate.
	last := plan.Tickets[2].Ticket
	if last.Key != evalKey || last.Type != model.TypeEval || last.Title != "Eval: Batch two, decided" {
		t.Fatalf("last ticket = %s %s %q, want the placeholder's eval ticket rewritten", last.Key, last.Type, last.Title)
	}
	if !strings.Contains(last.Description, "run just demo-two and read the count") ||
		strings.Contains(last.Description, "to be decided") {
		t.Errorf("the eval ticket still carries the placeholder's demo:\n%s", last.Description)
	}
	// And nothing arrived as an unbatched finding on the way.
	var loose int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tickets WHERE batch_id IS NULL`).Scan(&loose); err != nil || loose != 0 {
		t.Errorf("filling left %d tickets outside the batch (%v)", loose, err)
	}
}

// The git_ref chain both planning paths now share (TP-170). The placeholder test above cannot see
// the middle step: its placeholder carries the derived name, so keeping the batch's ref and deriving
// a fresh one give the same string. A ref someone chose is the case that tells them apart.
func TestFillingAPlaceholderKeepsTheRefItWasGiven(t *testing.T) {
	ctx, pool, project := setup(t)
	roadmap, err := service.CreateRoadmap(ctx, pool, project.ID, "Evolution", "the brief")
	if err != nil {
		t.Fatal(err)
	}
	placeholder := planInput("Batch two, one day")
	placeholder.RoadmapID = &roadmap.ID
	placeholder.GitRef = model.Ptr("release/2026-11")
	held := mustPlan(t, ctx, pool, project, placeholder)

	fill := planInput("Batch two, decided", planned("the work"))
	fill.BatchID = &held.Batch.ID
	plan, err := service.PlanBatch(ctx, pool, project, fill)
	if err != nil {
		t.Fatalf("fill: %v", err)
	}
	if ref := model.Deref(plan.Batch.GitRef); ref != "release/2026-11" {
		t.Errorf("git_ref = %q, want the placeholder's own ref kept, not a derived one", ref)
	}

	next := planInput("Batch three, one day")
	next.RoadmapID = &roadmap.ID
	waiting := mustPlan(t, ctx, pool, project, next)
	moved := planInput("Batch three, decided", planned("the later work"))
	moved.BatchID = &waiting.Batch.ID
	moved.GitRef = model.Ptr("release/2026-12")
	filled, err := service.PlanBatch(ctx, pool, project, moved)
	if err != nil {
		t.Fatalf("fill with a ref: %v", err)
	}
	if ref := model.Deref(filled.Batch.GitRef); ref != "release/2026-12" {
		t.Errorf("git_ref = %q, want the ref the caller gave to win over the one the batch carried", ref)
	}

	loose := planInput("Loose", planned("work"))
	loose.GitRef = model.Ptr("  ")
	if ref := model.Deref(mustPlan(t, ctx, pool, project, loose).Batch.GitRef); !strings.HasPrefix(ref, "batch/") {
		t.Errorf("git_ref = %q, want a blank one ignored and a name derived", ref)
	}
}

func TestPlanBatchRefusesToFillWhatIsNotAPlaceholder(t *testing.T) {
	ctx, pool, project := setup(t)
	full := mustPlan(t, ctx, pool, project, planInput("Already planned", planned("work")))
	empty := mustPlan(t, ctx, pool, project, planInput("Still empty"))

	intoWork := planInput("Filling", planned("new work"))
	intoWork.BatchID = &full.Batch.ID
	if err := fillErr(ctx, pool, project, intoWork); !apperr.Is(err, apperr.Conflict) {
		t.Errorf("a batch with tickets: err = %v, want 409", err)
	}

	withRoadmap := planInput("Filling", planned("other work"))
	withRoadmap.BatchID = &empty.Batch.ID
	seq := int32(1)
	withRoadmap.Sequence = &seq
	if err := fillErr(ctx, pool, project, withRoadmap); !apperr.Is(err, apperr.BadRequest) {
		t.Errorf("batch with sequence: err = %v, want 400", err)
	}

	noTickets := planInput("Filling")
	noTickets.BatchID = &empty.Batch.ID
	if err := fillErr(ctx, pool, project, noTickets); !apperr.Is(err, apperr.BadRequest) {
		t.Errorf("no tickets: err = %v, want 400", err)
	}

	missing := planInput("Filling", planned("more work"))
	gone := uuid.New()
	missing.BatchID = &gone
	if err := fillErr(ctx, pool, project, missing); !apperr.Is(err, apperr.NotFound) {
		t.Errorf("unknown batch: err = %v, want 404", err)
	}

	// A refused fill leaves the placeholder exactly as it was.
	views, err := service.ListBatchTickets(ctx, pool, empty.Batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].Ticket.Type != model.TypeEval {
		t.Errorf("the placeholder now holds %s", keys(views))
	}
}

func fillErr(ctx context.Context, pool *pgxpool.Pool, project *model.Project, in service.PlanInput) error {
	_, err := service.PlanBatch(ctx, pool, project, in)
	return err
}

func TestReorderKeepsStartedWorkAndTheEvalTicketInPlace(t *testing.T) {
	ctx, pool, project := setup(t)
	plan := mustPlan(t, ctx, pool, project, planInput("Reorder", planned("a"), planned("b"), planned("c")))
	if _, err := service.AcceptBatch(ctx, pool, project.ID, plan.Batch.ID, "joel"); err != nil {
		t.Fatal(err)
	}
	a, b, c, eval := plan.Tickets[0].Ticket.Key, plan.Tickets[1].Ticket.Key, plan.Tickets[2].Ticket.Key, plan.Tickets[3].Ticket.Key
	if got := claimKey(t, ctx, pool, project); got != a {
		t.Fatalf("claimed %q, want %s", got, a)
	}

	reorder := func(order ...string) ([]model.TicketView, error) {
		return service.ReorderBatch(ctx, pool, project.ID, plan.Batch.ID, order, "tester")
	}
	if _, err := reorder(b, a, c, eval); !apperr.Is(err, apperr.Conflict) || !strings.Contains(err.Error(), a) {
		t.Errorf("moving started %s: %v", a, err)
	}
	if _, err := reorder(a, b, eval, c); !apperr.Is(err, apperr.Conflict) || !strings.Contains(err.Error(), "stays last") {
		t.Errorf("moving the eval ticket: %v", err)
	}
	if _, err := reorder(a, b, c); !apperr.Is(err, apperr.BadRequest) {
		t.Errorf("leaving a ticket out: %v", err)
	}
	got, err := reorder(a, c, b, eval)
	if err != nil {
		t.Fatal(err)
	}
	if keys(got) != strings.Join([]string{a, c, b, eval}, " ") {
		t.Errorf("order = %s", keys(got))
	}
}

func TestTicketsMoveInAndOutOfAPlanAroundItsEvalTicket(t *testing.T) {
	ctx, pool, project := setup(t)
	plan := mustPlan(t, ctx, pool, project, planInput("Membership", planned("planned")))
	loose := mustCreate(t, ctx, pool, project, ticket("filed separately", model.SourceHuman, model.PriorityLow)).View.Ticket

	got, err := service.AddToBatch(ctx, pool, project.ID, plan.Batch.ID, loose.Key, nil, "tester")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{plan.Tickets[0].Ticket.Key, loose.Key, plan.Tickets[1].Ticket.Key}, " ")
	if keys(got) != want {
		t.Errorf("after add: %s, want %s (just before the eval ticket)", keys(got), want)
	}
	if _, err := service.RemoveFromBatch(ctx, pool, project.ID, plan.Batch.ID, plan.Tickets[1].Ticket.Key, "tester"); !apperr.Is(err, apperr.Conflict) {
		t.Errorf("removing the eval ticket: %v", err)
	}
	got, err = service.RemoveFromBatch(ctx, pool, project.ID, plan.Batch.ID, plan.Tickets[0].Ticket.Key, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if keys(got) != loose.Key+" "+plan.Tickets[1].Ticket.Key || model.Deref(got[0].Ticket.BatchPosition) != 1 {
		t.Errorf("after remove: %s, positions should close up", keys(got))
	}
}

// TP-171: plan_batch is the one door to a plan batch, because it is the one that files the eval
// ticket gating it. start_batch opened a bare plan that rule 16 could never let finish.
func TestStartBatchRefusesAPlanAndSendsYouToPlanBatch(t *testing.T) {
	ctx, pool, project := setup(t)
	_, err := service.StartBatch(ctx, pool, project.ID, service.NewBatch{Kind: model.BatchPlan, Title: "by hand"})
	if !apperr.Is(err, apperr.BadRequest) {
		t.Fatalf("StartBatch(kind: plan) = %v, want a 400", err)
	}
	if !strings.Contains(err.Error(), "plan_batch") {
		t.Errorf("the refusal should name plan_batch: %v", err)
	}
	// Exactly one kind is refused: findings batches are reported into, not planned.
	for _, kind := range []model.BatchKind{model.BatchAudit, model.BatchReview, model.BatchPrompt} {
		if _, err := service.StartBatch(ctx, pool, project.ID, service.NewBatch{Kind: kind, Title: string(kind)}); err != nil {
			t.Errorf("StartBatch(kind: %s): %v", kind, err)
		}
	}
}

func TestAcceptBatchAndCheckpointAreTheHumansMoves(t *testing.T) {
	ctx, pool, project := setup(t)
	plan := mustPlan(t, ctx, pool, project, planInput("Human moves", planned("work")))
	accepted, err := service.AcceptBatch(ctx, pool, project.ID, plan.Batch.ID, "joel")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range accepted {
		if v.Ticket.Status != model.StatusOpen {
			t.Errorf("%s is %s after accepting the batch", v.Ticket.Key, v.Ticket.Status)
		}
	}
	if _, err := service.CheckpointBatch(ctx, pool, project.ID, plan.Batch.ID, model.CheckpointContinue, "", "joel"); !apperr.Is(err, apperr.Conflict) {
		t.Errorf("checkpointing an unfinished batch: %v", err)
	}
	finishAll(t, ctx, pool, project, accepted, plan.Batch.ID)
	cp, err := service.CheckpointBatch(ctx, pool, project.ID, plan.Batch.ID, model.CheckpointAdjust, "swap batches 3 and 4", "joel")
	if err != nil {
		t.Fatal(err)
	}
	if model.Deref(cp.CheckpointDecision) != model.CheckpointAdjust || model.Deref(cp.CheckpointBy) != "joel" ||
		model.Deref(cp.CheckpointNote) != "swap batches 3 and 4" {
		t.Errorf("checkpoint = %+v", cp)
	}
	if _, err := service.CheckpointBatch(ctx, pool, project.ID, plan.Batch.ID, model.CheckpointStop, "", "joel"); !apperr.Is(err, apperr.Conflict) {
		t.Errorf("a checkpoint is set once: %v", err)
	}
}

// TP-175: rule 16 lets a plan batch finish only through its eval ticket, so the plan a stale API
// created — nobody's work, no eval that could ever pass — sat unfinished for good. A `stop`
// checkpoint retires it; a batch whose work has started is refused, because its account is the eval.
func TestAStopCheckpointRetiresAPlanNobodyStarted(t *testing.T) {
	ctx, pool, project := setup(t)
	roadmap, err := service.CreateRoadmap(ctx, pool, project.ID, "Retirement", "")
	if err != nil {
		t.Fatal(err)
	}
	empty := planInput("Never worked")
	empty.RoadmapID = &roadmap.ID
	plan := mustPlan(t, ctx, pool, project, empty)

	// `stop` is the whole escape hatch: the other two decisions still wait for the eval.
	for _, decision := range []model.CheckpointDecision{model.CheckpointContinue, model.CheckpointAdjust} {
		_, err := service.CheckpointBatch(ctx, pool, project.ID, plan.Batch.ID, decision, "", "joel")
		if !apperr.Is(err, apperr.Conflict) || !strings.Contains(err.Error(), "only stop retires") {
			t.Errorf("checkpointing an unfinished batch %s = %v, want a 409 naming stop", decision, err)
		}
	}
	retired, err := service.CheckpointBatch(ctx, pool, project.ID, plan.Batch.ID, model.CheckpointStop,
		"a stale API created it", "joel")
	if err != nil {
		t.Fatalf("retiring a plan nobody started: %v", err)
	}
	if retired.FinishedAt == nil || model.Deref(retired.CheckpointDecision) != model.CheckpointStop {
		t.Errorf("retired batch = %+v, want finished_at set and checkpointed stop", retired)
	}
	if retired.Digest != nil {
		t.Errorf("a retired batch has no digest, nothing happened in it to digest: %q", *retired.Digest)
	}
	stands, err := service.RoadmapBatches(ctx, pool, roadmap.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stands) != 1 || stands[0].State != service.StateStopped {
		t.Errorf("the roadmap reads %+v, want the one batch stopped rather than unfinished", stands)
	}

	// The same call on a batch whose work has started is refused, and names the ticket.
	live := mustPlan(t, ctx, pool, project, planInput("Half worked", planned("work somebody picked up")))
	accepted, err := service.AcceptBatch(ctx, pool, project.ID, live.Batch.ID, "joel")
	if err != nil {
		t.Fatal(err)
	}
	tk := accepted[0].Ticket
	move(t, ctx, pool, &tk, model.StatusInProgress)
	_, err = service.CheckpointBatch(ctx, pool, project.ID, live.Batch.ID, model.CheckpointStop, "", "joel")
	if !apperr.Is(err, apperr.Conflict) || !strings.Contains(err.Error(), tk.Key+" is in_progress") {
		t.Errorf("retiring a batch with started work = %v, want a 409 naming %s", err, tk.Key)
	}
	// And a findings batch is not a plan: finish_batch already closes one whenever its author says.
	audit, err := service.StartBatch(ctx, pool, project.ID, service.NewBatch{Kind: model.BatchAudit, Title: "a sweep"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.CheckpointBatch(ctx, pool, project.ID, audit.ID, model.CheckpointStop, "", "joel")
	if !apperr.Is(err, apperr.Conflict) || !strings.Contains(err.Error(), "not a plan") {
		t.Errorf("retiring an audit batch = %v, want a 409 saying it is not a plan", err)
	}
}

func TestARoadmapShowsWhereEachBatchStands(t *testing.T) {
	ctx, pool, project := setup(t)
	roadmap, err := service.CreateRoadmap(ctx, pool, project.ID, "Evolution", "")
	if err != nil {
		t.Fatal(err)
	}
	first := planInput("one", planned("work one"))
	first.RoadmapID = &roadmap.ID
	plan := mustPlan(t, ctx, pool, project, first)
	placeholder := planInput("two")
	placeholder.RoadmapID = &roadmap.ID
	mustPlan(t, ctx, pool, project, placeholder)

	states := func() string {
		batches, err := service.RoadmapBatches(ctx, pool, roadmap.ID)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, len(batches))
		for i, b := range batches {
			out[i] = string(b.State)
		}
		return strings.Join(out, " ")
	}
	if got := states(); got != "open locked" {
		t.Errorf("states = %q, want open locked", got)
	}
	accepted, err := service.AcceptBatch(ctx, pool, project.ID, plan.Batch.ID, "joel")
	if err != nil {
		t.Fatal(err)
	}
	work, eval := accepted[0].Ticket, accepted[1].Ticket
	move(t, ctx, pool, &work, model.StatusInProgress, model.StatusDone)
	if got := states(); got != "at_eval locked" {
		t.Errorf("states = %q, want at_eval locked", got)
	}
	move(t, ctx, pool, &eval, model.StatusInProgress, model.StatusDone)
	if _, err := service.FinishBatch(ctx, pool, plan.Batch.ID, nil); err != nil {
		t.Fatal(err)
	}
	if got := states(); got != "awaiting_checkpoint locked" {
		t.Errorf("states = %q", got)
	}
	if _, err := service.CheckpointBatch(ctx, pool, project.ID, plan.Batch.ID, model.CheckpointContinue, "", "joel"); err != nil {
		t.Fatal(err)
	}
	if got := states(); got != "finished unplanned" {
		t.Errorf("states = %q, want finished unplanned", got)
	}
}

func TestBatchProgressNamesTheNextTicketAndWhatTheRestWaitOn(t *testing.T) {
	ctx, pool, project := setup(t)
	plan := mustPlan(t, ctx, pool, project, planInput("Progress", planned("a"), planned("b")))
	progress, err := service.BatchProgress(ctx, pool, &plan.Batch, plan.Tickets)
	if err != nil {
		t.Fatal(err)
	}
	a, b, eval := plan.Tickets[0].Ticket.Key, plan.Tickets[1].Ticket.Key, plan.Tickets[2].Ticket.Key
	if model.Deref(progress.Next) != a || progress.BlockedBy[b] != a || progress.BlockedBy[eval] != a || progress.LockedBy != "" {
		t.Errorf("progress = %+v", progress)
	}
}

func TestTicketsCanBeListedByTag(t *testing.T) {
	ctx, pool, project := setup(t)
	mustPlan(t, ctx, pool, project, planInput("Tagged", planned("secure it", "security"), planned("speed it", "performance")))
	views, _, err := service.ListTickets(ctx, pool, project.ID, service.TicketFilter{Tag: model.Ptr("security")}, "", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].Ticket.Title != "secure it" {
		t.Errorf("tag filter returned %s", keys(views))
	}
}

var _ = uuid.Nil

// A batch finished before rule 16 can hold tickets that never started; they must be able to leave it
// to be re-planned, without reopening anything a checkpoint signed off.
func TestUnstartedTicketsCanLeaveABatchFinishedWithoutACheckpoint(t *testing.T) {
	ctx, pool, project := setup(t)
	batch, tickets := planBatch(t, ctx, pool, project, false, "done before the rules", "never started", "also never started")
	move(t, ctx, pool, tickets[0], model.StatusInProgress, model.StatusDone)
	if _, err := pool.Exec(ctx, `UPDATE batches SET finished_at = now() WHERE id = $1`, batch.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := service.RemoveFromBatch(ctx, pool, project.ID, batch.ID, tickets[0].Key, "tester"); !apperr.Is(err, apperr.Conflict) {
		t.Errorf("a done ticket left a finished batch: %v", err)
	}
	if _, err := service.ReorderBatch(ctx, pool, project.ID, batch.ID,
		[]string{tickets[0].Key, tickets[2].Key, tickets[1].Key}, "tester"); !apperr.Is(err, apperr.Conflict) {
		t.Errorf("a finished batch was reordered: %v", err)
	}
	got, err := service.RemoveFromBatch(ctx, pool, project.ID, batch.ID, tickets[1].Key, "tester")
	if err != nil {
		t.Fatalf("freeing an unstarted ticket: %v", err)
	}
	if len(got) != 2 || model.Deref(got[1].Ticket.BatchPosition) != 3 {
		t.Errorf("after removal: %s; the rest should keep their recorded positions", keys(got))
	}
	freed, err := service.GetTicket(ctx, pool, project.ID, tickets[1].Key)
	if err != nil || freed.Ticket.BatchID != nil || freed.Ticket.BatchPosition != nil {
		t.Errorf("freed ticket = %+v (%v)", freed, err)
	}

	if _, err := pool.Exec(ctx, `UPDATE batches SET checkpoint_decision = 'continue' WHERE id = $1`, batch.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RemoveFromBatch(ctx, pool, project.ID, batch.ID, tickets[2].Key, "tester"); !apperr.Is(err, apperr.Conflict) {
		t.Errorf("a checkpointed batch gave up a ticket: %v", err)
	}
}

// Tags are set on create and replaced on update, from the managed list only (TP-74).
func TestTicketsAreTaggedOnCreateAndRetaggedOnUpdate(t *testing.T) {
	ctx, pool, project := setup(t)
	input := ticket("tagged at birth", model.SourceHuman, model.PriorityLow)
	input.Tags = []string{"security", "tech-debt"}
	created := mustCreate(t, ctx, pool, project, input).View
	if got := strings.Join(created.Tags, ","); got != "security,tech-debt" {
		t.Fatalf("tags on create = %q", got)
	}

	bad := ticket("tagged badly", model.SourceHuman, model.PriorityLow)
	bad.Tags = []string{"perf"}
	if _, err := service.CreateTicket(ctx, pool, project, bad); !apperr.Is(err, apperr.BadRequest) {
		t.Errorf("unknown tag on create: %v", err)
	}

	retag := []string{"performance"}
	updated, err := service.UpdateTicket(ctx, pool, created.Ticket.ID, created.Ticket.Version,
		service.TicketPatch{Tags: &retag}, "tester")
	if err != nil || strings.Join(updated.Tags, ",") != "performance" {
		t.Fatalf("retag = %v, %v", updated, err)
	}
	none := []string{}
	cleared, err := service.UpdateTicket(ctx, pool, created.Ticket.ID, updated.Ticket.Version,
		service.TicketPatch{Tags: &none}, "tester")
	if err != nil || len(cleared.Tags) != 0 {
		t.Fatalf("clear = %v, %v", cleared, err)
	}
	events, err := service.ListEvents(ctx, pool, created.Ticket.ID)
	if err != nil {
		t.Fatal(err)
	}
	var tagEvents int
	for _, e := range events {
		if e.Field == "tags" {
			tagEvents++
		}
	}
	if tagEvents != 3 {
		t.Errorf("tag events = %d, want 3 (create, retag, clear)", tagEvents)
	}
}

// Migration 0004 (TP-77): the ownership columns and tables exist, scan into the model, and hold
// their constraints.
func TestTheOwnershipSchemaHoldsItsShape(t *testing.T) {
	ctx, pool, project := setup(t)
	tk := mustCreate(t, ctx, pool, project, ticket("owned", model.SourceHuman, model.PriorityLow)).View.Ticket
	if _, err := pool.Exec(ctx,
		`UPDATE tickets SET intent = 'know first', estimated_cost_usd = 0.4217, estimated_tokens = 9000,
		   evidence_refs = '{a.go:1}', gate_passed = true WHERE id = $1`, tk.ID); err != nil {
		t.Fatal(err)
	}
	got, err := service.GetTicket(ctx, pool, project.ID, tk.Key)
	if err != nil {
		t.Fatal(err)
	}
	g := got.Ticket
	if g.Intent != "know first" || model.Deref(g.EstimatedCostUSD) != 0.4217 || model.Deref(g.EstimatedTokens) != 9000 ||
		len(g.EvidenceRefs) != 1 || !model.Deref(g.GatePassed) {
		t.Errorf("ownership fields read back as %+v", g)
	}

	if _, err := pool.Exec(ctx, `INSERT INTO ticket_criteria (ticket_id, tier, ordinal, text) VALUES ($1, 'vibes', 1, 'x')`, tk.ID); err == nil {
		t.Error("a criterion with an unknown tier was stored")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ticket_criteria (ticket_id, tier, ordinal, text) VALUES ($1, 'gate', 1, 'just test')`, tk.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ticket_criteria (ticket_id, tier, ordinal, text) VALUES ($1, 'gate', 1, 'again')`, tk.ID); err == nil {
		t.Error("two criteria took ordinal 1")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO tripwires (project_id, test_ref, meaning, body_sha256) VALUES ($1, 'a_test.go:TestX', 'm', 'h'), ($1, 'a_test.go:TestX', 'm', 'h')`, project.ID); err == nil {
		t.Error("a test was registered as two tripwires")
	}
	var visibility string
	if err := pool.QueryRow(ctx,
		`INSERT INTO ticket_checks (ticket_id, ordinal, description, kind, command) VALUES ($1, 1, 'd', 'command', 'true') RETURNING visibility`,
		tk.ID).Scan(&visibility); err != nil || visibility != "held_out" {
		t.Errorf("a new check is %q (%v); checks are hidden unless made public", visibility, err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO comments (ticket_id, author, body, kind) VALUES ($1, 'ai', 'propose', 'proposal')`, tk.ID); err != nil {
		t.Errorf("a proposal comment was refused: %v", err)
	}
}

// Migration 0005 (TP-82): decisions hold their constraints, and a ticket can point at its current one.
func TestTheDecisionsSchemaHoldsItsShape(t *testing.T) {
	ctx, pool, project := setup(t)
	tk := mustCreate(t, ctx, pool, project, ticket("decided", model.SourceHuman, model.PriorityLow)).View.Ticket
	if _, err := pool.Exec(ctx, `INSERT INTO decisions (ticket_id, kind, made_by, actor) VALUES ($1, 'deferred', 'ai', 'x')`, tk.ID); err == nil {
		t.Error("a deferral without a revisit trigger was stored")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO decisions (ticket_id, kind, made_by, actor) VALUES ($1, 'accepted', 'robot', 'x')`, tk.ID); err == nil {
		t.Error("made_by accepted something other than human or ai")
	}
	var id string
	if err := pool.QueryRow(ctx, `INSERT INTO decisions (ticket_id, kind, made_by, revisit_trigger, actor) VALUES ($1, 'deferred', 'ai', 'when the harness runs nightly', 'x') RETURNING id`, tk.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE tickets SET decision_id = $2 WHERE id = $1`, tk.ID, id); err != nil {
		t.Fatal(err)
	}
	got, err := service.GetTicket(ctx, pool, project.ID, tk.Key)
	if err != nil || got.Ticket.DecisionID == nil || got.Ticket.DecisionID.String() != id {
		t.Errorf("decision_id read back as %v (%v)", got.Ticket.DecisionID, err)
	}
}

// standInReport has the Narrator's five sections, for tests about other rules.
const standInReport = "## What was added\nx\n## Why\nx\n## What it achieved\nx\n## Where in the code\nx\n## How it moves the goals\nx\n"
