// SPDX-License-Identifier: AGPL-3.0-only

package export

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/activityfiles"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "testdata", name))
	require.NoError(t, err)
	return b
}

func gz(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, err := zw.Write(b)
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

// zipOf builds a zip of the entries, in the order given.
func zipOf(t *testing.T, entries ...any) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i := 0; i < len(entries); i += 2 {
		w, err := zw.Create(entries[i].(string))
		require.NoError(t, err)
		_, err = w.Write(entries[i+1].([]byte))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func write(t *testing.T, path string, b []byte) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, b, 0o644))
}

func walk(t *testing.T, root string) (Stats, []File) {
	t.Helper()
	var files []File
	st, err := Walk(context.Background(), root, func(f File) error {
		files = append(files, f)
		return nil
	})
	require.NoError(t, err)
	return st, files
}

// TestGarminLayout walks a zip laid out like a Garmin account export: the
// activity files sit in a zip inside the export zip, next to JSON files.
func TestGarminLayout(t *testing.T) {
	// Nested zips are copied to the temporary directory. Other packages
	// tested at the same time do the same, so this test gets its own.
	t.Setenv("TMPDIR", t.TempDir())
	ride, run := fixture(t, "garmin_ride.fit"), fixture(t, "garmin_run.gpx")
	inner := zipOf(t,
		"someone@example.com_1001.fit", ride,
		"someone@example.com_1002.gpx", run,
	)
	outer := zipOf(t,
		"DI_CONNECT/DI-Connect-Wellness/2026-01-01_sleepData.json", []byte(`{"invented":true}`),
		"DI_CONNECT/DI-Connect-Uploaded-Files/UploadedFiles_0-_Part1.zip", inner,
		"__MACOSX/DI_CONNECT/._UploadedFiles_0-_Part1.zip", []byte("metadata"),
	)
	root := filepath.Join(t.TempDir(), "export.zip")
	write(t, root, outer)

	st, files := walk(t, root)
	require.Equal(t, map[string]int{"fit": 1, "gpx": 1}, st.Files)
	require.Equal(t, 2, st.Archives)
	require.Equal(t, 1, st.Other)
	require.Zero(t, st.Unreadable)
	require.Len(t, files, 2)
	require.Equal(t, "fit", files[0].Format)
	require.Equal(t, ride, files[0].Data)
	require.Equal(t, sha256.Sum256(ride), files[0].SHA256)
	require.Equal(t, "gpx", files[1].Format)

	// No temporary file is left behind.
	left, err := filepath.Glob(filepath.Join(os.TempDir(), "ohs-import-*.zip"))
	require.NoError(t, err)
	require.Empty(t, left)
}

// TestStravaLayout walks a folder laid out like a Strava archive: original
// files in activities/, some gzip compressed, next to CSV files.
func TestStravaLayout(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "activities.csv"), []byte("invented,header\n"))
	write(t, filepath.Join(dir, "activities", "1001.fit.gz"), gz(t, fixture(t, "coros_run.fit")))
	write(t, filepath.Join(dir, "activities", "1002.GPX"), fixture(t, "strava_ride.gpx"))
	write(t, filepath.Join(dir, "activities", "1003.tcx.gz"), gz(t, fixture(t, "strava_ride.tcx")))
	write(t, filepath.Join(dir, "activities", "1004.fit.gz"), []byte("not gzip"))
	write(t, filepath.Join(dir, "activities", "._1001.fit.gz"), []byte("metadata"))
	write(t, filepath.Join(dir, "__MACOSX", "activities", "1001.fit"), []byte("metadata"))

	st, files := walk(t, dir)
	require.Equal(t, map[string]int{"fit": 1, "gpx": 1, "tcx": 1}, st.Files)
	require.Equal(t, 1, st.Unreadable)
	require.Equal(t, 1, st.Other)
	require.Equal(t, []string{"fit", "gpx", "tcx"}, st.Formats())
	require.Equal(t, fixture(t, "coros_run.fit"), files[0].Data, "decompressed")
	require.Equal(t, fixture(t, "strava_ride.tcx"), files[2].Data)
}

func TestSingleFiles(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "ride.fit")
	write(t, p, fixture(t, "garmin_ride.fit"))
	st, files := walk(t, p)
	require.Equal(t, map[string]int{"fit": 1}, st.Files)
	require.Len(t, files, 1)

	p = filepath.Join(dir, "notes.txt")
	write(t, p, []byte("x"))
	st, files = walk(t, p)
	require.Empty(t, files)
	require.Equal(t, 1, st.Other)

	p = filepath.Join(dir, "broken.zip")
	write(t, p, []byte("not a zip"))
	_, err := Walk(context.Background(), p, func(File) error { return nil })
	require.ErrorContains(t, err, "not a readable zip")

	_, err = Walk(context.Background(), filepath.Join(dir, "missing.zip"), func(File) error { return nil })
	require.Error(t, err)
	require.NotContains(t, err.Error(), dir, "the message names no folder")
}

func TestLimits(t *testing.T) {
	big := make([]byte, activityfiles.MaxFileBytes+1)
	root := filepath.Join(t.TempDir(), "export.zip")
	write(t, root, zipOf(t,
		"big.gpx", big,
		"broken.zip", []byte("not a zip"),
		"ride.fit", fixture(t, "garmin_ride.fit"),
	))
	st, files := walk(t, root)
	require.Equal(t, 1, st.TooLarge)
	require.Equal(t, 1, st.Unreadable)
	require.Len(t, files, 1)

	// Zips nested deeper than maxDepth are not opened.
	z := zipOf(t, "ride.fit", fixture(t, "garmin_ride.fit"))
	for range maxDepth {
		z = zipOf(t, "inner.zip", z)
	}
	write(t, root, z)
	st, files = walk(t, root)
	require.Empty(t, files)
	require.Equal(t, maxDepth, st.Archives)
}

func TestStopsOnError(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a.fit"), fixture(t, "garmin_ride.fit"))
	write(t, filepath.Join(dir, "b.fit"), fixture(t, "coros_run.fit"))
	stop := errors.New("stop")
	n := 0
	_, err := Walk(context.Background(), dir, func(File) error {
		n++
		return stop
	})
	require.ErrorIs(t, err, stop)
	require.Equal(t, 1, n)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = Walk(ctx, dir, func(File) error { return nil })
	require.ErrorIs(t, err, context.Canceled)
}
