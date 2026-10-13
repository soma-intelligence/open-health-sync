// SPDX-License-Identifier: AGPL-3.0-only

package recorded

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/applehealth/export"
	"github.com/soma-intelligence/open-health-sync/connectors/oura"
	"github.com/soma-intelligence/open-health-sync/connectors/oura/ourafake"
	"github.com/soma-intelligence/open-health-sync/connectors/withings"
	"github.com/soma-intelligence/open-health-sync/connectors/withings/withingsfake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/connector/cassette"
)

// The whole loop against the connectors' own fakes: record a Fetch,
// anonymize it, replay it. Replay must make exactly the recorded requests
// and map to the same record counts as the original answers, so
// anonymization keeps what the mapping depends on while it changes every
// identifier, date and value.
func TestRecordAnonymizeReplay(t *testing.T) {
	now := time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC)
	from, to := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), now

	cases := []struct {
		name string
		uid  string
		// build returns the provider, pointed at a fresh fake.
		build func(t *testing.T) Provider
	}{
		{"oura", "fake-oura-user-0001", func(t *testing.T) Provider {
			f := ourafake.New(t)
			p, _ := Lookup(oura.Slug)
			p.New = func(b Build) connector.Connector {
				return oura.New(oura.Options{APIBase: f.URL(), AccountBase: f.URL(), HTTPClient: b.HTTPClient, Now: b.Now, Sleep: b.Sleep, PageDelay: -1})
			}
			return p
		}},
		{"withings", "424242", func(t *testing.T) Provider {
			f := withingsfake.New(t)
			p, _ := Lookup(withings.Slug)
			p.New = func(b Build) connector.Connector {
				return withings.New(withings.Options{APIBase: f.URL(), AccountBase: f.URL(), HTTPClient: b.HTTPClient, Now: b.Now, Sleep: b.Sleep, ChunkDelay: -1, PageDelay: -1})
			}
			return p
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			p := tc.build(t)
			rec := cassette.NewRecorder(nil)
			b := Build{HTTPClient: rec.Client(), Now: func() time.Time { return now }, Sleep: noSleep, Fast: true}
			c := p.New(b)
			require.NoError(t, Configure(c, nil))
			o, _ := connector.AsOAuth(c)
			cn := connector.Connection{Provider: p.Slug, Tokens: connector.Tokens{Access: "fake-access-token-1", ProviderUserID: tc.uid}}

			// The original answers, mapped directly.
			want := directSummary(t, ctx, p, c, cn, from, to)
			require.NotEmpty(t, want, "the fake answered nothing")

			cas, err := RecordFetch(ctx, p, o, rec, cn, from, to, now, nil, "", cassette.Options{ShiftDays: -70})
			require.NoError(t, err)
			require.NotEmpty(t, cas.Interactions)
			require.Equal(t, from.AddDate(0, 0, -70), cas.Window.From)

			raw, err := cassette.Marshal(cas)
			require.NoError(t, err)
			require.Empty(t, cassette.Check(cas, raw))
			require.NotContains(t, string(raw), "fake-access-token-1")
			require.NotContains(t, string(raw), tc.uid)
			require.NotContains(t, string(raw), "2026-03-02", "a date was not shifted")

			got, rep, err := Replay(ctx, p, cas)
			require.NoError(t, err)
			require.True(t, rep.OK(), rep.String())
			require.Equal(t, want, got)
		})
	}
}

func noSleep(ctx context.Context, _ time.Duration) error { return ctx.Err() }

// directSummary fetches through c (no recording) and maps every line.
func directSummary(t *testing.T, ctx context.Context, p Provider, c connector.Connector, cn connector.Connection, from, to time.Time) Summary {
	t.Helper()
	o, _ := connector.AsOAuth(c)
	s := &sink{lines: map[string][][]byte{}}
	require.NoError(t, o.Fetch(ctx, cn, from, to, s))
	sum := Summary{}
	for _, dt := range s.order {
		ts := TypeSummary{Records: map[string]int{}}
		for _, line := range s.lines[dt] {
			ts.Lines++
			res, err := c.Map(connector.RawRecord{UserID: "recorded", Provider: p.Slug, DataType: dt, Fields: decode(t, line)})
			require.NoError(t, err, "%s: %s", dt, strings.SplitN(string(line), ",", 2)[0])
			count(ts.Records, res)
		}
		sum[dt] = ts
	}
	return sum
}

// The same loop for a device connector: an Apple Health export (the
// converter's fixture) becomes upload parts, anonymized, then replayed.
func TestUploadAnonymizeReplay(t *testing.T) {
	ctx := context.Background()
	p, _ := Lookup("apple_health")
	e, err := export.Open(filepath.Join("..", "..", "connectors", "applehealth", "export", "testdata", "apple_health_export", "export.xml"))
	require.NoError(t, err)
	defer e.Close()
	var parts []cassette.RawUpload
	_, err = export.Convert(ctx, e, export.Options{}, func(pt export.Part) error {
		parts = append(parts, cassette.RawUpload{Type: pt.Type, Day: pt.Day, Lines: bytes.Split(bytes.TrimSpace(pt.NDJSON), []byte("\n"))})
		return nil
	})
	require.NoError(t, err)
	require.NotEmpty(t, parts)

	// The original lines, mapped directly.
	want := Summary{}
	c := p.New(Build{})
	for _, pt := range parts {
		ts, ok := want[pt.Type]
		if !ok {
			ts = TypeSummary{Records: map[string]int{}}
		}
		for _, l := range pt.Lines {
			ts.Lines++
			res, err := c.Map(connector.RawRecord{UserID: "recorded", Provider: p.Slug, DataType: pt.Type, Fields: decode(t, l)})
			require.NoError(t, err)
			count(ts.Records, res)
		}
		want[pt.Type] = ts
	}

	now := time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)
	cas, err := cassette.AnonymizeUploads(parts, cassette.Meta{Provider: p.Slug, APIVersion: p.APIVersion, RecordedAt: now, Days: 1, From: now.AddDate(0, 0, -1), To: now, Now: now}, cassette.Options{})
	require.NoError(t, err)
	raw, err := cassette.Marshal(cas)
	require.NoError(t, err)
	require.Empty(t, cassette.Check(cas, raw))
	for _, s := range []string{"Test Watch", "Test Cuff", "2026-07-01"} {
		require.NotContains(t, string(raw), s)
	}

	got, rep, err := Replay(ctx, p, cas)
	require.NoError(t, err)
	require.True(t, rep.OK(), rep.String())
	require.Equal(t, want, got)
}
