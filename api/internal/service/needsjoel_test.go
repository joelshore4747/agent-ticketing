package service_test

import (
	"strings"
	"testing"

	"github.com/joelshore/ticketing/api/internal/model"
	"github.com/joelshore/ticketing/api/internal/service"
)

// The section every needs-joel ticket has to carry, in the shape TP-218 already uses.
const whatYouDo = `## What you do (about 3 minutes)
1. **Look at what changed.** Run: git -C ~/WebstormProjects/ticketing show f603f39
2. **What counts as OK:** every check is still there.
3. **If it's OK:** open http://localhost:5173 → **Needs you** → click **Confirm**.
4. **What happens next:** nothing else.
5. **If it's not OK:** comment here saying what looks wrong, and leave it.`

// TP-240 rule 27: a ticket that asks for Joel is refused unless it tells him what to do. TP-216 is
// the finding — it reached his queue saying only "re-read the document and re-pin it".
func TestNeedsJoelTicketIsRefusedWithoutItsSteps(t *testing.T) {
	ctx, pool, project := setup(t)

	bare := ticket("Re-confirm the guard test", model.SourceHuman, model.PriorityMedium)
	bare.Description = "Please re-read go-service-layer against TP-184's diff and re-pin it."
	bare.Tags = []string{"needs-joel"}
	_, err := service.CreateTicket(ctx, pool, project, bare)
	if err == nil {
		t.Fatal("a needs-joel ticket with no What you do section was accepted")
	}
	// The 400 names what is missing, so the next attempt is not a guess.
	for _, want := range []string{"needs-joel", "What you do", "what to click or run"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not mention %q", err, want)
		}
	}

	// The same ticket with the section is accepted.
	good := ticket("Re-confirm the guard test", model.SourceHuman, model.PriorityMedium)
	good.Description = whatYouDo
	good.Tags = []string{"needs-joel"}
	if _, err := service.CreateTicket(ctx, pool, project, good); err != nil {
		t.Fatalf("a needs-joel ticket carrying its steps was refused: %v", err)
	}

	// An untagged ticket is nobody's interruption, so the section is not asked of it.
	untagged := ticket("Ordinary work", model.SourceHuman, model.PriorityMedium)
	untagged.Description = "No steps here."
	if _, err := service.CreateTicket(ctx, pool, project, untagged); err != nil {
		t.Fatalf("an untagged ticket was held to rule 27: %v", err)
	}
}

// report_findings goes through CreateTicket, so a scout cannot file its way past the rule either.
func TestReportedFindingCannotAskForJoelWithoutSteps(t *testing.T) {
	ctx, pool, project := setup(t)

	finding := ticket("Joel must sign in to GitHub", model.SourceAI, model.PriorityHigh)
	finding.Description = "gh is not authenticated."
	finding.Tags = []string{"needs-joel"}
	if _, err := service.ReportFindings(ctx, pool, project, []service.NewTicket{finding}); err == nil {
		t.Fatal("report_findings filed a needs-joel ticket with no What you do section")
	}
}

// The tag can also arrive later, by update; so can a description that drops the section.
func TestUpdateCannotLeaveJoelWithoutSteps(t *testing.T) {
	ctx, pool, project := setup(t)

	created := mustCreate(t, ctx, pool, project,
		ticket("Where should backups live?", model.SourceHuman, model.PriorityMedium)).View.Ticket

	// Adding the tag to a ticket with no steps is refused.
	if _, err := service.UpdateTicket(ctx, pool, created.ID, created.Version,
		service.TicketPatch{Tags: &[]string{"needs-joel"}}, "claude-code"); err == nil {
		t.Fatal("the needs-joel tag was added to a ticket with no What you do section")
	}

	// Adding it together with the steps is the way through.
	tagged, err := service.UpdateTicket(ctx, pool, created.ID, created.Version,
		service.TicketPatch{Tags: &[]string{"needs-joel"}, Description: model.Ptr(whatYouDo)}, "claude-code")
	if err != nil {
		t.Fatalf("tag and steps together were refused: %v", err)
	}

	// Editing the steps back out is refused too.
	if _, err := service.UpdateTicket(ctx, pool, tagged.Ticket.ID, tagged.Ticket.Version,
		service.TicketPatch{Description: model.Ptr("Just re-pin it.")}, "claude-code"); err == nil {
		t.Fatal("the What you do section was edited out of a needs-joel ticket")
	}

	// Dropping the tag is always a way out, steps or no steps.
	untagged, err := service.UpdateTicket(ctx, pool, tagged.Ticket.ID, tagged.Ticket.Version,
		service.TicketPatch{Tags: &[]string{}, Description: model.Ptr("Just re-pin it.")}, "claude-code")
	if err != nil {
		t.Fatalf("dropping the tag was refused: %v", err)
	}
	if len(untagged.Tags) != 0 {
		t.Errorf("tags = %v, want none", untagged.Tags)
	}
}

