package store

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func trace(t *testing.T, threshold time.Duration, sql string, err error) string {
	t.Helper()
	var buf bytes.Buffer
	tracer := NewSlowQueryTracer(threshold, slog.New(slog.NewTextHandler(&buf, nil)))
	ctx := tracer.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: sql})
	tracer.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{CommandTag: pgconn.NewCommandTag("SELECT 3"), Err: err})
	return buf.String()
}

// TP-104: a query over the threshold is logged with its time, its SQL (cut short) and its rows.
func TestASlowQueryIsLogged(t *testing.T) {
	long := "SELECT " + strings.Repeat("x, ", 60) + "y FROM tickets"
	line := trace(t, 0, long, nil)
	for _, want := range []string{`msg="slow query"`, "ms=", "rows=3", "…"} {
		if !strings.Contains(line, want) {
			t.Errorf("log line %q lacks %q", line, want)
		}
	}
	if strings.Contains(line, "y FROM tickets") {
		t.Errorf("the SQL was not cut to 120 characters: %q", line)
	}
	if line := trace(t, 0, "SELECT 1", errors.New("boom")); !strings.Contains(line, "err=boom") {
		t.Errorf("a failed slow query should carry its error: %q", line)
	}
}

func TestAFastQueryIsNotLogged(t *testing.T) {
	if line := trace(t, defaultSlowQuery, "SELECT 1", nil); line != "" {
		t.Errorf("a fast query was logged: %q", line)
	}
}

func TestTheThresholdComesFromTheEnvironment(t *testing.T) {
	t.Setenv("SLOW_QUERY_MS", "50")
	if got := SlowQueryThreshold(); got != 50*time.Millisecond {
		t.Errorf("SLOW_QUERY_MS=50 → %v", got)
	}
	t.Setenv("SLOW_QUERY_MS", "nonsense")
	if got := SlowQueryThreshold(); got != defaultSlowQuery {
		t.Errorf("an unparseable value → %v, want the default", got)
	}
}
