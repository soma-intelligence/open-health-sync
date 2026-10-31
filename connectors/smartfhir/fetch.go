// SPDX-License-Identifier: AGPL-3.0-only

package smartfhir

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"sort"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// Data types of the stored lines: the FHIR resource type, one resource per
// line, as the server returned it.
const (
	DataTypePatient           = "Patient"
	DataTypeObservation       = "Observation"
	DataTypeCondition         = "Condition"
	DataTypeAllergy           = "AllergyIntolerance"
	DataTypeMedicationRequest = "MedicationRequest"
	DataTypeImmunization      = "Immunization"
	DataTypeProcedure         = "Procedure"
	DataTypeDiagnosticReport  = "DiagnosticReport"
)

// AllDataTypes lists every data type Fetch pulls.
var AllDataTypes = []string{
	DataTypePatient, DataTypeObservation, DataTypeCondition, DataTypeAllergy,
	DataTypeMedicationRequest, DataTypeImmunization, DataTypeProcedure, DataTypeDiagnosticReport,
}

// chartTypes are the resources read whole at each pull: a problem list or an
// allergy list is a state, not a series, and few servers filter them by date.
var chartTypes = []string{
	DataTypeCondition, DataTypeAllergy, DataTypeMedicationRequest,
	DataTypeImmunization, DataTypeProcedure, DataTypeDiagnosticReport,
}

// observationCategories are the Observation categories searched. Epic
// requires a category (or a code) on every Observation search.
var observationCategories = []string{"vital-signs", "laboratory"}

// chartFreshness is how recent the end of a window must be for its pull to
// read the chart resources too. Backfill jobs of older windows skip them: the
// newest one reads them, and re-reading the same chart 12 times stores
// nothing new anyway (identical raw objects are skipped).
const chartFreshness = 48 * time.Hour

// maxPages bounds the pages followed for one search.
const maxPages = 50

// fhirID is the FHIR id grammar (R4 datatypes: [A-Za-z0-9\-\.]{1,64}).
var fhirID = regexp.MustCompile(`^[A-Za-z0-9\-.]{1,64}$`)

// Fetch pulls the record of the connection's patient. Observations of the
// window [from, to) are emitted one raw object per UTC day of their effective
// time; the Patient and the chart resources are read whole, once per pull
// whose window ends near now, and emitted under the window's last day. A
// search the server refuses for missing permission (403) or as unsupported
// (400, 404) is skipped with a warning: one resource the grant lacks never
// blocks the others. A rejected token gives connector.ErrNeedsReauth.
func (c *Connector) Fetch(ctx context.Context, conn connector.Connection, from, to time.Time, emit connector.RawSink) error {
	token := conn.Tokens.Access
	if token == "" {
		return fmt.Errorf("smart_fhir: connection has no access token: %w", connector.ErrNeedsReauth)
	}
	patient := conn.Tokens.ProviderUserID
	if !fhirID.MatchString(patient) {
		return fmt.Errorf("smart_fhir: connection names no valid patient id: %w", connector.ErrNeedsReauth)
	}
	base := c.base()
	if base == "" {
		return errNotConfigured
	}
	now := c.now().UTC()
	from, to = from.UTC(), to.UTC()
	if to.After(now) {
		to = now
	}

	obs, err := c.observations(ctx, token, patient, from, to)
	if err != nil {
		return err
	}
	byDay := map[time.Time][][]byte{}
	for _, r := range obs {
		day := observationDay(r)
		if day.IsZero() {
			day = to.Truncate(24 * time.Hour)
		}
		byDay[day] = append(byDay[day], r)
	}
	days := make([]time.Time, 0, len(byDay))
	for d := range byDay {
		days = append(days, d)
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Before(days[j]) })
	for _, d := range days {
		if err := emit.Emit(ctx, DataTypeObservation, d, sortByID(dedupe(byDay[d]))); err != nil {
			return fmt.Errorf("smart_fhir: emit Observation %s: %w", d.Format(time.DateOnly), err)
		}
	}

	if now.Sub(to) > chartFreshness {
		return nil
	}
	day := to.Truncate(24 * time.Hour)
	pat, err := c.read(ctx, token, DataTypePatient, patient)
	if err != nil {
		return err
	}
	if pat != nil {
		if err := emit.Emit(ctx, DataTypePatient, day, [][]byte{pat}); err != nil {
			return fmt.Errorf("smart_fhir: emit Patient: %w", err)
		}
	}
	for _, rt := range chartTypes {
		q := url.Values{}
		q.Set("patient", patient)
		res, err := c.search(ctx, token, rt, q)
		if err != nil {
			return err
		}
		if len(res) == 0 {
			continue
		}
		if err := emit.Emit(ctx, rt, day, sortByID(dedupe(res))); err != nil {
			return fmt.Errorf("smart_fhir: emit %s: %w", rt, err)
		}
	}
	return nil
}

