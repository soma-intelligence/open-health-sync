// SPDX-License-Identifier: AGPL-3.0-only

package cassette

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Redacted replaces a credential.
const Redacted = "REDACTED"

// Meta describes a recording.
type Meta struct {
	Provider, APIVersion string
	RecordedAt           time.Time // real time of the recording
	Days                 int
	From, To, Now        time.Time // real; shifted on the way in
	Config               map[string]string
	Note                 string
}

// Options tune Anonymize. The zero value is what a recording uses: a fresh
// random salt and a random shift, both forgotten once the cassette exists.
type Options struct {
	// Salt keys the stand-ins and the scaling. Empty draws a random one.
	Salt []byte
	// ShiftDays moves every date. Zero draws a whole number of weeks between
	// 8 and 60 into the past; a whole number of weeks keeps weekdays and
	// times of day.
	ShiftDays int
	// Secrets are values that must not appear in the cassette (tokens,
	// client secret). Anonymize fails if one survives.
	Secrets []string
	// ProviderUserID is the connection's provider user id; it gets the same
	// stand-in wherever it appears.
	ProviderUserID string
	// Keep names extra fields whose scalar values are kept as they are,
	// for a provider whose mapping needs one the rules would change.
	Keep []string
}

// RawUpload is one device upload part before anonymization.
type RawUpload struct {
	Type, Day string
	Lines     [][]byte
}

// AnonymizeHTTP turns recorded exchanges into a cassette.
//
// The rules (AnonymizerVersion 1), applied to response bodies, request
// URLs and request bodies alike:
//
//   - Headers: only Content-Type, Retry-After, Link and rate limit headers are
//     kept. Request headers are never kept.
//   - Credentials (access_token, client_secret, code and the like) become
//     REDACTED.
//   - Identifiers (id, *_id, uuid, tokens of pagination), names, free text
//     (any string holding a space), emails, device names and serial numbers
//     get a stand-in of the same shape: digits stay digits, letters stay
//     letters, a UUID becomes a numbered UUID, an @ becomes _. The same
//     value always gets the same stand-in, in bodies, URL paths and
//     queries, so pagination and references still work. Long digit runs, UUIDs and hex
//     ids found in URL paths are treated the same way.
//   - Dates move by the same secret number of weeks, wherever they appear
//     (ISO dates and date-times inside any string, Unix seconds and
//     milliseconds as numbers or query values).
//   - Measured values (numbers other than codes, small integers and times)
//     are scaled by a factor between 0.95 and 1.05 drawn per value, keeping
//     their type and decimals.
//   - Locations (latitude, longitude, polylines, altitude, course) become 0
//     or empty; time zone names become Etc/UTC;
//     birth dates become 1980-01-01.
//   - A body that is not JSON is dropped.
//
// It fails if a secret or an original identifier is still in the result.
func AnonymizeHTTP(ex []RawExchange, m Meta, o Options) (*Cassette, error) {
	a, err := newAnon(o)
	if err != nil {
		return nil, err
	}
	c := a.cassette(KindHTTP, m)
	// Responses first: they introduce the identifiers requests then echo.
	resps := make([]Response, len(ex))
	for i, e := range ex {
		resps[i] = a.response(e)
	}
	for i, e := range ex {
		c.Interactions = append(c.Interactions, Interaction{Request: a.request(e), Response: resps[i]})
	}
	return c, a.leakCheck(c)
}

// AnonymizeUploads turns device upload parts into a cassette, under the
// rules of AnonymizeHTTP.
func AnonymizeUploads(parts []RawUpload, m Meta, o Options) (*Cassette, error) {
	a, err := newAnon(o)
	if err != nil {
		return nil, err
	}
	c := a.cassette(KindUpload, m)
	for _, p := range parts {
		u := Upload{Type: p.Type, Day: a.dates(p.Day)}
		for i, line := range p.Lines {
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			b, err := a.jsonDoc(line)
			if err != nil {
				return nil, fmt.Errorf("upload %s %s line %d: %w", p.Type, p.Day, i+1, err)
			}
			u.Lines = append(u.Lines, b)
		}
		c.Uploads = append(c.Uploads, u)
	}
	return c, a.leakCheck(c)
}

