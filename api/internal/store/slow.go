package store

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// Slow queries are logged, not stored (TP-45, built in TP-104): timing is observability, and a table
// of it would be one more thing to migrate and prune. The threshold is SLOW_QUERY_MS, default 200.

const defaultSlowQuery = 200 * time.Millisecond

type slowQueryTracer struct {
	threshold time.Duration
	log       *slog.Logger
}

type queryStartKey struct{}

type queryStart struct {
	at  time.Time
	sql string
}

// SlowQueryThreshold reads SLOW_QUERY_MS; anything unparseable or negative falls back to the default.
func SlowQueryThreshold() time.Duration {
	if ms, err := strconv.Atoi(os.Getenv("SLOW_QUERY_MS")); err == nil && ms >= 0 {
		return time.Duration(ms) * time.Millisecond
	}
	return defaultSlowQuery
}

func NewSlowQueryTracer(threshold time.Duration, log *slog.Logger) pgx.QueryTracer {
	return &slowQueryTracer{threshold: threshold, log: log}
}

func (t *slowQueryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, queryStartKey{}, queryStart{at: time.Now(), sql: data.SQL})
}

func (t *slowQueryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	start, ok := ctx.Value(queryStartKey{}).(queryStart)
	if !ok {
		return
	}
	took := time.Since(start.at)
	if took < t.threshold {
		return
	}
	sql := start.sql
	if len(sql) > 120 {
		sql = sql[:120] + "…"
	}
	attrs := []any{"ms", took.Milliseconds(), "sql", sql, "rows", data.CommandTag.RowsAffected()}
	if data.Err != nil {
		attrs = append(attrs, "err", data.Err.Error())
	}
	t.log.WarnContext(ctx, "slow query", attrs...)
}
