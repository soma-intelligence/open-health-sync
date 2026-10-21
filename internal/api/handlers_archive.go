// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/api/read"
	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/fhir"
	"github.com/soma-intelligence/open-health-sync/internal/omh"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

// archiveVersion is the layout version the manifest states; it changes when
// a file moves or changes shape.
const archiveVersion = 1

// archiveFolders describe each part of the archive in its manifest.
var archiveFolders = map[string]string{
	"user.json": "The user as GET /v1/users/{id} returns it, with the user's provider connections " +
		"(provider, status, provider user id, scopes, last sync, created and updated times). " +
		"Tokens and sealed values are never included.",
	"canonical/": "Every record of each list of the REST API, one JSON object per line (NDJSON), " +
		"the same JSON as the list items: samples, category-samples, sleep, sleep-phases, workouts, " +
		"workout-routes, nutrition, daily, and ecg (each ECG recording with its voltage samples, as " +
		"GET /v1/users/{id}/ecg/{ecg_id} returns it). Times are RFC 3339 in UTC, values in canonical units.",
	"fhir/": "bundle.json: a FHIR R4 Bundle of type collection holding every Observation the FHIR " +
		"endpoints serve for the user (quantity samples, blood pressure panels, sleep sessions, " +
		"workouts, daily summaries, ECG recordings, nutrition logs).",
	"omh/": "data-points.ndjson: the records that have an Open mHealth schema, as Open mHealth data " +
		"points, one per line.",
	"raw/": "Every raw payload providers and devices sent for the user, byte for byte as stored, " +
		"under its path in the raw store below the user (<provider>/<day>/<file>).",
}

// archiveLists are the canonical files, in archive order: each REST list,
// the two lists that belong to a parent (sleep phases, route points) read
// session by session and workout by workout, and the ECG recordings, each
// read with its samples.
var archiveLists = []struct {
	name  string
	write archiveWrite
}{
	{"samples", archiveList(read.AllSamples)},
	{"category-samples", archiveList(read.CategorySamples)},
	{"sleep", archiveList(read.Sleep)},
	{"sleep-phases", archiveChildren(read.Sleep, func(s read.SleepSession) int64 { return s.ID }, read.SleepPhases)},
	{"workouts", archiveList(read.Workouts)},
	{"workout-routes", archiveChildren(read.Workouts, func(w read.Workout) int64 { return w.ID }, read.WorkoutRoute)},
	{"nutrition", archiveList(read.Nutrition)},
	{"daily", archiveList(read.Daily)},
	{"ecg", archiveChildren(read.ECGRecordings, func(e read.ECGRecording) int64 { return e.ID }, ecgDetail)},
}

// ecgDetail is the one-record list of an ECG recording with its samples.
func ecgDetail(ctx context.Context, db read.Querier, tenantID, user uuid.UUID, id int64, _ read.Query) (read.Page[read.ECGRecordingDetail], error) {
	rec, err := read.ECGRecordingByID(ctx, db, tenantID, user, id)
	if err != nil {
		return read.Page[read.ECGRecordingDetail]{}, err
	}
	return read.Page[read.ECGRecordingDetail]{Records: []read.ECGRecordingDetail{rec}}, nil
}

func (s *server) mountArchive(mux *http.ServeMux) {
	mux.Handle("GET /v1/users/{id}/archive",
		auth.MiddlewareAny(s.lookup, auth.TenantKey, auth.UserRead)(http.HandlerFunc(s.userArchive)))
}

// archiveUser is user.json: the user object and its connections.
type archiveUser struct {
	userJSON
	Connections []read.ConnectionDetail `json:"connections"`
}

// archiveFile is one counted file of the manifest.
type archiveFile struct {
	Path    string `json:"path"`
	Records int    `json:"records"`
}

// archiveRaw sums up the raw/ folder.
type archiveRaw struct {
	Objects int   `json:"objects"`
	Bytes   int64 `json:"bytes"`
}

type archiveManifest struct {
	ArchiveVersion int               `json:"archive_version"`
	UserID         uuid.UUID         `json:"user_id"`
	ExternalRef    *string           `json:"external_ref"`
	CreatedAt      time.Time         `json:"created_at"`
	GeneratedAt    time.Time         `json:"generated_at"`
	Version        string            `json:"version,omitempty"`
	Files          []archiveFile     `json:"files"`
	Raw            archiveRaw        `json:"raw"`
	Folders        map[string]string `json:"folders"`
}

