package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/joelshore/ticketing/api/internal/apperr"
	"github.com/joelshore/ticketing/api/internal/model"
)

// The digest's Measures section (goal 9; TP-160). Size is reported in, not measured here: the API
// never runs git and never opens repo_path (rule 22), so scripts/batch-size.sh posts the counts and
// a batch nobody measured says so.

// howManyPrevious batches of pass rate go beside this one's, so a trend is visible without the
// section becoming a table.
const howManyPrevious = 3

// SizeRequest is what scripts/batch-size.sh posts: the counts it read from `git diff --numstat` and
// the commit it took them against.
type SizeRequest struct {
	// LinesAdded and LinesDeleted are the whole diff, which is what every figure recorded before
	// TP-212 means.
	LinesAdded   int32 `json:"lines_added"`
	LinesDeleted int32 `json:"lines_deleted"`
	// How much of that diff was test files, split by path in scripts/batch-size.sh (TP-212). Nil when
	// the caller did not split it; zero when it split and the batch touched no test.
	TestLinesAdded   *int32  `json:"test_lines_added,omitempty"`
	TestLinesDeleted *int32  `json:"test_lines_deleted,omitempty"`
	BaseCommit       *string `json:"base_commit,omitempty"`
}

// ReportBatchSize records how big a batch's change was. Reporting again replaces the figures: a
// re-run after more commits must not add to them.
func ReportBatchSize(ctx context.Context, db DB, projectID, batchID uuid.UUID, in SizeRequest) (*model.Batch, error) {
	if in.LinesAdded < 0 || in.LinesDeleted < 0 {
		return nil, apperr.BadRequestf("lines_added and lines_deleted cannot be negative")
	}
	if (in.TestLinesAdded == nil) != (in.TestLinesDeleted == nil) {
		return nil, apperr.BadRequestf("test_lines_added and test_lines_deleted come together or not at all")
	}
	if in.TestLinesAdded != nil {
		testAdded, testDeleted := *in.TestLinesAdded, *in.TestLinesDeleted
		if testAdded < 0 || testDeleted < 0 {
			return nil, apperr.BadRequestf("test_lines_added and test_lines_deleted cannot be negative")
		}
		// The test lines are part of the diff they were split out of, so a half bigger than the whole
		// is a bad post — and it would render as negative code, which reads as a deletion.
		if testAdded > in.LinesAdded || testDeleted > in.LinesDeleted {
			return nil, apperr.BadRequestf(
				"test lines (+%d / -%d) cannot exceed the diff they were counted from (+%d / -%d)",
				testAdded, testDeleted, in.LinesAdded, in.LinesDeleted)
		}
	}
	base := strings.TrimSpace(model.Deref(in.BaseCommit))
	if base != "" && !isCommitish(base) {
		return nil, apperr.BadRequestf("%q is not a commit: 7 to 40 lowercase hex digits", base)
	}
	var commit *string
	if base != "" {
		commit = &base
	}
	rows, err := db.Query(ctx,
		`UPDATE batches SET lines_added = $3, lines_deleted = $4, test_lines_added = $5, `+
			`test_lines_deleted = $6, size_base_commit = $7, size_reported_at = now() `+
			`WHERE id = $1 AND project_id = $2 RETURNING `+batchColumns,
		batchID, projectID, in.LinesAdded, in.LinesDeleted, in.TestLinesAdded, in.TestLinesDeleted, commit)
	batch, err := collectOne[model.Batch](rows, err)
	if err != nil {
		return nil, err
	}
	if batch == nil {
		return nil, apperr.NotFoundf("no batch %s", batchID)
	}
	return batch, nil
}

