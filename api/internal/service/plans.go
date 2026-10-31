package service

import (
	"cmp"
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/joelshore/ticketing/api/internal/apperr"
	"github.com/joelshore/ticketing/api/internal/model"
)

// The batch commands. A plan batch is an ordered list of tickets ending in an eval ticket; these
// are the only ways to make one, change its order, or move work in and out of it, so the shape is
// kept here rather than trusted to every caller.

type PlannedTicket struct {
	Title       string
	Description string
	Type        model.TicketType
	Priority    model.Priority
	Area        *string
	Tags        []string
	// Goals are the key goals (1-9) the ticket serves; a planned ticket must name at least one.
	Goals []int32
}

// PlannedTicketRequest is a planned ticket as REST and MCP receive it, in strings. Both transports
// used to parse type and priority themselves, which is two places to miss when a field is added
// (TP-117); the jsonschema tags are what MCP shows the agent.
type PlannedTicketRequest struct {
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty" jsonschema:"markdown; say what done looks like"`
	Type        string   `json:"type" jsonschema:"bug | feature | improvement | chore | research | design | refactor (not eval: it is added for you)"`
	Priority    *string  `json:"priority,omitempty" jsonschema:"low | medium | high | urgent"`
	Area        *string  `json:"area,omitempty" jsonschema:"area name from list_areas"`
	Tags        []string `json:"tags,omitempty" jsonschema:"names from list_tags"`
	Goals       []int32  `json:"goals" jsonschema:"the key goals (1-9) this ticket serves; see get_guidance key-goals. Required"`
}

// Parse turns one request into a PlannedTicket. Priority is optional and defaults to medium.
func (r PlannedTicketRequest) Parse() (PlannedTicket, error) {
	ticketType, priority, err := ParseTypeAndPriority(r.Type, r.Priority)
	if err != nil {
		return PlannedTicket{}, err
	}
	return PlannedTicket{Title: r.Title, Description: r.Description, Type: ticketType,
		Priority: priority, Area: r.Area, Tags: r.Tags, Goals: r.Goals}, nil
}

// ParsePlannedTickets parses a whole batch's tickets, keeping their order.
func ParsePlannedTickets(reqs []PlannedTicketRequest) ([]PlannedTicket, error) {
	if len(reqs) == 0 {
		return nil, nil
	}
	tickets := make([]PlannedTicket, 0, len(reqs))
	for _, r := range reqs {
		planned, err := r.Parse()
		if err != nil {
			return nil, err
		}
		tickets = append(tickets, planned)
	}
	return tickets, nil
}

// PlanInput is a batch planned in advance. What, How and Why are the human read of the batch; Demo
// is the check a human runs in under five minutes, and becomes part of the eval ticket.
type PlanInput struct {
	Title     string
	What      string
	How       string
	Why       string
	Demo      string
	RoadmapID *uuid.UUID
	Sequence  *int32
	// BatchID fills an existing roadmap placeholder in place instead of creating a new batch: the
	// placeholder keeps its id, its roadmap sequence and its eval ticket (TP-149).
	BatchID *uuid.UUID
	// GitRef is the batch's integration branch; empty means one is named for it (TP-98).
	GitRef  *string
	Tickets []PlannedTicket
	Actor   string
}

type Plan struct {
	Batch   model.Batch
	Tickets []model.TicketView
}

