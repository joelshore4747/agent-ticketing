package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/joelshore/ticketing/api/internal/model"
)

// The digest's cost section (TP-45/TP-73, built in TP-103). Tokens lead because they are what the
// session actually used; the dollar figure is the CLI's own estimate and is labelled so. A batch whose
// cache-hit rate falls well below recent batches is flagged, because that is what a changed prompt
// prefix looks like from here.

const (
	// A drop of more than this, in points, against the rolling average is worth a line in the digest.
	cacheDropPoints = 10.0
	// How many earlier finished batches with usage make the rolling average.
	cacheWindow = 5
)

type batchUsage struct {
	Claims    int64    `db:"claims"`
	Reported  int64    `db:"reported"`
	Input     int64    `db:"input"`
	Output    int64    `db:"output"`
	CacheMade int64    `db:"cache_made"`
	CacheRead int64    `db:"cache_read"`
	CostUSD   *float64 `db:"cost_usd"`
	WallSecs  int64    `db:"wall_secs"`
	Unmetered []string `db:"unmetered"` // the claims that reported nothing, by key (TP-296)
}

// hitRate is cache_read / (cache_read + input), the formula eval-report.py already uses. Cache
// writes are left out: they are the price of a hit later, not a miss now.
func (u batchUsage) hitRate() float64 {
	if u.CacheRead+u.Input == 0 {
		return 0
	}
	return float64(u.CacheRead) / float64(u.CacheRead+u.Input)
}

func usageOf(ctx context.Context, db DB, batchID uuid.UUID) (batchUsage, error) {
	var u batchUsage
	rows, err := db.Query(ctx,
		`SELECT count(*) AS claims, count(c.usage_reported_at) AS reported, `+
			`COALESCE(sum(c.input_tokens), 0) AS input, COALESCE(sum(c.output_tokens), 0) AS output, `+
			`COALESCE(sum(c.cache_creation_input_tokens), 0) AS cache_made, COALESCE(sum(c.cache_read_input_tokens), 0) AS cache_read, `+
			`sum(c.cost_usd)::float8 AS cost_usd, COALESCE(sum(c.wall_time_seconds), 0) AS wall_secs, COALESCE(array_agg(t.key ORDER BY t.key) FILTER (WHERE c.usage_reported_at IS NULL), '{}') AS unmetered `+
			`FROM ticket_claims c JOIN tickets t ON t.id = c.ticket_id WHERE t.batch_id = $1`, batchID)
	got, err := collectOne[batchUsage](rows, err)
	if err != nil || got == nil {
		return u, err
	}
	return *got, nil
}

// costSection renders the Cost section's lines for a batch.
func costSection(ctx context.Context, db DB, batch *model.Batch) ([]string, error) {
	u, err := usageOf(ctx, db, batch.ID)
	if err != nil {
		return nil, err
	}
	unmetered := "" // what the figure leaves out: a step run from a supervising session posts none (rule 23, TP-296). A flag, never a refusal.
	if len(u.Unmetered) > 0 {
		unmetered = " No usage reported for " + strings.Join(u.Unmetered, ", ") + "."
	}
	if u.Reported == 0 {
		return []string{fmt.Sprintf("Not measured: no claim in this batch reported usage (%d %s).%s scripts/loop.sh reports "+
			"every step it runs; a hand-worked or subagent session reports itself with scripts/usage-report.sh --key "+
			"(--since/--until bound one step of a shared transcript).", u.Claims, plural(int(u.Claims), "claim", "claims"), unmetered)}, nil
	}
	lines := []string{
		fmt.Sprintf("Tokens: %s input · %s output · %s cache read · %s cache written, from %d of %d %s.",
			thousands(u.Input), thousands(u.Output), thousands(u.CacheRead), thousands(u.CacheMade), u.Reported, u.Claims, plural(int(u.Claims), "claim", "claims")) + unmetered,
		fmt.Sprintf("Cache-hit rate %.1f%% · wall time %s.", 100*u.hitRate(), (time.Duration(u.WallSecs) * time.Second).String()),
	}
	if u.CostUSD != nil {
		lines = append(lines, fmt.Sprintf("About $%.2f by the CLI's own figure — an estimate, not a charge.", *u.CostUSD))
	}
	flag, err := cacheFlag(ctx, db, batch, u.hitRate())
	if err != nil {
		return nil, err
	}
	if flag != "" {
		lines = append(lines, "", flag)
	}
	return lines, nil
}

// cacheFlag compares the batch's hit rate with the last few finished batches that reported usage,
// and names any guidance revised while the batch ran: the likeliest reason a cached prefix changed.
func cacheFlag(ctx context.Context, db DB, batch *model.Batch, rate float64) (string, error) {
	rows, err := db.Query(ctx,
		`SELECT b.id FROM batches b WHERE b.project_id = $1 AND b.id <> $2 AND b.finished_at IS NOT NULL `+
			`AND b.finished_at < COALESCE($3, now()) AND EXISTS (SELECT 1 FROM ticket_claims c JOIN tickets t ON t.id = c.ticket_id `+
			`WHERE t.batch_id = b.id AND c.usage_reported_at IS NOT NULL) ORDER BY b.finished_at DESC LIMIT `+fmt.Sprint(cacheWindow),
		batch.ProjectID, batch.ID, batch.FinishedAt)
	earlier, err := collect[struct {
		ID uuid.UUID `db:"id"`
	}](rows, err)
	if err != nil || len(earlier) == 0 {
		return "", err
	}
	var sum float64
	for _, e := range earlier {
		u, err := usageOf(ctx, db, e.ID)
		if err != nil {
			return "", err
		}
		sum += u.hitRate()
	}
	avg := sum / float64(len(earlier))
	drop := 100 * (avg - rate)
	if drop <= cacheDropPoints {
		return "", nil
	}
	rows, err = db.Query(ctx,
		`SELECT d.slug || ' r' || r.revision AS name FROM guidance_revisions r JOIN guidance_documents d ON d.id = r.document_id `+
			`WHERE d.project_id = $1 AND r.created_at >= $2 AND r.created_at <= COALESCE($3, now()) ORDER BY r.created_at`,
		batch.ProjectID, batch.StartedAt, batch.FinishedAt)
	revised, err := collect[struct {
		Name string `db:"name"`
	}](rows, err)
	if err != nil {
		return "", err
	}
	line := fmt.Sprintf("**Cache-hit rate dropped**: %.1f%% is %.0f points below the last %d %s' %.1f%%.",
		100*rate, drop, len(earlier), plural(len(earlier), "batch", "batches"), 100*avg)
	if len(revised) == 0 {
		return line + " No guidance changed while it ran, so the prefix changed somewhere else.", nil
	}
	names := make([]string, len(revised))
	for i, r := range revised {
		names[i] = r.Name
	}
	return line + " Guidance changed while it ran (" + strings.Join(names, ", ") + "): a changed prompt prefix is the usual cause.", nil
}

// thousands writes 1234567 as 1,234,567.
func thousands(n int64) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
