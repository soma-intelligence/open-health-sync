// SPDX-License-Identifier: AGPL-3.0-only

package vault

import (
	"fmt"
	"io"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Kinds of ohs_vault_open_failures_total: a sealed row that does not open,
// or a wrapped user key that does not open under its tenant key.
const (
	failRow = "row"
	failKey = "key"
)

// providerBuckets are the upper bounds, in seconds, of the
// ohs_vault_provider_seconds histogram: a local unwrap takes microseconds, a
// KMS call tens of milliseconds, a struggling one seconds.
var providerBuckets = []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// Metrics counts what the vault does, for /metrics. Every label takes a fixed
// set of values. The zero value is not ready: use NewMetrics.
type Metrics struct {
	sealsOK, sealsErr   atomic.Int64
	opensOK, opensErr   atomic.Int64
	failRow, failKey    atomic.Int64
	cacheHit, cacheMiss atomic.Int64

	providerCount  atomic.Int64
	providerMicros atomic.Int64
	providerBucket []atomic.Int64 // one per providerBuckets entry, not cumulative

	// clear is the last Status: the rows still stored clear, by table, and
	// the raw objects not sealed. Nil until SetClearRows.
	clearMu sync.Mutex
	clear   *StatusReport
}

// SetClearRows records what Status found, for the ohs_vault_clear_rows and
// ohs_vault_clear_raw_objects gauges: what `ohs vault seal` still has to
// convert before the release that drops the clear columns.
func (m *Metrics) SetClearRows(s StatusReport) {
	m.clearMu.Lock()
	defer m.clearMu.Unlock()
	m.clear = &s
}

// NewMetrics returns counters at zero.
func NewMetrics() *Metrics {
	return &Metrics{providerBucket: make([]atomic.Int64, len(providerBuckets))}
}

func (m *Metrics) seal(ok bool) {
	if ok {
		m.sealsOK.Add(1)
	} else {
		m.sealsErr.Add(1)
	}
}

func (m *Metrics) open(ok bool) {
	if ok {
		m.opensOK.Add(1)
	} else {
		m.opensErr.Add(1)
	}
}

func (m *Metrics) openFailure(kind string) {
	switch kind {
	case failRow:
		m.failRow.Add(1)
	case failKey:
		m.failKey.Add(1)
	}
}

func (m *Metrics) cacheResult(hit bool) {
	if hit {
		m.cacheHit.Add(1)
	} else {
		m.cacheMiss.Add(1)
	}
}

// providerTime records one call to the key provider, failed or not.
func (m *Metrics) providerTime(d time.Duration) {
	m.providerCount.Add(1)
	m.providerMicros.Add(d.Microseconds())
	s := d.Seconds()
	for i, le := range providerBuckets {
		if s <= le {
			m.providerBucket[i].Add(1)
			return
		}
	}
}

// Write renders the counters in the Prometheus text format (version 0.0.4).
func (m *Metrics) Write(w io.Writer) {
	fmt.Fprint(w, "# HELP ohs_vault_seals_total Rows sealed, by result.\n")
	fmt.Fprint(w, "# TYPE ohs_vault_seals_total counter\n")
	fmt.Fprintf(w, "ohs_vault_seals_total{result=\"ok\"} %d\n", m.sealsOK.Load())
	fmt.Fprintf(w, "ohs_vault_seals_total{result=\"error\"} %d\n", m.sealsErr.Load())
	fmt.Fprint(w, "# HELP ohs_vault_opens_total Sealed rows opened, by result.\n")
	fmt.Fprint(w, "# TYPE ohs_vault_opens_total counter\n")
	fmt.Fprintf(w, "ohs_vault_opens_total{result=\"ok\"} %d\n", m.opensOK.Load())
	fmt.Fprintf(w, "ohs_vault_opens_total{result=\"error\"} %d\n", m.opensErr.Load())
	fmt.Fprint(w, "# HELP ohs_vault_open_failures_total Sealed rows and wrapped user keys that did not open, by kind.\n")
	fmt.Fprint(w, "# TYPE ohs_vault_open_failures_total counter\n")
	fmt.Fprintf(w, "ohs_vault_open_failures_total{kind=\"%s\"} %d\n", failRow, m.failRow.Load())
	fmt.Fprintf(w, "ohs_vault_open_failures_total{kind=\"%s\"} %d\n", failKey, m.failKey.Load())
	fmt.Fprint(w, "# HELP ohs_vault_cache_total Lookups of the user key cache, by result.\n")
	fmt.Fprint(w, "# TYPE ohs_vault_cache_total counter\n")
	fmt.Fprintf(w, "ohs_vault_cache_total{result=\"hit\"} %d\n", m.cacheHit.Load())
	fmt.Fprintf(w, "ohs_vault_cache_total{result=\"miss\"} %d\n", m.cacheMiss.Load())
	fmt.Fprint(w, "# HELP ohs_vault_provider_seconds Time of the key provider calls.\n")
	fmt.Fprint(w, "# TYPE ohs_vault_provider_seconds histogram\n")
	var cum int64
	for i, le := range providerBuckets {
		cum += m.providerBucket[i].Load()
		fmt.Fprintf(w, "ohs_vault_provider_seconds_bucket{le=\"%s\"} %d\n", strconv.FormatFloat(le, 'g', -1, 64), cum)
	}
	n := m.providerCount.Load()
	fmt.Fprintf(w, "ohs_vault_provider_seconds_bucket{le=\"+Inf\"} %d\n", n)
	fmt.Fprintf(w, "ohs_vault_provider_seconds_sum %s\n", strconv.FormatFloat(float64(m.providerMicros.Load())/1e6, 'g', -1, 64))
	fmt.Fprintf(w, "ohs_vault_provider_seconds_count %d\n", n)

	m.clearMu.Lock()
	st := m.clear
	m.clearMu.Unlock()
	if st == nil {
		return
	}
	fmt.Fprint(w, "# HELP ohs_vault_clear_rows Rows still stored clear, by table (zero once `ohs vault seal` is done).\n")
	fmt.Fprint(w, "# TYPE ohs_vault_clear_rows gauge\n")
	tables := make([]string, 0, len(st.Tables))
	for t := range st.Tables {
		tables = append(tables, t)
	}
	slices.Sort(tables)
	for _, t := range tables {
		fmt.Fprintf(w, "ohs_vault_clear_rows{table=\"%s\"} %d\n", t, st.Tables[t])
	}
	fmt.Fprint(w, "# HELP ohs_vault_clear_raw_objects Raw objects stored before the vault and not sealed yet.\n")
	fmt.Fprint(w, "# TYPE ohs_vault_clear_raw_objects gauge\n")
	fmt.Fprintf(w, "ohs_vault_clear_raw_objects %d\n", st.RawObjects)
}