// PlanBatch creates the batch, its tickets in the order given, and its eval ticket last, in one
// transaction: a half-created plan with no gate is exactly the thing this exists to prevent. With
// BatchID it fills a roadmap placeholder in the same one transaction instead, so filling one is a
// plan and not a pile of findings moved in afterwards (TP-149).
func PlanBatch(ctx context.Context, db DB, project *model.Project, in PlanInput) (*Plan, error) {
	actor := ActorOr(in.Actor, "claude-code")
	title, err := validateTitle(in.Title)
	if err != nil {
		return nil, err
	}
	for _, f := range []struct{ name, value string }{{"what", in.What}, {"how", in.How}, {"why", in.Why}, {"demo", in.Demo}} {
		if strings.TrimSpace(f.value) == "" {
			return nil, apperr.BadRequestf("a planned batch needs %s: the human read is what/how/why, and demo is how they check it", f.name)
		}
	}
	for i, t := range in.Tickets {
		if t.Type == model.TypeEval {
			return nil, apperr.BadRequestf("ticket %d is type eval; plan_batch adds the eval ticket itself", i+1)
		}
	}
	if in.Sequence != nil && in.RoadmapID == nil {
		return nil, apperr.BadRequestf("a sequence only means something inside a roadmap")
	}
	if in.BatchID != nil {
		if in.RoadmapID != nil || in.Sequence != nil {
			return nil, apperr.BadRequestf("a placeholder already has its roadmap and its place in it; give batch on its own")
		}
		if len(in.Tickets) == 0 {
			return nil, apperr.BadRequestf("filling a placeholder needs its tickets; without them there is nothing to fill")
		}
	}

	return inTx(ctx, db, func(tx pgx.Tx) (*Plan, error) {
		if in.BatchID != nil {
			return fillPlaceholder(ctx, tx, project, in, title, actor)
		}
		var sequence *int32
		if in.RoadmapID != nil {
			roadmap, err := GetRoadmap(ctx, tx, project.ID, *in.RoadmapID)
			if err != nil {
				return nil, err
			}
			if roadmap.ArchivedAt != nil {
				return nil, apperr.Conflictf("roadmap %q is archived", roadmap.Title)
			}
			sequence = in.Sequence
			if sequence == nil {
				var next int32
				if err := tx.QueryRow(ctx,
					`SELECT COALESCE(max(sequence), 0) + 1 FROM batches WHERE roadmap_id = $1`,
					roadmap.ID).Scan(&next); err != nil {
					return nil, apperr.FromDB(err)
				}
				sequence = &next
			}
		}

		rows, err := tx.Query(ctx,
			`INSERT INTO batches (project_id, kind, actor, title, brief, what, how, roadmap_id, sequence) `+
				`VALUES ($1, 'plan', $2, $3, $4, $5, $6, $7, $8) RETURNING `+batchColumns,
			project.ID, actor, title, strings.TrimSpace(in.Why), strings.TrimSpace(in.What),
			strings.TrimSpace(in.How), in.RoadmapID, sequence)
		batch, err := collectOne[model.Batch](rows, err)
		if err != nil {
			return nil, err
		}
		return planTail(ctx, tx, project, in, batch, nil, title, actor)
	})
}

// planTail is the tail both planning paths run once the batch row exists (TP-297: they held a copy
// each): shape the plan, resolve the branch — which needs the batch's id or roadmap place, so it
// cannot happen earlier — write the human read, file the tickets, and settle the eval ticket. A new
// batch gets one appended; a placeholder's own is rewritten in place, never replaced: one gate each.
func planTail(ctx context.Context, tx pgx.Tx, project *model.Project, in PlanInput, batch *model.Batch,
	eval *model.Ticket, title, actor string) (*Plan, error) {

	selftest, err := planShape(in.Tickets)
	if err != nil {
		return nil, err
	}
	if batch, err = writeBatchPlan(ctx, tx, batch.ID, title, in, resolveRef(batch, in.GitRef, batch.GitRef)); err != nil {
		return nil, err
	}
	if err := attachStandingGuidance(ctx, tx, project.ID, batch.ID); err != nil {
		return nil, err
	}
	planned, desc := in.Tickets, evalDescription(batch.ID, title, in.Demo, selftest)
	if eval == nil { // an eval ticket is goal 1: a change counts once its evaluation passes
		planned = append(append([]PlannedTicket{}, in.Tickets...), PlannedTicket{Title: evalTitle(title),
			Description: desc, Type: model.TypeEval, Priority: model.PriorityMedium, Goals: []int32{1}})
	}
	if err := writePlannedTickets(ctx, tx, project, batch.ID, planned, actor); err != nil {
		return nil, err
	}
	if eval != nil {
		if _, err := tx.Exec(ctx, `UPDATE tickets SET title = $2, description = $3, batch_position = $4, `+
			`version = version + 1, updated_at = now() WHERE id = $1`,
			eval.ID, evalTitle(title), desc, int32(len(in.Tickets)+1)); err != nil {
			return nil, apperr.FromDB(err)
		}
	}
	views, err := ListBatchTickets(ctx, tx, batch.ID)
	if err != nil {
		return nil, err
	}
	return &Plan{Batch: *batch, Tickets: views}, nil
}

