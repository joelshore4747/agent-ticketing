// Package testdb gives each test its own migrated database, which is what sqlx::test did for the
// Rust suite and what Go has no equivalent of.
//
// A template database is created and migrated once per `go test` run; every test then clones it
// with CREATE DATABASE ... TEMPLATE, which Postgres does by copying files rather than replaying
// the migration. Cloning rather than sharing one database in a rolled-back transaction matters for
// the tests that need two real connections at once — the fingerprint races cannot be observed
// inside a single transaction.
package testdb

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/joelshore/ticketing/api/internal/store"
)

const defaultURL = "postgres://ticketing:ticketing@127.0.0.1:5434/ticketing"

// The template is named for this process. `go test ./...` runs one binary per package, in
// parallel, and a shared template would have one process dropping it while another cloned it.
var templateName = fmt.Sprintf("ticketing_test_tmpl_%d", os.Getpid())

var (
	setupOnce sync.Once
	setupErr  error
	counter   atomic.Int64
)

// Main wraps a package's tests so this process's template database is dropped at the end. Every
// test package that calls New needs it:
//
//	func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }
func Main(m *testing.M) int {
	code := m.Run()
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, adminURL())
	if err != nil {
		return code
	}
	defer func() { _ = admin.Close(ctx) }()
	_, _ = admin.Exec(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %q WITH (FORCE)`, templateName))
	return code
}

// New returns a pool onto a fresh database, dropped when the test ends.
func New(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := t.Context()

	setupOnce.Do(func() { setupErr = buildTemplate(ctx) })
	if setupErr != nil {
		t.Fatalf("prepare the test template database: %v", setupErr)
	}

	name := fmt.Sprintf("ticketing_test_%d_%d", os.Getpid(), counter.Add(1))
	admin, err := pgx.Connect(ctx, adminURL())
	if err != nil {
		t.Fatalf("connect as admin: %v", err)
	}
	if _, err := admin.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %q TEMPLATE %q`, name, templateName)); err != nil {
		_ = admin.Close(ctx)
		t.Fatalf("clone the template: %v", err)
	}
	_ = admin.Close(ctx)

	pool, err := store.Connect(ctx, urlFor(name))
	if err != nil {
		t.Fatalf("connect to %s: %v", name, err)
	}

	t.Cleanup(func() {
		pool.Close()
		// A new context: the test's is already cancelled by the time cleanup runs.
		ctx := context.Background()
		admin, err := pgx.Connect(ctx, adminURL())
		if err != nil {
			return
		}
		defer func() { _ = admin.Close(ctx) }()
		_, _ = admin.Exec(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %q WITH (FORCE)`, name))
	})
	return pool
}

func buildTemplate(ctx context.Context) error {
	admin, err := pgx.Connect(ctx, adminURL())
	if err != nil {
		return err
	}
	defer func() { _ = admin.Close(ctx) }()

	if _, err := admin.Exec(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %q WITH (FORCE)`, templateName)); err != nil {
		return err
	}
	if _, err := admin.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %q`, templateName)); err != nil {
		return err
	}

	pool, err := store.Connect(ctx, urlFor(templateName))
	if err != nil {
		return err
	}
	if err := store.Migrate(ctx, pool); err != nil {
		pool.Close()
		return err
	}
	// The template must have no connections left, or cloning it fails.
	pool.Close()
	return nil
}

func baseURL() string {
	if url := os.Getenv("DATABASE_URL"); url != "" {
		return url
	}
	return defaultURL
}

// urlFor swaps the database name in DATABASE_URL, keeping host, port and credentials.
func urlFor(name string) string {
	url := baseURL()
	cut := strings.LastIndex(url, "/")
	if cut < 0 {
		return url
	}
	rest := url[cut+1:]
	if q := strings.Index(rest, "?"); q >= 0 {
		return url[:cut+1] + name + rest[q:]
	}
	return url[:cut+1] + name
}

// adminURL points at `postgres`, which is always there and is never a database under test.
func adminURL() string { return urlFor("postgres") }
