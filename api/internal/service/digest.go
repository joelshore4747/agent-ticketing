package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/joelshore/ticketing/api/internal/model"
)

// The batch digest (TP-43/TP-73): what a finished batch did, in words Joel can read in a minute
// without opening a ticket. Every line is computed from what the tracker already records — the
// batch's what/how/why, its tickets' resolutions, their decisions, and anything still waiting on a
// human — so the digest cannot say more than the record does.

// Scopes for the "waiting on a human" queries: one batch's tickets for the digest, or a whole
// project for the needs-you queue. Each is a WHERE fragment over `t` (tickets) bound to $1.
const (
	scopeBatch   = `t.batch_id = $1`
	scopeProject = `t.project_id = $1`
)

// DecisionOnTicket is a decision with the ticket it is about.
type DecisionOnTicket struct {
	model.Decision
	Key   string `json:"key" db:"key"`
	Title string `json:"title" db:"title"`
}

// PendingProposal is Claude's proposed change to a human-owned field that no one has acted on: its
// field has not changed since. The same rule as the web's pendingProposals.
type PendingProposal struct {
	Key       string    `json:"key" db:"key"`
	Title     string    `json:"title" db:"title"`
	Field     string    `json:"field" db:"field"`
	Proposed  string    `json:"proposed" db:"proposed"`
	Actor     string    `json:"actor" db:"actor"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
}

const decisionOnTicketColumns = `d.id, d.ticket_id, d.kind, d.made_by, d.evidence_refs, d.rationale, ` +
	`d.revisit_trigger, d.supersedes, d.cites_revision_id, d.actor, d.created_at, t.key, t.title`

// decisionsInForce are the decisions in force on the tickets in scope, newest first. onlyUncited
// narrows them in SQL to the ones isUncited picks out, for the callers that want nothing else.
func decisionsInForce(ctx context.Context, db DB, scope string, id uuid.UUID, onlyUncited bool) ([]DecisionOnTicket, error) {
	if onlyUncited {
		scope += ` AND d.made_by = 'ai' AND d.kind <> 'deferred' AND d.evidence_refs = '{}'`
	}
	rows, err := db.Query(ctx,
		`SELECT `+decisionOnTicketColumns+` FROM decisions d JOIN tickets t ON t.decision_id = d.id `+
			`WHERE `+scope+` ORDER BY d.created_at DESC, d.id`, id)
	return collect[DecisionOnTicket](rows, err)
}

// isUncited is that same test in Go, for the digest, which has the decisions in force already.
func isUncited(d DecisionOnTicket) bool {
	return d.MadeBy == MadeByAI && d.Kind != model.DecisionDeferred && len(d.EvidenceRefs) == 0
}

func pendingProposals(ctx context.Context, db DB, scope string, id uuid.UUID) ([]PendingProposal, error) {
	rows, err := db.Query(ctx,
		`SELECT t.key, t.title, replace(e.field, '_proposed', '') AS field, COALESCE(e.new_value, '') AS proposed, `+
			`e.actor, e.created_at FROM ticket_events e JOIN tickets t ON t.id = e.ticket_id `+
			`WHERE `+scope+` AND e.field IN ('intent_proposed', 'constraints_proposed', 'non_goals_proposed', 'criteria_proposed') `+
			`AND NOT EXISTS (SELECT 1 FROM ticket_events later WHERE later.ticket_id = e.ticket_id `+
			`AND later.field = replace(e.field, '_proposed', '') AND later.created_at > e.created_at) `+
			`ORDER BY e.created_at, e.id`, id)
	return collect[PendingProposal](rows, err)
}

// unconfirmedTripwires are tests no human has said the meaning of, or whose confirmation was cleared
// because the test changed. In a batch, only those its tickets' criteria rely on.
func unconfirmedTripwires(ctx context.Context, db DB, scope string, id uuid.UUID) ([]model.Tripwire, error) {
	query := `SELECT ` + tripwireColumns + ` FROM tripwires t WHERE t.project_id = $1 AND confirmed_at IS NULL ORDER BY test_ref`
	if scope == scopeBatch {
		query = `SELECT ` + tripwireColumns + ` FROM tripwires WHERE confirmed_at IS NULL AND id IN (` +
			`SELECT c.tripwire_id FROM ticket_criteria c JOIN tickets t ON t.id = c.ticket_id WHERE ` + scope + `) ` +
			`ORDER BY test_ref`
	}
	rows, err := db.Query(ctx, query, id)
	return collect[model.Tripwire](rows, err)
}

// ComposeDigest renders a batch's digest as markdown. A section with nothing in it is left out; a
// measurement nobody reported says so, and names the command that reports it.
func ComposeDigest(ctx context.Context, db DB, batch *model.Batch) (string, error) {
	tickets, err := ListBatchTickets(ctx, db, batch.ID)
	if err != nil {
		return "", err
	}
	decisions, err := decisionsInForce(ctx, db, scopeBatch, batch.ID, false)
	if err != nil {
		return "", err
	}
	proposals, err := pendingProposals(ctx, db, scopeBatch, batch.ID)
	if err != nil {
		return "", err
	}
	tripwires, err := unconfirmedTripwires(ctx, db, scopeBatch, batch.ID)
	if err != nil {
		return "", err
	}
	verdicts, err := batchVerdicts(ctx, db, batch.ID)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	section := func(title string, lines []string) {
		if len(lines) == 0 {
			return
		}
		fmt.Fprintf(&b, "## %s\n\n%s\n\n", title, strings.Join(lines, "\n"))
	}
	para := func(text string) []string {
		if strings.TrimSpace(text) == "" {
			return nil
		}
		return []string{strings.TrimSpace(text)}
	}
	section("What", para(batch.What))
	section("How", para(batch.How))
	section("Why", para(batch.Brief))
	section("Goals served", goalsServed(tickets))

	var work []string
	finished := 0
	for _, v := range tickets {
		t := v.Ticket
		line := fmt.Sprintf("- **%s** %s — %s", t.Key, oneLine(t.Title), strings.ReplaceAll(string(t.Status), "_", " "))
		if t.Status.IsTerminal() {
			finished++
			if r := oneLine(model.Deref(t.Resolution)); r != "" {
				line += ": " + r
			}
		}
		work = append(work, line)
	}
	if len(work) > 0 {
		work = append([]string{fmt.Sprintf("%d of %d tickets finished.", finished, len(work)), ""}, work...)
		work = append(work, "", verdictLine(verdicts))
		for _, v := range verdicts {
			if v.Verdict == model.VerdictFail {
				work = append(work, fmt.Sprintf("- **%s** criterion %d missed (%s): %s", v.Key, v.Ordinal, oneLine(v.Text), oneLine(v.Reason)))
			}
		}
	}
	section("Did it do what was asked?", work)

	var decided, deferred, uncited []string
	// Accepting from triage is routine; one line for all of them, prepended once the loop has
	// counted them, keeps the decisions that carry a reason readable.
	routine := 0
	for _, d := range decisions {
		if d.MadeBy == MadeByHuman && d.Kind == model.DecisionAccepted && strings.TrimSpace(d.Rationale) == "" {
			routine++
			continue
		}
		if isUncited(d) {
			uncited = append(uncited, fmt.Sprintf("- **%s** Claude's decision (%s) cites no evidence", d.Key, d.Kind))
		}
		who := "Claude Code"
		if d.MadeBy == MadeByHuman {
			who = "you (" + d.Actor + ")"
		}
		line := fmt.Sprintf("- **%s** %s by %s", d.Key, d.Kind, who)
		if r := oneLine(d.Rationale); r != "" {
			line += ": " + r
		}
		if d.Kind == model.DecisionDeferred {
			deferred = append(deferred, line+" — revisit "+oneLine(model.Deref(d.RevisitTrigger)))
			continue
		}
		decided = append(decided, line)
	}
	if routine > 0 {
		decided = append([]string{fmt.Sprintf("- You accepted %d %s from triage as filed.",
			routine, plural(routine, "ticket", "tickets"))}, decided...)
	}
	section("Decided", decided)
	section("Deferred", deferred)

	var open []string
	for _, p := range proposals {
		open = append(open, fmt.Sprintf("- **%s** Claude proposes a change to %s: %q", p.Key, strings.ReplaceAll(p.Field, "_", "-"), oneLine(p.Proposed)))
	}
	open = append(open, uncited...)
	for _, tw := range tripwires {
		open = append(open, fmt.Sprintf("- Tripwire `%s` needs you to confirm what it protects", tw.TestRef))
	}
	section("Open questions for you", open)
	measures, err := measuresSection(ctx, db, batch, verdicts)
	if err != nil {
		return "", err
	}
	section("Measures", measures)
	cost, err := costSection(ctx, db, batch)
	if err != nil {
		return "", err
	}
	section("Cost", cost)
	return strings.TrimSpace(b.String()) + "\n", nil
}

// verdictLine counts the verdicts that count, one per criterion.
func verdictLine(verdicts []latestVerdict) string {
	if len(verdicts) == 0 {
		return "No convergence verdicts were recorded: nobody judged the criteria against the work."
	}
	counts := map[model.Verdict]int{}
	for _, v := range verdicts {
		counts[v.Verdict]++
	}
	return fmt.Sprintf("Convergence verdicts on %d %s: %d pass, %d deviation, %d fail.", len(verdicts),
		plural(len(verdicts), "criterion", "criteria"), counts[model.VerdictPass], counts[model.VerdictDeviation], counts[model.VerdictFail])
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// oneLine keeps a digest line a line: the first line of a multi-line value, trimmed.
func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i]) + " …"
	}
	return s
}

// NeedsYou is everything in a project waiting on a human decision: the human's queue. Each item
// names where the decision is made — a ticket key or a batch id.
type NeedsYou struct {
	Triage      []model.TicketView
	Checkpoints []model.Batch
	Proposals   []PendingProposal
	Uncited     []DecisionOnTicket
	Tripwires   []model.Tripwire
}

func (n *NeedsYou) Total() int {
	return len(n.Triage) + len(n.Checkpoints) + len(n.Proposals) + len(n.Uncited) + len(n.Tripwires)
}

// ListNeedsYou assembles the human's queue from the same queries the digest uses, project-wide.
func ListNeedsYou(ctx context.Context, db DB, projectID uuid.UUID) (*NeedsYou, error) {
	out := &NeedsYou{}
	var err error
	// Triage tickets in a roadmap batch still locked behind earlier ones (a placeholder's eval ticket,
	// say) are not a decision for now: they are accepted when their batch is planned.
	rows, err := db.Query(ctx,
		`SELECT `+model.TicketColumns+` FROM tickets WHERE project_id = $1 AND status = 'triage' `+
			`AND NOT EXISTS (SELECT 1 FROM batches b JOIN batches prev ON prev.roadmap_id = b.roadmap_id AND prev.sequence < b.sequence `+
			`WHERE b.id = tickets.batch_id AND (prev.finished_at IS NULL OR prev.checkpoint_decision IS NULL OR prev.checkpoint_decision = 'stop')) `+
			`ORDER BY `+priorityRank+` DESC, created_at`, projectID)
	triage, err := collect[model.Ticket](rows, err)
	if err != nil {
		return nil, err
	}
	if out.Triage, err = hydrateMany(ctx, db, triage); err != nil {
		return nil, err
	}
	// A roadmap batch is finished but not signed off: the next batch waits on this (rule 15).
	rows, err = db.Query(ctx,
		`SELECT `+batchColumns+` FROM batches WHERE project_id = $1 AND roadmap_id IS NOT NULL `+
			`AND finished_at IS NOT NULL AND checkpoint_decision IS NULL ORDER BY sequence`, projectID)
	if out.Checkpoints, err = collect[model.Batch](rows, err); err != nil {
		return nil, err
	}
	if out.Proposals, err = pendingProposals(ctx, db, scopeProject, projectID); err != nil {
		return nil, err
	}
	if out.Uncited, err = decisionsInForce(ctx, db, scopeProject, projectID, true); err != nil {
		return nil, err
	}
	if out.Tripwires, err = unconfirmedTripwires(ctx, db, scopeProject, projectID); err != nil {
		return nil, err
	}
	return out, nil
}

// goalsServed says which key goals the batch's tickets name, most-served first, so the next batch
// can be chosen by the goals left behind (goal 5).
func goalsServed(tickets []model.TicketView) []string {
	counts := map[int32]int{}
	for _, v := range tickets {
		for _, g := range v.Ticket.Goals {
			counts[g]++
		}
	}
	if len(counts) == 0 {
		return nil
	}
	goals := make([]int32, 0, len(counts))
	for g := range counts {
		goals = append(goals, g)
	}
	sort.Slice(goals, func(i, j int) bool {
		if counts[goals[i]] != counts[goals[j]] {
			return counts[goals[i]] > counts[goals[j]]
		}
		return goals[i] < goals[j]
	})
	parts := make([]string, len(goals))
	for i, g := range goals {
		parts[i] = fmt.Sprintf("goal %d (%d %s)", g, counts[g], plural(counts[g], "ticket", "tickets"))
	}
	var missing []string
	for g := int32(1); g <= KeyGoals; g++ {
		if counts[g] == 0 {
			missing = append(missing, fmt.Sprint(g))
		}
	}
	lines := []string{"Served " + strings.Join(parts, ", ") + "."}
	if len(missing) > 0 {
		lines = append(lines, "Not touched: goals "+strings.Join(missing, ", ")+".")
	}
	return lines
}