type anon struct {
	salt      []byte
	shift     int
	keep      map[string]bool
	pseudo    map[string]string
	used      map[string]string // stand-in -> original
	originals map[string]bool
	secrets   []string
	// uuids counts the UUID stand-ins handed out.
	uuids int
	// providerUID is the connection's provider user id.
	providerUID string
}

func newAnon(o Options) (*anon, error) {
	a := &anon{salt: o.Salt, shift: o.ShiftDays, keep: map[string]bool{}, pseudo: map[string]string{}, used: map[string]string{}, originals: map[string]bool{}}
	if len(a.salt) == 0 {
		a.salt = make([]byte, 32)
		if _, err := rand.Read(a.salt); err != nil {
			return nil, err
		}
	}
	if a.shift == 0 {
		n, err := rand.Int(rand.Reader, big.NewInt(53))
		if err != nil {
			return nil, err
		}
		a.shift = -7 * int(8+n.Int64())
	}
	for _, k := range o.Keep {
		a.keep[norm(k)] = true
	}
	for _, s := range o.Secrets {
		if strings.TrimSpace(s) != "" {
			a.secrets = append(a.secrets, s)
		}
	}
	if o.ProviderUserID != "" {
		a.providerUID = o.ProviderUserID
		a.stand(o.ProviderUserID)
	}
	return a, nil
}

func (a *anon) cassette(kind string, m Meta) *Cassette {
	c := &Cassette{
		Format: Format, Anonymizer: AnonymizerVersion, Kind: kind,
		Provider: m.Provider, APIVersion: m.APIVersion,
		RecordedAt: m.RecordedAt.UTC().Format(time.DateOnly), Days: m.Days,
		Window: Window{From: a.time(m.From), To: a.time(m.To), Now: a.time(m.Now)},
		Config: m.Config, Note: m.Note,
	}
	if a.providerUID != "" {
		c.ProviderUserID = a.stand(a.providerUID)
	}
	return c
}

func (a *anon) time(t time.Time) time.Time {
	if t.IsZero() {
		return t
	}
	return t.AddDate(0, 0, a.shift).UTC().Truncate(time.Second)
}

func (a *anon) response(e RawExchange) Response {
	r := Response{Status: e.Status}
	for name, vs := range e.Header {
		if !keptHeader(name) || len(vs) == 0 {
			continue
		}
		if r.Header == nil {
			r.Header = map[string]string{}
		}
		v := vs[0]
		if http.CanonicalHeaderKey(name) == "Link" {
			v = linkRE.ReplaceAllStringFunc(v, func(s string) string { return "<" + a.url(s[1:len(s)-1]) + ">" })
		}
		r.Header[http.CanonicalHeaderKey(name)] = v
	}
	if len(bytes.TrimSpace(e.Body)) == 0 {
		return r
	}
	b, err := a.jsonDoc(e.Body)
	if err != nil {
		r.BodyOmitted = true
		return r
	}
	r.Body = b
	return r
}

func (a *anon) request(e RawExchange) Request {
	r := Request{Method: e.Method, URL: a.url(e.URL)}
	if len(e.ReqBody) == 0 || !bodyKept(e.ReqType) {
		return r
	}
	if strings.Contains(strings.ToLower(e.ReqType), "json") {
		if b, err := a.jsonDoc(e.ReqBody); err == nil {
			var buf bytes.Buffer
			if json.Compact(&buf, b) == nil {
				r.Body = buf.String()
			}
		}
		return r
	}
	q, err := url.ParseQuery(string(e.ReqBody))
	if err == nil {
		r.Body = a.query(q).Encode()
	}
	return r
}

