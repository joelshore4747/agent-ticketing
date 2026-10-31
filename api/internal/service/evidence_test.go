package service_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/joelshore/ticketing/api/internal/apperr"
	"github.com/joelshore/ticketing/api/internal/model"
	"github.com/joelshore/ticketing/api/internal/service"
)

// startedTicket files a human ticket of the given type and moves it to in_progress.
func startedTicket(t *testing.T, ctx context.Context, pool *pgxpool.Pool, project *model.Project, typ model.TicketType) model.Ticket {
	t.Helper()
	in := ticket("a "+string(typ), model.SourceHuman, model.PriorityMedium)
	in.Type = typ
	tk := mustCreate(t, ctx, pool, project, in).View.Ticket
	started := model.StatusInProgress
	v, err := service.UpdateTicket(ctx, pool, tk.ID, tk.Version, service.TicketPatch{Status: &started}, "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	return v.Ticket
}

// finish tries to move a ticket to done with the given extra fields, as the agent.
func finish(ctx context.Context, pool *pgxpool.Pool, project *model.Project, key string, extra service.TicketPatch) error {
	cur, err := service.GetTicket(ctx, pool, project.ID, key)
	if err != nil {
		return err
	}
	done := model.StatusDone
	extra.Status, extra.Resolution = &done, model.Ptr("it works now")
	_, err = service.UpdateTicket(ctx, pool, cur.Ticket.ID, cur.Ticket.Version, extra, "claude-code")
	return err
}

func refusedFor(t *testing.T, err error, wants ...string) {
	t.Helper()
	if !apperr.Is(err, apperr.BadRequest) {
		t.Fatalf("want a 400 naming %v, got %v", wants, err)
	}
	for _, w := range wants {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("refusal %q does not name %q", err, w)
		}
	}
}

// Rule 21, per type: refused without its evidence, naming all of it; accepted with it.
func TestEachTypeMustShowItsEvidenceToBeDone(t *testing.T) {
	ctx, pool, project := setup(t)

	for _, typ := range []model.TicketType{model.TypeBug, model.TypeChore, model.TypeImprovement, model.TypeEval} {
		tk := startedTicket(t, ctx, pool, project, typ)
		refusedFor(t, finish(ctx, pool, project, tk.Key, service.TicketPatch{}), "verified_by")
		if err := finish(ctx, pool, project, tk.Key, service.TicketPatch{VerifiedBy: model.Ptr("go test ./... — ok")}); err != nil {
			t.Errorf("%s with verified_by: %v", typ, err)
		}
	}

	feature := startedTicket(t, ctx, pool, project, model.TypeFeature)
	refusedFor(t, finish(ctx, pool, project, feature.Key, service.TicketPatch{}), "verified_by", "worked_example")
	if err := finish(ctx, pool, project, feature.Key, service.TicketPatch{
		VerifiedBy: model.Ptr("go test — ok"), WorkedExample: model.Ptr("POST {x} -> 400"),
	}); err != nil {
		t.Errorf("feature with its evidence: %v", err)
	}

	research := startedTicket(t, ctx, pool, project, model.TypeResearch)
	refusedFor(t, finish(ctx, pool, project, research.Key, service.TicketPatch{VerifiedBy: model.Ptr("n/a")}), "evidence_refs")
	if err := finish(ctx, pool, project, research.Key, service.TicketPatch{EvidenceRefs: &[]string{"docs/api.md:70"}}); err != nil {
		t.Errorf("research with citations: %v", err)
	}

	design := startedTicket(t, ctx, pool, project, model.TypeDesign)
	refusedFor(t, finish(ctx, pool, project, design.Key, service.TicketPatch{VerifiedBy: model.Ptr("n/a")}), "approved approach")
	cur, _ := service.GetTicket(ctx, pool, project.ID, design.Key)
	v, err := service.UpdateTicket(ctx, pool, cur.Ticket.ID, cur.Ticket.Version, service.TicketPatch{Approach: model.Ptr("Two tables.")}, "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApproveApproach(ctx, pool, v.Ticket.ID, v.Ticket.Version, "joel"); err != nil {
		t.Fatal(err)
	}
	if err := finish(ctx, pool, project, design.Key, service.TicketPatch{}); err != nil {
		t.Errorf("design after sign-off: %v", err)
	}
	// Editing the approach in the same patch clears the sign-off, so that is not evidence either.
	design2 := startedTicket(t, ctx, pool, project, model.TypeDesign)
	cur, _ = service.GetTicket(ctx, pool, project.ID, design2.Key)
	v, _ = service.UpdateTicket(ctx, pool, cur.Ticket.ID, cur.Ticket.Version, service.TicketPatch{Approach: model.Ptr("One table.")}, "claude-code")
	if _, err := service.ApproveApproach(ctx, pool, v.Ticket.ID, v.Ticket.Version, "joel"); err != nil {
		t.Fatal(err)
	}
	refusedFor(t, finish(ctx, pool, project, design2.Key, service.TicketPatch{Approach: model.Ptr("Three tables.")}), "approved approach")
}

