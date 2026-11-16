// SPDX-License-Identifier: AGPL-3.0-only

package etl

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// Replaceable observations (lab results, docs/labs.md).
//
// The latest copy of a source record replaces every stored copy of it whole:
// a cancelled result or a result turned from a number into a string must
// clear what the earlier copy stored, so the latest wins merge of the other
// tables (a figure the new copy omits keeps the stored one) does not fit.
// The stored copies are found by provider and external id, whatever their
// time: an amended result may carry another collection time. The order of
// two copies is the capture time of their raw objects (captured_at): a copy
// from an object captured before the stored one's is stale and dropped, so a
// late re-processed object never brings an old value back. A copy equal to
// the stored one changes nothing.

// splitReplaceable splits observations into the insert-only ones and the
// Replaceable ones with a source id.
func splitReplaceable(obs []model.Observation) (insertOnly, replace []model.Observation) {
	for _, o := range obs {
		if o.Replaceable && o.ExternalID != "" {
			replace = append(replace, o)
		} else {
			insertOnly = append(insertOnly, o)
		}
	}
	return insertOnly, replace
}

// storedObservationCopies reads the stored copies of the source records
// ($3, $4: provider, external id, one element per record).
const storedObservationCopies = `SELECT o.id, o.ts, o.end_ts, o.content_hash, o.sealed, o.captured_at,
		o.provider, o.external_id, o.report_id, o.canonical_code, o.unit_ucum,
		o.value_num, o.value_code, o.value_text, o.value_canonical, o.is_primary_source,
		o.type_concept, o.source_value, o.raw_ref, o.source_name, o.device_id, o.metadata
	FROM ohs_data.observations o
	JOIN unnest($3::text[], $4::text[]) AS m(provider, external_id)
	  ON o.provider = m.provider AND o.external_id = m.external_id
	WHERE o.tenant_id = $1 AND o.user_id = $2`

// deleteObservationCopies deletes stored rows by (id, ts), the primary key.
const deleteObservationCopies = `DELETE FROM ohs_data.observations o
	USING unnest($3::bigint[], $4::timestamptz[]) AS d(id, ts)
	WHERE o.tenant_id = $1 AND o.user_id = $2 AND o.id = d.id AND o.ts = d.ts`

// advanceObservationCapture moves the capture time of unchanged rows ($3,
// $4: id, ts) forward to this object's ($5).
const advanceObservationCapture = `UPDATE ohs_data.observations o SET captured_at = $5
	FROM unnest($3::bigint[], $4::timestamptz[]) AS d(id, ts)
	WHERE o.tenant_id = $1 AND o.user_id = $2 AND o.id = d.id AND o.ts = d.ts
	  AND (o.captured_at IS NULL OR o.captured_at < $5)`

type sourceRecord struct{ provider, externalID string }

