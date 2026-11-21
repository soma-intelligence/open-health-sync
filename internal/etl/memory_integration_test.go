// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package etl

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/applehealth"
	"github.com/soma-intelligence/open-health-sync/internal/queue"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
)

// The memory of one ETL job on a large part.
//
// TestLargePartPeakHeap processes a synthetic Apple Health part, sealed and
// clear, and samples the Go heap while the job runs. The part is 8 MiB of
// NDJSON by default, which keeps the suite quick and still spans several write
// chunks; OHS_ETL_MEMBENCH_MIB sets another size (64 is the largest part the
// upload endpoint accepts by default):
//
//	OHS_ETL_MEMBENCH_MIB=64 go test -tags integration -count=1 -run TestLargePartPeakHeap -v ./internal/etl/
//
// The peak is the largest HeapAlloc seen (live objects plus garbage not
// collected yet) above the heap in use before the job started. The test runs
// under a soft memory limit, as a container does (internal/memlimit), so the
// figure is what the process needs rather than what an idle collector lets
// pile up.

// memBenchLimit is the soft memory limit of the measured job, and the bound
// on its peak heap.
const memBenchLimit = 256 << 20

// heapSampler records the largest heap seen while it runs.
type heapSampler struct {
	stop     chan struct{}
	done     sync.WaitGroup
	peak     uint64 // HeapAlloc
	peakSys  uint64 // HeapSys
	baseline uint64
}

func startHeapSampler() *heapSampler {
	runtime.GC()
	debug.FreeOSMemory()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	s := &heapSampler{stop: make(chan struct{}), baseline: ms.HeapAlloc}
	s.done.Add(1)
	go func() {
		defer s.done.Done()
		tick := time.NewTicker(2 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-tick.C:
				var ms runtime.MemStats
				runtime.ReadMemStats(&ms)
				s.peak = max(s.peak, ms.HeapAlloc)
				s.peakSys = max(s.peakSys, ms.HeapSys)
			}
		}
	}()
	return s
}

// end stops the sampler and returns the peak heap above the baseline.
func (s *heapSampler) end() uint64 {
	close(s.stop)
	s.done.Wait()
	if s.peak < s.baseline {
		return 0
	}
	return s.peak - s.baseline
}

// largePartTypes are the HealthKit types of the synthetic part, with their
// unit and a plausible value for sample i.
var largePartTypes = []struct {
	id, unit string
	value    func(i int) float64
}{
	{"HKQuantityTypeIdentifierHeartRate", "count/min", func(i int) float64 { return 55 + float64(i%90) + float64(i%7)/10 }},
	{"HKQuantityTypeIdentifierStepCount", "count", func(i int) float64 { return float64(5 + i%140) }},
	{"HKQuantityTypeIdentifierHeartRate", "count/min", func(i int) float64 { return 60 + float64(i%70) + float64(i%9)/10 }},
	{"HKQuantityTypeIdentifierActiveEnergyBurned", "kcal", func(i int) float64 { return 0.5 + float64(i%300)/40 }},
	{"HKQuantityTypeIdentifierHeartRate", "count/min", func(i int) float64 { return 58 + float64(i%80) + float64(i%3)/10 }},
	{"HKQuantityTypeIdentifierDistanceWalkingRunning", "m", func(i int) float64 { return 3 + float64(i%900)/7 }},
	{"HKQuantityTypeIdentifierBasalEnergyBurned", "kcal", func(i int) float64 { return 1 + float64(i%50)/30 }},
	{"HKQuantityTypeIdentifierOxygenSaturation", "%", func(i int) float64 { return 0.93 + float64(i%6)/100 }},
}

// writeLargePart writes about size bytes of Apple Health NDJSON, gzipped, to
// w, one sample every 20 seconds from start, and returns the number of lines.
// Every line carries its own UUID, as the SDK sends it, so the lines neither
// repeat nor compress into nothing.
func writeLargePart(w io.Writer, size int64, start time.Time) (lines int, err error) {
	gz := gzip.NewWriter(w)
	bw := bufio.NewWriterSize(gz, 1<<20)
	var written int64
	for i := 0; written < size; i++ {
		ty := largePartTypes[i%len(largePartTypes)]
		at := start.Add(time.Duration(i) * 20 * time.Second).Format("2006-01-02T15:04:05.000Z")
		// A UUID derived from the position: the part is the same on every run.
		id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(strconv.Itoa(i)))
		n, err := fmt.Fprintf(bw, `{"uuid":"%s","device":"Apple Watch","deviceModel":"Watch","endDate":"%s","metadata":{"HKTimeZone":"Europe/Paris"},"productType":"Watch7,1","quantityType":"%s","sourceBundleId":"com.apple.health.ABC","sourceName":"Apple Watch","startDate":"%s","unit":"%s","value":%s,"wasUserEntered":false}`+"\n",
			id, at, ty.id, at, ty.unit, strconv.FormatFloat(ty.value(i), 'f', -1, 64))
		if err != nil {
			return 0, err
		}
		written += int64(n)
		lines++
	}
	if err := bw.Flush(); err != nil {
		return 0, err
	}
	return lines, gz.Close()
}