func TestARefactorNeedsAConfirmedTripwireAndHeldOutChecksNeedTheGate(t *testing.T) {
	ctx, pool, project := setup(t)
	evidence := service.TicketPatch{VerifiedBy: model.Ptr("go test ./... — ok")}

	refactor := startedTicket(t, ctx, pool, project, model.TypeRefactor)
	refusedFor(t, finish(ctx, pool, project, refactor.Key, evidence), "confirmed tripwire")
	tw, err := service.RegisterTripwire(ctx, pool, project.ID, "api/x_test.go:TestX", "X holds", "h")
	if err != nil {
		t.Fatal(err)
	}
	cur, _ := service.GetTicket(ctx, pool, project.ID, refactor.Key)
	criteria := []service.CriterionInput{{Tier: model.TierGate, Text: "behaviour unchanged", TripwireID: &tw.ID}}
	if _, err := service.SetTicketFoundation(ctx, pool, cur.Ticket.ID, cur.Ticket.Version, service.Foundation{Criteria: &criteria}, "joel"); err != nil {
		t.Fatal(err)
	}
	refusedFor(t, finish(ctx, pool, project, refactor.Key, evidence), "confirmed tripwire") // linked, not yet confirmed
	if _, err := service.ConfirmTripwire(ctx, pool, project.ID, tw.ID, nil, "joel"); err != nil {
		t.Fatal(err)
	}
	if err := finish(ctx, pool, project, refactor.Key, evidence); err != nil {
		t.Errorf("refactor guarded by a confirmed tripwire: %v", err)
	}

	gated := startedTicket(t, ctx, pool, project, model.TypeBug)
	if _, err := pool.Exec(ctx, `INSERT INTO ticket_checks (ticket_id, ordinal, description, kind, command) VALUES ($1, 1, 'probe', 'command', 'true')`, gated.ID); err != nil {
		t.Fatal(err)
	}
	refusedFor(t, finish(ctx, pool, project, gated.Key, evidence), "held-out gate", "run-checks.sh "+gated.Key)
	if _, err := pool.Exec(ctx, `UPDATE tickets SET gate_passed = false WHERE id = $1`, gated.ID); err != nil {
		t.Fatal(err)
	}
	refusedFor(t, finish(ctx, pool, project, gated.Key, evidence), "held-out gate")
	if _, err := pool.Exec(ctx, `UPDATE tickets SET gate_passed = true WHERE id = $1`, gated.ID); err != nil {
		t.Fatal(err)
	}
	if err := finish(ctx, pool, project, gated.Key, evidence); err != nil {
		t.Errorf("a passing gate: %v", err)
	}
}

