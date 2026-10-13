// SPDX-License-Identifier: AGPL-3.0-only

// Package cassette records what a provider's real API answered a connector,
// anonymizes it, and replays it in tests.
//
// A cassette is one recording of one connector against a real account: the
// HTTP exchanges of one Fetch for a cloud connector, or the upload parts of
// one export for a device connector. Recording happens on a contributor's
// machine (test/recorded, build tag record); only the anonymized cassette is
// committed. CI replays every committed cassette through the connector and
// its Map, so a change that breaks what a real account sends fails the build.
//
// Anonymize is the only way a recording becomes a cassette. It drops every
// header but a short list, redacts credentials, replaces identifiers and free
// text with stand-ins of the same shape (consistently, so references still
// join), shifts every date by a secret whole number of weeks, scales measured
// values by a few percent, and blanks locations. See Anonymize for the rules.
package cassette

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

// Format is the version of the cassette file layout.
const Format = 1

// AnonymizerVersion is the version of the anonymization rules. A committed
// cassette made under older rules fails Check and has to be recorded again.
const AnonymizerVersion = 1

// Kinds of cassette.
const (
	KindHTTP   = "http"   // the HTTP exchanges of a cloud connector's Fetch
	KindUpload = "upload" // the upload parts of a device connector
)

// Cassette is one anonymized recording.
type Cassette struct {
	Format     int    `json:"format"`
	Anonymizer int    `json:"anonymizer"`
	Kind       string `json:"kind"`
	Provider   string `json:"provider"`
	// APIVersion is the provider API version the connector called, as the
	// connector's entry in test/recorded names it at record time.
	APIVersion string `json:"api_version"`
	// RecordedAt is the real day of the recording (UTC). It is the only date
	// that is not shifted: it says how fresh the recording is.
	RecordedAt string `json:"recorded_at"`
	// Days is how many days of data were pulled.
	Days int `json:"days"`
	// Window is the pulled span and the clock of the recording, shifted
	// like every other date. Replay runs the connector with this clock.
	Window Window `json:"window"`
	// ProviderUserID is the connection's provider user id, anonymized.
	ProviderUserID string `json:"provider_user_id,omitempty"`
	// Config holds the non-secret operator keys the recording used (a
	// region, an environment), so replay builds the same connector.
	Config map[string]string `json:"config,omitempty"`
	// Note is free text from the recorder (device, app version). Never
	// personal.
	Note string `json:"note,omitempty"`

	Interactions []Interaction `json:"interactions,omitempty"` // KindHTTP
	Uploads      []Upload      `json:"uploads,omitempty"`      // KindUpload
}

// Window is a pulled span and the clock it was pulled at.
type Window struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
	Now  time.Time `json:"now"`
}

// Interaction is one HTTP exchange.
type Interaction struct {
	Request  Request  `json:"request"`
	Response Response `json:"response"`
}

// Request is what the connector sent: method, full URL and, for form or
// JSON bodies only, the body. Headers are never kept.
type Request struct {
	Method string `json:"method"`
	URL    string `json:"url"`
	Body   string `json:"body,omitempty"`
}

// Response is what the provider answered. Body holds a JSON document as is;
// any other body is dropped and BodyOmitted is set.
type Response struct {
	Status      int               `json:"status"`
	Header      map[string]string `json:"header,omitempty"`
	Body        json.RawMessage   `json:"body,omitempty"`
	BodyOmitted bool              `json:"body_omitted,omitempty"`
}

// Upload is one device upload part: its type, its day and its NDJSON lines.
type Upload struct {
	Type  string            `json:"type"`
	Day   string            `json:"day"`
	Lines []json.RawMessage `json:"lines"`
}

// Load reads a cassette file.
func Load(path string) (*Cassette, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Cassette
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("cassette %s: %w", path, err)
	}
	return &c, nil
}

// Save writes a cassette file, indented, with a final newline.
func Save(path string, c *Cassette) error {
	b, err := Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// Marshal encodes a cassette the way Save writes it.
func Marshal(c *Cassette) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(c); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Key identifies a request for replay: the method, the URL with its query
// sorted, and the body (form bodies sorted too).
func Key(method, rawURL, body string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return method + " " + rawURL + " " + body
	}
	u.RawQuery = u.Query().Encode()
	u.Fragment = ""
	if q, err := url.ParseQuery(body); err == nil && body != "" && !strings.HasPrefix(strings.TrimSpace(body), "{") {
		body = q.Encode()
	}
	return strings.ToUpper(method) + " " + u.String() + " " + body
}

// Check lists what makes a committed cassette unsafe or stale. An empty
// list means the file can be committed.
func Check(c *Cassette, raw []byte) []string {
	var p []string
	if c.Format != Format {
		p = append(p, fmt.Sprintf("format %d, want %d", c.Format, Format))
	}
	if c.Anonymizer != AnonymizerVersion {
		p = append(p, fmt.Sprintf("anonymized under rules v%d, current rules are v%d: record it again", c.Anonymizer, AnonymizerVersion))
	}
	if c.Kind != KindHTTP && c.Kind != KindUpload {
		p = append(p, fmt.Sprintf("kind %q is not %q or %q", c.Kind, KindHTTP, KindUpload))
	}
	if _, err := time.Parse(time.DateOnly, c.RecordedAt); err != nil {
		p = append(p, "recorded_at is not a date as YYYY-MM-DD")
	}
	if c.Provider == "" || c.APIVersion == "" {
		p = append(p, "provider and api_version are required")
	}
	for i, it := range c.Interactions {
		for h := range it.Response.Header {
			if !keptHeader(h) {
				p = append(p, fmt.Sprintf("interaction %d keeps header %q", i, h))
			}
		}
		if u, err := url.Parse(it.Request.URL); err == nil {
			for k, vs := range u.Query() {
				if secretKey(k) && !allRedacted(vs) {
					p = append(p, fmt.Sprintf("interaction %d: query parameter %q is not redacted", i, k))
				}
			}
		}
	}
	low := strings.ToLower(string(raw))
	for _, s := range []string{"authorization", "bearer ", "set-cookie"} {
		if strings.Contains(low, s) {
			p = append(p, fmt.Sprintf("the file contains %q", s))
		}
	}
	if m := emailRE.FindString(string(raw)); m != "" {
		p = append(p, "the file contains an email address")
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err == nil {
		walkSecrets(doc, func(path string) { p = append(p, fmt.Sprintf("secret field %s is not redacted", path)) }, "$")
	}
	sort.Strings(p)
	return p
}

func allRedacted(vs []string) bool {
	for _, v := range vs {
		if v != Redacted {
			return false
		}
	}
	return true
}

func walkSecrets(v any, bad func(string), path string) {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			if s, ok := x.(string); ok && secretKey(k) && s != Redacted && s != "" {
				bad(path + "." + k)
				continue
			}
			walkSecrets(x, bad, path+"."+k)
		}
	case []any:
		for i, x := range t {
			walkSecrets(x, bad, fmt.Sprintf("%s[%d]", path, i))
		}
	}
}

// ErrNoAnswer is returned by the replay transport for a request the
// cassette holds no answer for.
var ErrNoAnswer = errors.New("cassette: no recorded answer")
