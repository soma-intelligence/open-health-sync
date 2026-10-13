// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/api/read"
)

// pageBody is the envelope of every list: next_cursor is null on the last page.
type pageBody[T any] struct {
	Data       []T     `json:"data"`
	NextCursor *string `json:"next_cursor"`
}

func newPage[T any](data []T, next string) pageBody[T] {
	if data == nil {
		data = []T{}
	}
	p := pageBody[T]{Data: data}
	if next != "" {
		p.NextCursor = &next
	}
	return p
}

// parseLimit reads ?limit: absent is 0 (the default), else 1..MaxLimit.
func parseLimit(r *http.Request) (int, error) {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > read.MaxLimit {
		return 0, fmt.Errorf("limit must be an integer between 1 and %d", read.MaxLimit)
	}
	return n, nil
}

// parseTime accepts RFC 3339 or a calendar date (YYYY-MM-DD, midnight UTC).
func parseTime(v string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse(time.DateOnly, v); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, errors.New("not an RFC 3339 time or a YYYY-MM-DD date")
}

// parseReadQuery reads type, start, end, cursor, limit and view.
func parseReadQuery(r *http.Request) (read.Query, error) {
	qs := r.URL.Query()
	q := read.Query{Type: qs.Get("type"), Cursor: qs.Get("cursor")}
	var err error
	if q.View, err = read.ParseView(qs.Get("view")); err != nil {
		return q, err
	}
	if q.Limit, err = parseLimit(r); err != nil {
		return q, err
	}
	for name, dst := range map[string]*time.Time{"start": &q.Start, "end": &q.End} {
		if v := qs.Get(name); v != "" {
			if *dst, err = parseTime(v); err != nil {
				return q, fmt.Errorf("%s: %w", name, err)
			}
		}
	}
	return q, nil
}
