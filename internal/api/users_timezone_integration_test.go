// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package api_test

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type userZoneJSON struct {
	ID             uuid.UUID `json:"id"`
	Timezone       *string   `json:"timezone"`
	TimezoneSource *string   `json:"timezone_source"`
}

// The user routes take and return the user's time zone: at creation, with
// PATCH /v1/users/{id}, and in every read. A name that is not an IANA zone is
// a 400; null clears the zone.
func TestUserTimezoneRoutes(t *testing.T) {
	e := newEnv(t)
	zone := func(u userZoneJSON) string {
		if u.Timezone == nil {
			require.Nil(t, u.TimezoneSource, "no zone, no source")
			return ""
		}
		require.NotNil(t, u.TimezoneSource)
		return *u.Timezone + " " + *u.TimezoneSource
	}

	r := e.do("POST", "/v1/users", e.keyA, map[string]any{"external_ref": "tz-1", "timezone": "Europe/Paris"})
	require.Equal(t, http.StatusCreated, r.code, string(r.body))
	var u userZoneJSON
	r.json(t, &u)
	assert.Equal(t, "Europe/Paris explicit", zone(u))

	r = e.do("POST", "/v1/users", e.keyA, map[string]any{"external_ref": "tz-2"})
	require.Equal(t, http.StatusCreated, r.code, string(r.body))
	var bare userZoneJSON
	r.json(t, &bare)
	assert.Equal(t, "", zone(bare))
	assert.Contains(t, string(r.body), `"timezone":null`)
	assert.Contains(t, string(r.body), `"timezone_source":null`)

	for _, bad := range []any{"Mars/Olympus", "Local", "", 2} {
		r = e.do("POST", "/v1/users", e.keyA, map[string]any{"timezone": bad})
		assert.Equal(t, http.StatusBadRequest, r.code, "create with %v", bad)
		r = e.do("PATCH", "/v1/users/"+bare.ID.String(), e.keyA, map[string]any{"timezone": bad})
		assert.Equal(t, http.StatusBadRequest, r.code, "update with %v", bad)
	}
	r = e.do("PATCH", "/v1/users/"+bare.ID.String(), e.keyA, map[string]any{})
	assert.Equal(t, http.StatusBadRequest, r.code, "timezone is the one field, and it is required")
	r = e.do("PATCH", "/v1/users/"+bare.ID.String(), e.keyA, map[string]any{"external_ref": "x"})
	assert.Equal(t, http.StatusBadRequest, r.code, "an unknown field")

	r = e.do("PATCH", "/v1/users/"+bare.ID.String(), e.keyA, map[string]any{"timezone": "America/Los_Angeles"})
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	r.json(t, &bare)
	assert.Equal(t, "America/Los_Angeles explicit", zone(bare))

	r = e.do("GET", "/v1/users/"+bare.ID.String(), e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code)
	var got userZoneJSON
	r.json(t, &got)
	assert.Equal(t, "America/Los_Angeles explicit", zone(got))

	r = e.do("GET", "/v1/users", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code)
	var list page[userZoneJSON]
	r.json(t, &list)
	zones := map[uuid.UUID]string{}
	for _, l := range list.Data {
		zones[l.ID] = zone(l)
	}
	assert.Equal(t, "Europe/Paris explicit", zones[u.ID])
	assert.Equal(t, "America/Los_Angeles explicit", zones[bare.ID])

	// null clears it.
	r = e.do("PATCH", "/v1/users/"+bare.ID.String(), e.keyA, `{"timezone":null}`)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	r.json(t, &got)
	assert.Equal(t, "", zone(got))

	// Another tenant's key, an unknown user and no key.
	assert.Equal(t, http.StatusNotFound, e.do("PATCH", "/v1/users/"+u.ID.String(), e.keyB, map[string]any{"timezone": "UTC"}).code)
	assert.Equal(t, http.StatusNotFound, e.do("PATCH", "/v1/users/"+uuid.NewString(), e.keyA, map[string]any{"timezone": "UTC"}).code)
	assert.Equal(t, http.StatusNotFound, e.do("PATCH", "/v1/users/not-a-uuid", e.keyA, map[string]any{"timezone": "UTC"}).code)
	assert.Equal(t, http.StatusUnauthorized, e.do("PATCH", "/v1/users/"+u.ID.String(), "", map[string]any{"timezone": "UTC"}).code)
	r = e.do("GET", "/v1/users/"+u.ID.String(), e.keyA, nil)
	r.json(t, &got)
	assert.Equal(t, "Europe/Paris explicit", zone(got), "the refused updates changed nothing")
}
