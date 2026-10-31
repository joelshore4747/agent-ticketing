package service_test

import (
	"strings"
	"testing"

	"github.com/joelshore/ticketing/api/internal/model"
	"github.com/joelshore/ticketing/api/internal/service"
)

// TP-98: a plan batch names its integration branch, and the steps an agent is handed use it.
func TestAPlanBatchNamesItsBranchAndTheStepsUseIt(t *testing.T) {
	ctx, pool, project := setup(t)

	loose := mustPlan(t, ctx, pool, project, planInput("Order the work: claims & moves!", planned("Refuse a skipped claim")))
	if ref := model.Deref(loose.Batch.GitRef); ref != "batch/"+loose.Batch.ID.String()[:8]+"-order-the-work-claims-moves" {
		t.Errorf("git_ref = %q", ref)
	}
	roadmap, err := service.CreateRoadmap(ctx, pool, project.ID, "Evolution", "spec")
	if err != nil {
		t.Fatal(err)
	}
	in := planInput("A very long batch title that goes on well past forty characters", planned("x"))
	in.RoadmapID = &roadmap.ID
	placed := mustPlan(t, ctx, pool, project, in)
	if ref := model.Deref(placed.Batch.GitRef); ref != "batch/1-a-very-long-batch-title-that-goes-on" {
		t.Errorf("roadmap git_ref = %q (want its place, and cut on a word)", ref)
	}
	custom := planInput("Custom", planned("y"))
	custom.GitRef = model.Ptr("release/2026-10")
	if ref := model.Deref(mustPlan(t, ctx, pool, project, custom).Batch.GitRef); ref != "release/2026-10" {
		t.Errorf("a given git_ref was replaced: %q", ref)
	}

	work := loose.Tickets[0].Ticket
	steps := strings.Join(service.ClaimSteps(&loose.Batch, &work), "\n")
	branch := "ticket/" + strings.ToLower(work.Key) + "-refuse-a-skipped-claim"
	for _, want := range []string{
		"git switch " + *loose.Batch.GitRef + " 2>/dev/null || git switch -c " + *loose.Batch.GitRef + " main",
		"git switch -c " + branch,
		"git merge --no-ff " + branch,
		`verified_by:`, `worked_example:`, // a feature: rule 21's evidence, named up front
	} {
		if !strings.Contains(steps, want) {
			t.Errorf("claim steps lack %q:\n%s", want, steps)
		}
	}
	noBatch := model.Ticket{Key: "TP-9", Title: "Fix it", Type: model.TypeResearch}
	steps = strings.Join(service.ClaimSteps(nil, &noBatch), "\n")
	if !strings.Contains(steps, "git switch -c ticket/tp-9-fix-it main") || !strings.Contains(steps, "evidence_refs") || strings.Contains(steps, "verified_by") {
		t.Errorf("unbatched research steps:\n%s", steps)
	}

	if got := service.FinishSteps(&loose.Batch); got != nil {
		t.Errorf("an unfinished batch was given merge steps: %v", got)
	}
	accepted, err := service.AcceptBatch(ctx, pool, project.ID, loose.Batch.ID, "joel")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range accepted {
		tk := v.Ticket
		move(t, ctx, pool, &tk, model.StatusInProgress, model.StatusDone)
	}
	finished, err := service.FinishBatch(ctx, pool, loose.Batch.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	merge := strings.Join(service.FinishSteps(finished), "\n")
	if !strings.Contains(merge, "./scripts/batch-merge.sh "+finished.ID.String()) || !strings.Contains(merge, "gh pr create --base main --head "+*finished.GitRef) {
		t.Errorf("finish steps:\n%s", merge)
	}
}
