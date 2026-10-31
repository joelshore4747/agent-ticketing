package service

import (
	"fmt"
	"strings"

	"github.com/joelshore/ticketing/api/internal/model"
)

// Branch per batch (TP-37, built in TP-98). A plan batch works on an integration branch: each ticket
// branches from it and merges back into it, and the batch merges into main once when it finishes —
// one reviewable change per batch, with each ticket's reasoning still findable in the history.
//
// The API never runs git. It names the branches and says which commands to run; the agent runs them,
// and scripts/batch-merge.sh does the final merge after checking that every ticket's branch is in.

// slug turns a title into a branch-safe fragment: lowercase words joined by hyphens, cut on a word
// boundary so a branch name stays readable in `git branch`.
func slug(title string, max int) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(title) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > max {
		s = s[:max]
		if i := strings.LastIndexByte(s, '-'); i > 0 {
			s = s[:i]
		}
	}
	return s
}

// BatchBranch is the integration branch a plan batch is given when it is planned: its roadmap place
// when it has one, else the start of its id, then its title.
func BatchBranch(b *model.Batch) string {
	prefix := b.ID.String()[:8]
	if b.Sequence != nil {
		prefix = fmt.Sprintf("%d", *b.Sequence)
	}
	if s := slug(b.Title, 40); s != "" {
		return "batch/" + prefix + "-" + s
	}
	return "batch/" + prefix
}

// TicketBranch is the branch one ticket is worked on.
func TicketBranch(t *model.Ticket) string {
	if s := slug(t.Title, 40); s != "" {
		return "ticket/" + strings.ToLower(t.Key) + "-" + s
	}
	return "ticket/" + strings.ToLower(t.Key)
}

// finishStep names the evidence rule 21 asks of this ticket's type, so the step the agent is handed
// is one the API will accept.
func finishStep(t *model.Ticket) string {
	evidence := `verified_by: "<the command you ran and what it printed>"`
	switch t.Type {
	case model.TypeFeature:
		evidence += `, worked_example: "<one input and what it now produces>"`
	case model.TypeResearch:
		evidence = `evidence_refs: ["<what the findings rest on>"]`
	case model.TypeDesign:
		evidence = "" // done on the human's sign-off of the approach
	}
	if evidence != "" {
		evidence = ", " + evidence
	}
	return fmt.Sprintf(`Finish with update_ticket(key: %q, version: <current>, status: "done", resolution: "<one line>"%s).`, t.Key, evidence)
}

// ClaimSteps are the git commands for working a claimed ticket, then how to finish it.
func ClaimSteps(batch *model.Batch, t *model.Ticket) []string {
	branch := TicketBranch(t)
	var steps []string
	if batch != nil && batch.GitRef != nil && *batch.GitRef != "" {
		base := *batch.GitRef
		steps = append(steps,
			fmt.Sprintf("Branch from the batch branch: git switch %s 2>/dev/null || git switch -c %s main; git switch -c %s", base, base, branch),
			fmt.Sprintf("When the work is committed, merge it back: git switch %s && git merge --no-ff %s", base, branch))
	} else {
		steps = append(steps, fmt.Sprintf("Work on its own branch: git switch -c %s main", branch))
	}
	return append(steps, finishStep(t))
}

// FinishSteps are what to run once a batch has finished: merge its branch into main, after checking
// every ticket branch is in it. Nothing for a batch that has no branch.
func FinishSteps(batch *model.Batch) []string {
	if batch.GitRef == nil || *batch.GitRef == "" || batch.FinishedAt == nil {
		return nil
	}
	return []string{
		fmt.Sprintf("Merge the batch into main: ./scripts/batch-merge.sh %s (it checks every ticket branch is merged into %s first; --dry-run to see the plan).", batch.ID, *batch.GitRef),
		fmt.Sprintf("With a GitHub remote, open it for review instead: gh pr create --base main --head %s --title %q --body-file <(the batch digest)", *batch.GitRef, batch.Title),
	}
}
