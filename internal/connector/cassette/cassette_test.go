// SPDX-License-Identifier: AGPL-3.0-only

package cassette

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var t0 = time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)

func meta() Meta {
	return Meta{Provider: "acme", APIVersion: "v1", RecordedAt: t0, Days: 7, From: t0.AddDate(0, 0, -7), To: t0, Now: t0}
}

func exchange(url, body string) RawExchange {
	return RawExchange{Method: http.MethodGet, URL: url, Status: 200,
		Header: http.Header{"Content-Type": {"application/json"}, "Set-Cookie": {"s=1"}, "X-Request-Id": {"abc"}},
		Body:   []byte(body)}
}

func anonymize(t *testing.T, ex []RawExchange, o Options) (*Cassette, string) {
	t.Helper()
	if o.Salt == nil {
		o.Salt = []byte("test salt")
	}
	if o.ShiftDays == 0 {
		o.ShiftDays = -14
	}
	c, err := AnonymizeHTTP(ex, meta(), o)
	require.NoError(t, err)
	b, err := Marshal(c)
	require.NoError(t, err)
	return c, string(b)
}

func body(t *testing.T, c *Cassette, i int) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(c.Interactions[i].Response.Body, &m))
	return m
}

func TestCredentialsAndHeadersAreDropped(t *testing.T) {
	ex := exchange("https://api.example.com/v1/data?access_token=sekret-token-1&client_secret=cs-999&limit=50", `{"refresh_token":"rt-abcdef","ok":true}`)
	ex.ReqBody, ex.ReqType, ex.Method = []byte("code=auth-code-1&grant_type=x"), "application/x-www-form-urlencoded", http.MethodPost
	c, raw := anonymize(t, []RawExchange{ex}, Options{Secrets: []string{"sekret-token-1"}})
	for _, s := range []string{"sekret-token-1", "cs-999", "rt-abcdef", "auth-code-1", "Set-Cookie", "X-Request-Id"} {
		assert.NotContains(t, raw, s)
	}
	assert.Equal(t, map[string]string{"Content-Type": "application/json"}, c.Interactions[0].Response.Header)
	assert.Contains(t, c.Interactions[0].Request.URL, "limit=50")
	assert.Contains(t, c.Interactions[0].Request.Body, "grant_type=x")
}

func TestIdentifiersGetConsistentStandIns(t *testing.T) {
	ex := []RawExchange{
		exchange("https://api.example.com/v1/users/7654321/sleep", `{"data":[{"id":"9b2c3d4e-1111-4222-8333-944455556666","user_id":7654321,"name":"Jane Doe","email":"jane@example.com"}],"next_token":"tok-page-2"}`),
		exchange("https://api.example.com/v1/users/7654321/sleep?next_token=tok-page-2", `{"data":[],"next_token":null}`),
		exchange("https://api.example.com/v1/sleep/9b2c3d4e-1111-4222-8333-944455556666", `{}`),
	}
	c, raw := anonymize(t, ex, Options{ProviderUserID: "7654321"})
	for _, s := range []string{"7654321", "9b2c3d4e-1111-4222-8333-944455556666", "Jane", "jane@example.com", "tok-page-2"} {
		assert.NotContains(t, raw, s)
	}
	b := body(t, c, 0)
	d := b["data"].([]any)[0].(map[string]any)
	uid := strconv.FormatFloat(d["user_id"].(float64), 'f', -1, 64)
	assert.Len(t, uid, 7)
	assert.Equal(t, uid, c.ProviderUserID)
	assert.Contains(t, c.Interactions[0].Request.URL, "/users/"+uid+"/sleep")
	assert.Regexp(t, uuidRE, d["id"])
	assert.Contains(t, c.Interactions[2].Request.URL, "/sleep/"+d["id"].(string))
	assert.Contains(t, c.Interactions[1].Request.URL, "next_token="+b["next_token"].(string))
	assert.Len(t, d["name"], len("Jane Doe"))
	assert.Contains(t, d["name"], " ")
}

