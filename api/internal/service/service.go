// Package service holds all the business rules. GraphQL, REST and MCP are thin shells over it.
// The rule numbers in the comments are the ones in docs/api.md.
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gobwas/glob"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/joelshore/ticketing/api/internal/apperr"
	"github.com/joelshore/ticketing/api/migrations"
)

const (
	DefaultLimit int64 = 50
	MaxLimit     int64 = 200
	// ClaimTTL: a claim older than this is up for grabs again.
	ClaimTTL = "30 minutes"
)

// priorityRank ranks urgent > high > medium > low.
const priorityRank = `CASE priority WHEN 'urgent' THEN 4 WHEN 'high' THEN 3 WHEN 'medium' THEN 2 ELSE 1 END`

const areaColumns = `a.id, a.project_id, a.name, a.description, a.path_globs, ` +
	`a.auto_accept_ai, a.archived_at, a.created_at, a.updated_at, ` +
	`(SELECT count(*) FROM tickets t WHERE t.area_id = a.id ` +
	`AND t.status IN ('triage', 'open', 'in_progress')) AS open_count`

// DB is whatever we can run a statement against. Both *pgxpool.Pool and pgx.Tx satisfy it, which is
// what lets a function take either the pool or an open transaction — and lets a test hand the whole
// service a transaction it will roll back. A nested Begin on a pgx.Tx is a SAVEPOINT, so the
// fingerprint-race recovery in CreateTicket works the same either way.
type DB interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Begin(ctx context.Context) (pgx.Tx, error)
}

// builder is the equivalent of sqlx's QueryBuilder: it grows a statement and its argument list
// together so a conditional filter can never get out of step with its placeholder number.
type builder struct {
	sb   strings.Builder
	args []any
}

func (b *builder) push(sql string) *builder {
	b.sb.WriteString(sql)
	return b
}

// bind appends the next placeholder and its value.
func (b *builder) bind(v any) *builder {
	b.args = append(b.args, v)
	b.sb.WriteString("$")
	b.sb.WriteString(strconv.Itoa(len(b.args)))
	return b
}

func (b *builder) sql() string { return b.sb.String() }

// --- scanning ---------------------------------------------------------------

func collect[T any](rows pgx.Rows, err error) ([]T, error) {
	if err != nil {
		return nil, apperr.FromDB(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByName[T])
	if err != nil {
		return nil, apperr.FromDB(err)
	}
	return out, nil
}

// collectOne returns nil when there is no row, which callers turn into their own 404 message.
func collectOne[T any](rows pgx.Rows, err error) (*T, error) {
	out, err := collect[T](rows, err)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}
	return &out[0], nil
}

// --- validation -------------------------------------------------------------

func validateTitle(raw string) (string, error) {
	title := strings.TrimSpace(raw)
	if title == "" || utf8.RuneCountInString(title) > 200 {
		return "", apperr.BadRequestf("title must be between 1 and 200 characters")
	}
	return title, nil
}

// validateAreaName enforces ^[a-z0-9][a-z0-9._/-]{0,63}$, spelled out so we do not need a regex.
func validateAreaName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	bad := func() error { return apperr.BadRequestf("invalid area name %q", raw) }
	if name == "" || len(name) > 64 {
		return "", bad()
	}
	for i, c := range name {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case i > 0 && (c == '.' || c == '_' || c == '/' || c == '-'):
		default:
			return "", bad()
		}
	}
	return name, nil
}

// validateProjectKey enforces ^[A-Z][A-Z0-9]{1,9}$, matching the CHECK constraint in
// docs/schema.sql. Spelled out rather than pulled from a regex, the way validateAreaName is.
func validateProjectKey(raw string) (string, error) {
	key := strings.ToUpper(strings.TrimSpace(raw))
	bad := func() error {
		return apperr.BadRequestf("invalid project key %q: 2 to 10 characters, A-Z then A-Z or 0-9", raw)
	}
	if n := utf8.RuneCountInString(key); n < 2 || n > 10 {
		return "", bad()
	}
	for i, c := range key {
		switch {
		case c >= 'A' && c <= 'Z':
		case i > 0 && c >= '0' && c <= '9':
		default:
			return "", bad()
		}
	}
	return key, nil
}

// compileGlob matches file paths, so '/' is a separator: '*' stops at one and '**' crosses them.
func compileGlob(pattern string) (*glob.Pattern, error) {
	return glob.Compile(pattern, '/')
}

// validatePathGlobs: globs must compile, otherwise auto-routing would silently ignore them.
func validatePathGlobs(globs []string) error {
	for _, pattern := range globs {
		if _, err := compileGlob(pattern); err != nil {
			return apperr.BadRequestf("invalid path glob %q: %v", pattern, err)
		}
	}
	return nil
}

