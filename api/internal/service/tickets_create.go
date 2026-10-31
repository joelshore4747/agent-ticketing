package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/joelshore/ticketing/api/internal/apperr"
	"github.com/joelshore/ticketing/api/internal/model"
)

type NewLocation struct {
	FilePath  string
	LineStart *int32
	LineEnd   *int32
	Snippet   *string
}

type NewTicket struct {
	Title       string
	Description string
	Type        model.TicketType
	Priority    model.Priority
	Source      model.Source
	Area        *AreaSelector
	Branch      *string
	Fingerprint *string
	Locations   []NewLocation
	BatchID     *uuid.UUID
	// Tags are names from the project's managed list; an unknown one is a 400 naming the list.
	Tags []string
	// Goals are the key goals (1-9) the ticket serves.
	Goals []int32
	Actor string
}

// CreateOutcome reports whether the ticket was inserted; Created == false means it was deduped.
type CreateOutcome struct {
	View    model.TicketView
	Created bool
}

// ParseTypeAndPriority reads a new ticket's required type and optional priority, defaulting it to
// medium. REST createTicket, MCP report_findings and PlannedTicketRequest.Parse each wrote it out
// until TP-312 — the fourth copy of the block TP-311 folded into ParsePatchEnums for patches.
func ParseTypeAndPriority(rawType string, rawPriority *string) (model.TicketType, model.Priority, error) {
	ticketType, err := model.ParseTicketType(rawType)
	if err != nil {
		return "", "", err
	}
	priority := model.PriorityMedium
	if rawPriority != nil {
		if priority, err = model.ParsePriority(*rawPriority); err != nil {
			return "", "", err
		}
	}
	return ticketType, priority, nil
}

// autoRoute is rule 7: first matching non-archived area wins, most specific (longest) glob first.
func autoRoute(ctx context.Context, db DB, projectID uuid.UUID, locations []NewLocation) (*model.Area, error) {
	if len(locations) == 0 {
		return nil, nil
	}
	rows, err := db.Query(ctx,
		`SELECT `+areaColumns+` FROM areas a WHERE a.project_id = $1 AND a.archived_at IS NULL ORDER BY a.name`,
		projectID)
	areas, err := collect[model.Area](rows, err)
	if err != nil {
		return nil, err
	}

	bestLen := -1
	var best *model.Area
	for _, location := range locations {
		for i := range areas {
			for _, pattern := range areas[i].PathGlobs {
				g, err := compileGlob(pattern)
				if err != nil {
					continue
				}
				if !g.Match(location.FilePath) {
					continue
				}
				if len(pattern) > bestLen {
					bestLen = len(pattern)
					best = &areas[i]
				}
			}
		}
	}
	return best, nil
}

// CreateTicket covers rules 1, 2, 3, 6 and 7.
func CreateTicket(ctx context.Context, db DB, project *model.Project, input NewTicket) (*CreateOutcome, error) {
	title, err := validateTitle(input.Title)
	if err != nil {
		return nil, err
	}
	if input.Goals, err = normaliseGoals(input.Goals); err != nil {
		return nil, err
	}
	// Rule 27: before anything is written, including the dedupe merge — a repeat finding that asks
	// for Joel is still the reporter's text to fix.
	if err := checkNeedsJoel(input.Tags, input.Description, title); err != nil {
		return nil, err
	}
	actor := ActorOr(input.Actor, "claude-code")
	locations := make([]NewLocation, 0, len(input.Locations))
	for _, location := range input.Locations {
		path, err := validateFilePath(location.FilePath)
		if err != nil {
			return nil, err
		}
		location.FilePath = path
		locations = append(locations, location)
	}

	tx, err := db.Begin(ctx)
	if err != nil {
		return nil, apperr.FromDB(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var area *model.Area
	if input.Area != nil {
		area, err = resolveArea(ctx, tx, project.ID, input.Area)
	} else {
		area, err = autoRoute(ctx, tx, project.ID, locations)
	}
	if err != nil {
		return nil, err
	}
	tagIDs, err := resolveTags(ctx, tx, project.ID, input.Tags)
	if err != nil {
		return nil, err
	}

	// Rule 3: AI findings always carry a fingerprint; compute one if absent.
	var fingerprint *string
	if given := strings.TrimSpace(model.Deref(input.Fingerprint)); given != "" {
		fingerprint = &given
	} else if input.Source == model.SourceAI {
		var firstPath, areaName *string
		if len(locations) > 0 {
			firstPath = &locations[0].FilePath
		}
		if area != nil {
			areaName = &area.Name
		}
		fingerprint = model.Ptr(ComputeFingerprint(title, firstPath, areaName))
	}

	if fingerprint != nil {
		existing, err := findOpenByFingerprint(ctx, tx, project.ID, *fingerprint)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			return commitMerge(ctx, tx, existing, actor, input.BatchID)
		}
	}

	// Rule 2: initial status.
	status := model.StatusOpen
	if input.Source == model.SourceAI && !(area != nil && area.AutoAcceptAI) {
		status = model.StatusTriage
	}

	var areaID *uuid.UUID
	if area != nil {
		areaID = &area.ID
	}

	// The check above cannot be atomic with the insert: nothing exists yet to lock, so two
	// concurrent creates can both find nothing and both insert. The loser hits
	// tickets_open_fingerprint_uq. A nested Begin is a SAVEPOINT, so when it does we can undo the
	// number allocation (no gap in the sequence) and fall through to the same "seen again" merge
	// instead of failing.
	sp, err := tx.Begin(ctx)
	if err != nil {
		return nil, apperr.FromDB(err)
	}
	ticket, insertErr := insertTicket(ctx, sp, project, input, title, locations, status, areaID, fingerprint, actor)
	if insertErr != nil {
		_ = sp.Rollback(ctx)
		if fingerprint == nil || !apperr.IsUniqueViolation(insertErr) {
			return nil, apperr.FromDB(insertErr)
		}
		existing, err := findOpenByFingerprint(ctx, tx, project.ID, *fingerprint)
		if err != nil {
			return nil, err
		}
		if existing == nil {
			// The conflict was not (or is no longer) a live fingerprint duplicate, so report it
			// as-is rather than inventing a merge.
			return nil, apperr.FromDB(insertErr)
		}
		return commitMerge(ctx, tx, existing, actor, input.BatchID)
	}
	if err := sp.Commit(ctx); err != nil {
		return nil, apperr.FromDB(err)
	}
	if err := tagTicket(ctx, tx, ticket.ID, tagIDs, actor); err != nil {
		return nil, err
	}

	if input.BatchID != nil {
		if err := bumpFindingsCount(ctx, tx, *input.BatchID); err != nil {
			return nil, err
		}
	}

	view, err := hydrateOne(ctx, tx, *ticket)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, apperr.FromDB(err)
	}
	return &CreateOutcome{View: *view, Created: true}, nil
}

