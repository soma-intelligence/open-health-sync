// SPDX-License-Identifier: AGPL-3.0-only

//go:build record

package recorded

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/applehealth/export"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/connector/cassette"
)

// TestRecord records one cassette from a real account. Never run in CI: it
// needs the record build tag, the provider's operator keys and a real
// account. See docs/connectors/recording.md. In short:
//
//	OHS_RECORD_PROVIDER=withings OHS_WITHINGS_CLIENT_ID=... OHS_WITHINGS_CLIENT_SECRET=... \
//	  go test -tags record -run TestRecord -v -timeout 30m ./test/recorded/
//
//	OHS_RECORD_PROVIDER=apple_health OHS_RECORD_EXPORT=~/Downloads/export.zip \
//	  go test -tags record -run TestRecord -v -timeout 30m ./test/recorded/
//
// A cloud connector signs in through the provider's consent page (the test
// prints the address to open; the developer app must allow the redirect
// OHS_RECORD_REDIRECT_URL, default http://localhost:8765/callback), or uses
// OHS_RECORD_ACCESS_TOKEN (and OHS_RECORD_PROVIDER_USER_ID) when set. It
// pulls OHS_RECORD_DAYS days (default 7, at most 30), anonymizes them in
// memory, checks the result replays, and writes
// testdata/<slug>/<OHS_RECORD_NAME or the day>.json and its summary. It
// prints counts only. Read the cassette before committing it.
func TestRecord(t *testing.T) {
	slug := strings.TrimSpace(os.Getenv("OHS_RECORD_PROVIDER"))
	if slug == "" {
		t.Skip("OHS_RECORD_PROVIDER is not set")
	}
	p, ok := Lookup(slug)
	require.True(t, ok, "no connector %q", slug)
	days := 7
	if s := os.Getenv("OHS_RECORD_DAYS"); s != "" {
		n, err := strconv.Atoi(s)
		require.NoError(t, err, "OHS_RECORD_DAYS")
		days = min(max(n, 1), 30)
	}
	now := time.Now().UTC().Truncate(time.Second)
	from := now.AddDate(0, 0, -days)
	note := os.Getenv("OHS_RECORD_NOTE")
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()

	var cas *cassette.Cassette
	switch p.Kind {
	case cassette.KindUpload:
		cas = recordUpload(t, ctx, p, from, now, days, note)
	default:
		cas = recordHTTP(t, ctx, p, from, now, note)
	}

	sum, rep, err := Replay(ctx, p, cas)
	require.NoError(t, err)
	require.True(t, rep.OK(), "the anonymized recording does not replay; add the field the mapping needs to the provider's Keep list:%s", rep.String())
	name := os.Getenv("OHS_RECORD_NAME")
	if name == "" {
		name = now.Format(time.DateOnly)
	}
	dir := filepath.Join("testdata", p.Slug)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, name+".json")
	require.NoError(t, cassette.Save(path, cas))
	b, err := MarshalSummary(sum)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, name+".summary.json"), b, 0o644))
	for dt, ts := range sum {
		t.Logf("%s: %d lines, %v", dt, ts.Lines, ts.Records)
	}
	t.Logf("wrote %s; read it before committing, then run go test ./test/recorded -run TestStatusPage -update", path)
}

// providerKeys reads OHS_<SLUG>_<FIELD> from the environment.
func providerKeys(slug string) map[string]string {
	prefix := "OHS_" + strings.ToUpper(slug) + "_"
	out := map[string]string{}
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, prefix) {
			out[strings.TrimPrefix(k, prefix)] = v
		}
	}
	return out
}

func recordHTTP(t *testing.T, ctx context.Context, p Provider, from, now time.Time, note string) *cassette.Cassette {
	cfg := providerKeys(p.Slug)
	rec := cassette.NewRecorder(nil)
	hc := rec.Client()
	hc.Timeout = 2 * time.Minute
	c := p.New(Build{HTTPClient: hc, Now: time.Now, Config: cfg})
	require.NoError(t, Configure(c, cfg))
	o, ok := connector.AsOAuth(c)
	require.True(t, ok)

	var tok connector.Tokens
	if a := os.Getenv("OHS_RECORD_ACCESS_TOKEN"); a != "" {
		tok = connector.Tokens{Access: a, ProviderUserID: os.Getenv("OHS_RECORD_PROVIDER_USER_ID")}
	} else {
		tok = signIn(t, ctx, o)
	}
	secrets := []string{}
	for _, v := range cfg {
		secrets = append(secrets, v)
	}
	cas, err := RecordFetch(ctx, p, o, rec, connector.Connection{Provider: p.Slug, Tokens: tok}, from, now, now, cfg, note,
		cassette.Options{Secrets: secrets})
	require.NoError(t, err)
	return cas
}

// signIn runs the OAuth consent on a local callback and returns the tokens.
// The exchange is not recorded.
func signIn(t *testing.T, ctx context.Context, o connector.OAuthConnector) connector.Tokens {
	redirect := os.Getenv("OHS_RECORD_REDIRECT_URL")
	if redirect == "" {
		redirect = "http://localhost:8765/callback"
	}
	ru, err := url.Parse(redirect)
	require.NoError(t, err)
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	state := hex.EncodeToString(b)
	auth, err := o.AuthorizeURL(state, redirect)
	require.NoError(t, err)

	codes := make(chan string, 1)
	ln, err := net.Listen("tcp", ru.Host)
	require.NoError(t, err, "listen on the redirect address")
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != ru.Path || r.URL.Query().Get("state") != state {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintln(w, "Signed in. You can close this tab and go back to the terminal.")
		select {
		case codes <- r.URL.Query().Get("code"):
		default:
		}
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	fmt.Fprintf(os.Stderr, "\nOpen this address, sign in and allow access:\n\n  %s\n\n", auth)
	var code string
	select {
	case code = <-codes:
	case <-ctx.Done():
		t.Fatal("no sign-in before the timeout")
	}
	require.NotEmpty(t, code, "the provider sent no code: was access refused?")
	var tok connector.Tokens
	if se, ok := o.(connector.StateExchanger); ok {
		tok, err = se.ExchangeState(ctx, code, state, redirect)
	} else {
		tok, err = o.Exchange(ctx, code, redirect)
	}
	require.NoError(t, err, "token exchange")
	return tok
}

func recordUpload(t *testing.T, ctx context.Context, p Provider, from, now time.Time, days int, note string) *cassette.Cassette {
	path := os.Getenv("OHS_RECORD_EXPORT")
	require.NotEmpty(t, path, "OHS_RECORD_EXPORT: the export.zip to record from")
	require.Equal(t, "apple_health", p.Slug, "only Apple Health exports can be recorded today")
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, path[2:])
	}
	e, err := export.Open(path)
	require.NoError(t, err)
	defer e.Close()
	var parts []cassette.RawUpload
	_, err = export.Convert(ctx, e, export.Options{Since: from.Format(time.DateOnly)}, func(pt export.Part) error {
		parts = append(parts, cassette.RawUpload{Type: pt.Type, Day: pt.Day, Lines: bytes.Split(bytes.TrimSpace(pt.NDJSON), []byte("\n"))})
		return nil
	})
	require.NoError(t, err)
	require.NotEmpty(t, parts, "the export holds nothing in the last %d days", days)
	cas, err := cassette.AnonymizeUploads(parts, cassette.Meta{
		Provider: p.Slug, APIVersion: p.APIVersion, RecordedAt: now, Days: days, From: from, To: now, Now: now, Note: note,
	}, cassette.Options{Keep: p.Keep})
	require.NoError(t, err)
	return cas
}
