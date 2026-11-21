// SPDX-License-Identifier: AGPL-3.0-only

package sandbox

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/demo"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

const callback = "http://ohs.example.test/oauth/sandbox/callback"

// clock is a settable test clock.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newTest(t *testing.T) (*Connector, *clock) {
	t.Helper()
	clk := &clock{t: time.Date(2026, 3, 20, 9, 0, 0, 0, time.UTC)}
	c := New(Options{Now: clk.now})
	require.NoError(t, c.Configure(connector.Config{KeyEnabled: "true"}))
	return c, clk
}

type sink struct {
	mu    sync.Mutex
	lines map[string][][]byte
}

func (s *sink) Emit(_ context.Context, dt string, _ time.Time, lines [][]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lines == nil {
		s.lines = map[string][][]byte{}
	}
	s.lines[dt] = append(s.lines[dt], lines...)
	return nil
}

var hrefRE = regexp.MustCompile(`href="([^"]+)"`)

// consent follows the authorize URL to the consent page and returns the
// callback link of the persona's choice.
func consent(t *testing.T, c *Connector, authorize, persona string) url.Values {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, authorize, nil)
	ctype, page, err := c.AnswerChallenge(req)
	require.NoError(t, err)
	assert.Equal(t, "text/html; charset=utf-8", ctype)
	assert.Contains(t, string(page), "This is not a real provider.")
	for _, m := range hrefRE.FindAllStringSubmatch(string(page), -1) {
		u, err := url.Parse(html.UnescapeString(m[1]))
		require.NoError(t, err)
		code := u.Query().Get("code")
		if code == "" {
			continue
		}
		if cl, err := c.Server().verify(code, "code"); err == nil && cl.Persona == persona {
			assert.Equal(t, callback, u.Scheme+"://"+u.Host+u.Path)
			return u.Query()
		}
	}
	t.Fatalf("no link for persona %s", persona)
	return nil
}

func TestOffUnlessEnabled(t *testing.T) {
	c := New(Options{})
	assert.False(t, c.Enabled(connector.Config{}))
	assert.False(t, c.Enabled(connector.Config{KeyEnabled: "false"}))
	assert.False(t, c.Enabled(connector.Config{KeySecret: "s"}))
	assert.True(t, c.Enabled(connector.Config{KeyEnabled: "true"}))
	assert.True(t, c.Enabled(connector.Config{KeyEnabled: "1"}))
	assert.Error(t, c.Configure(connector.Config{}), "Configure refuses a configuration that does not turn it on")

	r := connector.NewRegistry()
	r.Register(c)
	assert.Empty(t, r.Enabled(nil), "no key: not enabled")
	assert.Len(t, r.Enabled(map[string]connector.Config{Slug: {KeyEnabled: "true"}}), 1)
	_, keyed := any(c).(connector.KeyedConnector)
	assert.False(t, keyed, "no tenant keys: the sandbox is the operator's switch only")
	_, inst := any(c).(connector.Instancer)
	assert.False(t, inst)
}

func TestAuthorizeURL(t *testing.T) {
	c, _ := newTest(t)
	u, err := c.AuthorizeURL("st-1", callback)
	require.NoError(t, err)
	p, err := url.Parse(u)
	require.NoError(t, err)
	assert.Equal(t, "http://ohs.example.test/inbound/sandbox", p.Scheme+"://"+p.Host+p.Path, "served by the instance itself")
	assert.Equal(t, "code", p.Query().Get("response_type"))
	assert.Equal(t, ClientID, p.Query().Get("client_id"))
	assert.Equal(t, callback, p.Query().Get("redirect_uri"))
	assert.Equal(t, "st-1", p.Query().Get("state"))

	_, err = c.AuthorizeURL("st", "https://elsewhere.test/oauth/oura/callback")
	assert.Error(t, err)
}

