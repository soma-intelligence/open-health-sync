// SPDX-License-Identifier: AGPL-3.0-only

package read

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/vault"
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
//
// The device fields are sealed on a sealed row, so SQL can only tell the
// devices of the rows still clear apart: the sealed rows of the window are
// opened one by one and their devices added, then the whole list is put in
// the database's order.
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
		FROM ohs_data.quantity_samples WHERE tenant_id = $1 AND user_id = $2 AND sealed IS NULL
			AND (device_id <> '' OR device_model <> '' OR product_type <> '')` + window("start_date"),
	}
	for _, t := range deviceParents {
		branches = append(branches, `SELECT provider, coalesce(source_slug, ''), device_id, '', ''
		FROM ohs_data.`+t.table+` WHERE tenant_id = $1 AND user_id = $2 AND sealed IS NULL AND device_id <> ''`+window(t.timeCol))
	}
	// UNION, not UNION ALL: each device once.
	rows, err := db.Query(ctx, strings.Join(branches, " UNION ")+" ORDER BY 1, 2, 3, 4, 5", args...)
	if err != nil {
		return nil, fmt.Errorf("read: devices: %w", err)
	}
	out := []Device{}
	for rows.Next() {
		var d Device
		if err := rows.Scan(&d.Provider, &d.Source, &d.DeviceID, &d.DeviceModel, &d.ProductType); err != nil {
			rows.Close()
			return nil, fmt.Errorf("read: devices: scan: %w", err)
		}
		out = append(out, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read: devices: %w", err)
	}

	seen := make(map[Device]bool, len(out))
	for _, d := range out {
		seen[d] = true
	}
	known := len(seen)
	keys := keysFor(ctx, tenantID, userID)
	// sealed adds the devices of the sealed rows of one table; full reads
	// the model and the product type too.
	sealed := func(sel *vault.Columns, timeCol string, full bool) error {
		args = args[:2]
		rows, err := db.Query(ctx, `SELECT `+sel.List("")+` FROM ohs_data.`+sel.Table().Name+`
			WHERE tenant_id = $1 AND user_id = $2 AND sealed IS NOT NULL`+window(timeCol), args...)
		if err != nil {
			return fmt.Errorf("read: devices: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var d Device
			var source, id, model, product *string
			dest := []any{&d.Provider, &source, &id}
			if full {
				dest = append(dest, &model, &product)
			}
			if err := sel.Scan(ctx, keys, rows, dest); err != nil {
				return fmt.Errorf("read: devices: scan: %w", err)
			}
			d.Source, d.DeviceID, d.DeviceModel, d.ProductType = deref(source), deref(id), deref(model), deref(product)
			if d.Known() {
				seen[d] = true
			}
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("read: devices: %w", err)
		}
		return nil
	}
	if err := sealed(deviceSampleSel, "start_date", true); err != nil {
		return nil, err
	}
	for _, t := range deviceParents {
		if err := sealed(t.sel, t.timeCol, false); err != nil {
			return nil, err
		}
	}
	if len(seen) == known {
		return out, nil
	}
	return sortDevices(ctx, db, seen)
}

// sortDevices returns the devices of set in the order of the SQL list: by
// their five fields, as the database orders text.
func sortDevices(ctx context.Context, db Querier, set map[Device]bool) ([]Device, error) {
	cols := make([][]string, 5)
	for d := range set {
		for i, v := range []string{d.Provider, d.Source, d.DeviceID, d.DeviceModel, d.ProductType} {
			cols[i] = append(cols[i], v)
		}
	}
	rows, err := db.Query(ctx, `SELECT * FROM unnest($1::text[], $2::text[], $3::text[], $4::text[], $5::text[])
		ORDER BY 1, 2, 3, 4, 5`, cols[0], cols[1], cols[2], cols[3], cols[4])
	if err != nil {
		return nil, fmt.Errorf("read: devices: order: %w", err)
	}
	defer rows.Close()
	out := make([]Device, 0, len(set))
	for rows.Next() {
		var d Device
		if err := rows.Scan(&d.Provider, &d.Source, &d.DeviceID, &d.DeviceModel, &d.ProductType); err != nil {
			return nil, fmt.Errorf("read: devices: order: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read: devices: order: %w", err)
	}
	return out, nil
}

// deviceSampleSel reads the device fields of a quantity sample, and
// deviceParents the device id of the records whose table keeps only that.
var (
	deviceSampleSel = vault.Select("quantity_samples", `provider, source_slug, device_id, device_model, product_type`)
	deviceParents   = func() []deviceParent {
		var out []deviceParent
		for _, t := range []struct{ table, timeCol string }{
			{"sleep_sessions", "bedtime_start"},
			{"workouts", "start_date"},
			{"ecg_recordings", "start_date"},
			{"nutrition_logs", "start_date"},
		} {
			out = append(out, deviceParent{t.table, t.timeCol, vault.Select(t.table, `provider, source_slug, device_id`)})
		}
		return out
	}()
)

type deviceParent struct {
	table, timeCol string
	sel            *vault.Columns
}
