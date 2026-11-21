// SPDX-License-Identifier: AGPL-3.0-only

package ops

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestMetricsWrite(t *testing.T) {
	r, c := testRegistry()
	r.Loop("etl_standard", 2*time.Second)
	r.Loop("webhooks", time.Second)
	m := NewMetrics(r)

	var empty bytes.Buffer
	NewMetrics(nil).Write(&empty)
	if empty.Len() != 0 {
		t.Fatalf("nothing recorded, nothing written: %q", empty.String())
	}

	m.JobDone("etl", "standard", "completed", 30*time.Millisecond)
	m.JobDone("etl", "standard", "completed", 3*time.Second)
	m.JobDone("etl", "standard", "failed", 20*time.Minute)
	m.JobDone("pull", "backfill", "deferred", time.Second)
	m.PullDone("oura", 700*time.Millisecond)
	m.WebhookDone("delivered", 120*time.Millisecond)
	m.WebhookDone("failed", 15*time.Second)
	m.ETLRows("quantity_samples", 40)
	m.ETLRows("quantity_samples", 2)
	m.ETLRows("workouts", 0) // nothing written: no series
	m.SetQueue("etl_standard", QueueStat{Depth: 12, OldestAge: 90 * time.Second})
	m.SetQueue("pull", QueueStat{})
	c.add(2 * time.Minute)
	r.Loop("webhooks", time.Second).Tick()

	var b bytes.Buffer
	m.Write(&b)
	out := b.String()
	for _, want := range []string{
		"# TYPE ohs_queue_depth gauge\n",
		`ohs_queue_depth{queue="etl_standard"} 12` + "\n",
		`ohs_queue_depth{queue="pull"} 0` + "\n",
		`ohs_queue_oldest_job_age_seconds{queue="etl_standard"} 90` + "\n",
		"# TYPE ohs_job_duration_seconds histogram\n",
		`ohs_job_duration_seconds_bucket{queue="etl",kind="standard",outcome="completed",le="0.01"} 0` + "\n",
		`ohs_job_duration_seconds_bucket{queue="etl",kind="standard",outcome="completed",le="0.05"} 1` + "\n",
		`ohs_job_duration_seconds_bucket{queue="etl",kind="standard",outcome="completed",le="5"} 2` + "\n",
		`ohs_job_duration_seconds_bucket{queue="etl",kind="standard",outcome="completed",le="+Inf"} 2` + "\n",
		`ohs_job_duration_seconds_sum{queue="etl",kind="standard",outcome="completed"} 3.03` + "\n",
		`ohs_job_duration_seconds_count{queue="etl",kind="standard",outcome="completed"} 2` + "\n",
		// Past the last bucket: only +Inf holds it.
		`ohs_job_duration_seconds_bucket{queue="etl",kind="standard",outcome="failed",le="600"} 0` + "\n",
		`ohs_job_duration_seconds_bucket{queue="etl",kind="standard",outcome="failed",le="+Inf"} 1` + "\n",
		`ohs_job_duration_seconds_count{queue="pull",kind="backfill",outcome="deferred"} 1` + "\n",
		// A value on a bound falls in that bucket.
		`ohs_job_duration_seconds_bucket{queue="pull",kind="backfill",outcome="deferred",le="1"} 1` + "\n",
		`ohs_job_duration_seconds_bucket{queue="pull",kind="backfill",outcome="deferred",le="0.5"} 0` + "\n",
		"# TYPE ohs_pull_duration_seconds histogram\n",
		`ohs_pull_duration_seconds_count{provider="oura"} 1` + "\n",
		"# TYPE ohs_webhook_delivery_duration_seconds histogram\n",
		`ohs_webhook_delivery_duration_seconds_count{outcome="delivered"} 1` + "\n",
		`ohs_webhook_delivery_duration_seconds_bucket{outcome="failed",le="30"} 1` + "\n",
		"# TYPE ohs_etl_rows_written_total counter\n",
		`ohs_etl_rows_written_total{table="quantity_samples"} 42` + "\n",
		"# TYPE ohs_loop_last_tick_unix_seconds gauge\n",
		`ohs_loop_last_tick_unix_seconds{loop="etl_standard"} 1767323045` + "\n",
		`ohs_loop_last_tick_unix_seconds{loop="webhooks"} 1767323165` + "\n",
		`ohs_loop_stalled{loop="etl_standard"} 1` + "\n",
		`ohs_loop_stalled{loop="webhooks"} 0` + "\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "workouts") {
		t.Errorf("a table with no row written has a series:\n%s", out)
	}
	// Every line is a comment or `name{labels} value`: what a scraper parses.
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if strings.HasPrefix(line, "# ") {
			continue
		}
		if f := strings.Fields(line); len(f) != 2 || !strings.HasPrefix(f[0], "ohs_") {
			t.Errorf("malformed line %q", line)
		}
	}
}

func TestNilMetricsRecordsNothing(t *testing.T) {
	var m *Metrics
	m.JobDone("etl", "standard", "completed", time.Second)
	m.PullDone("oura", time.Second)
	m.WebhookDone("delivered", time.Second)
	m.ETLRows("workouts", 1)
	m.SetQueue("pull", QueueStat{})
	m.Write(&bytes.Buffer{})
}

func TestWriteSnapshot(t *testing.T) {
	var b bytes.Buffer
	WriteSnapshot(&b, "ohs_etl_", map[string]int64{"parked_jobs": 3, "drainer_replays_total": 7})
	want := "# TYPE ohs_etl_drainer_replays_total counter\nohs_etl_drainer_replays_total 7\n" +
		"# TYPE ohs_etl_parked_jobs gauge\nohs_etl_parked_jobs 3\n"
	if b.String() != want {
		t.Fatalf("got:\n%s", b.String())
	}
}
