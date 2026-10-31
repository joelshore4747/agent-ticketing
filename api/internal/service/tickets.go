package service

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/joelshore/ticketing/api/internal/apperr"
	"github.com/joelshore/ticketing/api/internal/model"
)

// Tickets are the largest concern in this package, so they are four files rather than one. This one
// holds what the other three share — the filter, the reads and the hydration behind them, the event
// and comment writers, and pull request links:
//
//	tickets_create.go  CreateTicket, the fingerprint dedupe and the seen-again merge
//	tickets_update.go  TicketPatch, the transition table and UpdateTicket
//	tickets_claim.go   ClaimNextTicket and the claim record
//
// They stay one package because they are one set of rules: dedupe reads what create writes, and a
// claim moves status through the same table an update does.

// --- inputs -----------------------------------------------------------------

type TicketFilter struct {
	Source   *model.Source
	Status   []model.Status
	Type     *model.TicketType
	Area     *AreaSelector
	Priority *model.Priority
	// CreatedAfter and UpdatedAfter are inclusive lower bounds, for "filed today" and "touched this
	// week". Both are instants rather than dates: the caller owns the timezone, so a board in London
	// and one in Sydney can each ask for their own midnight without the server guessing which.
	CreatedAfter *time.Time
	UpdatedAfter *time.Time
	// Tag keeps tickets carrying this tag name.
	Tag *string
}

// TicketFilterInput is a ticket list's filters as they arrive: raw strings, before any enum knows
// about them. Status is split by the transport, the one part it owns: a query string cannot nest a
// list, so REST splits `?status=open,triage` and MCP, handed an array, does not.
type TicketFilterInput struct {
	Status                            []string
	Source, Type, Priority, Area, Tag *string
	CreatedAfter, UpdatedAfter        *string
}

// ParseTicketFilter turns those raw strings into a TicketFilter, refusing with a 400 that names the
// parameter it could not read. REST and MCP each did this by hand before TP-275, which is how a
// filter stayed broken on one transport after being fixed on the other. Limit and offset stay out:
// REST reads them from the query string and MCP's schema types them, so those refuse in different words.
func ParseTicketFilter(in TicketFilterInput) (TicketFilter, error) {
	filter := TicketFilter{Tag: in.Tag}
	for _, raw := range in.Status {
		status, err := model.ParseStatus(raw)
		if err != nil {
			return TicketFilter{}, err
		}
		filter.Status = append(filter.Status, status)
	}
	if in.Area != nil {
		filter.Area = AreaByName(*in.Area)
	}
	// One slot per parameter, checked in order below, so the refusal names the first bad one.
	errs := make([]error, 5)
	filter.Source, errs[0] = optional(in.Source, model.ParseSource)
	filter.Type, errs[1] = optional(in.Type, model.ParseTicketType)
	filter.Priority, errs[2] = optional(in.Priority, model.ParsePriority)
	filter.CreatedAfter, errs[3] = instant(in.CreatedAfter, "created_after")
	filter.UpdatedAfter, errs[4] = instant(in.UpdatedAfter, "updated_after")
	for _, err := range errs {
		if err != nil {
			return TicketFilter{}, err
		}
	}
	return filter, nil
}

// ParsePatchEnums reads a patch's three optional enums into the patch, in that order, so REST
// patchTicket and MCP update_ticket refuse the same bad value identically instead of each keeping
// its own copy of the blocks (TP-311). Area stays in the shells: REST's raw-JSON null and MCP's
// clear_area are different inputs.
func ParsePatchEnums(patch *TicketPatch, ticketType, priority, status *string) error {
	var err error
	if patch.Type, err = optional(ticketType, model.ParseTicketType); err != nil {
		return err
	}
	if patch.Priority, err = optional(priority, model.ParsePriority); err != nil {
		return err
	}
	patch.Status, err = optional(status, model.ParseStatus)
	return err
}

