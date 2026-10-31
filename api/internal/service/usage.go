package service

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/joelshore/ticketing/api/internal/apperr"
	"github.com/joelshore/ticketing/api/internal/model"
)

// Usage (TP-45/TP-73, built in TP-101): what the session that worked a claim cost. It arrives from
// outside — scripts/loop.sh after each agent step, or scripts/usage-report.sh from a transcript —
// because the API never instruments the agent. Tokens are the real signal; the dollar is an estimate.

// UsageRequest is what a reporter posts. SessionID picks the claim that session made; without it the
// ticket's most recent claim is used.
type UsageRequest struct {
	InputTokens              int64    `json:"input_tokens"`
	OutputTokens             int64    `json:"output_tokens"`
	CacheCreationInputTokens int64    `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64    `json:"cache_read_input_tokens"`
	CostUSD                  *float64 `json:"cost_usd,omitempty"`
	WallTimeSeconds          int32    `json:"wall_time_seconds"`
	SessionID                *string  `json:"session_id,omitempty"`
}

// ClaimUsage is the claim the figures landed on.
type ClaimUsage struct {
	ClaimID                  uuid.UUID `json:"claim_id" db:"id"`
	Actor                    string    `json:"actor" db:"actor"`
	ClaimedAt                time.Time `json:"claimed_at" db:"claimed_at"`
	InputTokens              int64     `json:"input_tokens" db:"input_tokens"`
	OutputTokens             int64     `json:"output_tokens" db:"output_tokens"`
	CacheCreationInputTokens int64     `json:"cache_creation_input_tokens" db:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64     `json:"cache_read_input_tokens" db:"cache_read_input_tokens"`
	CostUSD                  *float64  `json:"cost_usd" db:"cost_usd"`
	WallTimeSeconds          int32     `json:"wall_time_seconds" db:"wall_time_seconds"`
	UsageReportedAt          time.Time `json:"usage_reported_at" db:"usage_reported_at"`
}

// RecordUsage writes a session's figures onto the claim that session made. Reporting again replaces
// them rather than adding: a hook that retries must not double the count.
func RecordUsage(ctx context.Context, db DB, projectID uuid.UUID, key string, u UsageRequest) (*ClaimUsage, error) {
	for name, v := range map[string]int64{"input_tokens": u.InputTokens, "output_tokens": u.OutputTokens,
		"cache_creation_input_tokens": u.CacheCreationInputTokens, "cache_read_input_tokens": u.CacheReadInputTokens,
		"wall_time_seconds": int64(u.WallTimeSeconds)} {
		if v < 0 {
			return nil, apperr.BadRequestf("%s cannot be negative", name)
		}
	}
	if u.CostUSD != nil && *u.CostUSD < 0 {
		return nil, apperr.BadRequestf("cost_usd cannot be negative")
	}
	view, err := RequireTicket(ctx, db, projectID, key)
	if err != nil {
		return nil, err
	}
	pick := `SELECT id FROM ticket_claims WHERE ticket_id = $1 ORDER BY claimed_at DESC LIMIT 1`
	args := []any{view.Ticket.ID}
	if s := strings.TrimSpace(model.Deref(u.SessionID)); s != "" {
		pick = `SELECT id FROM ticket_claims WHERE ticket_id = $1 AND session_id = $2 ORDER BY claimed_at DESC LIMIT 1`
		args = append(args, s)
	}
	var claimID uuid.UUID
	if err := db.QueryRow(ctx, pick, args...).Scan(&claimID); err != nil {
		if isNoRows(err) {
			if len(args) > 1 {
				return nil, apperr.Conflictf("%s has no claim from session %s", view.Ticket.Key, args[1])
			}
			return nil, apperr.Conflictf("%s was never claimed, so there is no session to charge this usage to", view.Ticket.Key)
		}
		return nil, apperr.FromDB(err)
	}
	rows, err := db.Query(ctx,
		`UPDATE ticket_claims SET input_tokens = $2, output_tokens = $3, cache_creation_input_tokens = $4, `+
			`cache_read_input_tokens = $5, cost_usd = $6, wall_time_seconds = $7, usage_reported_at = now() `+
			`WHERE id = $1 RETURNING id, actor, claimed_at, input_tokens, output_tokens, cache_creation_input_tokens, `+
			`cache_read_input_tokens, cost_usd::float8 AS cost_usd, wall_time_seconds, usage_reported_at`,
		claimID, u.InputTokens, u.OutputTokens, u.CacheCreationInputTokens, u.CacheReadInputTokens, u.CostUSD, u.WallTimeSeconds)
	return collectOne[ClaimUsage](rows, err)
}
