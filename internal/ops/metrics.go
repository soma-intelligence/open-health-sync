// SPDX-License-Identifier: AGPL-3.0-only

package ops

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// durationBuckets are the upper bounds, in seconds, of every duration
// histogram: from a claim that found nothing to do (10 ms) to a job that
// used its whole ten minutes.
var durationBuckets = []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600}

// Metrics holds the worker metrics of one process and writes them in the
// Prometheus text format, like the API's own: no metrics library. Label
// values are fixed sets (queues, tiers, triggers, outcomes, provider slugs,
// table names, loop names), so the cardinality is bounded. Every method is
// safe for concurrent use, and valid on a nil Metrics, which records nothing.
type Metrics struct {
	loops *Registry

	mu       sync.Mutex
	jobs     map[[3]string]*histogram // queue, kind, outcome
	pulls    map[string]*histogram    // provider
	webhooks map[string]*histogram    // outcome
	rows     map[string]int64         // table
	queues   map[string]QueueStat
}

// QueueStat is the backlog of one queue at the last sample.
type QueueStat struct {
	// Depth is the number of jobs due and not claimed yet.
	Depth int64
	// OldestAge is how long the oldest of them has been due.
	OldestAge time.Duration
}

// NewMetrics returns an empty set. loops, when set, is reported as the last
// heartbeat of each loop.
func NewMetrics(loops *Registry) *Metrics {
	return &Metrics{
		loops: loops,
		jobs:  map[[3]string]*histogram{}, pulls: map[string]*histogram{}, webhooks: map[string]*histogram{},
		rows: map[string]int64{}, queues: map[string]QueueStat{},
	}
}

// JobDone records one job of a queue ("etl", "pull"): its kind (the ETL
// tier, the pull trigger), how it ended and how long it ran.
func (m *Metrics) JobDone(queue, kind, outcome string, d time.Duration) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	k := [3]string{queue, kind, outcome}
	h := m.jobs[k]
	if h == nil {
		h = newHistogram()
		m.jobs[k] = h
	}
	h.observe(d)
}

// PullDone records how long one pull of provider took, whatever its outcome.
func (m *Metrics) PullDone(provider string, d time.Duration) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	h := m.pulls[provider]
	if h == nil {
		h = newHistogram()
		m.pulls[provider] = h
	}
	h.observe(d)
}

// WebhookDone records one delivery attempt: "delivered" or "failed", and how
// long the receiver took.
func (m *Metrics) WebhookDone(outcome string, d time.Duration) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	h := m.webhooks[outcome]
	if h == nil {
		h = newHistogram()
		m.webhooks[outcome] = h
	}
	h.observe(d)
}

// ETLRows adds rows the ETL wrote to table (its unqualified name, for
// example "quantity_samples").
func (m *Metrics) ETLRows(table string, rows int) {
	if m == nil || rows <= 0 {
		return
	}
	m.mu.Lock()
	m.rows[table] += int64(rows)
	m.mu.Unlock()
}

// SetQueue stores the last sample of a queue's backlog.
func (m *Metrics) SetQueue(queue string, s QueueStat) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.queues[queue] = s
	m.mu.Unlock()
}

