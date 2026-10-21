// SPDX-License-Identifier: AGPL-3.0-only

package read

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// Device is the provenance that names the device a record came from: the
// provider it came through, the vendor that produced it, and the device
// fields the record carries. An unknown field is "". Two records with the
// same five fields came from the same device.
type Device struct {
	Provider    string
	Source      string
	DeviceID    string
	DeviceModel string
	ProductType string
}

// Known reports whether d names a device at all: a record with no device
// id, model or product type has none.
func (d Device) Known() bool {
	return d.DeviceID != "" || d.DeviceModel != "" || d.ProductType != ""
}

// SampleDevice is the device of a quantity sample.
func SampleDevice(s Sample) Device {
	return Device{Provider: s.Provider, Source: deref(s.Source), DeviceID: deref(s.Device),
		DeviceModel: deref(s.DeviceModel), ProductType: deref(s.ProductType)}
}

// ProvenanceDevice is the device of a record whose table keeps only a device
// id (sleep sessions, workouts, ECG recordings, nutrition logs; daily
// summaries have none).
func ProvenanceDevice(p Provenance) Device {
	return Device{Provider: p.Provider, Source: deref(p.Source), DeviceID: deref(p.Device)}
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// Devices lists, sorted and each once, the devices of the user's records
// that are FHIR Observations (quantity samples, sleep sessions, workouts,
// ECG recordings, nutrition logs) whose time is in [q.Start, q.End); a zero
// bound is open. Cursor and Limit are ignored: a user has a handful of
// devices.
//
// It reads the provenance columns of every record in the window once (each
// branch on its (tenant_id, user_id, time) index, so a window reads only its
// chunks): bounded, it is cheap; unbounded, on years of minute-level samples,
// it is the one read of this package that costs a scan of the user's rows.
func Devices(ctx context.Context, db Querier, tenantID, userID uuid.UUID, q Query) ([]Device, error) {
	q.Cursor, q.Limit = "", 0
	n, err := q.normalize(false)
	if err != nil {
		return nil, err
	}
	if err := userExists(ctx, db, tenantID, userID); err != nil {
		return nil, err
	}
	args := []any{tenantID, userID}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	window := func(timeCol string) string {
		var w string
		if !n.Start.IsZero() {
			w += " AND " + timeCol + " >= " + arg(n.Start) + "::timestamptz"
		}
		if !n.End.IsZero() {
			w += " AND " + timeCol + " < " + arg(n.End) + "::timestamptz"
		}
		return w
	}
	branches := []string{
		`SELECT provider, coalesce(source_slug, ''), coalesce(device_id, ''), coalesce(device_model, ''),
			coalesce(product_type, '')
		FROM ohs_data.quantity_samples WHERE tenant_id = $1 AND user_id = $2
			AND (device_id <> '' OR device_model <> '' OR product_type <> '')` + window("start_date"),
	}
	for _, t := range []struct{ table, timeCol string }{
		{"ohs_data.sleep_sessions", "bedtime_start"},
		{"ohs_data.workouts", "start_date"},
		{"ohs_data.ecg_recordings", "start_date"},
		{"ohs_data.nutrition_logs", "start_date"},
	} {
		branches = append(branches, `SELECT provider, coalesce(source_slug, ''), device_id, '', ''
		FROM `+t.table+` WHERE tenant_id = $1 AND user_id = $2 AND device_id <> ''`+window(t.timeCol))
	}
	// UNION, not UNION ALL: each device once.
	rows, err := db.Query(ctx, strings.Join(branches, " UNION ")+" ORDER BY 1, 2, 3, 4, 5", args...)
	if err != nil {
		return nil, fmt.Errorf("read: devices: %w", err)
	}
	defer rows.Close()
	out := []Device{}
	for rows.Next() {
		var d Device
		if err := rows.Scan(&d.Provider, &d.Source, &d.DeviceID, &d.DeviceModel, &d.ProductType); err != nil {
			return nil, fmt.Errorf("read: devices: scan: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read: devices: %w", err)
	}
	return out, nil
}