// The whole flow, in process: consent page, code, exchange, pulls, maps.
func TestConnectFetchMap(t *testing.T) {
	c, clk := newTest(t)
	authorize, err := c.AuthorizeURL("st-1", callback)
	require.NoError(t, err)
	cb := consent(t, c, authorize, demo.PersonaRunner)
	assert.Equal(t, "st-1", cb.Get("state"))

	tok, err := c.Exchange(context.Background(), cb.Get("code"), callback)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(tok.Access, "sbx_"))
	assert.NotEmpty(t, tok.Refresh)
	assert.True(t, strings.HasPrefix(tok.ProviderUserID, "sbx-runner-"))
	assert.True(t, tok.Expiry.Equal(clk.now().Add(DefaultAccessTTL)))

	_, err = c.Exchange(context.Background(), cb.Get("code"), "http://other.test/oauth/sandbox/callback")
	assert.ErrorIs(t, err, connector.ErrNeedsReauth, "a code is bound to its redirect_uri")

	from := clk.now().AddDate(0, 0, -14)
	s := &sink{}
	conn := connector.Connection{Provider: Slug, Tokens: tok}
	require.NoError(t, c.Fetch(context.Background(), conn, from, clk.now(), s))
	for _, dt := range AllDataTypes {
		assert.NotEmpty(t, s.lines[dt], dt)
	}

	kinds := map[string]int{}
	for dt, lines := range s.lines {
		for _, l := range lines {
			var f map[string]any
			require.NoError(t, json.Unmarshal(l, &f))
			res, err := c.Map(connector.RawRecord{UserID: "u1", Provider: Slug, DataType: dt, Fields: f})
			require.NoError(t, err, dt)
			for _, q := range res.QuantitySamples {
				kinds[q.QuantityType]++
				assert.Equal(t, Slug, q.Provider)
				assert.Equal(t, Slug, q.SourceSlug)
				assert.Equal(t, SourceName, q.SourceName)
				assert.Equal(t, true, q.Metadata[demo.MetadataSynthetic])
				assert.Equal(t, "runner", q.Metadata["sandbox_persona"])
				assert.False(t, q.StartDate.Before(from))
				assert.True(t, q.StartDate.Before(clk.now()), "nothing from the future")
			}
			for _, w := range res.Workouts {
				kinds["workout"]++
				assert.Equal(t, Slug, w.Provider)
				assert.Equal(t, true, w.Metadata[demo.MetadataSynthetic])
				assert.NotEmpty(t, w.ActivityType)
			}
			for _, sl := range res.SleepSessions {
				kinds["sleep"]++
				assert.Equal(t, Slug, sl.Provider)
				assert.Equal(t, true, sl.Metadata[demo.MetadataSynthetic])
				assert.NotEmpty(t, res.SleepPhases)
				for _, p := range res.SleepPhases {
					assert.Equal(t, sl.ContentHash, p.ParentContentHash)
				}
			}
		}
	}
	// Nights from the evening of Mar 6 to the one ending Mar 19: the night
	// ending this morning belongs to a day that is not over yet.
	assert.Equal(t, 13, kinds["sleep"], "one night a day: %v", kinds)
	assert.Positive(t, kinds[model.QTHeartRate])
	assert.Positive(t, kinds[model.QTStepCount])
	assert.Positive(t, kinds["workout"], "the runner runs on most days")
}

// The same dates give the same records whatever the window.
func TestDeterministicAcrossWindows(t *testing.T) {
	now := time.Date(2026, 3, 20, 9, 0, 0, 0, time.UTC)
	from := now.AddDate(0, 0, -10)
	mid := now.AddDate(0, 0, -4)
	a, err := generate(demo.PersonaMix, from, now, now)
	require.NoError(t, err)
	b, err := generate(demo.PersonaMix, mid, now, now)
	require.NoError(t, err)
	inB := map[string]bool{}
	for _, s := range b.Sleep {
		bs, _ := json.Marshal(s)
		inB[string(bs)] = true
	}
	n := 0
	for _, s := range a.Sleep {
		st, _ := time.Parse(time.RFC3339, s.Start)
		if st.Before(mid) {
			continue
		}
		as, _ := json.Marshal(s)
		assert.True(t, inB[string(as)], "night %s differs between windows", s.Start)
		n++
	}
	assert.Equal(t, len(b.Sleep), n)
	assert.Positive(t, n)

	again, err := generate(demo.PersonaMix, from, now, now)
	require.NoError(t, err)
	assert.Equal(t, a, again)

	future, err := generate(demo.PersonaMix, now, now.AddDate(0, 0, 3), now)
	require.NoError(t, err)
	assert.Empty(t, future.Sleep, "no data after now")
}