// writeBatchPlan writes the human read of a plan onto the batch row, over whatever a placeholder
// already said. Goal 2: a batch planned from here on is reviewed before it starts and after it ends.
func writeBatchPlan(ctx context.Context, tx pgx.Tx, batchID uuid.UUID, title string, in PlanInput, ref string) (*model.Batch, error) {
	rows, err := tx.Query(ctx,
		`UPDATE batches SET title = $2, brief = $3, what = $4, how = $5, git_ref = $6, reviews_required = true `+
			`WHERE id = $1 RETURNING `+batchColumns,
		batchID, title, strings.TrimSpace(in.Why), strings.TrimSpace(in.What), strings.TrimSpace(in.How), ref)
	return collectOne[model.Batch](rows, err)
}

// resolveRef holds both planning paths to one git_ref chain (TP-170: they had drifted a layer
// apart). The order is the point: a ref someone chose outlives a derived one, so filling a
// placeholder keeps its ref rather than renaming a branch work may be on; a new batch carries none.
func resolveRef(batch *model.Batch, explicit, existing *string) string {
	return cmp.Or(strings.TrimSpace(model.Deref(explicit)), strings.TrimSpace(model.Deref(existing)), BatchBranch(batch))
}

// planShape checks every planned ticket names a key goal and reports whether the batch touches
// infra, which is what decides the eval ticket's selftest line.
func planShape(tickets []PlannedTicket) (selftest bool, err error) {
	for _, t := range tickets {
		// Goal 5: work is chosen by the key goals, so work that serves none is not planned.
		if len(t.Goals) == 0 {
			return false, apperr.BadRequestf("%q names no key goal; every planned ticket says which of goals 1-%d it serves (key-goals guidance)", t.Title, KeyGoals)
		}
		if t.Area != nil && *t.Area == "infra" {
			selftest = true
		}
	}
	return selftest, nil
}

