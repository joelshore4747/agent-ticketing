// Command ticketing-api serves GraphQL (/graphql), REST (/api) and MCP (/mcp) from one binary.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/joelshore/ticketing/api/internal/app"
	"github.com/joelshore/ticketing/api/internal/store"
)

const (
	defaultDatabaseURL = "postgres://ticketing:ticketing@127.0.0.1:5434/ticketing"
	defaultCorsOrigin  = "http://localhost:5173"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	// pgx decodes timestamptz into time.Local, so the process timezone is what decides whether an
	// instant is written "…Z" or "…+01:00". This API only speaks UTC — docs/api.md says RFC 3339
	// and every client compares instants, not strings — so pin it rather than letting the machine's
	// zone leak into the wire format.
	time.Local = time.UTC

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	databaseURL := envOr("DATABASE_URL", defaultDatabaseURL)
	corsOrigin := envOr("CORS_ORIGIN", defaultCorsOrigin)
	port := 8080
	if raw := os.Getenv("PORT"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			port = parsed
		}
	}

	pool, err := store.Connect(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := store.Migrate(ctx, pool); err != nil {
		return err
	}

	// Both checked before the listener opens, so a bad CORS_ORIGIN or an unreadable credentials file
	// fails loudly at startup rather than serving one request wrongly.
	handler, err := app.New(pool, corsOrigin, os.Getenv("TICKETING_TOKENS"))
	if err != nil {
		return err
	}

	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()

	fmt.Printf("ticketing api on http://localhost:%d (graphql /graphql, rest /api, mcp /mcp, health /healthz)\n", port)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