// userArchive streams everything the instance holds about one user as one
// zip file (data portability): the user and its connections, every record
// of every list, the FHIR and Open mHealth forms of the records, and the raw
// payloads. A tenant key reads any user of the tenant; a user token with
// scope read only its own user (another user is a 404, never a 403).
func (s *server) userArchive(w http.ResponseWriter, r *http.Request) {
	p, id, ok := pathUser(w, r)
	if !ok {
		return
	}
	if p.UserID != nil && *p.UserID != id {
		notFound(w, "user")
		return
	}
	u, err := s.d.Store.GetActiveUser(r.Context(), p.TenantID, id)
	switch {
	case errors.Is(err, tenant.ErrNotFound):
		notFound(w, "user")
		return
	case err != nil:
		internalError(w, r, "api: archive user", err)
		return
	}
	// Read before anything is written, so a failure is still a problem response.
	conns, err := read.ConnectionDetails(r.Context(), s.d.Pool, p.TenantID, id)
	if err != nil {
		readError(w, r, err, "user")
		return
	}
	generated := time.Now().UTC().Truncate(time.Second)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="ohs-user-`+id.String()+"-"+
		generated.Format("20060102T150405Z")+`.zip"`)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	a := &archive{z: zip.NewWriter(w), modified: generated}
	err = s.writeArchive(r, a, p.TenantID, u, conns)
	if err == nil {
		err = a.z.Close()
	}
	if err != nil {
		// The status is sent: cut the response so the client sees a broken
		// transfer, never an archive that looks complete.
		if r.Context().Err() == nil { // not the client going away
			slog.ErrorContext(r.Context(), "api: archive", "error", err, "route", r.Pattern)
		}
		panic(http.ErrAbortHandler)
	}
}

// writeArchive writes every entry, the manifest last (it holds the counts).
func (s *server) writeArchive(r *http.Request, a *archive, tenantID uuid.UUID, u tenant.User, conns []read.ConnectionDetail) error {
	ctx := r.Context()
	uj := toUserJSON(u)
	if err := a.json("user.json", archiveUser{userJSON: uj, Connections: conns}); err != nil {
		return err
	}
	a.count("user.json", 1)
	for _, l := range archiveLists {
		err := a.ndjson("canonical/"+l.name+".ndjson", func(emit func(any) error) error {
			return l.write(ctx, s.d.Pool, tenantID, u.ID, emit)
		})
		if err != nil {
			return fmt.Errorf("%s: %w", l.name, err)
		}
	}
	if err := s.archiveFHIR(r, a, tenantID, u.ID); err != nil {
		return fmt.Errorf("fhir: %w", err)
	}
	err := a.ndjson("omh/data-points.ndjson", func(emit func(any) error) error {
		return eachRecord(func(q read.Query) (read.Page[read.ObservationRecord], error) {
			return read.Observations(ctx, s.d.Pool, tenantID, u.ID, q, omh.Filter(nil))
		}, func(rec read.ObservationRecord) error {
			if d, ok := omh.FromRecord(u.ID, rec); ok {
				return emit(d)
			}
			return nil
		})
	})
	if err != nil {
		return fmt.Errorf("omh: %w", err)
	}
	raw, err := s.archiveRaw(ctx, a, tenantID, u.ID)
	if err != nil {
		return fmt.Errorf("raw: %w", err)
	}
	return a.json("manifest.json", archiveManifest{
		ArchiveVersion: archiveVersion,
		UserID:         u.ID,
		ExternalRef:    uj.ExternalRef,
		CreatedAt:      uj.CreatedAt,
		GeneratedAt:    a.modified,
		Version:        s.d.Version,
		Files:          a.files,
		Raw:            raw,
		Folders:        archiveFolders,
	})
}

// archiveFHIR writes fhir/bundle.json, one collection Bundle of every
// Observation, entry by entry as the merged list is read page by page. FHIR
// JSON has no empty arrays: a user without records gets a Bundle without
// entry.
func (s *server) archiveFHIR(r *http.Request, a *archive, tenantID, user uuid.UUID) error {
	const name = "fhir/bundle.json"
	w, err := a.create(name, true)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(w, `{"resourceType":"Bundle","type":"collection","timestamp":"`+
		a.modified.Format(time.RFC3339)+`"`); err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false) // as the FHIR routes write them
	base := s.fhirBase(r, user)
	n := 0
	err = eachRecord(func(q read.Query) (read.Page[read.ObservationRecord], error) {
		return read.Observations(r.Context(), s.d.Pool, tenantID, user, q, read.AllObservations())
	}, func(rec read.ObservationRecord) error {
		sep := ","
		if n == 0 {
			sep = `,"entry":[` + "\n"
		}
		if _, err := io.WriteString(w, sep); err != nil {
			return err
		}
		n++
		o := fhir.FromRecord(user, rec)
		return enc.Encode(fhir.BundleEntry{FullURL: base + "/Observation/" + o.ID, Resource: o})
	})
	if err != nil {
		return err
	}
	end := "}\n"
	if n > 0 {
		end = "]}\n"
	}
	if _, err := io.WriteString(w, end); err != nil {
		return err
	}
	a.count(name, n)
	return nil
}