// validatePRURL checks an absolute http(s) URL. Deliberately a shape check, not a parser: we only
// need to keep junk out of pull_requests.url.
func validatePRURL(raw string) (string, error) {
	url := strings.TrimSpace(raw)
	bad := func(why string) error {
		return apperr.BadRequestf("invalid pull request url %q: %s", raw, why)
	}
	var rest string
	switch {
	case strings.HasPrefix(url, "https://"):
		rest = strings.TrimPrefix(url, "https://")
	case strings.HasPrefix(url, "http://"):
		rest = strings.TrimPrefix(url, "http://")
	default:
		return "", bad("must start with http:// or https://")
	}
	if rest == "" {
		return "", bad("missing host")
	}
	if len(url) > 2048 {
		return "", bad("must be at most 2048 characters")
	}
	if strings.ContainsFunc(url, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' }) {
		return "", bad("must not contain whitespace")
	}
	return url, nil
}

func validateFilePath(raw string) (string, error) {
	path := strings.TrimSpace(raw)
	bad := func(why string) error { return apperr.BadRequestf("invalid file path %q: %s", raw, why) }
	if path == "" {
		return "", bad("must not be empty")
	}
	if len(path) > 1024 {
		return "", bad("must be at most 1024 characters")
	}
	if strings.ContainsRune(path, 0) {
		return "", bad("must not contain a NUL byte")
	}
	return path, nil
}

// ComputeFingerprint is rule 3: sha256(lowercase(trim(title)) + "|" + first location file_path or
// area name).
func ComputeFingerprint(title string, firstPath, areaName *string) string {
	anchor := ""
	switch {
	case firstPath != nil:
		anchor = *firstPath
	case areaName != nil:
		anchor = *areaName
	}
	h := sha256.New()
	h.Write([]byte(strings.ToLower(strings.TrimSpace(title))))
	h.Write([]byte("|"))
	h.Write([]byte(anchor))
	return hex.EncodeToString(h.Sum(nil))
}

// --- misc -------------------------------------------------------------------

// Actor is who is acting on one request: the name its events are recorded under, and whether the
// credential behind it is a human's. One middleware settles it per request (rule 10) and all three
// shells read it back, so a request has one answer rather than one per transport.
type Actor struct {
	Name         string
	Human        bool
	Credentialed bool // a credentials file answered for this request; without one, Human says nothing
}

type actorKey struct{}

func WithActor(ctx context.Context, actor Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, actor)
}

// ActorFrom returns what the middleware put there. A zero Actor is an unnamed caller in local mode,
// so the shell reading it applies its own default through ActorOr.
func ActorFrom(ctx context.Context) Actor {
	actor, _ := ctx.Value(actorKey{}).(Actor)
	return actor
}

// OnlyJoel refuses a move on the Only Joel list (`the-agents` guidance) to any credential not marked
// human, so the moves that used to be the human's by being GraphQL-only are now his by credential.
// Local mode has no credential to read that from, so there it refuses nothing (rule 10).
func OnlyJoel(ctx context.Context, move string) error {
	if actor := ActorFrom(ctx); actor.Credentialed && !actor.Human {
		return apperr.Forbiddenf("%s is on the Only Joel list in the `the-agents` guidance: it needs a "+
			"credential marked human, and %s's is not", move, actor.Name)
	}
	return nil
}

// ActorOr is rule 10: actors are free strings, with a per-transport default.
func ActorOr(actor, fallback string) string {
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return fallback
	}
	return actor
}

// Health runs SELECT 1 so /healthz fails loudly when the database is gone.
func Health(ctx context.Context, db DB) error {
	var ok int32
	if err := db.QueryRow(ctx, "SELECT 1 AS ok").Scan(&ok); err != nil {
		return apperr.FromDB(err)
	}
	return nil
}

// Version is what the running process is serving, for a caller holding a checkout to compare
// against. Both halves come from the binary itself: the API never runs git (TP-175).
type Version struct {
	Commit    string `json:"commit"`
	Migration string `json:"migration"`
}

// CurrentVersion reads GIT_COMMIT, which scripts/dev.sh sets from `git rev-parse HEAD` at launch.
// Unset is reported as "unknown" rather than guessed, because a wrong commit is worse than no
// commit: it would tell the loop a stale process is current.
func CurrentVersion() Version {
	commit := strings.TrimSpace(os.Getenv("GIT_COMMIT"))
	if commit == "" {
		commit = "unknown"
	}
	return Version{Commit: commit, Migration: migrations.Latest()}
}

// inTx runs fn inside a transaction, committing on success and rolling back on any error. On a
// pgx.Tx this nests as a savepoint, so it composes.
func inTx[T any](ctx context.Context, db DB, fn func(tx pgx.Tx) (T, error)) (T, error) {
	var zero T
	tx, err := db.Begin(ctx)
	if err != nil {
		return zero, apperr.FromDB(err)
	}
	out, err := fn(tx)
	if err != nil {
		_ = tx.Rollback(ctx)
		return zero, err
	}
	if err := tx.Commit(ctx); err != nil {
		return zero, apperr.FromDB(err)
	}
	return out, nil
}

// AreaSelector names an area either by id (GraphQL) or by name (REST/MCP).
type AreaSelector struct {
	ID   *uuid.UUID
	Name *string
}

func AreaByID(id uuid.UUID) *AreaSelector  { return &AreaSelector{ID: &id} }
func AreaByName(name string) *AreaSelector { return &AreaSelector{Name: &name} }
