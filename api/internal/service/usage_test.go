package service_test

import (
	"testing"

	"github.com/joelshore/ticketing/api/internal/apperr"
	"github.com/joelshore/ticketing/api/internal/model"
	"github.com/joelshore/ticketing/api/internal/service"
)

// TP-101: a session's usage lands on the claim that session made, and a retry does not double it.
func TestUsageLandsOnTheClaimThatDidTheWork(t *testing.T) {
	ctx, pool, project := setup(t)
	tk := mustCreate(t, ctx, pool, project, ticket("measure me", model.SourceHuman, model.PriorityHigh)).View.Ticket
	report := service.UsageRequest{InputTokens: 1200, OutputTokens: 800, CacheCreationInputTokens: 5000, CacheReadInputTokens: 42000,
		CostUSD: model.Ptr(0.37), WallTimeSeconds: 610}

	if _, err := service.RecordUsage(ctx, pool, project.ID, tk.Key, report); !apperr.Is(err, apperr.Conflict) {
		t.Errorf("usage for a ticket never claimed: %v", err)
	}
	if _, err := service.ClaimNextTicket(ctx, pool, project, service.ClaimRequest{Actor: "claude-code", SessionID: "s1"}); err != nil {
		t.Fatal(err)
	}
	got, err := service.RecordUsage(ctx, pool, project.ID, tk.Key, report)
	if err != nil || got.CacheReadInputTokens != 42000 || got.CostUSD == nil || *got.CostUSD != 0.37 || got.WallTimeSeconds != 610 {
		t.Fatalf("usage = %+v (%v)", got, err)
	}
	again, err := service.RecordUsage(ctx, pool, project.ID, tk.Key, report)
	if err != nil || again.InputTokens != 1200 || again.ClaimID != got.ClaimID {
		t.Errorf("a retried report doubled or moved the figures: %+v (%v)", again, err)
	}

	// An older claim from another session can be named; the default is the newest.
	if _, err := pool.Exec(ctx, `INSERT INTO ticket_claims (ticket_id, project_id, actor, session_id, payload, claimed_at) `+
		`VALUES ($1, $2, 'claude-code', 's0', '{}', now() - interval '1 day')`, tk.ID, project.ID); err != nil {
		t.Fatal(err)
	}
	older, err := service.RecordUsage(ctx, pool, project.ID, tk.Key, service.UsageRequest{InputTokens: 7, SessionID: model.Ptr("s0")})
	if err != nil || older.ClaimID == got.ClaimID || older.InputTokens != 7 || older.CostUSD != nil {
		t.Errorf("choosing the s0 claim: %+v (%v)", older, err)
	}
	if _, err := service.RecordUsage(ctx, pool, project.ID, tk.Key, service.UsageRequest{SessionID: model.Ptr("nobody")}); !apperr.Is(err, apperr.Conflict) {
		t.Errorf("an unknown session: %v", err)
	}
	if _, err := service.RecordUsage(ctx, pool, project.ID, tk.Key, service.UsageRequest{OutputTokens: -1}); !apperr.Is(err, apperr.BadRequest) {
		t.Errorf("negative tokens: %v", err)
	}
}