// observations searches the patient's Observations of [from, to). It asks
// for every category at once, which catches Observations filed without one;
// a server that requires a category (Epic answers 400) is asked once per
// category of observationCategories instead.
func (c *Connector) observations(ctx context.Context, token, patient string, from, to time.Time) ([][]byte, error) {
	query := func(cat string) url.Values {
		q := url.Values{}
		q.Set("patient", patient)
		if cat != "" {
			q.Set("category", cat)
		}
		q.Add("date", "ge"+from.Format(time.RFC3339))
		q.Add("date", "lt"+to.Format(time.RFC3339))
		return q
	}
	res, err := c.searchStrict(ctx, token, DataTypeObservation, query(""))
	var he *httpError
	if !errors.As(err, &he) || he.status != 400 {
		if err != nil {
			if skip, serr := c.classify(DataTypeObservation, err); !skip {
				return nil, serr
			}
		}
		return res, nil
	}
	var all [][]byte
	for _, cat := range observationCategories {
		r, err := c.search(ctx, token, DataTypeObservation, query(cat))
		if err != nil {
			return nil, err
		}
		all = append(all, r...)
	}
	return all, nil
}

// search runs one search and follows its next links, returning each matching
// resource compacted. A search the server refuses for missing permission or
// as unsupported returns nothing and no error.
func (c *Connector) search(ctx context.Context, token, resourceType string, q url.Values) ([][]byte, error) {
	res, err := c.searchStrict(ctx, token, resourceType, q)
	if skip, serr := c.classify(resourceType, err); skip || serr != nil {
		return nil, serr
	}
	return res, nil
}

// searchStrict runs one search and follows its next links, returning each matching
// resource compacted. Errors are returned as the request gave them.
func (c *Connector) searchStrict(ctx context.Context, token, resourceType string, q url.Values) ([][]byte, error) {
	next := c.base() + "/" + resourceType + "?" + q.Encode()
	var out [][]byte
	for page := 0; next != "" && page < maxPages; page++ {
		if page > 0 {
			if err := pause(ctx, c.pageDelay); err != nil {
				return nil, err
			}
		}
		var b bundle
		if err := c.getJSON(ctx, token, next, &b); err != nil {
			return nil, err
		}
		if b.ResourceType != "Bundle" {
			return nil, fmt.Errorf("smart_fhir: search %s: answer is a %q, not a Bundle", resourceType, b.ResourceType)
		}
		for _, e := range b.Entry {
			var head struct {
				ResourceType string `json:"resourceType"`
				ID           string `json:"id"`
			}
			if len(e.Resource) == 0 || json.Unmarshal(e.Resource, &head) != nil {
				continue
			}
			// Servers add OperationOutcome entries (warnings) and included
			// resources; only the searched type is kept.
			if head.ResourceType != resourceType || head.ID == "" {
				continue
			}
			var line bytes.Buffer
			if err := json.Compact(&line, e.Resource); err != nil {
				return nil, fmt.Errorf("smart_fhir: search %s: %w", resourceType, err)
			}
			out = append(out, line.Bytes())
		}
		next = ""
		for _, l := range b.Link {
			if l.Relation == "next" && l.URL != "" {
				if err := c.sameServer(l.URL); err != nil {
					return nil, fmt.Errorf("smart_fhir: search %s: next link: %w", resourceType, err)
				}
				next = l.URL
			}
		}
	}
	if next != "" {
		slog.Warn("smart_fhir: search stopped at the page limit", "provider", Slug, "resource", resourceType, "pages", maxPages)
	}
	return out, nil
}