// writePlannedTickets files a plan's tickets into the batch at consecutive positions from `at`. The
// position constraint is deferred, so the order it passes through inside the transaction may collide
// with the placeholder's eval ticket still sitting at position 1.
func writePlannedTickets(ctx context.Context, tx pgx.Tx, project *model.Project, batchID uuid.UUID,
	tickets []PlannedTicket, actor string) error {

	for i, t := range tickets {
		var area *AreaSelector
		if t.Area != nil {
			area = AreaByName(*t.Area)
		}
		priority := t.Priority
		if priority == "" {
			priority = model.PriorityMedium
		}
		// An eval ticket is per batch, so it is fingerprinted by the batch: by title alone, two
		// batches with the same name would dedupe into one gate.
		var fingerprint *string
		if t.Type == model.TypeEval {
			fingerprint = model.Ptr("eval:" + batchID.String())
		}
		outcome, err := CreateTicket(ctx, tx, project, NewTicket{
			Title: t.Title, Description: t.Description, Type: t.Type, Priority: priority,
			Source: model.SourceAI, Area: area, BatchID: &batchID, Fingerprint: fingerprint, Tags: t.Tags, Goals: t.Goals, Actor: actor,
		})
		if err != nil {
			return err
		}
		if !outcome.Created {
			return apperr.Conflictf("%q is already live as %s; move it in with add_to_batch instead",
				t.Title, outcome.View.Ticket.Key)
		}
		if _, err := tx.Exec(ctx, `UPDATE tickets SET batch_position = $2 WHERE id = $1`,
			outcome.View.Ticket.ID, i+1); err != nil {
			return apperr.FromDB(err)
		}
		// The eval ticket is skipped: it carries the gate commands in its description and is judged on
		// having run them all, of which `just gate` is the first line.
		if t.Type != model.TypeEval {
			if err := attachDefaultChecks(ctx, tx, outcome.View.Ticket.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// fillPlaceholder plans into a roadmap placeholder that already exists: the batch a roadmap was laid
// out with, holding a title, a what/how/why and nothing but its eval ticket. It keeps the batch's id,
// its roadmap sequence and its eval ticket, so the roadmap's shape does not move, and it is the same
// one transaction a new plan gets. Before TP-149 this took a report_findings call plus one
// add_to_batch per ticket, which arrived as findings, without the batch's context, non-atomically.
func fillPlaceholder(ctx context.Context, tx pgx.Tx, project *model.Project, in PlanInput, title, actor string) (*Plan, error) {
	batch, current, err := editablePlan(ctx, tx, project.ID, *in.BatchID, false)
	if err != nil {
		return nil, err
	}
	var eval *model.Ticket
	work := 0
	for i := range current {
		if current[i].Type == model.TypeEval {
			eval = &current[i]
			continue
		}
		work++
	}
	if work > 0 {
		return nil, apperr.Conflictf("batch %q already holds %d planned ticket(s), so it is not a placeholder; "+
			"move more work in with add_to_batch", batch.Title, work)
	}
	if started(eval.Status) {
		return nil, apperr.Conflictf("%s, the batch's eval ticket, is %s; this placeholder is already being worked", eval.Key, eval.Status)
	}

	return planTail(ctx, tx, project, in, batch, eval, title, actor)
}

func evalTitle(batchTitle string) string {
	title := "Eval: " + batchTitle
	if r := []rune(title); len(r) > 200 {
		title = string(r[:200])
	}
	return title
}

// evalDescription is the gate every planned batch ends with: the checks that catch breakage and
// drift, plus the demonstration the brief asks for ("demonstrated, not done"). Selftest only runs
// when the batch touches infra, where the evaluation harness lives; elsewhere it is noise.
func evalDescription(batchID uuid.UUID, batchTitle, demo string, selftest bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The gate for %q. This ticket is last in the batch, and the batch cannot finish until it is done.\n\n", batchTitle)
	b.WriteString("**Gate** — run each and put the output in the resolution:\n")
	b.WriteString("- `just lint`\n- `just test`\n- `just guidance-check`\n")
	if selftest {
		b.WriteString("- `just selftest-quick` (this batch touches infra, where the eval harness lives)\n")
	}
	fmt.Fprintf(&b, "\n**Cleanup** — goal 3: the batch leaves the codebase no bigger than it needs to be.\n"+
		"- `./scripts/batch-size.sh %s` — the batch's net lines, recorded so the digest can read them.\n", batchID)
	b.WriteString("- Net above zero **fails this check**, and the batch cannot finish, until a `record_decision` " +
		"on this ticket cites the measurement in `evidence_refs` and says why the growth was needed. " +
		"The evaluator may record it itself, as may the reviewer or the Approver; it names what the batch's brief needed " +
		"the lines for, and growth the brief did not ask for is a finding, not an acceptance. One citing a measurement " +
		"at or above the final net still covers it, since trimming after it only shrank the batch.\n")
	b.WriteString("- `just deadcode` — the dead-code pass: what should have been deleted and was not is a finding.\n")
	b.WriteString("- **Evidence before changing the harness** — if this batch's diff touches `.claude/agents/*` or guidance " +
		"(`docs/guidance/*`, or a PUT to /api/guidance), this check **fails the batch** unless a `record_decision` on this " +
		"ticket cites either the digest's guidance-score line for each revision it changes, or the output of an `eval.sh` A/B " +
		"run on 3 past tickets. A prompt changed on a hunch is a change every later batch runs on (TP-189). " +
		"**Exempt** (Joel, 2026-10-16): an edit that only corrects a document to match the code. Its decision cites the " +
		"code the document now matches instead. **Covered by the plan** (Joel, 2026-10-18): an edit a planned ticket in this " +
		"batch asks for was approved with the plan; its decision cites that ticket. Only an unplanned edit needs the " +
		"evidence above. The evaluator may record either decision itself.\n")
	b.WriteString("\n**Demo** — for a human, under five minutes, no diff reading:\n\n")
	b.WriteString(strings.TrimSpace(demo))
	b.WriteString("\n")
	return b.String()
}

// --- changing a plan ------------------------------------------------------------

// editablePlan locks a plan batch for a change to its membership or order. A finished batch is
// history; changing it would rewrite what its checkpoint signed off.
func editablePlan(ctx context.Context, tx pgx.Tx, projectID, batchID uuid.UUID, removing bool) (*model.Batch, []model.Ticket, error) {
	rows, err := tx.Query(ctx,
		`SELECT `+batchColumns+` FROM batches WHERE project_id = $1 AND id = $2 FOR UPDATE`, projectID, batchID)
	batch, err := collectOne[model.Batch](rows, err)
	if err != nil {
		return nil, nil, err
	}
	if batch == nil {
		return nil, nil, apperr.NotFoundf("no batch %s", batchID)
	}
	if batch.Kind != model.BatchPlan {
		return nil, nil, apperr.Conflictf("only a plan batch has an order; this one is %s", batch.Kind)
	}
	// A finished batch is history, with one exception: a batch finished before rule 16 existed can
	// hold tickets that never started. Taking those out changes nothing anyone signed off, so it is
	// allowed until the batch has a checkpoint.
	if batch.FinishedAt != nil && (!removing || batch.CheckpointDecision != nil) {
		return nil, nil, apperr.Conflictf("batch %q is finished; plan the change as a new batch", batch.Title)
	}
	rows, err = tx.Query(ctx,
		`SELECT `+model.TicketColumns+` FROM tickets WHERE batch_id = $1 ORDER BY batch_position NULLS LAST, number FOR UPDATE`,
		batchID)
	tickets, err := collect[model.Ticket](rows, err)
	if err != nil {
		return nil, nil, err
	}
	return batch, tickets, nil
}

func started(s model.Status) bool {
	return s == model.StatusInProgress || s == model.StatusDone || s == model.StatusClosed
}

// writePositions applies a new order and records each move. The unique constraint on positions is
// deferred, so the intermediate states inside the transaction may collide.
func writePositions(ctx context.Context, tx pgx.Tx, order []model.Ticket, actor string) error {
	for i, t := range order {
		position := int32(i + 1)
		if model.Deref(t.BatchPosition) == position {
			continue
		}
		if _, err := tx.Exec(ctx,
			`UPDATE tickets SET batch_position = $2, version = version + 1, updated_at = now() WHERE id = $1`,
			t.ID, position); err != nil {
			return apperr.FromDB(err)
		}
		var old *string
		if t.BatchPosition != nil {
			old = model.Ptr(strconv.Itoa(int(*t.BatchPosition)))
		}
		if err := recordEvent(ctx, tx, t.ID, actor, "batch_position", old, model.Ptr(strconv.Itoa(int(position)))); err != nil {
			return err
		}
	}
	return nil
}

// checkOrder is the shape every plan keeps: work that has started does not move, and the eval
// ticket stays last.
func checkOrder(after []model.Ticket) error {
	for i, t := range after {
		if t.Type == model.TypeEval && i != len(after)-1 {
			return apperr.Conflictf("%s is the eval ticket and stays last", t.Key)
		}
		if started(t.Status) && model.Deref(t.BatchPosition) != int32(i+1) {
			return apperr.Conflictf("%s is %s, so it keeps position %d", t.Key, t.Status, model.Deref(t.BatchPosition))
		}
	}
	return nil
}

// ReorderBatch takes every ticket key in the batch, once, in the new order.
func ReorderBatch(ctx context.Context, db DB, projectID, batchID uuid.UUID, keys []string, actor string) ([]model.TicketView, error) {
	actor = ActorOr(actor, "claude-code")
	return inTx(ctx, db, func(tx pgx.Tx) ([]model.TicketView, error) {
		_, current, err := editablePlan(ctx, tx, projectID, batchID, false)
		if err != nil {
			return nil, err
		}
		byKey := map[string]model.Ticket{}
		for _, t := range current {
			byKey[t.Key] = t
		}
		if len(keys) != len(current) {
			return nil, apperr.BadRequestf("give all %d tickets of the batch, in the new order; got %d", len(current), len(keys))
		}
		after := make([]model.Ticket, 0, len(keys))
		for _, raw := range keys {
			key := strings.ToUpper(strings.TrimSpace(raw))
			t, ok := byKey[key]
			if !ok {
				return nil, apperr.BadRequestf("%s is not in this batch, or is listed twice", raw)
			}
			delete(byKey, key)
			after = append(after, t)
		}
		if err := checkOrder(after); err != nil {
			return nil, err
		}
		if err := writePositions(ctx, tx, after, actor); err != nil {
			return nil, err
		}
		return ListBatchTickets(ctx, tx, batchID)
	})
}

// AddToBatch moves a ticket that belongs to no batch into this one. Without a position it goes just
// before the eval ticket; a position must come after every ticket already started.
func AddToBatch(ctx context.Context, db DB, projectID, batchID uuid.UUID, key string, position *int32, actor string) ([]model.TicketView, error) {
	actor = ActorOr(actor, "claude-code")
	return inTx(ctx, db, func(tx pgx.Tx) ([]model.TicketView, error) {
		_, current, err := editablePlan(ctx, tx, projectID, batchID, false)
		if err != nil {
			return nil, err
		}
		view, err := GetTicket(ctx, tx, projectID, key)
		if err != nil {
			return nil, err
		}
		t := view.Ticket
		switch {
		case t.BatchID != nil && *t.BatchID == batchID:
			return nil, apperr.Conflictf("%s is already in this batch; use reorder_batch to move it", t.Key)
		case t.BatchID != nil:
			return nil, apperr.Conflictf("%s belongs to another batch; remove it from that one first", t.Key)
		case started(t.Status):
			return nil, apperr.Conflictf("%s is %s; only work not yet started can join a plan", t.Key, t.Status)
		}

		// Every plan holds exactly one eval ticket (TP-171), and loose work joins just before it.
		at := len(current) + 1
		for i, c := range current {
			if c.Type == model.TypeEval {
				at = i + 1
				break
			}
		}
		if t.Type == model.TypeEval {
			return nil, apperr.Conflictf("%s is an eval ticket and this batch already has one", t.Key)
		}
		if position != nil {
			if *position < 1 || int(*position) > at {
				return nil, apperr.BadRequestf("position must be between 1 and %d (the eval ticket stays last)", at)
			}
			at = int(*position)
		}
		for i, c := range current {
			if started(c.Status) && i+1 >= at {
				return nil, apperr.Conflictf("%s is already %s at position %d; add after it", c.Key, c.Status, i+1)
			}
		}

		after := append(append(append([]model.Ticket{}, current[:at-1]...), t), current[at-1:]...)
		if _, err := tx.Exec(ctx, `UPDATE tickets SET batch_id = $2 WHERE id = $1`, t.ID, batchID); err != nil {
			return nil, apperr.FromDB(err)
		}
		if err := recordEvent(ctx, tx, t.ID, actor, "batch", nil, model.Ptr(batchID.String())); err != nil {
			return nil, err
		}
		// Joining a plan is joining its gate: a ticket moved in mid-batch is gated like one planned
		// with it (TP-173). An eval ticket is already refused above, so there is no type to skip.
		if err := attachDefaultChecks(ctx, tx, t.ID); err != nil {
			return nil, err
		}
		if err := writePositions(ctx, tx, after, actor); err != nil {
			return nil, err
		}
		return ListBatchTickets(ctx, tx, batchID)
	})
}

// RemoveFromBatch takes a ticket not yet started back out of the plan, closing the gap it leaves.
func RemoveFromBatch(ctx context.Context, db DB, projectID, batchID uuid.UUID, key string, actor string) ([]model.TicketView, error) {
	actor = ActorOr(actor, "claude-code")
	return inTx(ctx, db, func(tx pgx.Tx) ([]model.TicketView, error) {
		batch, current, err := editablePlan(ctx, tx, projectID, batchID, true)
		if err != nil {
			return nil, err
		}
		key = strings.ToUpper(strings.TrimSpace(key))
		idx := -1
		for i, c := range current {
			if c.Key == key {
				idx = i
			}
		}
		if idx < 0 {
			return nil, apperr.NotFoundf("%s is not in this batch", key)
		}
		t := current[idx]
		switch {
		case t.Type == model.TypeEval:
			return nil, apperr.Conflictf("%s is the batch's eval ticket; a plan always ends in one", t.Key)
		case started(t.Status):
			return nil, apperr.Conflictf("%s is %s; started work stays in its batch", t.Key, t.Status)
		}
		if _, err := tx.Exec(ctx,
			`UPDATE tickets SET batch_id = NULL, batch_position = NULL, version = version + 1, updated_at = now() WHERE id = $1`,
			t.ID); err != nil {
			return nil, apperr.FromDB(err)
		}
		if err := recordEvent(ctx, tx, t.ID, actor, "batch", model.Ptr(batchID.String()), nil); err != nil {
			return nil, err
		}
		// In a finished batch the rest keep their recorded positions; a gap orders the same.
		if batch.FinishedAt == nil {
			after := append(append([]model.Ticket{}, current[:idx]...), current[idx+1:]...)
			if err := writePositions(ctx, tx, after, actor); err != nil {
				return nil, err
			}
		}
		return ListBatchTickets(ctx, tx, batchID)
	})
}

// --- the human's two moves ----------------------------------------------------------

// AcceptBatch accepts every triage ticket in the batch at once. It is a human decision and is
// exposed only over GraphQL, like acceptTicket.
func AcceptBatch(ctx context.Context, db DB, projectID, batchID uuid.UUID, actor string) ([]model.TicketView, error) {
	actor = ActorOr(actor, "human")
	return inTx(ctx, db, func(tx pgx.Tx) ([]model.TicketView, error) {
		if _, err := GetBatch(ctx, tx, projectID, batchID); err != nil {
			return nil, err
		}
		rows, err := tx.Query(ctx,
			`SELECT `+model.TicketColumns+` FROM tickets WHERE batch_id = $1 AND status = 'triage' `+
				`ORDER BY batch_position NULLS LAST, number`, batchID)
		pending, err := collect[model.Ticket](rows, err)
		if err != nil {
			return nil, err
		}
		open := model.StatusOpen
		for _, t := range pending {
			if _, err := UpdateTicket(ctx, tx, t.ID, t.Version, TicketPatch{Status: &open, HumanDecision: true}, actor); err != nil {
				return nil, err
			}
		}
		return ListBatchTickets(ctx, tx, batchID)
	})
}

// CheckpointBatch records the human's decision on a finished batch: continue, adjust the batches
// still to come, or stop. Set once; the next batch in the roadmap opens on it (rule 15). A `stop` on
// a batch that never finished retires it instead — see retireBlocked.
func CheckpointBatch(ctx context.Context, db DB, projectID, batchID uuid.UUID, decision model.CheckpointDecision,
	note, actor string) (*model.Batch, error) {

	actor = ActorOr(actor, "human")
	return inTx(ctx, db, func(tx pgx.Tx) (*model.Batch, error) {
		batch, err := GetBatch(ctx, tx, projectID, batchID)
		if err != nil {
			return nil, err
		}
		if batch.CheckpointDecision != nil {
			return nil, apperr.Conflictf("batch %q was already checkpointed %s", batch.Title, *batch.CheckpointDecision)
		}
		if batch.FinishedAt == nil {
			if err := retireBlocked(ctx, tx, batch, decision); err != nil {
				return nil, err
			}
		}
		rows, err := tx.Query(ctx,
			`UPDATE batches SET checkpoint_decision = $2, checkpoint_note = $3, checkpoint_by = $4, checkpoint_at = now(), `+
				`finished_at = COALESCE(finished_at, now()) WHERE id = $1 RETURNING `+batchColumns,
			batchID, string(decision), strings.TrimSpace(note), actor)
		return collectOne[model.Batch](rows, err)
	})
}

// retireBlocked says why an unfinished batch cannot be retired, or nil when it can. Rule 16 lets a
// plan batch finish only through its eval ticket, which leaves a plan nobody ever worked — one a
// stale API created (TP-175), one a replan walked away from — unfinished for good, in the Batches
// view and in every list of live work. Checkpointing it `stop` is the way out: it sets finished_at
// with the decision, so the batch reads stopped rather than in flight, and no digest is computed
// because there is nothing that happened to digest. Started work is refused, because its account is
// the eval, not a retirement.
func retireBlocked(ctx context.Context, tx pgx.Tx, batch *model.Batch, decision model.CheckpointDecision) error {
	if decision != model.CheckpointStop {
		return apperr.Conflictf("batch %q has not finished; its checkpoint comes after its eval, and only "+
			"%s retires a plan nobody started", batch.Title, model.CheckpointStop)
	}
	if batch.Kind != model.BatchPlan {
		return apperr.Conflictf("batch %q is a %s batch, not a plan; finish_batch closes one whenever its "+
			"author says, so there is nothing to retire", batch.Title, batch.Kind)
	}
	rows, err := tx.Query(ctx,
		`SELECT key, status FROM tickets WHERE batch_id = $1 ORDER BY batch_position NULLS LAST, number`, batch.ID)
	if err != nil {
		return apperr.FromDB(err)
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var status model.Status
		if err := rows.Scan(&key, &status); err != nil {
			return apperr.FromDB(err)
		}
		if started(status) {
			return apperr.Conflictf("batch %q has started work (%s is %s); finish it through its eval "+
				"rather than retiring it", batch.Title, key, status)
		}
	}
	return apperr.FromDB(rows.Err())
}

// --- reading a plan ------------------------------------------------------------------

// Progress is what get_batch adds to a plan: the ticket to work next, why the whole batch is held
// (roadmap), and which earlier ticket each waiting one is behind.
type Progress struct {
	Next      *string           `json:"next"`
	LockedBy  string            `json:"locked_by,omitempty"`
	BlockedBy map[string]string `json:"blocked_by"`
}

func BatchProgress(ctx context.Context, db DB, batch *model.Batch, tickets []model.TicketView) (Progress, error) {
	p := Progress{BlockedBy: map[string]string{}}
	reason, err := BatchLockReason(ctx, db, batch.ID)
	if err != nil {
		return p, err
	}
	p.LockedBy = reason
	var first *model.Ticket
	for i := range tickets {
		t := &tickets[i].Ticket
		if t.BatchPosition == nil || t.Status.IsTerminal() {
			continue
		}
		if first == nil {
			first = t
			if reason == "" {
				p.Next = model.Ptr(t.Key)
			}
			continue
		}
		p.BlockedBy[t.Key] = first.Key
	}
	return p, nil
}

// PlanProgress is BatchProgress for a caller holding only the batch id, as a GraphQL field resolver
// does: the id already names the project.
func PlanProgress(ctx context.Context, db DB, batchID uuid.UUID) (*Progress, error) {
	rows, err := db.Query(ctx, `SELECT `+batchColumns+` FROM batches WHERE id = $1`, batchID)
	batch, err := collectOne[model.Batch](rows, err)
	if err != nil {
		return nil, err
	}
	if batch == nil {
		return nil, apperr.NotFoundf("no batch %s", batchID)
	}
	views, err := ListBatchTickets(ctx, db, batchID)
	if err != nil {
		return nil, err
	}
	progress, err := BatchProgress(ctx, db, batch, views)
	return &progress, err
}