// jsonDoc anonymizes one JSON document.
func (a *anon) jsonDoc(b []byte) (json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("more than one JSON document")
	}
	v = a.value("", v)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return json.RawMessage(bytes.TrimSpace(buf.Bytes())), nil
}

// value walks v; key is the field v sits under (array items inherit it).
func (a *anon) value(key string, v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			t[k] = a.value(k, x)
		}
		return t
	case []any:
		for i, x := range t {
			t[i] = a.value(key, x)
		}
		return t
	default:
		return a.scalar(key, v)
	}
}

func (a *anon) scalar(key string, v any) any {
	n := norm(key)
	switch x := v.(type) {
	case nil, bool:
		return v
	case string:
		switch {
		case x == "":
			return x
		case secretKey(key):
			return Redacted
		case a.keep[n]:
			return x
		case geoKeys[n]:
			return ""
		case birthKeys[n]:
			return "1980-01-01"
		case identityKey(key):
			return a.stand(x)
		case zoneKeys[n] && strings.Contains(x, "/"):
			return "Etc/UTC"
		case keepKeys[n]:
			return x
		}
		return a.str(key, x)
	case json.Number:
		switch {
		case a.keep[n]:
			return x
		case geoKeys[n]:
			return json.Number("0")
		case birthKeys[n]:
			return json.Number("1980")
		case identityKey(key):
			return json.Number(a.stand(x.String()))
		case keepKeys[n]:
			return x
		}
		return a.number(key, x)
	}
	return v
}

// str handles a string under a field the rules do not name.
func (a *anon) str(key, s string) string {
	if p, ok := a.pseudo[s]; ok {
		return p
	}
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return a.url(s)
	}
	if dateRE.MatchString(s) {
		return a.dates(s)
	}
	if numRE.MatchString(s) {
		return string(a.number(key, json.Number(s)))
	}
	if m := numUnitRE.FindStringSubmatch(s); m != nil { // "21.5 degC", "64 %"
		return string(a.number(key, json.Number(m[1]))) + " " + m[2]
	}
	if emailRE.MatchString(s) || strings.IndexFunc(s, unicode.IsSpace) >= 0 || uuidRE.MatchString(s) || isHexID(s) {
		return a.stand(s)
	}
	return s
}

// number handles a number under a field the rules do not name.
func (a *anon) number(key string, n json.Number) json.Number {
	lit := n.String()
	if i, err := strconv.ParseInt(lit, 10, 64); err == nil {
		switch {
		case i >= epochSecMin && i <= epochSecMax:
			return json.Number(strconv.FormatInt(i+int64(a.shift)*86400, 10))
		case i >= epochSecMin*1000 && i <= epochSecMax*1000:
			return json.Number(strconv.FormatInt(i+int64(a.shift)*86400000, 10))
		case i >= -10 && i <= 10:
			return n
		}
		r := int64(math.Round(float64(i) * a.factor(key, lit)))
		if r == 0 {
			r = i
		}
		return json.Number(strconv.FormatInt(r, 10))
	}
	f, err := strconv.ParseFloat(lit, 64)
	if err != nil || f == 0 || math.IsInf(f, 0) || math.IsNaN(f) {
		return n
	}
	if f >= epochSecMin && f <= epochSecMax {
		return json.Number(strconv.FormatFloat(f+float64(a.shift)*86400, 'f', decimals(lit), 64))
	}
	r := f * a.factor(key, lit)
	// Ratios and percentages stay in range.
	switch {
	case f > 0 && f <= 1 && r > 1:
		r = 1
	case f > 0 && f <= 100 && r > 100:
		r = 100
	}
	if strings.ContainsAny(lit, "eE") {
		return json.Number(strconv.FormatFloat(r, 'g', -1, 64))
	}
	return json.Number(strconv.FormatFloat(r, 'f', decimals(lit), 64))
}

func decimals(lit string) int {
	if _, frac, ok := strings.Cut(lit, "."); ok {
		return len(frac)
	}
	return 0
}