// optional parses a filter parameter that may be absent: nil in, nil out, nothing to refuse.
func optional[T any](raw *string, parse func(string) (T, error)) (*T, error) {
	if raw == nil {
		return nil, nil
	}
	v, err := parse(*raw)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// instant reads an RFC 3339 bound, naming the parameter in the refusal. It was rest.timestampParam
// and mcpserver.parseInstant, the same body in two packages.
func instant(raw *string, name string) (*time.Time, error) {
	ts, err := optional(raw, func(s string) (time.Time, error) { return time.Parse(time.RFC3339, s) })
	if err != nil {
		return nil, apperr.BadRequestf("%s must be an RFC 3339 timestamp", name)
	}
	return ts, nil
}

// --- reads ------------------------------------------------------------------

// hydrateMany attaches area and pr_url to a batch of rows with two extra queries.
func hydrateMany(ctx context.Context, db DB, tickets []model.Ticket) ([]model.TicketView, error) {
	if len(tickets) == 0 {
		return []model.TicketView{}, nil
	}
	var areaIDs []uuid.UUID
	for _, t := range tickets {
		if t.AreaID != nil {
			areaIDs = append(areaIDs, *t.AreaID)
		}
	}
	var areas []model.Area
	if len(areaIDs) > 0 {
		rows, err := db.Query(ctx, `SELECT `+areaColumns+` FROM areas a WHERE a.id = ANY($1)`, areaIDs)
		var qerr error
		areas, qerr = collect[model.Area](rows, err)
		if qerr != nil {
			return nil, qerr
		}
	}

	ticketIDs := make([]uuid.UUID, len(tickets))
	for i, t := range tickets {
		ticketIDs[i] = t.ID
	}
	rows, err := db.Query(ctx,
		`SELECT DISTINCT ON (ticket_id) ticket_id, url FROM pull_requests `+
			`WHERE ticket_id = ANY($1) ORDER BY ticket_id, created_at DESC, id DESC`, ticketIDs)
	if err != nil {
		return nil, apperr.FromDB(err)
	}
	prs := map[uuid.UUID]string{}
	for rows.Next() {
		var id uuid.UUID
		var url string
		if err := rows.Scan(&id, &url); err != nil {
			rows.Close()
			return nil, apperr.FromDB(err)
		}
		prs[id] = url
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, apperr.FromDB(err)
	}

	tags, err := ticketTags(ctx, db, ticketIDs)
	if err != nil {
		return nil, err
	}

	out := make([]model.TicketView, 0, len(tickets))
	for _, ticket := range tickets {
		view := model.TicketView{Ticket: ticket, Tags: tags[ticket.ID]}
		if ticket.AreaID != nil {
			for i := range areas {
				if areas[i].ID == *ticket.AreaID {
					view.Area = &areas[i]
					break
				}
			}
		}
		if url, ok := prs[ticket.ID]; ok {
			view.PRURL = model.Ptr(url)
		}
		out = append(out, view)
	}
	return out, nil
}

func hydrateOne(ctx context.Context, db DB, ticket model.Ticket) (*model.TicketView, error) {
	view := model.TicketView{Ticket: ticket}
	if ticket.AreaID != nil {
		area, err := findArea(ctx, db, ticket.ProjectID, *ticket.AreaID)
		if err != nil {
			return nil, err
		}
		view.Area = area
	}
	var url *string
	err := db.QueryRow(ctx,
		`SELECT url FROM pull_requests WHERE ticket_id = $1 ORDER BY created_at DESC, id DESC LIMIT 1`,
		ticket.ID).Scan(&url)
	if err != nil && !apperr.Is(apperr.FromDB(err), apperr.NotFound) {
		return nil, apperr.FromDB(err)
	}
	view.PRURL = url
	tags, err := ticketTags(ctx, db, []uuid.UUID{ticket.ID})
	if err != nil {
		return nil, err
	}
	view.Tags = tags[ticket.ID]
	return &view, nil
}

func loadTicket(ctx context.Context, db DB, id uuid.UUID) (*model.Ticket, error) {
	rows, err := db.Query(ctx, `SELECT `+model.TicketColumns+` FROM tickets WHERE id = $1`, id)
	ticket, err := collectOne[model.Ticket](rows, err)
	if err != nil {
		return nil, err
	}
	if ticket == nil {
		return nil, apperr.NotFoundf("no ticket %s", id)
	}
	return ticket, nil
}

// GetTicket looks a ticket up by TP-42 key or by bare uuid.
func GetTicket(ctx context.Context, db DB, projectID uuid.UUID, keyOrID string) (*model.TicketView, error) {
	var asUUID *uuid.UUID
	if parsed, err := uuid.Parse(keyOrID); err == nil {
		asUUID = &parsed
	}
	rows, err := db.Query(ctx,
		`SELECT `+model.TicketColumns+` FROM tickets WHERE project_id = $1 AND (key = $2 OR id = $3)`,
		projectID, keyOrID, asUUID)
	ticket, err := collectOne[model.Ticket](rows, err)
	if err != nil || ticket == nil {
		return nil, err
	}
	views, err := hydrateMany(ctx, db, []model.Ticket{*ticket})
	if err != nil {
		return nil, err
	}
	return &views[0], nil
}

func RequireTicket(ctx context.Context, db DB, projectID uuid.UUID, keyOrID string) (*model.TicketView, error) {
	view, err := GetTicket(ctx, db, projectID, keyOrID)
	if err != nil {
		return nil, err
	}
	if view == nil {
		return nil, apperr.NotFoundf("no ticket %s", keyOrID)
	}
	return view, nil
}

// ListTickets returns a filtered, searched, ordered page plus the unpaged total.
func ListTickets(ctx context.Context, db DB, projectID uuid.UUID, filter TicketFilter,
	search string, limit, offset int64) ([]model.TicketView, int64, error) {

	limit = min(max(limit, 1), MaxLimit)
	offset = max(offset, 0)
	search = strings.TrimSpace(search)

	// Resolve an area filter given by name into an id up front.
	var areaID *uuid.UUID
	if filter.Area != nil {
		id, err := resolveAreaFilter(ctx, db, projectID, filter.Area)
		if err != nil {
			return nil, 0, err
		}
		areaID = &id
	}

	where := func(b *builder) {
		b.push(` WHERE project_id = `).bind(projectID)
		if filter.Source != nil {
			b.push(` AND source = `).bind(string(*filter.Source))
		}
		if len(filter.Status) > 0 {
			statuses := make([]string, len(filter.Status))
			for i, s := range filter.Status {
				statuses[i] = string(s)
			}
			b.push(` AND status = ANY(`).bind(statuses).push(`)`)
		}
		if filter.Type != nil {
			b.push(` AND type = `).bind(string(*filter.Type))
		}
		if filter.Priority != nil {
			b.push(` AND priority = `).bind(string(*filter.Priority))
		}
		if areaID != nil {
			b.push(` AND area_id = `).bind(*areaID)
		}
		if filter.CreatedAfter != nil {
			b.push(` AND created_at >= `).bind(*filter.CreatedAfter)
		}
		if filter.UpdatedAfter != nil {
			b.push(` AND updated_at >= `).bind(*filter.UpdatedAfter)
		}
		if filter.Tag != nil {
			b.push(` AND EXISTS (SELECT 1 FROM ticket_tags tt JOIN tags g ON g.id = tt.tag_id ` +
				`WHERE tt.ticket_id = tickets.id AND g.name = `).bind(strings.ToLower(*filter.Tag)).push(`)`)
		}
		if search != "" {
			b.push(` AND (search @@ websearch_to_tsquery('english', `).bind(search).
				push(`) OR key ILIKE '%' || `).bind(search).push(` || '%')`)
		}
	}

	countB := &builder{}
	countB.push(`SELECT count(*) FROM tickets`)
	where(countB)
	var total int64
	if err := db.QueryRow(ctx, countB.sql(), countB.args...).Scan(&total); err != nil {
		return nil, 0, apperr.FromDB(err)
	}

	b := &builder{}
	b.push(`SELECT ` + model.TicketColumns + ` FROM tickets`)
	where(b)
	if search != "" {
		// Rule 9: relevance first when a query is active.
		b.push(` ORDER BY ts_rank(search, websearch_to_tsquery('english', `).bind(search).
			push(`)) DESC, ` + priorityRank + ` DESC, created_at DESC`)
	} else {
		// Rule 11: priority then newest.
		b.push(` ORDER BY ` + priorityRank + ` DESC, created_at DESC`)
	}
	b.push(` LIMIT `).bind(limit).push(` OFFSET `).bind(offset)

	rows, err := db.Query(ctx, b.sql(), b.args...)
	tickets, err := collect[model.Ticket](rows, err)
	if err != nil {
		return nil, 0, err
	}
	views, err := hydrateMany(ctx, db, tickets)
	if err != nil {
		return nil, 0, err
	}
	return views, total, nil
}

func ListComments(ctx context.Context, db DB, ticketID uuid.UUID) ([]model.Comment, error) {
	rows, err := db.Query(ctx,
		`SELECT id, ticket_id, author, body, kind, created_at FROM comments `+
			`WHERE ticket_id = $1 ORDER BY created_at, id`, ticketID)
	return collect[model.Comment](rows, err)
}

func ListEvents(ctx context.Context, db DB, ticketID uuid.UUID) ([]model.TicketEvent, error) {
	rows, err := db.Query(ctx,
		`SELECT id, ticket_id, actor, field, old_value, new_value, created_at FROM ticket_events `+
			`WHERE ticket_id = $1 ORDER BY created_at, id`, ticketID)
	return collect[model.TicketEvent](rows, err)
}

func ListLocations(ctx context.Context, db DB, ticketID uuid.UUID) ([]model.TicketLocation, error) {
	rows, err := db.Query(ctx,
		`SELECT id, ticket_id, file_path, line_start, line_end, snippet, created_at `+
			`FROM ticket_locations WHERE ticket_id = $1 ORDER BY created_at, id`, ticketID)
	return collect[model.TicketLocation](rows, err)
}

func ListPullRequests(ctx context.Context, db DB, ticketID uuid.UUID) ([]model.PullRequest, error) {
	rows, err := db.Query(ctx,
		`SELECT id, ticket_id, url, number, branch, state, created_at, updated_at `+
			`FROM pull_requests WHERE ticket_id = $1 ORDER BY created_at DESC, id DESC`, ticketID)
	return collect[model.PullRequest](rows, err)
}

// --- writes -----------------------------------------------------------------

func recordEvent(ctx context.Context, db DB, ticketID uuid.UUID, actor, field string, old, new *string) error {
	_, err := db.Exec(ctx,
		`INSERT INTO ticket_events (ticket_id, actor, field, old_value, new_value) `+
			`VALUES ($1, $2, $3, $4, $5)`, ticketID, actor, field, old, new)
	return apperr.FromDB(err)
}

func insertComment(ctx context.Context, db DB, ticketID uuid.UUID, author, body, kind string) (*model.Comment, error) {
	rows, err := db.Query(ctx,
		`INSERT INTO comments (ticket_id, author, body, kind) VALUES ($1, $2, $3, $4) `+
			`RETURNING id, ticket_id, author, body, kind, created_at`, ticketID, author, body, kind)
	comment, err := collectOne[model.Comment](rows, err)
	if err != nil {
		return nil, err
	}
	return comment, nil
}

func AddComment(ctx context.Context, db DB, ticketID uuid.UUID, author, body string) (*model.Comment, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, apperr.BadRequestf("comment body must not be empty")
	}
	// Fail with 404 rather than a foreign key error.
	if _, err := loadTicket(ctx, db, ticketID); err != nil {
		return nil, err
	}
	return insertComment(ctx, db, ticketID, author, body, "comment")
}

