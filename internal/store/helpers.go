package store

import (
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// mapErr turns the two PostgreSQL conditions callers must distinguish into the
// package's sentinel errors, and leaves everything else alone.
//
// Everything else is deliberate. A handler that renders "duplicate" for a dropped
// connection is worse than one that renders "internal", and the way to keep that
// from happening is to translate only what has a meaning.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return fmt.Errorf("%w: %s", ErrConflict, pgErr.ConstraintName)
	}
	return err
}

// Cursor pagination.
//
// The cursor is an opaque encoding of the last row's sort key, not an offset.
// OFFSET on a list that is being written to skips or repeats rows -- on
// /sessions, where accounting inserts constantly, that is a page the operator
// scrolls past without ever seeing.
//
// It is base64 rather than the bare key so that nothing is tempted to construct one:
// the contract's own parameter says "opaque", and a client that parses it is a
// client that breaks when the sort changes.
func encodeCursor(key string) *string {
	if key == "" {
		return nil
	}
	s := base64.RawURLEncoding.EncodeToString([]byte(key))
	return &s
}

func decodeCursor(cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", fmt.Errorf("store: malformed cursor")
	}
	return string(raw), nil
}

// clampLimit keeps a caller-supplied page size inside the contract's bounds. The
// contract's Limit parameter is 1..200 with a default of 50.
func clampLimit(limit int) int {
	switch {
	case limit <= 0:
		return 50
	case limit > 200:
		return 200
	default:
		return limit
	}
}

// ListOptions is the shape every paginated list takes.
type ListOptions struct {
	SiteID string
	Query  string
	Cursor string
	Limit  int
}

func ptr[T any](v T) *T { return &v }