// factor is a scale between 0.95 and 1.05, fixed by the salt, the field and
// the value.
func (a *anon) factor(key, lit string) float64 {
	h := a.mac("scale", key+"\x00"+lit)
	u := float64(binary.BigEndian.Uint64(h[:8])) / float64(math.MaxUint64)
	return 0.95 + 0.1*u
}

func (a *anon) mac(domain, s string) []byte {
	m := hmac.New(sha256.New, a.salt)
	m.Write([]byte(domain + "\x00" + s))
	return m.Sum(nil)
}

// stand returns the stand-in of s: same length, digits for digits, letters
// for letters (hex letters for a hex or UUID string), other characters kept.
func (a *anon) stand(s string) string {
	if p, ok := a.pseudo[s]; ok {
		return p
	}
	if s == "" || s == Redacted {
		return s
	}
	if uuidRE.MatchString(s) {
		// A numbered UUID: unique, and plainly not a secret to a scanner.
		a.uuids++
		p := fmt.Sprintf("00000000-0000-4000-8000-%012x", a.uuids)
		if strings.ToUpper(s) == s {
			p = strings.ToUpper(p)
		}
		a.pseudo[s] = p
		a.used[p] = s
		a.originals[s] = true
		return p
	}
	hex := hexRE.MatchString(s)
	var p string
	for try := 0; ; try++ {
		p = a.shape(s, hex, try)
		if _, taken := a.used[p]; !taken && (p != s || try > 20) {
			break
		}
		if try > 40 { // a short value whose stand-ins are all taken grows
			s2 := p + string(p[len(p)-1])
			if _, taken := a.used[s2]; !taken {
				p = s2
				break
			}
		}
	}
	a.pseudo[s] = p
	a.used[p] = s
	if p != s && len(s) >= 6 {
		a.originals[s] = true
	}
	return p
}

// shape draws the try-th stand-in of s.
func (a *anon) shape(s string, hex bool, try int) string {
	stream := a.mac("id", fmt.Sprintf("%d\x00%s", try, s))
	pos := 0
	next := func() byte {
		if pos == len(stream) {
			stream = a.mac("id", string(stream))
			pos = 0
		}
		b := stream[pos]
		pos++
		return b
	}
	out := []rune(s)
	for i, r := range out {
		switch {
		case r >= '0' && r <= '9':
			d := rune('0' + next()%10)
			if i == 0 && r != '0' && d == '0' {
				d = '1' + rune(next()%9)
			}
			out[i] = d
		case hex && r >= 'a' && r <= 'f':
			out[i] = rune('a' + next()%6)
		case hex && r >= 'A' && r <= 'F':
			out[i] = rune('A' + next()%6)
		case unicode.IsLower(r):
			out[i] = rune('a' + next()%26)
		case unicode.IsUpper(r):
			out[i] = rune('A' + next()%26)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			out[i] = rune('a' + next()%26)
		case r == '@': // an email stops looking like one
			out[i] = '_'
		}
	}
	return string(out)
}

// dates moves every ISO date found in s.
func (a *anon) dates(s string) string {
	return dateRE.ReplaceAllStringFunc(s, func(d string) string {
		t, err := time.Parse(time.DateOnly, d)
		if err != nil {
			return d
		}
		return t.AddDate(0, 0, a.shift).Format(time.DateOnly)
	})
}

// url rewrites the path and query of a URL; the scheme and host stay.
func (a *anon) url(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return a.stand(raw)
	}
	segs := strings.Split(u.Path, "/")
	for i, seg := range segs {
		switch {
		case seg == "":
		case a.pseudo[seg] != "":
			segs[i] = a.pseudo[seg]
		case digitsIDRE.MatchString(seg) || uuidRE.MatchString(seg) || isHexID(seg):
			segs[i] = a.stand(seg)
		case dateRE.MatchString(seg):
			segs[i] = a.dates(seg)
		}
	}
	u.Path = strings.Join(segs, "/")
	u.RawPath = ""
	u.User = nil
	u.Fragment = ""
	u.RawQuery = a.query(u.Query()).Encode()
	return u.String()
}

