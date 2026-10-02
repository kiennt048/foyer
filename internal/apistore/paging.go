package apistore

import (
	"encoding/base64"
	"encoding/json"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/store"
)

// Paging, and the one thing about it that is not obvious.
//
// Every list port here goes through `fill`, including the ones whose repository
// could answer directly. That is deliberate: the cursor format would otherwise
// change depending on whether a query happened to need post-filtering, so
// `listSessions?state=online` and `listSessions?state=online,ended` would hand a
// client two incompatible cursors for one operation. The contract types the cursor
// `string` and says "opaque", which makes one stable encoding per operation the
// caller's only defence.
//
// **Post-filtering exists because some port queries have no repository
// equivalent** — a multi-valued session state, an audit actor, a time window, a
// site scope on `radpostauth`. Each one is named at its call site and reported as a
// finding; none of them is invented here.

// pageCursor is the adapter's cursor.
//
// Two members rather than one, because a filtered page can end in the middle of a
// repository page. `C` is the repository cursor the chunk began at and `S` is how
// many raw rows into that chunk the next page resumes from. Without `S`, resuming
// from `C` would replay every row already returned, and resuming from the chunk's
// own next cursor would skip the tail of it.
type pageCursor struct {
	C string `json:"c"`
	S int    `json:"s"`
}

func encodeCursor(c pageCursor) *string {
	raw, err := json.Marshal(c)
	if err != nil {
		return nil
	}
	s := base64.RawURLEncoding.EncodeToString(raw)
	return &s
}

// decodeCursor is deliberately forgiving of a cursor it cannot read: it starts
// from the beginning rather than refusing.
//
// A malformed cursor reaches this code only from a client that constructed one,
// and the contract says not to. Refusing would answer a screen with an error where
// starting over answers it with rows.
func decodeCursor(raw string) pageCursor {
	if raw == "" {
		return pageCursor{}
	}
	buf, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return pageCursor{}
	}
	var c pageCursor
	if err := json.Unmarshal(buf, &c); err != nil {
		return pageCursor{}
	}
	if c.S < 0 {
		c.S = 0
	}
	return c
}

// chunkSize is how many rows a single repository read asks for.
//
// Larger than the page when the page is small, because a predicate that rejects
// most rows would otherwise make one page of fifty into fifty round trips.
func chunkSize(limit int) int {
	const floor, ceiling = 50, 200
	switch {
	case limit < floor:
		return floor
	case limit > ceiling:
		return ceiling
	default:
		return limit
	}
}

// fill drives a repository listing until `limit` rows survive `keep`.
//
// `keep` returns the converted row and whether it belongs in the answer. It is
// where a filter the repository cannot express is applied, and every use of it
// names the missing repository capability.
func fill[S, T any](
	w api.Window,
	fetch func(cursor string, limit int) (store.Page[S], error),
	keep func(S) (T, bool),
) (api.Page[T], error) {
	limit := w.Limit
	if limit <= 0 {
		limit = api.DefaultPageLimit
	}
	if limit > api.MaxPageLimit {
		limit = api.MaxPageLimit
	}

	cur := decodeCursor(w.Cursor)
	chunkStart, consumed := cur.C, cur.S
	items := make([]T, 0, limit)

	for {
		page, err := fetch(chunkStart, chunkSize(limit))
		if err != nil {
			return api.Page[T]{}, out(err)
		}
		if consumed > len(page.Items) {
			consumed = len(page.Items)
		}
		for i := consumed; i < len(page.Items); i++ {
			v, ok := keep(page.Items[i])
			if !ok {
				continue
			}
			if len(items) == limit {
				// The first row of the next page. Its position is recorded
				// rather than its key, because the repository's own cursor
				// cannot address a row in the middle of a page.
				return api.Page[T]{Items: items, NextCursor: encodeCursor(pageCursor{C: chunkStart, S: i})}, nil
			}
			items = append(items, v)
		}
		if page.NextCursor == nil {
			return api.Page[T]{Items: items}, nil
		}
		chunkStart, consumed = *page.NextCursor, 0
	}
}

// listOptions is the repository's shape of one page request.
func listOptions(siteID, query, cursor string, limit int) store.ListOptions {
	return store.ListOptions{SiteID: siteID, Query: query, Cursor: cursor, Limit: limit}
}