func TestRefreshAndExpiry(t *testing.T) {
	clk := &clock{t: time.Date(2026, 3, 20, 9, 0, 0, 0, time.UTC)}
	c := New(Options{Now: clk.now, AccessTTL: time.Minute})
	require.NoError(t, c.Configure(connector.Config{KeyEnabled: "true"}))
	authorize, err := c.AuthorizeURL("st-2", callback)
	require.NoError(t, err)
	tok, err := c.Exchange(context.Background(), consent(t, c, authorize, demo.PersonaSleep).Get("code"), callback)
	require.NoError(t, err)

	clk.add(2 * time.Minute)
	conn := connector.Connection{Provider: Slug, Tokens: tok}
	err = c.Fetch(context.Background(), conn, clk.now().AddDate(0, 0, -2), clk.now(), &sink{})
	assert.ErrorIs(t, err, connector.ErrNeedsReauth, "an expired access token is refused")

	nt, err := c.Refresh(context.Background(), tok)
	require.NoError(t, err)
	assert.NotEqual(t, tok.Access, nt.Access)
	assert.Equal(t, tok.ProviderUserID, nt.ProviderUserID)
	conn.Tokens = nt
	require.NoError(t, c.Fetch(context.Background(), conn, clk.now().AddDate(0, 0, -2), clk.now(), &sink{}))
	assert.EqualValues(t, 1, c.Server().refreshes.Load())

	// Tokens signed with another secret are refused.
	other := New(Options{Now: clk.now})
	require.NoError(t, other.Configure(connector.Config{KeyEnabled: "true", KeySecret: "another-secret"}))
	_, err = other.Refresh(context.Background(), nt)
	assert.ErrorIs(t, err, connector.ErrNeedsReauth)
	_, err = c.Refresh(context.Background(), connector.Tokens{})
	assert.ErrorIs(t, err, connector.ErrNeedsReauth)
}

func TestConsentPageRefusals(t *testing.T) {
	c, _ := newTest(t)
	// A plain probe: an empty 200.
	ctype, body, err := c.AnswerChallenge(httptest.NewRequest(http.MethodGet, "http://ohs.example.test/inbound/sandbox", nil))
	require.NoError(t, err)
	assert.Empty(t, ctype)
	assert.Nil(t, body)

	for name, q := range map[string]url.Values{
		"other host":     {"response_type": {"code"}, "client_id": {ClientID}, "state": {"s"}, "redirect_uri": {"https://evil.test/oauth/sandbox/callback"}},
		"other path":     {"response_type": {"code"}, "client_id": {ClientID}, "state": {"s"}, "redirect_uri": {"http://ohs.example.test/elsewhere"}},
		"no state":       {"response_type": {"code"}, "client_id": {ClientID}, "redirect_uri": {callback}},
		"unknown client": {"response_type": {"code"}, "client_id": {"x"}, "state": {"s"}, "redirect_uri": {callback}},
		"token flow":     {"response_type": {"token"}, "client_id": {ClientID}, "state": {"s"}, "redirect_uri": {callback}},
	} {
		_, _, err := c.AnswerChallenge(httptest.NewRequest(http.MethodGet, "http://ohs.example.test/inbound/sandbox?"+q.Encode(), nil))
		assert.Error(t, err, name)
	}

	_, err = c.HandleInbound(context.Background(), httptest.NewRequest(http.MethodPost, "/inbound/sandbox", strings.NewReader("{}")))
	assert.ErrorIs(t, err, errNoNotifications)
}

// The connector reaches a Server over a real network too.
func TestOverHTTP(t *testing.T) {
	inner, clk := newTest(t)
	srv := httptest.NewServer(inner.Server())
	t.Cleanup(srv.Close)
	c := New(Options{Now: clk.now, Base: srv.URL, HTTPClient: srv.Client()})
	c.mu.Lock()
	c.server = inner.Server()
	c.mu.Unlock()

	authorize, err := c.AuthorizeURL("st-3", callback)
	require.NoError(t, err)
	tok, err := c.Exchange(context.Background(), consent(t, c, authorize, demo.PersonaWeight).Get("code"), callback)
	require.NoError(t, err)
	s := &sink{}
	require.NoError(t, c.Fetch(context.Background(), connector.Connection{Tokens: tok}, clk.now().AddDate(0, 0, -3), clk.now(), s))
	assert.NotEmpty(t, s.lines[DataTypeSteps])

	res, err := http.Get(srv.URL + "/v1/sleep?start=x&end=y")
	require.NoError(t, err)
	res.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, res.StatusCode)
}

func TestMapRefusals(t *testing.T) {
	c := New(Options{})
	for dt, f := range map[string]map[string]any{
		DataTypeSleep:     {"id": "x", "start": "bad", "end": "2026-03-01T07:00:00Z"},
		DataTypeHeartRate: {"time": "bad", "bpm": 60},
		DataTypeSteps:     {"start": "2026-03-01T07:00:00Z", "end": "2026-03-01T06:00:00Z", "count": 1},
		DataTypeWorkouts:  {"start": "2026-03-01T07:00:00Z", "end": "2026-03-01T08:00:00Z"},
	} {
		_, err := c.Map(connector.RawRecord{DataType: dt, Fields: f})
		assert.Error(t, err, dt)
	}
	res, err := c.Map(connector.RawRecord{DataType: "unknown", Fields: map[string]any{}})
	require.NoError(t, err)
	assert.Empty(t, res.Workouts)
}