func (a *anon) query(q url.Values) url.Values {
	out := url.Values{}
	for k, vs := range q {
		for _, v := range vs {
			out.Add(k, a.param(k, v))
		}
	}
	return out
}

func (a *anon) param(k, v string) string {
	n := norm(k)
	switch {
	case v == "":
		return v
	case secretKey(k) || n == "code":
		return Redacted
	case a.keep[n]:
		return v
	case identityKey(k):
		return a.stand(v)
	case a.pseudo[v] != "":
		return a.pseudo[v]
	case strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://"):
		return a.url(v)
	case dateRE.MatchString(v):
		return a.dates(v)
	}
	if i, err := strconv.ParseInt(v, 10, 64); err == nil {
		switch {
		case i >= epochSecMin && i <= epochSecMax:
			return strconv.FormatInt(i+int64(a.shift)*86400, 10)
		case i >= epochSecMin*1000 && i <= epochSecMax*1000:
			return strconv.FormatInt(i+int64(a.shift)*86400000, 10)
		}
		return v
	}
	if emailRE.MatchString(v) || uuidRE.MatchString(v) || isHexID(v) {
		return a.stand(v)
	}
	return v
}

// leakCheck fails when a secret or an original identifier survived.
func (a *anon) leakCheck(c *Cassette) error {
	b, err := Marshal(c)
	if err != nil {
		return err
	}
	s := string(b)
	for _, sec := range a.secrets {
		if len(sec) >= 4 && strings.Contains(s, sec) {
			return errors.New("cassette: a credential survived anonymization; nothing was written")
		}
	}
	var doc any
	if err := json.Unmarshal(b, &doc); err != nil {
		return err
	}
	n := 0
	tokens(doc, func(tok string) {
		if a.originals[tok] {
			n++
		}
	})
	if n > 0 {
		return fmt.Errorf("cassette: %d original identifiers survived anonymization; nothing was written", n)
	}
	if p := Check(c, b); len(p) > 0 {
		return fmt.Errorf("cassette: the result fails Check: %s", strings.Join(p, "; "))
	}
	return nil
}

// tokens calls fn with every string and number of a decoded document, and
// with every path segment and query value of the URLs among them (request
// URLs and form bodies included).
func tokens(v any, fn func(string)) {
	switch t := v.(type) {
	case map[string]any:
		for _, x := range t {
			tokens(x, fn)
		}
	case []any:
		for _, x := range t {
			tokens(x, fn)
		}
	case float64:
		fn(strconv.FormatFloat(t, 'f', -1, 64))
	case string:
		fn(t)
		u, err := url.Parse(t)
		if err == nil && (u.Scheme != "" || strings.HasPrefix(t, "/")) {
			for _, seg := range strings.Split(u.Path, "/") {
				fn(seg)
			}
			for _, vs := range u.Query() {
				for _, x := range vs {
					fn(x)
				}
			}
		} else if q, err := url.ParseQuery(t); err == nil && strings.Contains(t, "=") {
			for _, vs := range q {
				for _, x := range vs {
					fn(x)
				}
			}
		}
	}
}

const (
	epochSecMin = 946684800  // 2000-01-01
	epochSecMax = 4102444800 // 2100-01-01
)

var (
	dateRE     = regexp.MustCompile(`\b(19|20)\d\d-[01]\d-[0-3]\d`)
	numRE      = regexp.MustCompile(`^-?\d+(\.\d+)?$`)
	numUnitRE  = regexp.MustCompile(`^(-?\d+(?:\.\d+)?) ([^\s\d]{1,12})$`)
	emailRE    = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	uuidRE     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	hexRE      = regexp.MustCompile(`^[0-9a-fA-F-]+$`)
	digitsIDRE = regexp.MustCompile(`^\d{5,}$`)
	linkRE     = regexp.MustCompile(`<[^>]*>`)
)