// isCommitish matches the same shape the schema's CHECK does, so a bad value is a 400 and not a 500.
func isCommitish(s string) bool {
	if len(s) < 7 || len(s) > 40 {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// passCounts are one batch's two recorded pass figures.
type passCounts struct {
	CriteriaPassed, Criteria int
	GatePassed, Finished     int
}

func (p passCounts) criteriaLine() string {
	if p.Criteria == 0 {
		return "no criteria were judged"
	}
	return fmt.Sprintf("%d of %d %s passed (%.0f%%)", p.CriteriaPassed, p.Criteria,
		plural(p.Criteria, "criterion", "criteria"), 100*float64(p.CriteriaPassed)/float64(p.Criteria))
}

// passCountsOf reads a batch's pass figures from the record: the verdicts it is handed (the newest
// per criterion, fetched once per batch by the caller) and gate_passed across the finished tickets.
func passCountsOf(ctx context.Context, db DB, batchID uuid.UUID, verdicts []latestVerdict) (passCounts, error) {
	out := passCounts{Criteria: len(verdicts)}
	for _, v := range verdicts {
		if v.Verdict == model.VerdictPass {
			out.CriteriaPassed++
		}
	}
	rows, err := db.Query(ctx,
		`SELECT count(*) FILTER (WHERE gate_passed) AS passed, count(*) AS finished FROM tickets `+
			`WHERE batch_id = $1 AND status IN ('done', 'closed')`, batchID)
	gate, err := collectOne[struct {
		Passed   int `db:"passed"`
		Finished int `db:"finished"`
	}](rows, err)
	if err != nil {
		return out, err
	}
	if gate != nil {
		out.GatePassed, out.Finished = gate.Passed, gate.Finished
	}
	return out, nil
}

// guidanceScore is how one guidance revision served in a batch fared (TP-188, goals 2 and 9).
// Served less Clean less Reworked is the claims whose ticket never had a gate recorded.
type guidanceScore struct {
	Slug     string `db:"slug"`
	Revision int32  `db:"revision"`
	Served   int    `db:"served"`
	Clean    int    `db:"clean"`
	Reworked int    `db:"reworked"`
}

// guidanceScores scores every revision a claim in the batch carried. A claim was sent back when a
// criterion on its ticket was ever judged anything but pass — every verdict, not just the newest,
// because a criterion that failed once did not pass first try — or when somebody rejected it; it is
// clean when it was not sent back and its gate passed. A ticket with no gate_passed is neither, as
// batches worked before scripts/run-checks.sh have none and rework would be a failure the record
// does not show.
func guidanceScores(ctx context.Context, db DB, batchID uuid.UUID) ([]guidanceScore, error) {
	rows, err := db.Query(ctx,
		`SELECT slug, revision, count(*) AS served, count(*) FILTER (WHERE sent_back) AS reworked, `+
			`count(*) FILTER (WHERE NOT sent_back AND gate) AS clean FROM (`+
			`SELECT g.slug, r.revision, coalesce(t.gate_passed, false) AS gate, `+
			`EXISTS (SELECT 1 FROM ticket_criteria c JOIN criterion_verdicts v ON v.criterion_id = c.id `+
			`WHERE c.ticket_id = t.id AND v.verdict <> 'pass') `+
			`OR EXISTS (SELECT 1 FROM decisions d WHERE d.ticket_id = t.id AND d.kind = 'rejected') AS sent_back `+
			`FROM ticket_claims cl JOIN ticket_claim_documents cd ON cd.claim_id = cl.id `+
			`JOIN guidance_revisions r ON r.id = cd.revision_id `+
			`JOIN guidance_documents g ON g.id = r.document_id `+
			`JOIN tickets t ON t.id = cl.ticket_id WHERE t.batch_id = $1) claims `+
			`GROUP BY slug, revision ORDER BY slug, revision`, batchID)
	return collect[guidanceScore](rows, err)
}

// earlierBatch is a finished batch before this one in the same roadmap.
type earlierBatch struct {
	ID       uuid.UUID `db:"id"`
	Sequence int32     `db:"sequence"`
	Title    string    `db:"title"`
}

// measuresSection renders the Measures section's lines: how big the change was, and how much of it
// was judged to pass, beside the batches before it.
func measuresSection(ctx context.Context, db DB, batch *model.Batch, verdicts []latestVerdict) ([]string, error) {
	lines := []string{sizeLine(batch)}

	here, err := passCountsOf(ctx, db, batch.ID, verdicts)
	if err != nil {
		return nil, err
	}
	rate := "Pass rate: " + here.criteriaLine()
	if here.Finished > 0 {
		rate += fmt.Sprintf(" · the gate passed on %d of %d finished %s", here.GatePassed, here.Finished,
			plural(here.Finished, "ticket", "tickets"))
	}
	lines = append(lines, rate+". No demo run is recorded anywhere, so the gate is the second count.")

	scores, err := guidanceScores(ctx, db, batch.ID)
	if err != nil {
		return nil, err
	}
	if len(scores) > 0 {
		lines = append(lines, "Guidance served, clean first try (the gate passed, every criterion ever "+
			"judged pass) against reworked (a criterion missed, or the ticket rejected):")
		for _, s := range scores {
			line := fmt.Sprintf("- `%s` r%d: %d of %d %s clean, %d reworked", s.Slug, s.Revision,
				s.Clean, s.Served, plural(s.Served, "claim", "claims"), s.Reworked)
			if n := s.Served - s.Clean - s.Reworked; n > 0 {
				line += fmt.Sprintf(", %d with no gate recorded", n)
			}
			lines = append(lines, line+".")
		}
	}

	if batch.RoadmapID == nil || batch.Sequence == nil {
		return lines, nil
	}
	rows, err := db.Query(ctx,
		`SELECT id, sequence, title FROM batches WHERE roadmap_id = $1 AND sequence < $2 `+
			`AND finished_at IS NOT NULL ORDER BY sequence DESC LIMIT `+fmt.Sprint(howManyPrevious),
		*batch.RoadmapID, *batch.Sequence)
	earlier, err := collect[earlierBatch](rows, err)
	if err != nil || len(earlier) == 0 {
		return lines, err
	}
	parts := make([]string, len(earlier))
	for i, e := range earlier {
		was, err := batchVerdicts(ctx, db, e.ID)
		if err != nil {
			return nil, err
		}
		p, err := passCountsOf(ctx, db, e.ID, was)
		if err != nil {
			return nil, err
		}
		parts[i] = fmt.Sprintf("batch %d (%s) %s", e.Sequence, oneLine(e.Title), p.criteriaLine())
	}
	return append(lines, "Before it: "+strings.Join(parts, "; ")+"."), nil
}

// sizeLine is the lines-added-against-deleted figure, or what to run when nobody measured it. Code
// leads and the test figure stands beside it, reported and not charged (TP-212).
func sizeLine(batch *model.Batch) string {
	if batch.LinesAdded == nil || batch.LinesDeleted == nil {
		// The batch's own id would be the friendlier line, but the digest is rendered into a committed
		// screenshot baseline and a uuid that changes per run makes that check fail on nothing.
		return "Size unknown: nobody reported it. scripts/batch-size.sh, run on the batch before it " +
			"finishes, counts its branch and posts the figures; the API never runs git itself."
	}
	added, deleted := int(*batch.LinesAdded), int(*batch.LinesDeleted)
	var line string
	if batch.TestLinesAdded != nil && batch.TestLinesDeleted != nil {
		testAdded, testDeleted := int(*batch.TestLinesAdded), int(*batch.TestLinesDeleted)
		line = fmt.Sprintf("Size: code %s; tests %s. The ceiling counts code only; test lines are "+
			"shown beside it, not charged against it.",
			addedDeleted(added-testAdded, deleted-testDeleted), addedDeleted(testAdded, testDeleted))
	} else {
		line = fmt.Sprintf("Size: %s. Counted before code and tests were split (TP-212), so this net "+
			"charges test lines as code.", addedDeleted(added, deleted))
	}
	if batch.SizeBaseCommit != nil && batch.GitRef != nil {
		line += fmt.Sprintf(" Counted on `%s` against `%s`.", *batch.GitRef, *batch.SizeBaseCommit)
	} else if batch.SizeBaseCommit != nil {
		line += fmt.Sprintf(" Counted against `%s`.", *batch.SizeBaseCommit)
	}
	return line
}

// addedDeleted renders one pair of counts and its net the way the digest reads them.
func addedDeleted(added, deleted int) string {
	net := added - deleted
	return fmt.Sprintf("%s added, %s deleted, net %s%s", thousands(int64(added)), thousands(int64(deleted)),
		sign(net), thousands(int64(max(net, -net))))
}

func sign(n int) string {
	if n < 0 {
		return "-"
	}
	return "+"
}