// A ticket that predates the rule must stay closable: refusing every update to it would trap it in
// Joel's queue, which is the thing rule 27 exists to prevent.
func TestARuleBreakingNeedsJoelTicketCanStillBeClosed(t *testing.T) {
	ctx, pool, project := setup(t)

	created := mustCreate(t, ctx, pool, project,
		ticket("Already done, still waiting", model.SourceHuman, model.PriorityMedium)).View.Ticket
	tagged, err := service.UpdateTicket(ctx, pool, created.ID, created.Version,
		service.TicketPatch{Tags: &[]string{"needs-joel"}, Description: model.Ptr(whatYouDo)}, "claude-code")
	if err != nil {
		t.Fatal(err)
	}

	closed, err := service.UpdateTicket(ctx, pool, tagged.Ticket.ID, tagged.Ticket.Version,
		service.TicketPatch{Status: model.Ptr(model.StatusClosed), Resolution: model.Ptr("TP-219 did it")},
		"claude-code")
	if err != nil {
		t.Fatalf("closing a needs-joel ticket was refused: %v", err)
	}
	if closed.Ticket.Status != model.StatusClosed {
		t.Errorf("status = %s, want closed", closed.Ticket.Status)
	}
}

// What counts as the section, and what does not. The shapes here are the ones the three tickets in
// Joel's queue actually use (TP-218, TP-224, TP-242), so the rule cannot drift away from them.
func TestWhatCountsAsAWhatYouDoSection(t *testing.T) {
	ctx, pool, project := setup(t)

	cases := []struct {
		name, description string
		ok                bool
	}{
		{"a markdown heading with steps", whatYouDo, true},
		{"a bold heading with steps", "**What you do**\n1. Go here.\n2. Click Confirm.", true},
		{"bold-wrapped step numbers (TP-242)",
			"## What you do (two guard files)\n**1. `test_loop.py`.** Look at it.\n**2. `test_gate.py`.** Then this one.", true},
		// TP-242 as filed: a paragraph before the steps, and a bold line that is not a step sitting
		// between two that are. Treating any bold line as the next section refused this.
		{"a bold aside between two steps (TP-242 as filed)",
			"## What you do (about 5 minutes, two guard files)\nBoth guards are whole test files.\n\n" +
				"**1. `scripts/test_guidance_check.py`.** Recent changes:\n\nLook at the newest one.\n\n" +
				"**What counts as OK:** TP-241 cut duplicated tests.\n\n" +
				"**2. `scripts/test_loop.py`.** Recent changes:\n\n**What counts as OK:** none weakened.", true},
		{"steps in parentheses", "### What you do\n1) Open the page.\n2) Click Confirm.", true},
		{"a section further down the description",
			"## Background\nSomething happened.\n\n## What you do\n1. Go here.\n2. Click Confirm.", true},
		{"no section at all", "Please re-pin the document.", false},
		{"the heading but prose instead of steps", "## What you do\nHave a look at the diff and confirm it.", false},
		{"only one step", "## What you do\n1. Have a look.", false},
		{"steps that belong to the next section",
			"## What you do\nHave a look.\n\n## Background\n1. First this.\n2. Then that.", false},
		{"the words in a sentence, not a heading",
			"What you do next is up to you.\n1. Maybe this.\n2. Maybe that.", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := ticket("Needs Joel: "+c.name, model.SourceHuman, model.PriorityMedium)
			in.Description = c.description
			in.Tags = []string{"needs-joel"}
			_, err := service.CreateTicket(ctx, pool, project, in)
			if c.ok && err != nil {
				t.Errorf("refused, want accepted: %v", err)
			}
			if !c.ok && err == nil {
				t.Error("accepted, want refused")
			}
		})
	}
}