// isHexID reports whether s looks like a hex identifier: 16 or more
// lower-case hex characters mixing digits and letters.
func isHexID(s string) bool {
	if len(s) < 16 {
		return false
	}
	digit, letter := false, false
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digit = true
		case r >= 'a' && r <= 'f':
			letter = true
		default:
			return false
		}
	}
	return digit && letter
}

// norm lower-cases a field name and drops separators: "user_id", "userId"
// and "user-id" all read "userid".
func norm(k string) string {
	var b strings.Builder
	for _, r := range k {
		switch r {
		case '_', '-', '.', ' ':
		default:
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// identityKey reports whether a field holds an identifier, a name or free
// text.
func identityKey(k string) bool {
	n := norm(k)
	if identityKeys[n] {
		return true
	}
	if strings.HasSuffix(n, "uuid") {
		return true
	}
	// "user_id", "activityId", "deviceID", "transaction-id", "signalid";
	// not "paid" or "valid".
	return len(n) > 2 && strings.HasSuffix(n, "id") && !notIDWords[n]
}

// secretKey reports whether a field or parameter holds a credential.
func secretKey(k string) bool { return secretKeys[norm(k)] }

func keptHeader(name string) bool {
	c := http.CanonicalHeaderKey(name)
	switch c {
	case "Content-Type", "Retry-After", "Link":
		return true
	}
	return strings.HasPrefix(c, "X-Ratelimit-") || strings.HasPrefix(c, "Ratelimit-")
}

// notIDWords are words that end in "id" without being identifiers.
var notIDWords = set("paid", "valid", "invalid", "isvalid", "fluid", "liquid", "acid", "uricacid", "lacticacid",
	"hybrid", "rapid", "solid", "avoid", "void", "humid", "lipid", "android", "steroid", "thyroid", "fid", "mid", "did", "bid", "kid", "lid", "rid")

func set(ks ...string) map[string]bool {
	m := map[string]bool{}
	for _, k := range ks {
		m[norm(k)] = true
	}
	return m
}

var (
	secretKeys = set("access_token", "refresh_token", "id_token", "token", "client_secret", "client_id",
		"api_key", "apikey", "password", "secret", "subscription_key", "authorization", "signature")
	identityKeys = set("id", "uuid", "guid", "user", "userid", "username", "email", "name", "first_name", "last_name",
		"firstname", "lastname", "fullname", "display_name", "nickname", "phone", "address", "street", "city",
		"zip", "postcode", "postal_code", "serial", "serial_number", "mac", "mac_address", "deviceid", "hash",
		"next_token", "next_page_token", "page_token", "cursor", "photo", "avatar", "picture", "profile_url",
		"description", "notes", "note", "comment", "comments", "title", "label", "location_name", "external_id",
		"sourcename", "source_name", "device_name", "devicename", "device", "hardware_version", "firmware",
		"software_version", "productid")
	geoKeys = set("lat", "lng", "lon", "long", "latitude", "longitude", "latlng", "start_latlng", "end_latlng",
		"polyline", "summary_polyline", "coordinates", "geo", "gps", "elevation", "altitude", "course", "heading",
		"bearing")
	zoneKeys  = set("timezone", "time_zone", "tz", "hktimezone")
	birthKeys = set("birthday", "birthdate", "birth_date", "date_of_birth", "dob", "birth_year")
	keepKeys  = set("type", "types", "category", "unit", "units", "stage", "stages", "level", "code", "status",
		"state", "version", "model", "kind", "source", "sport", "sport_type", "activity_type", "timezone", "tz",
		"zone", "offset", "utc_offset", "timezone_offset", "zone_offset", "attrib", "algo", "algorithm", "more",
		"has_more", "page", "limit", "per_page", "count", "total", "index", "seq", "sequence", "quality",
		"precision", "resolution", "interval", "sample_rate", "hypnogram", "sleep_phase_5_min",
		"movement_30_sec", "class_5_min")
)