// Write writes every metric in the Prometheus text exposition format
// (version 0.0.4). A family with no series yet is left out.
func (m *Metrics) Write(w io.Writer) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.queues) > 0 {
		names := sortedKeys(m.queues)
		fmt.Fprint(w, "# HELP ohs_queue_depth Jobs due and not claimed yet, by queue, at the last sample.\n")
		fmt.Fprint(w, "# TYPE ohs_queue_depth gauge\n")
		for _, q := range names {
			fmt.Fprintf(w, "ohs_queue_depth{queue=\"%s\"} %d\n", escapeLabel(q), m.queues[q].Depth)
		}
		fmt.Fprint(w, "# HELP ohs_queue_oldest_job_age_seconds How long the oldest due job of the queue has waited, at the last sample.\n")
		fmt.Fprint(w, "# TYPE ohs_queue_oldest_job_age_seconds gauge\n")
		for _, q := range names {
			fmt.Fprintf(w, "ohs_queue_oldest_job_age_seconds{queue=\"%s\"} %s\n", escapeLabel(q), seconds(m.queues[q].OldestAge))
		}
	}

	if len(m.jobs) > 0 {
		keys := make([][3]string, 0, len(m.jobs))
		for k := range m.jobs {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			for n := range 3 {
				if keys[i][n] != keys[j][n] {
					return keys[i][n] < keys[j][n]
				}
			}
			return false
		})
		fmt.Fprint(w, "# HELP ohs_job_duration_seconds How long jobs ran, from claim to settle, by queue, kind and outcome.\n")
		fmt.Fprint(w, "# TYPE ohs_job_duration_seconds histogram\n")
		for _, k := range keys {
			m.jobs[k].write(w, "ohs_job_duration_seconds", fmt.Sprintf("queue=\"%s\",kind=\"%s\",outcome=\"%s\"",
				escapeLabel(k[0]), escapeLabel(k[1]), escapeLabel(k[2])))
		}
	}
	if len(m.pulls) > 0 {
		fmt.Fprint(w, "# HELP ohs_pull_duration_seconds How long pulls of a provider took.\n")
		fmt.Fprint(w, "# TYPE ohs_pull_duration_seconds histogram\n")
		for _, p := range sortedKeys(m.pulls) {
			m.pulls[p].write(w, "ohs_pull_duration_seconds", fmt.Sprintf("provider=\"%s\"", escapeLabel(p)))
		}
	}
	if len(m.webhooks) > 0 {
		fmt.Fprint(w, "# HELP ohs_webhook_delivery_duration_seconds How long webhook delivery attempts took, by outcome.\n")
		fmt.Fprint(w, "# TYPE ohs_webhook_delivery_duration_seconds histogram\n")
		for _, o := range sortedKeys(m.webhooks) {
			m.webhooks[o].write(w, "ohs_webhook_delivery_duration_seconds", fmt.Sprintf("outcome=\"%s\"", escapeLabel(o)))
		}
	}
	if len(m.rows) > 0 {
		fmt.Fprint(w, "# HELP ohs_etl_rows_written_total Rows the ETL wrote, by table.\n")
		fmt.Fprint(w, "# TYPE ohs_etl_rows_written_total counter\n")
		for _, t := range sortedKeys(m.rows) {
			fmt.Fprintf(w, "ohs_etl_rows_written_total{table=\"%s\"} %d\n", escapeLabel(t), m.rows[t])
		}
	}

	if m.loops == nil {
		return
	}
	states := m.loops.States()
	if len(states) == 0 {
		return
	}
	fmt.Fprint(w, "# HELP ohs_loop_last_tick_unix_seconds When each background loop of this process last beat.\n")
	fmt.Fprint(w, "# TYPE ohs_loop_last_tick_unix_seconds gauge\n")
	for _, s := range states {
		fmt.Fprintf(w, "ohs_loop_last_tick_unix_seconds{loop=\"%s\"} %d\n", escapeLabel(s.Name), s.LastTick.Unix())
	}
	fmt.Fprint(w, "# HELP ohs_loop_stalled 1 when the loop missed its heartbeats and /healthz answers 503.\n")
	fmt.Fprint(w, "# TYPE ohs_loop_stalled gauge\n")
	for _, s := range states {
		v := 0
		if s.Stalled {
			v = 1
		}
		fmt.Fprintf(w, "ohs_loop_stalled{loop=\"%s\"} %d\n", escapeLabel(s.Name), v)
	}
}

// WriteSnapshot writes a set of plain values as prefix + name, sorted by
// name: a name ending in _total is a counter, anything else a gauge.
func WriteSnapshot(w io.Writer, prefix string, values map[string]int64) {
	for _, n := range sortedKeys(values) {
		typ := "gauge"
		if strings.HasSuffix(n, "_total") {
			typ = "counter"
		}
		fmt.Fprintf(w, "# TYPE %s%s %s\n%s%s %d\n", prefix, n, typ, prefix, n, values[n])
	}
}

// histogram is a Prometheus histogram over durationBuckets.
type histogram struct {
	counts []int64 // per bucket, not cumulative; the last is +Inf
	sum    float64
	count  int64
}

func newHistogram() *histogram { return &histogram{counts: make([]int64, len(durationBuckets)+1)} }

func (h *histogram) observe(d time.Duration) {
	s := max(d.Seconds(), 0)
	h.counts[sort.SearchFloat64s(durationBuckets, s)]++
	h.sum += s
	h.count++
}

func (h *histogram) write(w io.Writer, name, labels string) {
	var cum int64
	for i, le := range durationBuckets {
		cum += h.counts[i]
		fmt.Fprintf(w, "%s_bucket{%s,le=\"%s\"} %d\n", name, labels, strconv.FormatFloat(le, 'g', -1, 64), cum)
	}
	fmt.Fprintf(w, "%s_bucket{%s,le=\"+Inf\"} %d\n", name, labels, h.count)
	fmt.Fprintf(w, "%s_sum{%s} %s\n", name, labels, strconv.FormatFloat(h.sum, 'g', -1, 64))
	fmt.Fprintf(w, "%s_count{%s} %d\n", name, labels, h.count)
}

func seconds(d time.Duration) string { return strconv.FormatFloat(d.Seconds(), 'g', -1, 64) }

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

func escapeLabel(s string) string { return labelEscaper.Replace(s) }