// uploadLargePart stores a synthetic part of about size bytes of NDJSON for
// the env's user without holding it in memory, and returns its job and its
// number of lines.
func (e *env) uploadLargePart(size int64) (queue.ETLJob, int) {
	e.t.Helper()
	ctx := context.Background()
	batch := fmt.Sprintf("b%d", batchSeq.Add(1))
	key := rawstore.Key(e.tenant, e.user, applehealth.Slug, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), batch, 0)
	pr, pw := io.Pipe()
	var lines int
	go func() {
		n, err := writeLargePart(pw, size, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
		lines = n
		_ = pw.CloseWithError(err)
	}()
	require.NoError(e.t, e.rs.Put(ctx, key, pr))
	sum := sha256.Sum256([]byte(batch))
	_, err := e.pool.Exec(ctx, `
		INSERT INTO ohs_sync.raw_objects (tenant_id, user_id, provider, key, content_hash, size_bytes, batch, part, data_type)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 0, 'mixed')`,
		e.tenant, e.user, applehealth.Slug, key, hex.EncodeToString(sum[:]), size, batch)
	require.NoError(e.t, err)
	return queue.ETLJob{TenantID: e.tenant, UserID: e.user, Provider: applehealth.Slug, Key: key, Tier: queue.TierStandard}, lines
}

func TestLargePartPeakHeap(t *testing.T) {
	mib := int64(8)
	if v := os.Getenv("OHS_ETL_MEMBENCH_MIB"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		require.NoError(t, err, "OHS_ETL_MEMBENCH_MIB")
		mib = n
	}
	// OHS_ETL_MEMBENCH_REPORT=1 only reports the peak: the figure of a build
	// that does not hold the bound yet.
	reportOnly := os.Getenv("OHS_ETL_MEMBENCH_REPORT") != ""
	for _, mode := range []struct {
		name  string
		clear bool
	}{{"sealed", false}, {"clear", true}} {
		t.Run(mode.name, func(t *testing.T) {
			e := newEnv(t)
			e.reg.Register(applehealth.New())
			job, lines := e.uploadLargePart(mib << 20)
			opts := e.opts()
			if mode.clear {
				opts = e.clearOpts()
			}
			// The chunk of the default profile, whatever the limit below
			// would pick (memlimit.Low); OHS_ETL_MEMBENCH_CHUNK sets another.
			opts.Writer.ChunkRecords = DefaultChunkRecords
			if v := os.Getenv("OHS_ETL_MEMBENCH_CHUNK"); v != "" {
				n, err := strconv.Atoi(v)
				require.NoError(t, err, "OHS_ETL_MEMBENCH_CHUNK")
				opts.Writer.ChunkRecords = n
			}

			limit := int64(memBenchLimit)
			if v := os.Getenv("OHS_ETL_MEMBENCH_LIMIT_MIB"); v != "" {
				// 0 runs without a limit, as a process outside a container.
				n, err := strconv.ParseInt(v, 10, 64)
				require.NoError(t, err, "OHS_ETL_MEMBENCH_LIMIT_MIB")
				if limit = n << 20; n == 0 {
					limit = math.MaxInt64
				}
			}
			prev := debug.SetMemoryLimit(limit)
			defer debug.SetMemoryLimit(prev)
			sampler := startHeapSampler()
			began := time.Now()
			counts, err := ProcessWith(context.Background(), e.pool, e.rs, job, opts)
			took := time.Since(began)
			peak := sampler.end()
			require.NoError(t, err)

			t.Logf("%s: %d MiB of NDJSON, %d lines, chunks of %d, %s: peak heap %d MiB above a baseline of %d MiB (heap from the OS: %d MiB)",
				mode.name, mib, lines, opts.Writer.ChunkRecords, took.Round(time.Millisecond), peak>>20, sampler.baseline>>20, sampler.peakSys>>20)
			assert.Equal(t, lines, counts[tQuantity].Written, "every line is one new sample")
			assert.Equal(t, lines, e.count(tQuantity))
			if !reportOnly {
				assert.Less(t, peak, uint64(memBenchLimit), "peak heap of one job")
			}
		})
	}
}
