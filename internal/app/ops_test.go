// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/api"
	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/ops"
	"github.com/soma-intelligence/open-health-sync/internal/queue"
)

// A taken address stops the start when the operator chose it. When it is the
// default of a worker, the worker starts without the listener and says so:
// two workers on one host must not stop each other at an upgrade.
func TestServeOpsAddressTaken(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(prev)
	started := 0
	goRole := func(string, func() error) { started++ }

	cfg := config.Config{OpsListen: taken.Addr().String()}
	err = serveOps(context.Background(), cfg, api.OpsDeps{}, time.Second, slog.Default(), goRole)
	if err == nil || !strings.Contains(err.Error(), "OHS_OPS_LISTEN") {
		t.Fatalf("an address the operator set: %v", err)
	}

	cfg.OpsListenDefault = true
	if err := serveOps(context.Background(), cfg, api.OpsDeps{}, time.Second, slog.Default(), goRole); err != nil {
		t.Fatalf("the default address: %v", err)
	}
	if started != 0 {
		t.Fatalf("%d goroutines started without a listener", started)
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "OHS_OPS_LISTEN") {
		t.Fatalf("no warning naming the setting: %s", logs.String())
	}
}

// The ETL hooks turn what the queue sees into the tiers' heartbeats and the
// job durations.
func TestETLHooksReportLoopsAndJobs(t *testing.T) {
	loops := ops.NewRegistry().WithMinStall(0)
	m := ops.NewMetrics(loops)
	h := etlHooks(loops, m, 50*time.Millisecond)

	h.Poll(queue.TierStandard)
	job := queue.ETLJob{ID: 1, Tier: queue.TierStandard}
	h.Claimed(job)
	h.Settled(job, "completed")
	h.Settled(queue.ETLJob{ID: 2, Tier: queue.TierHighFreq}, "failed") // never claimed here: not observed

	var b bytes.Buffer
	m.Write(&b)
	out := b.String()
	if !strings.Contains(out, `ohs_job_duration_seconds_count{queue="etl",kind="standard",outcome="completed"} 1`) {
		t.Errorf("the settled job is not observed:\n%s", out)
	}
	if strings.Contains(out, `kind="high_freq"`) {
		t.Errorf("a job this process never claimed is observed:\n%s", out)
	}
	for _, loop := range []string{"etl_standard", "etl_high_freq"} {
		if !strings.Contains(out, `ohs_loop_last_tick_unix_seconds{loop="`+loop+`"}`) {
			t.Errorf("loop %s is not registered:\n%s", loop, out)
		}
	}
}