func TestDatesShiftByWholeDays(t *testing.T) {
	ex := exchange("https://api.example.com/v1/sleep?start_date=2026-10-01&end_date=2026-10-05&startdate=1759276800",
		`{"day":"2026-10-03","start":"2026-10-02T23:10:00+02:00","ts":1759449600,"ms":1759449600000,"filter":"time >= \"2026-10-01T00:00:00Z\""}`)
	c, _ := anonymize(t, []RawExchange{ex}, Options{ShiftDays: -14})
	b := body(t, c, 0)
	assert.Equal(t, "2026-09-19", b["day"])
	assert.Equal(t, "2026-09-18T23:10:00+02:00", b["start"])
	assert.Equal(t, float64(1759449600-14*86400), b["ts"])
	assert.Equal(t, float64(1759449600000-14*86400000), b["ms"])
	assert.Equal(t, `time >= "2026-09-17T00:00:00Z"`, b["filter"])
	u := c.Interactions[0].Request.URL
	assert.Contains(t, u, "start_date=2026-09-17")
	assert.Contains(t, u, "end_date=2026-09-21")
	assert.Contains(t, u, "startdate="+strconv.Itoa(1759276800-14*86400))
	assert.Equal(t, t0.AddDate(0, 0, -14), c.Window.Now)
	assert.Equal(t, "2026-10-05", c.RecordedAt, "the recording day is real")
}

func TestValuesAreScaledCodesKept(t *testing.T) {
	ex := exchange("https://api.example.com/v1/m", `{"heart_rate":62,"hrv":48.25,"efficiency":0.97,"score":99,"stage":3,"type":11,"unit":-3,"value":72350,"hypnogram":"4433221","zero":0,"lat":48.85,"polyline":"abc","birthday":"1990-04-02","zone":"Europe/Paris"}`)
	c, _ := anonymize(t, []RawExchange{ex}, Options{})
	b := body(t, c, 0)
	within := func(k string, v float64) {
		got := b[k].(float64)
		assert.InDelta(t, v, got, v*0.05+1, k)
	}
	within("heart_rate", 62)
	within("hrv", 48.25)
	within("value", 72350)
	assert.LessOrEqual(t, b["efficiency"].(float64), 1.0)
	assert.LessOrEqual(t, b["score"].(float64), 100.0)
	assert.Equal(t, float64(3), b["stage"])
	assert.Equal(t, float64(11), b["type"])
	assert.Equal(t, float64(-3), b["unit"])
	assert.Equal(t, "4433221", b["hypnogram"])
	assert.Equal(t, float64(0), b["zero"])
	assert.Equal(t, float64(0), b["lat"])
	assert.Equal(t, "", b["polyline"])
	assert.Equal(t, "1980-01-01", b["birthday"])
	assert.Equal(t, "Europe/Paris", b["zone"])
	// Decimals are kept.
	assert.Regexp(t, `"hrv":\d+\.\d\d[,}]`, string(c.Interactions[0].Response.Body))
}

func TestKeepOverridesTheRules(t *testing.T) {
	ex := exchange("https://api.example.com/v1/m", `{"name":"steps","hr":70}`)
	c, _ := anonymize(t, []RawExchange{ex}, Options{Keep: []string{"name", "hr"}})
	assert.Equal(t, map[string]any{"name": "steps", "hr": float64(70)}, body(t, c, 0))
}

func TestNonJSONBodiesAreDropped(t *testing.T) {
	ex := exchange("https://api.example.com/v1/file.fit", "\x0e\x10binary")
	c, _ := anonymize(t, []RawExchange{ex}, Options{})
	assert.True(t, c.Interactions[0].Response.BodyOmitted)
	assert.Empty(t, c.Interactions[0].Response.Body)
}

func TestAnonymizeFailsWhenASecretSurvives(t *testing.T) {
	// A credential in a field the rules do not name, as a plain word.
	ex := exchange("https://api.example.com/v1/m", `{"weird":"supersecretvalue"}`)
	_, err := AnonymizeHTTP([]RawExchange{ex}, meta(), Options{Secrets: []string{"supersecretvalue"}})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "supersecretvalue")
}

func TestStandInsDoNotCollide(t *testing.T) {
	a, err := newAnon(Options{Salt: []byte("s"), ShiftDays: -7})
	require.NoError(t, err)
	seen := map[string]string{}
	for i := range 10 {
		s := strconv.Itoa(i)
		p := a.stand(s)
		if o, dup := seen[p]; dup {
			t.Fatalf("%s and %s share the stand-in %s", o, s, p)
		}
		seen[p] = s
	}
}

