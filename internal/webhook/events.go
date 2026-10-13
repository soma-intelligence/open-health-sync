// SPDX-License-Identifier: AGPL-3.0-only

package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// Event types.
const (
	// DataWritten: a sync wrote new or changed records for a user.
	DataWritten = "data.written"
	// DataDeleted: records the provider deleted were removed.
	DataDeleted = "data.deleted"
	// DataErased: everything a user held from one provider was erased, on a
	// request of the tenant (a disconnect with delete_data=true, or
	// DELETE /v1/users/{id}/data/{provider}).
	DataErased = "data.erased"
	// UserDeleted: a user and all their data were erased.
	UserDeleted = "user.deleted"
	// ConnectionNeedsReauth: a provider refused the user's grant; nothing
	// syncs until the user connects again.
	ConnectionNeedsReauth = "connection.needs_reauth"
	// ConnectionBroken: a connection stopped syncing and says why (grant
	// refused, a permission missing, pulls failing, provider quota spent)
	// and what the user can do.
	ConnectionBroken = "connection.broken"
	// ConnectionRestored: a broken connection synced again.
	ConnectionRestored = "connection.restored"
	// TagCreated, TagUpdated and TagDeleted: a time tag of a user was
	// created, changed or deleted through the API.
	TagCreated = "tag.created"
	TagUpdated = "tag.updated"
	TagDeleted = "tag.deleted"
	// Test is sent by POST /v1/webhooks/{id}/test only.
	Test = "webhook.test"
)

// Types are the event types an endpoint can subscribe to, in documentation
// order. Test is not one: it goes to the endpoint it is asked for.
var Types = []string{DataWritten, DataDeleted, DataErased, UserDeleted, ConnectionNeedsReauth, ConnectionBroken, ConnectionRestored, TagCreated, TagUpdated, TagDeleted}

// KnownType reports whether t can be subscribed to.
func KnownType(t string) bool { return slices.Contains(Types, t) }

// Event is one thing that happened. Data is the payload's data object; the
// user's id and external_ref are added to it when User is set.
type Event struct {
	Tenant uuid.UUID
	User   *uuid.UUID
	Type   string
	Data   map[string]any
}

// Execer is a pool or a transaction.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Payload is the JSON body of every delivery.
type Payload struct {
	ID        uuid.UUID      `json:"id"`
	Type      string         `json:"type"`
	Timestamp time.Time      `json:"timestamp"`
	Data      map[string]any `json:"data"`
	// Test marks an event sent by the test route: it describes nothing
	// that happened.
	Test bool `json:"test,omitempty"`
}

// enqueueSQL writes one delivery per active endpoint of the tenant that wants
// the type (an empty event_types means every type). The user's external_ref
// is read in the same statement, so it is the one current in the
// transaction. A tenant without endpoints costs one index probe.
const enqueueSQL = `
	INSERT INTO ohs_core.webhook_deliveries (tenant_id, endpoint_id, user_id, event_id, event_type, payload)
	SELECT e.tenant_id, e.id, $2, $3, $4,
		jsonb_set($5::jsonb, '{data}', ($5::jsonb -> 'data') || COALESCE(
			(SELECT jsonb_build_object('user_id', u.id, 'external_ref', u.external_ref)
			 FROM ohs_core.end_users u WHERE u.tenant_id = $1 AND u.id = $2),
			CASE WHEN $2::uuid IS NULL THEN '{}'::jsonb
				ELSE jsonb_build_object('user_id', $2::uuid, 'external_ref', NULL) END))
	FROM ohs_core.webhook_endpoints e
	WHERE e.tenant_id = $1 AND e.status = 'active'
	  AND (cardinality(e.event_types) = 0 OR $4 = ANY (e.event_types))`

// Enqueue records ev for every endpoint that wants it, inside the caller's
// transaction so the deliveries commit (or roll back) with the change they
// announce. It returns how many deliveries were written.
func Enqueue(ctx context.Context, db Execer, ev Event) (int64, error) {
	body, err := newPayload(ev)
	if err != nil {
		return 0, err
	}
	tag, err := db.Exec(ctx, enqueueSQL, ev.Tenant, ev.User, body.ID, ev.Type, mustJSON(body))
	if err != nil {
		return 0, fmt.Errorf("enqueue webhook %s: %w", ev.Type, err)
	}
	return tag.RowsAffected(), nil
}

func newPayload(ev Event) (Payload, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return Payload{}, err
	}
	data := ev.Data
	if data == nil {
		data = map[string]any{}
	}
	return Payload{ID: id, Type: ev.Type, Timestamp: time.Now().UTC().Truncate(time.Millisecond), Data: data}, nil
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("webhook: encode payload: %v", err))
	}
	return b
}

// SampleUserRef is the external_ref of the user a sample event names; its
// user_id is the nil UUID, so it never matches a real user.
const SampleUserRef = "sample-user"

// SampleData is a realistic data object for an event of type typ, as the
// test route sends it: the fields a real event of that type carries, about
// no real user. now sets the days a data.written sample names.
func SampleData(typ string, now time.Time) map[string]any {
	data := map[string]any{"user_id": uuid.Nil.String(), "external_ref": SampleUserRef}
	switch typ {
	case DataWritten:
		day := now.UTC()
		data["provider"] = "oura"
		data["records"] = map[string]any{"sleep_sessions": 1, "sleep_phases": 24, "daily_summaries": 1}
		data["types"] = []string{"daily_summary", "sleep_analysis"}
		data["first_day"] = day.AddDate(0, 0, -1).Format(time.DateOnly)
		data["last_day"] = day.Format(time.DateOnly)
	case DataDeleted:
		data["provider"] = "oura"
		data["records_deleted"] = 3
	case ConnectionNeedsReauth:
		data["provider"] = "oura"
	}
	return data
}