// replaceObservations writes Replaceable observations: of several copies of
// one record in the batch the last one, then for each record nothing (a
// stale or unchanged copy, counted as skipped) or its stored copies deleted
// and the new row inserted.
func (w *Writer) replaceObservations(ctx context.Context, tx TxLike, sc scope, obs []model.Observation) (written, skipped int, err error) {
	last := map[sourceRecord]int{}
	for i, o := range obs {
		last[sourceRecord{sc.providerOr(o.Provider), o.ExternalID}] = i
	}
	keep := make([]model.Observation, 0, len(last))
	var providers, ids []string
	for i, o := range obs {
		k := sourceRecord{sc.providerOr(o.Provider), o.ExternalID}
		if last[k] != i {
			skipped++
			continue
		}
		keep = append(keep, o)
		providers = append(providers, k.provider)
		ids = append(ids, k.externalID)
	}

	stored := map[sourceRecord][]Row{}
	for r, err := range openRows(ctx, tx, sc.uv, tObservation, storedObservationCopies,
		[]any{sc.tenant, sc.user, providers, ids}) {
		if err != nil {
			return written, skipped, fmt.Errorf("read stored observations: %w", err)
		}
		k := sourceRecord{asString(r["provider"]), asString(r["external_id"])}
		stored[k] = append(stored[k], r)
	}

	var (
		delIDs, sameIDs     []int64
		delTS, sameTS       []time.Time
		rows                [][]any
		columns             = observationShape.columns(sc)
		insertCount, stales int
	)
	for _, o := range keep {
		k := sourceRecord{sc.providerOr(o.Provider), o.ExternalID}
		base := observationRow(sc, o, observationHash(sc, o))
		copies := stored[k]
		if newerCopy(copies, sc.captured) {
			stales++
			continue
		}
		if len(copies) == 1 && sameObservation(base, copies[0]) {
			sameIDs = append(sameIDs, copies[0]["id"].(int64))
			sameTS = append(sameTS, copies[0]["ts"].(time.Time))
			skipped++
			continue
		}
		for _, c := range copies {
			delIDs = append(delIDs, c["id"].(int64))
			delTS = append(delTS, c["ts"].(time.Time))
		}
		row, err := observationShape.finish(sc, base)
		if err != nil {
			return written, skipped, err
		}
		rows = append(rows, row)
		insertCount++
	}
	skipped += stales
	if len(delIDs) > 0 {
		if _, err := tx.Exec(ctx, deleteObservationCopies, sc.tenant, sc.user, delIDs, delTS); err != nil {
			return written, skipped, fmt.Errorf("replace observations: %w", err)
		}
	}
	if len(sameIDs) > 0 && !sc.captured.IsZero() {
		if _, err := tx.Exec(ctx, advanceObservationCapture, sc.tenant, sc.user, sameIDs, sameTS, sc.captured); err != nil {
			return written, skipped, fmt.Errorf("advance observation capture: %w", err)
		}
	}
	if len(rows) == 0 {
		return written, skipped, nil
	}
	n, k, err := w.insertBatches(ctx, tx, tObservation, insertHead(tObservation, columns),
		doNothing(hyperConflict("ts")), len(columns), rows)
	return written + n, skipped + k, err
}

// newerCopy reports whether a stored copy comes from an object captured
// after captured.
func newerCopy(copies []Row, captured time.Time) bool {
	if captured.IsZero() {
		return false
	}
	for _, c := range copies {
		if at, ok := c["captured_at"].(time.Time); ok && at.After(captured) {
			return true
		}
	}
	return false
}

// observationCompared are the columns of a base row a stored copy must match
// to be the same record.
var observationCompared = []string{
	"ts", "end_ts", "content_hash", "canonical_code", "value_num", "value_code", "value_text",
	"unit_ucum", "value_canonical", "is_primary_source", "type_concept", "source_value",
	"raw_ref", "source_name", "device_id", "report_id",
}

// sameObservation reports whether a new base row holds what the stored row
// holds. Empty text and NULL are the same; metadata compares as JSON values.
func sameObservation(base []any, stored Row) bool {
	at := map[string]int{}
	for i, c := range observationColumns {
		at[c] = i
	}
	for _, c := range observationCompared {
		a, b := blankNil(base[at[c]]), blankNil(stored[c])
		if !sameValue(a, b) {
			return false
		}
	}
	return sameJSON(base[at["metadata"]], stored["metadata"])
}

// blankNil maps an empty string, a nil pointer and a zero time to nil.
func blankNil(v any) any {
	v = deref(v)
	switch x := v.(type) {
	case string:
		if x == "" {
			return nil
		}
	case time.Time:
		if x.IsZero() {
			return nil
		}
	}
	return v
}

func sameJSON(a, b any) bool {
	da, ok1 := jsonValue(a)
	db, ok2 := jsonValue(b)
	return ok1 && ok2 && reflect.DeepEqual(da, db)
}

func jsonValue(v any) (any, bool) {
	var raw []byte
	switch x := v.(type) {
	case nil:
		return map[string]any{}, true
	case json.RawMessage:
		raw = x
	case []byte:
		raw = x
	case string:
		raw = []byte(x)
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return nil, false
		}
		raw = b
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, false
	}
	return out, true
}

func asString(v any) string {
	s, _ := deref(v).(string)
	return s
}