// read fetches one resource by id, compacted. A skipped read returns nil.
func (c *Connector) read(ctx context.Context, token, resourceType, id string) ([]byte, error) {
	var raw json.RawMessage
	err := c.getJSON(ctx, token, c.base()+"/"+resourceType+"/"+url.PathEscape(id), &raw)
	if skip, serr := c.classify(resourceType, err); skip {
		return nil, nil
	} else if serr != nil {
		return nil, serr
	}
	var line bytes.Buffer
	if err := json.Compact(&line, raw); err != nil {
		return nil, fmt.Errorf("smart_fhir: read %s: %w", resourceType, err)
	}
	return line.Bytes(), nil
}

// classify sorts a request error: skip for a resource the grant or the
// server does not offer, an error wrapping ErrNeedsReauth for a rejected
// token, the error itself otherwise.
func (c *Connector) classify(resourceType string, err error) (skip bool, _ error) {
	var he *httpError
	switch {
	case err == nil:
		return false, nil
	case errors.Is(err, errUnauthorized):
		return false, fmt.Errorf("smart_fhir: %s: %w: %w", resourceType, err, connector.ErrNeedsReauth)
	case errors.As(err, &he) && (he.status == 400 || he.status == 403 || he.status == 404):
		slog.Warn("smart_fhir: resource skipped", "provider", Slug, "resource", resourceType, "status", he.status)
		return true, nil
	}
	return false, fmt.Errorf("smart_fhir: %s: %w", resourceType, err)
}

// sameServer refuses a URL on another origin than the FHIR base: the access
// token is only ever sent to the server that issued it.
func (c *Connector) sameServer(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	b, err := url.Parse(c.base())
	if err != nil {
		return err
	}
	if u.Scheme != b.Scheme || u.Host != b.Host {
		return fmt.Errorf("points to another server (%s)", u.Host)
	}
	return nil
}

// bundle is the part of a FHIR searchset Bundle the pager reads.
type bundle struct {
	ResourceType string `json:"resourceType"`
	Link         []struct {
		Relation string `json:"relation"`
		URL      string `json:"url"`
	} `json:"link"`
	Entry []struct {
		Resource json.RawMessage `json:"resource"`
	} `json:"entry"`
}

// observationDay is the UTC day of an Observation's effective time (or
// issued time), zero when it carries none.
func observationDay(line []byte) time.Time {
	var o observation
	if json.Unmarshal(line, &o) != nil {
		return time.Time{}
	}
	at, ok := o.when()
	if !ok {
		return time.Time{}
	}
	return at.UTC().Truncate(24 * time.Hour)
}

// dedupe drops a resource seen twice (an Observation in two categories).
func dedupe(lines [][]byte) [][]byte {
	seen := map[string]bool{}
	out := lines[:0:0]
	for _, l := range lines {
		k := idOf(l)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, l)
	}
	return out
}

// sortByID orders lines by resource id, so the same set always gives the
// same bytes and an unchanged re-pull is recognised and skipped.
func sortByID(lines [][]byte) [][]byte {
	sort.SliceStable(lines, func(i, j int) bool { return idOf(lines[i]) < idOf(lines[j]) })
	return lines
}

// idOf is the id of one compacted resource line.
func idOf(line []byte) string {
	var h struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(line, &h)
	return h.ID
}
