package service

import (
	"context"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/joelshore/ticketing/api/internal/apperr"
	"github.com/joelshore/ticketing/api/internal/model"
)

// Tags are a managed list, for the reason areas are: an agent asked to tag freely invents a synonym
// for a tag that already exists, and a filter on "perf" then misses everything tagged "performance".
// Agents read the list; a human adds to it.

const tagColumns = `id, project_id, name, description, archived_at, created_at`

// starterTags is the list every project starts with. Migrations 0003 and 0011 seeded exactly these
// for the projects that existed when they ran; migrations are history and are not edited, so this
// is the forward copy and CreateProject is what applies it. TestNewProjectGetsTheStarterTags holds
// the two together by comparing a created project against the migrated one — a tag added here
// without a migration for the existing projects, or the reverse, fails there (TP-129: a second
// project started with no tags at all, so plan_batch refused every tag its tickets named).
var starterTags = []struct{ Name, Description string }{
	{"consistency", "Code or docs that disagree with the project's own conventions"},
	{"eval", "Measurement, test gates and the evaluation harness"},
	{"bug-check", "Hunting for defects rather than building"},
	{"architecture", "Structure and direction of the system"},
	{"tech-debt", "Cleanup that makes later work cheaper"},
	{"security", "Anything an attacker could use"},
	{"performance", "Speed, cost and token spend"},
	{"docs-drift", "Documentation that no longer matches the code"},
	// Not a copy of the list: the moves themselves are the "Only Joel" section of the-agents guidance,
	// and this description points at it. The three copies that disagreed with each other are TP-244.
	{needsJoelTag, "Waiting on a move on the Only Joel list (the-agents guidance). The loop stops while any open ticket carries it."},
}

// seedStarterTags gives a new project the starter list in one statement.
func seedStarterTags(ctx context.Context, tx DB, projectID uuid.UUID) error {
	names := make([]string, len(starterTags))
	descriptions := make([]string, len(starterTags))
	for i, t := range starterTags {
		names[i], descriptions[i] = t.Name, t.Description
	}
	_, err := tx.Exec(ctx,
		`INSERT INTO tags (project_id, name, description) `+
			`SELECT $1, * FROM unnest($2::text[], $3::text[])`, projectID, names, descriptions)
	return apperr.FromDB(err)
}

func ListTags(ctx context.Context, db DB, projectID uuid.UUID, includeArchived bool) ([]model.Tag, error) {
	sql := `SELECT ` + tagColumns + ` FROM tags WHERE project_id = $1`
	if !includeArchived {
		sql += ` AND archived_at IS NULL`
	}
	rows, err := db.Query(ctx, sql+` ORDER BY name`, projectID)
	return collect[model.Tag](rows, err)
}

// validateTagName enforces ^[a-z0-9][a-z0-9-]{0,31}$, matching the CHECK constraint.
func validateTagName(raw string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(raw))
	bad := func() error {
		return apperr.BadRequestf("invalid tag %q: 1 to 32 characters, a-z or 0-9 then a-z, 0-9 or -", raw)
	}
	if n := utf8.RuneCountInString(name); n < 1 || n > 32 {
		return "", bad()
	}
	for i, c := range name {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case i > 0 && c == '-':
		default:
			return "", bad()
		}
	}
	return name, nil
}

func CreateTag(ctx context.Context, db DB, projectID uuid.UUID, name, description string) (*model.Tag, error) {
	name, err := validateTagName(name)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(ctx,
		`INSERT INTO tags (project_id, name, description) VALUES ($1, $2, $3) RETURNING `+tagColumns,
		projectID, name, description)
	tag, err := collectOne[model.Tag](rows, err)
	if apperr.Is(err, apperr.Conflict) {
		return nil, apperr.Conflictf("tag %q already exists", name)
	}
	return tag, err
}