// commitMerge folds a repeat finding into the existing ticket and commits, for the two places
// CreateTicket discovers a duplicate.
func commitMerge(ctx context.Context, tx pgx.Tx, existing *model.Ticket, actor string,
	batchID *uuid.UUID) (*CreateOutcome, error) {

	updated, err := mergeSeenAgain(ctx, tx, existing, actor, batchID)
	if err != nil {
		return nil, err
	}
	view, err := hydrateOne(ctx, tx, *updated)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, apperr.FromDB(err)
	}
	return &CreateOutcome{View: *view, Created: false}, nil
}

// findOpenByFingerprint returns the live (non-closed) ticket holding fingerprint, locked for update.
func findOpenByFingerprint(ctx context.Context, db DB, projectID uuid.UUID, fingerprint string) (*model.Ticket, error) {
	rows, err := db.Query(ctx,
		`SELECT `+model.TicketColumns+` FROM tickets WHERE project_id = $1 AND fingerprint = $2 `+
			`AND status NOT IN ('done', 'closed') FOR UPDATE`, projectID, fingerprint)
	return collectOne[model.Ticket](rows, err)
}

// mergeSeenAgain is rule 3: fold a repeat finding into the ticket that already exists.
func mergeSeenAgain(ctx context.Context, db DB, existing *model.Ticket, actor string,
	batchID *uuid.UUID) (*model.Ticket, error) {

	seenCount := existing.SeenCount + 1
	rows, err := db.Query(ctx,
		`UPDATE tickets SET seen_count = $2, last_seen_at = now(), updated_at = now() `+
			`WHERE id = $1 RETURNING `+model.TicketColumns, existing.ID, seenCount)
	updated, err := collectOne[model.Ticket](rows, err)
	if err != nil {
		return nil, err
	}
	if _, err := insertComment(ctx, db, existing.ID, actor,
		fmt.Sprintf("Seen again by %s (%d times)", actor, seenCount), "system"); err != nil {
		return nil, err
	}
	if err := recordEvent(ctx, db, existing.ID, actor, "seen_again",
		model.Ptr(fmt.Sprint(existing.SeenCount)), model.Ptr(fmt.Sprint(seenCount))); err != nil {
		return nil, err
	}
	if batchID != nil {
		if err := bumpFindingsCount(ctx, db, *batchID); err != nil {
			return nil, err
		}
	}
	return updated, nil
}

// insertTicket is rule 1 plus rule 6: allocate the number, insert the row, its locations and its
// `created` event. Returns the raw database error so the caller can tell a fingerprint race from a
// real failure.
func insertTicket(ctx context.Context, db DB, project *model.Project, input NewTicket, title string,
	locations []NewLocation, status model.Status, areaID *uuid.UUID, fingerprint *string,
	actor string) (*model.Ticket, error) {

	// Rule 1: allocate the number inside the same transaction as the insert.
	var number int32
	if err := db.QueryRow(ctx,
		`UPDATE projects SET next_ticket_number = next_ticket_number + 1 WHERE id = $1 `+
			`RETURNING next_ticket_number - 1`, project.ID).Scan(&number); err != nil {
		return nil, err
	}
	key := fmt.Sprintf("%s-%d", project.Key, number)

	rows, err := db.Query(ctx,
		`INSERT INTO tickets (project_id, number, key, title, description, type, priority, `+
			`status, source, area_id, batch_id, fingerprint, branch, goals) `+
			`VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14) `+
			`RETURNING `+model.TicketColumns,
		project.ID, number, key, title, input.Description, string(input.Type), string(input.Priority),
		string(status), string(input.Source), areaID, input.BatchID, fingerprint, input.Branch, model.NonNil(input.Goals))
	if err != nil {
		return nil, err
	}
	tickets, err := pgx.CollectRows(rows, pgx.RowToStructByName[model.Ticket])
	if err != nil {
		return nil, err
	}
	ticket := tickets[0]

	for _, location := range locations {
		if _, err := db.Exec(ctx,
			`INSERT INTO ticket_locations (ticket_id, file_path, line_start, line_end, snippet) `+
				`VALUES ($1, $2, $3, $4, $5)`,
			ticket.ID, location.FilePath, location.LineStart, location.LineEnd, location.Snippet); err != nil {
			return nil, err
		}
	}

	// Rule 6: creation writes field `created`.
	if _, err := db.Exec(ctx,
		`INSERT INTO ticket_events (ticket_id, actor, field, old_value, new_value) `+
			`VALUES ($1, $2, 'created', NULL, $3)`, ticket.ID, actor, key); err != nil {
		return nil, err
	}
	return &ticket, nil
}