func TestCheckFindsUnsafeFiles(t *testing.T) {
	c := &Cassette{Format: Format, Anonymizer: AnonymizerVersion, Kind: KindHTTP, Provider: "acme", APIVersion: "v1", RecordedAt: "2026-10-05",
		Interactions: []Interaction{{
			Request:  Request{Method: "GET", URL: "https://x/y?access_token=abc"},
			Response: Response{Status: 200, Header: map[string]string{"Authorization": "Bearer x"}, Body: json.RawMessage(`{"refresh_token":"r1","mail":"a@b.co"}`)},
		}}}
	raw, err := Marshal(c)
	require.NoError(t, err)
	p := strings.Join(Check(c, raw), "\n")
	for _, s := range []string{`query parameter "access_token"`, `header "Authorization"`, "email address", "refresh_token is not redacted", `"bearer "`} {
		assert.Contains(t, p, s)
	}
	c.Anonymizer = 0
	assert.Contains(t, strings.Join(Check(c, raw), "\n"), "record it again")
}

func TestRecorderAndReplayer(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"n":`+strconv.Itoa(n)+`}`)
	}))
	defer srv.Close()

	rec := NewRecorder(nil)
	get := func(c *http.Client, path string) (string, error) {
		res, err := c.Get(srv.URL + path)
		if err != nil {
			return "", err
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return string(b), nil
	}
	_, _ = get(rec.Client(), "/off")
	rec.Start()
	_, _ = get(rec.Client(), "/a?y=2&x=1")
	_, _ = get(rec.Client(), "/a?x=1&y=2")
	_, _ = get(rec.Client(), "/b")
	rec.Stop()
	require.Len(t, rec.Exchanges(), 3, "nothing is recorded while off")

	c := &Cassette{}
	for _, e := range rec.Exchanges() {
		c.Interactions = append(c.Interactions, Interaction{Request: Request{Method: e.Method, URL: e.URL}, Response: Response{Status: e.Status, Body: e.Body}})
	}
	rp := NewReplayer(c)
	b1, err := get(rp.Client(), "/a?x=1&y=2")
	require.NoError(t, err)
	b2, err := get(rp.Client(), "/a?y=2&x=1")
	require.NoError(t, err)
	assert.Equal(t, []string{`{"n":2}`, `{"n":3}`}, []string{b1, b2}, "same request, answers in recorded order")
	_, err = get(rp.Client(), "/c")
	require.ErrorIs(t, err, ErrNoAnswer)
	assert.Len(t, rp.Misses(), 1)
	assert.Len(t, rp.Unused(), 1)
}

func TestUploadsAreAnonymized(t *testing.T) {
	c, err := AnonymizeUploads([]RawUpload{{Type: "HKQuantityTypeIdentifierHeartRate", Day: "2026-10-03", Lines: [][]byte{
		[]byte(`{"startDate":"2026-10-03T08:00:00Z","value":"61","sourceName":"Jane's Apple Watch","quantityType":"HKQuantityTypeIdentifierHeartRate"}`), {},
	}}}, meta(), Options{Salt: []byte("s"), ShiftDays: -7})
	require.NoError(t, err)
	require.Len(t, c.Uploads, 1)
	assert.Equal(t, "2026-09-26", c.Uploads[0].Day)
	require.Len(t, c.Uploads[0].Lines, 1)
	var m map[string]any
	require.NoError(t, json.Unmarshal(c.Uploads[0].Lines[0], &m))
	assert.Equal(t, "2026-09-26T08:00:00Z", m["startDate"])
	assert.NotContains(t, m["sourceName"], "Jane")
	assert.Equal(t, "HKQuantityTypeIdentifierHeartRate", m["quantityType"])
	v, err := strconv.Atoi(m["value"].(string))
	require.NoError(t, err)
	assert.InDelta(t, 61, v, 4)
}

func TestKeyIgnoresQueryOrder(t *testing.T) {
	assert.Equal(t, Key("get", "https://h/p?b=2&a=1", ""), Key("GET", "https://h/p?a=1&b=2", ""))
	assert.Equal(t, Key("POST", "https://h/p", "b=2&a=1"), Key("POST", "https://h/p", "a=1&b=2"))
}