// archiveRaw copies every raw object of the user under raw/, keeping its
// path below raw/<tenant>/<user>/. An object removed since the listing (raw
// retention) is left out.
func (s *server) archiveRaw(ctx context.Context, a *archive, tenantID, user uuid.UUID) (archiveRaw, error) {
	var out archiveRaw
	if s.d.Raw == nil {
		return out, nil
	}
	prefix := fmt.Sprintf("raw/%s/%s/", tenantID, user)
	err := s.d.Raw.Walk(ctx, prefix, func(keys []string) error {
		for _, k := range keys {
			rel, ok := strings.CutPrefix(k, prefix)
			if !ok || rel == "" {
				continue
			}
			rc, err := s.d.Raw.Get(ctx, k)
			if errors.Is(err, rawstore.ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			// A gzip part is stored as is: compressing it again gains nothing.
			w, err := a.create("raw/"+rel, !strings.HasSuffix(rel, ".gz"))
			if err == nil {
				var n int64
				n, err = io.Copy(w, rc)
				out.Bytes += n
			}
			_ = rc.Close()
			if err != nil {
				return err
			}
			out.Objects++
		}
		return nil
	})
	return out, err
}

// archive writes the zip entries and keeps the record counts of the
// manifest.
type archive struct {
	z        *zip.Writer
	modified time.Time
	files    []archiveFile
}

func (a *archive) create(name string, compress bool) (io.Writer, error) {
	method := zip.Deflate
	if !compress {
		method = zip.Store
	}
	return a.z.CreateHeader(&zip.FileHeader{Name: name, Method: method, Modified: a.modified})
}

func (a *archive) count(name string, n int) {
	a.files = append(a.files, archiveFile{Path: name, Records: n})
}

// json writes one entry holding v, indented for a reader.
func (a *archive) json(name string, v any) error {
	w, err := a.create(name, true)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// ndjson writes one entry of records, each as one line of JSON, and counts
// them.
func (a *archive) ndjson(name string, each func(emit func(any) error) error) error {
	w, err := a.create(name, true)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	n := 0
	if err := each(func(v any) error {
		n++
		return enc.Encode(v)
	}); err != nil {
		return err
	}
	a.count(name, n)
	return nil
}

// archiveWrite emits every record of one list of the user.
type archiveWrite func(ctx context.Context, db read.Querier, tenantID, user uuid.UUID, emit func(any) error) error

func archiveList[T any](fn listFunc[T]) archiveWrite {
	return func(ctx context.Context, db read.Querier, tenantID, user uuid.UUID, emit func(any) error) error {
		return eachRecord(func(q read.Query) (read.Page[T], error) {
			return fn(ctx, db, tenantID, user, q)
		}, func(rec T) error { return emit(rec) })
	}
}

// archiveChildren emits the records of every parent of the list parents. A
// parent replaced since its page was read (its children are gone with it)
// is skipped.
func archiveChildren[P, T any](parents listFunc[P], id func(P) int64, children childFunc[T]) archiveWrite {
	return func(ctx context.Context, db read.Querier, tenantID, user uuid.UUID, emit func(any) error) error {
		return eachRecord(func(q read.Query) (read.Page[P], error) {
			return parents(ctx, db, tenantID, user, q)
		}, func(parent P) error {
			err := eachRecord(func(q read.Query) (read.Page[T], error) {
				return children(ctx, db, tenantID, user, id(parent), q)
			}, func(rec T) error { return emit(rec) })
			if errors.Is(err, read.ErrNotFound) {
				return nil
			}
			return err
		})
	}
}

// eachRecord calls fn with every record of a list, read page by page at the
// largest page size.
func eachRecord[T any](list func(read.Query) (read.Page[T], error), fn func(T) error) error {
	q := read.Query{Limit: read.MaxLimit}
	for {
		pg, err := list(q)
		if err != nil {
			return err
		}
		for _, rec := range pg.Records {
			if err := fn(rec); err != nil {
				return err
			}
		}
		if pg.NextCursor == "" {
			return nil
		}
		q.Cursor = pg.NextCursor
	}
}