// SetTagArchived hides a tag from new tickets without stripping it from the ones that carry it.
func SetTagArchived(ctx context.Context, db DB, projectID, id uuid.UUID, archived bool) (*model.Tag, error) {
	rows, err := db.Query(ctx,
		`UPDATE tags SET archived_at = CASE WHEN $3 THEN COALESCE(archived_at, now()) END `+
			`WHERE project_id = $1 AND id = $2 RETURNING `+tagColumns, projectID, id, archived)
	tag, err := collectOne[model.Tag](rows, err)
	if err != nil {
		return nil, err
	}
	if tag == nil {
		return nil, apperr.NotFoundf("no tag %s", id)
	}
	return tag, nil
}

// resolveTags turns names into ids, refusing any name that is not a live tag in this project and
// naming the ones that are, so the caller's next attempt is not a guess.
func resolveTags(ctx context.Context, db DB, projectID uuid.UUID, names []string) ([]uuid.UUID, error) {
	if len(names) == 0 {
		return nil, nil
	}
	live, err := ListTags(ctx, db, projectID, false)
	if err != nil {
		return nil, err
	}
	byName := map[string]uuid.UUID{}
	known := make([]string, 0, len(live))
	for _, t := range live {
		byName[t.Name] = t.ID
		known = append(known, t.Name)
	}
	seen := map[uuid.UUID]bool{}
	var ids []uuid.UUID
	for _, raw := range names {
		id, ok := byName[strings.ToLower(strings.TrimSpace(raw))]
		if !ok {
			return nil, apperr.BadRequestf("unknown tag %q (tags are a managed list: %s)", raw, strings.Join(known, ", "))
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// tagTicket adds tags to a ticket, recording one event with the full resulting set.
func tagTicket(ctx context.Context, tx DB, ticketID uuid.UUID, tagIDs []uuid.UUID, actor string) error {
	if len(tagIDs) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO ticket_tags (ticket_id, tag_id) SELECT $1, unnest($2::uuid[]) ON CONFLICT DO NOTHING`,
		ticketID, tagIDs); err != nil {
		return apperr.FromDB(err)
	}
	names, err := ticketTags(ctx, tx, []uuid.UUID{ticketID})
	if err != nil {
		return err
	}
	joined := strings.Join(names[ticketID], ", ")
	return recordEvent(ctx, tx, ticketID, actor, "tags", nil, &joined)
}

// ticketTags loads the tag names of many tickets in one query, sorted for stable output.
func ticketTags(ctx context.Context, db DB, ticketIDs []uuid.UUID) (map[uuid.UUID][]string, error) {
	out := map[uuid.UUID][]string{}
	if len(ticketIDs) == 0 {
		return out, nil
	}
	rows, err := db.Query(ctx,
		`SELECT tt.ticket_id, g.name FROM ticket_tags tt JOIN tags g ON g.id = tt.tag_id `+
			`WHERE tt.ticket_id = ANY($1)`, ticketIDs)
	if err != nil {
		return nil, apperr.FromDB(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, apperr.FromDB(err)
		}
		out[id] = append(out[id], name)
	}
	for id := range out {
		sort.Strings(out[id])
	}
	return out, rows.Err()
}

// retagTicket replaces a ticket's tags with the named set, recording the change as one event.
func retagTicket(ctx context.Context, tx DB, ticket *model.Ticket, names []string, actor string) error {
	ids, err := resolveTags(ctx, tx, ticket.ProjectID, names)
	if err != nil {
		return err
	}
	before, err := ticketTags(ctx, tx, []uuid.UUID{ticket.ID})
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM ticket_tags WHERE ticket_id = $1`, ticket.ID); err != nil {
		return apperr.FromDB(err)
	}
	if len(ids) > 0 {
		if _, err := tx.Exec(ctx,
			`INSERT INTO ticket_tags (ticket_id, tag_id) SELECT $1, unnest($2::uuid[])`, ticket.ID, ids); err != nil {
			return apperr.FromDB(err)
		}
	}
	after, err := ticketTags(ctx, tx, []uuid.UUID{ticket.ID})
	if err != nil {
		return err
	}
	old, now := strings.Join(before[ticket.ID], ", "), strings.Join(after[ticket.ID], ", ")
	if old == now {
		return nil
	}
	return recordEvent(ctx, tx, ticket.ID, actor, "tags", &old, &now)
}
