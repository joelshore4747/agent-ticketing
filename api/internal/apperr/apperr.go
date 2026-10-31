// Package apperr is the one error type for the whole API. Each transport renders it its own way:
// REST as {"error": ...} with a status, GraphQL as a message plus extensions.code, MCP as invalid
// params or an internal error.
package apperr

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type Kind int

const (
	// BadRequest is invalid input: 400.
	BadRequest Kind = iota
	// NotFound is a missing row: 404.
	NotFound
	// Conflict is a version conflict, duplicate name or illegal transition: 409.
	Conflict
	// Forbidden is a move the caller's credential may not make: 403.
	Forbidden
	// Internal is anything unexpected: 500.
	Internal
)

type Error struct {
	Kind    Kind
	Message string
}

func (e *Error) Error() string { return e.Message }

func (e *Error) Status() int {
	switch e.Kind {
	case BadRequest:
		return http.StatusBadRequest
	case NotFound:
		return http.StatusNotFound
	case Conflict:
		return http.StatusConflict
	case Forbidden:
		return http.StatusForbidden
	default:
		return http.StatusInternalServerError
	}
}

func (e *Error) Code() string {
	switch e.Kind {
	case BadRequest:
		return "BAD_REQUEST"
	case NotFound:
		return "NOT_FOUND"
	case Conflict:
		return "CONFLICT"
	case Forbidden:
		return "FORBIDDEN"
	default:
		return "INTERNAL"
	}
}

func BadRequestf(format string, args ...any) *Error {
	return &Error{Kind: BadRequest, Message: fmt.Sprintf(format, args...)}
}

func NotFoundf(format string, args ...any) *Error {
	return &Error{Kind: NotFound, Message: fmt.Sprintf(format, args...)}
}

func Conflictf(format string, args ...any) *Error {
	return &Error{Kind: Conflict, Message: fmt.Sprintf(format, args...)}
}

func Forbiddenf(format string, args ...any) *Error {
	return &Error{Kind: Forbidden, Message: fmt.Sprintf(format, args...)}
}

func Internalf(format string, args ...any) *Error {
	return &Error{Kind: Internal, Message: fmt.Sprintf(format, args...)}
}

// Is reports whether err is an *Error of this kind.
func Is(err error, kind Kind) bool {
	var e *Error
	return errors.As(err, &e) && e.Kind == kind
}

// As returns err as an *Error, wrapping anything unrecognised as Internal so every transport has a
// status and a code to render.
func As(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return &Error{Kind: Internal, Message: err.Error()}
}

// Postgres error codes that are the caller's fault rather than ours.
const (
	uniqueViolation     = "23505"
	checkViolation      = "23514"
	foreignKeyViolation = "23503"
)

// FromDB maps a database error onto the right kind. A unique or check violation is a 409/400
// rather than a 500: the caller asked for something the schema forbids.
func FromDB(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return &Error{Kind: NotFound, Message: "not found"}
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case uniqueViolation:
			return &Error{Kind: Conflict, Message: pgErr.Message}
		case checkViolation, foreignKeyViolation:
			return &Error{Kind: BadRequest, Message: pgErr.Message}
		}
	}
	return &Error{Kind: Internal, Message: err.Error()}
}

// IsUniqueViolation reports whether err is Postgres 23505, which create_ticket needs to tell a
// fingerprint race from a real failure.
func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation
}