// --- pull requests ----------------------------------------------------------

func upsertPullRequest(ctx context.Context, db DB, ticketID uuid.UUID, url string, number *int32,
	branch *string, state model.PullRequestState) (*model.PullRequest, error) {

	rows, err := db.Query(ctx,
		`INSERT INTO pull_requests (ticket_id, url, number, branch, state) `+
			`VALUES ($1, $2, $3, $4, $5) `+
			`ON CONFLICT (ticket_id, url) DO UPDATE SET `+
			`  number = COALESCE(EXCLUDED.number, pull_requests.number), `+
			`  branch = COALESCE(EXCLUDED.branch, pull_requests.branch), `+
			`  state = EXCLUDED.state, updated_at = now() `+
			`RETURNING id, ticket_id, url, number, branch, state, created_at, updated_at`,
		ticketID, url, number, branch, string(state))
	return collectOne[model.PullRequest](rows, err)
}

func LinkPullRequest(ctx context.Context, db DB, ticketID uuid.UUID, url string, number *int32,
	branch *string, state model.PullRequestState, actor string) (*model.TicketView, error) {

	validURL, err := validatePRURL(url)
	if err != nil {
		return nil, err
	}
	actor = ActorOr(actor, "claude-code")

	return inTx(ctx, db, func(tx pgx.Tx) (*model.TicketView, error) {
		ticket, err := loadTicket(ctx, tx, ticketID)
		if err != nil {
			return nil, err
		}
		if _, err := upsertPullRequest(ctx, tx, ticketID, validURL, number, branch, state); err != nil {
			return nil, err
		}
		if err := recordEvent(ctx, tx, ticketID, actor, "pr_url", nil, &validURL); err != nil {
			return nil, err
		}
		if branch != nil && *branch != model.Deref(ticket.Branch) {
			rows, qerr := tx.Query(ctx,
				`UPDATE tickets SET branch = $2, updated_at = now() WHERE id = $1 RETURNING `+model.TicketColumns,
				ticketID, *branch)
			updated, err := collectOne[model.Ticket](rows, qerr)
			if err != nil {
				return nil, err
			}
			if err := recordEvent(ctx, tx, ticketID, actor, "branch", ticket.Branch, branch); err != nil {
				return nil, err
			}
			ticket = updated
		}
		return hydrateOne(ctx, tx, *ticket)
	})
}