// TP-269: the service refuses done without a passing gate, and `scripts/run-checks.sh` is the only
// thing that writes one. That half was shown only by scripts/demo-checks.sh, against a live API;
// here the script runs against the test database, so the two halves are pinned by the same suite.
// What matters is the verdict it records and that a held-out command never reaches its output — the
// script's output can end up pasted into a ticket, where the agent being judged would read it.
func TestRunChecksRecordsTheGateAndNeverPrintsAHeldOutCommand(t *testing.T) {
	ctx, pool, project := setup(t)
	if _, err := exec.LookPath("psql"); err != nil {
		t.Skip("run-checks.sh writes gate_passed with psql, which is not on PATH")
	}
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	tk := startedTicket(t, ctx, pool, project, model.TypeBug)
	if _, err := pool.Exec(ctx, `INSERT INTO ticket_checks (ticket_id, ordinal, description, kind, command, visibility) VALUES
		   ($1, 1, 'the repo builds', 'command', 'test -f Justfile', 'public'),
		   ($1, 2, 'the probe the agent never sees', 'command', 'test -f .no-such-held-out-probe', 'held_out')`, tk.ID); err != nil {
		t.Fatal(err)
	}
	cfg := pool.Config().ConnConfig

	for _, c := range []struct {
		name, fix     string
		wantGate      bool
		wants, absent []string
	}{
		{
			name:     "a failing held-out check is reported, recorded and not spelled out",
			wantGate: false,
			wants: []string{"PASS  1. the repo builds — test -f Justfile exited 0",
				"FAIL  2. the probe the agent never sees — (held out) exited 1", "gate_passed = false"},
			absent: []string{"no-such-held-out-probe"},
		},
		{
			name:     "once the work is right the runner records a passing gate",
			fix:      `UPDATE ticket_checks SET command = 'test -d scripts' WHERE ordinal = 2 AND ticket_id = $1`,
			wantGate: true,
			wants:    []string{"PASS  2. the probe the agent never sees — (held out) exited 0", "gate_passed = true (2 of 2 checks)"},
			absent:   []string{"test -d scripts"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.fix != "" {
				if _, err := pool.Exec(ctx, c.fix, tk.ID); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command(filepath.Join(root, "scripts", "run-checks.sh"), tk.Key)
			cmd.Dir = root
			cmd.Env = append(os.Environ(),
				fmt.Sprintf("PGURL=postgres://%s:%s@%s:%d", cfg.User, cfg.Password, cfg.Host, cfg.Port),
				"DB="+cfg.Database,
				// TP-339 (TP-326): the counts below are this ticket's two checks and nothing else.
				// The script also fails the gate for a STALE guidance document whose source_paths the
				// branch moved (run-checks.sh:70), which is a real rule and not this test's subject:
				// against the live repository it made the case depend on the state of docs/guidance/,
				// so one ticket's drift turned every later ticket's `just test` red — the opposite of
				// run-checks.sh:26, "no ticket ever waits on another's drift". GATE_BASE=HEAD leaves
				// that scan empty by construction (HEAD..HEAD is no commits), whatever the checkout's
				// guidance is doing. The rule itself is table-tested in scripts/test_gate.py.
				"GATE_BASE=HEAD")
			raw, err := cmd.CombinedOutput()
			out := string(raw)
			if (err == nil) != c.wantGate {
				t.Fatalf("run-checks.sh exit = %v, want gate %v:\n%s", err, c.wantGate, out)
			}
			for _, want := range c.wants {
				if !strings.Contains(out, want) {
					t.Errorf("output lacks %q:\n%s", want, out)
				}
			}
			for _, leak := range c.absent {
				if strings.Contains(out, leak) {
					t.Errorf("a held-out command reached the output (%q):\n%s", leak, out)
				}
			}
			got, err := service.GetTicket(ctx, pool, project.ID, tk.Key)
			if err != nil {
				t.Fatal(err)
			}
			if got.Ticket.GatePassed == nil || *got.Ticket.GatePassed != c.wantGate || got.Ticket.GateCheckedAt == nil {
				t.Errorf("gate_passed = %v at %v, want %v recorded", got.Ticket.GatePassed, got.Ticket.GateCheckedAt, c.wantGate)
			}
		})
	}
}

// Won't-do and the human's own judgement are not asked for evidence.
func TestClosingAndTheHumansDoneNeedNoEvidence(t *testing.T) {
	ctx, pool, project := setup(t)
	dropped := startedTicket(t, ctx, pool, project, model.TypeFeature)
	closed := model.StatusClosed
	if _, err := service.UpdateTicket(ctx, pool, dropped.ID, dropped.Version,
		service.TicketPatch{Status: &closed, Resolution: model.Ptr("not needed after all")}, "claude-code"); err != nil {
		t.Errorf("closing as won't-do: %v", err)
	}
	judged := startedTicket(t, ctx, pool, project, model.TypeFeature)
	if _, err := service.TransitionTicket(ctx, pool, judged.ID, judged.Version, model.StatusDone, model.Ptr("looks right to me"), "joel"); err != nil {
		t.Errorf("the human's own done: %v", err)
	}
}
