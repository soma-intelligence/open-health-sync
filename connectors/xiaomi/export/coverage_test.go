// SPDX-License-Identifier: AGPL-3.0-only

package export

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/xiaomi"
)

// writeCopy writes an invented Mi Fitness copy: file name to content.
func writeCopy(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
	return dir
}

const fitnessHeader = "Uid,Sid,Key,Time,Value,UpdateTime\n"

func TestToLine(t *testing.T) {
	cases := []struct {
		name   string
		row    map[string]string
		reason string
		want   Line
	}{
		{"ok", map[string]string{"sid": "5", "key": " steps ", "time": " 1772960400 ", "value": `{ "steps" : 12 }`, "updatetime": "1772961000", "category": " daily "},
			"", Line{Table: "t", SID: "5", Key: "steps", Category: "daily", Time: 1772960400, UpdateTime: 1772961000, Value: []byte(`{"steps":12}`)}},
		{"misspelled columns", map[string]string{"key": "spo2", "time": "1772935200", "value": `{"spo2":97}`, "uptimedata": "1772935300", "categroy": "x"},
			"", Line{Table: "t", Key: "spo2", Category: "x", Time: 1772935200, UpdateTime: 1772935300, Value: []byte(`{"spo2":97}`)}},
		{"bad update time is left out", map[string]string{"key": "spo2", "time": "1772935200", "value": `{}`, "updatetime": "soon"},
			"", Line{Table: "t", Key: "spo2", Time: 1772935200, Value: []byte(`{}`)}},
		{"no key", map[string]string{"key": "  ", "time": "1772935200", "value": `{}`}, "no key", Line{}},
		{"time not a number", map[string]string{"key": "k", "time": "2026-03-08", "value": `{}`}, "unreadable time", Line{}},
		{"time zero", map[string]string{"key": "k", "time": "0", "value": `{}`}, "unreadable time", Line{}},
		{"value an array", map[string]string{"key": "k", "time": "1", "value": `[1]`}, "unreadable value", Line{}},
		{"value broken", map[string]string{"key": "k", "time": "1", "value": `{"a":`}, "unreadable value", Line{}},
		{"value empty", map[string]string{"key": "k", "time": "1"}, "unreadable value", Line{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := toLine("t", tc.row)
			require.Equal(t, tc.reason, reason)
			if tc.reason == "" {
				require.Equal(t, tc.want.Key, got.Key)
				require.Equal(t, tc.want.SID, got.SID)
				require.Equal(t, tc.want.Category, got.Category)
				require.Equal(t, tc.want.Time, got.Time)
				require.Equal(t, tc.want.UpdateTime, got.UpdateTime)
				require.Equal(t, string(tc.want.Value), string(got.Value))
			}
		})
	}
}

func TestValidate(t *testing.T) {
	require.NoError(t, Options{}.Validate())
	require.NoError(t, Options{Since: "2026-02-28"}.Validate())
	require.EqualError(t, Options{Since: "2026-02-30"}.Validate(), `since "2026-02-30" is not a date as YYYY-MM-DD`)
}

func TestConvertTableQuirks(t *testing.T) {
	dir := writeCopy(t, map[string]string{
		// An older copy in the same folder is ignored for the newer one.
		"20260101_1_MiFitness_hlth_center_fitness_data.csv": fitnessHeader + `1,5,steps,1767225600,"{""steps"":1}",0` + "\n",
		"20260310_1_MiFitness_hlth_center_fitness_data.csv": "\ufeff" + fitnessHeader +
			"\n" +
			`1,5,steps,1772960400,"{""steps"":30}",1772961000` + "\n" +
			`1,5,bad"quote,1772960460,x` + "\n" + // unreadable row: skipped
			`1,6,spo2,1772960520` + "\n" + // short row: no value
			`1,7,spo2,1772960580,"{""spo2"":98}"` + "\n",
		"20260310_1_MiFitness_hlth_center_data_source.csv": "Uid,Sid,Model,Name,Alias\n" +
			"1,5,band.example.m2,Example Band,my wrist\n" +
			"1,6,,,\n" + // no device named: not joined
			"1,,band.example.m3,Orphan,\n",
	})
	require.NoError(t, os.Mkdir(filepath.Join(dir, "nested"), 0o700))

	e, err := Open(dir)
	require.NoError(t, err)
	defer e.Close()
	require.Equal(t, "20260310_1_MiFitness_hlth_center_fitness_data.csv", e.files[fileFitness].name)

	var parts []Part
	st, err := e.Convert(Options{}, func(p Part) error { parts = append(parts, p); return nil })
	require.NoError(t, err)
	require.Equal(t, 3, st.Rows, "the unreadable row is skipped before it counts")
	require.Equal(t, 2, st.Lines)
	require.Equal(t, map[string]int{"unreadable value": 1}, st.Invalid)
	require.Equal(t, map[string]int{"fitness_data/steps": 1, "fitness_data/spo2": 1}, st.Keys)
	require.Len(t, parts, 1)
	require.Equal(t, xiaomi.TableFitnessData, parts[0].Type)
	require.Equal(t, "2026-03-08", parts[0].Day)
	got := strings.Split(strings.TrimSpace(string(parts[0].NDJSON)), "\n")
	require.Equal(t, []string{
		`{"table":"fitness_data","sid":"5","key":"steps","time":1772960400,"updateTime":1772961000,"device":{"model":"band.example.m2","name":"Example Band"},"value":{"steps":30}}`,
		`{"table":"fitness_data","sid":"7","key":"spo2","time":1772960580,"value":{"spo2":98}}`,
	}, got)
	require.NotContains(t, string(parts[0].NDJSON), "my wrist")
}

func TestConvertErrors(t *testing.T) {
	good := fitnessHeader + `1,5,steps,1772960400,"{""steps"":30}",0` + "\n" + `1,5,steps,1772960460,"{""steps"":31}",0` + "\n"

	t.Run("emit error stops", func(t *testing.T) {
		e, err := Open(writeCopy(t, map[string]string{"a_hlth_center_fitness_data.csv": good}))
		require.NoError(t, err)
		stop := errors.New("upload refused")
		calls := 0
		_, err = e.Convert(Options{MaxPartBytes: 10}, func(Part) error { calls++; return stop })
		require.ErrorIs(t, err, stop)
		require.Equal(t, 1, calls)
	})
	t.Run("emit error on the last part", func(t *testing.T) {
		e, err := Open(writeCopy(t, map[string]string{"a_hlth_center_fitness_data.csv": good}))
		require.NoError(t, err)
		stop := errors.New("upload refused")
		_, err = e.Convert(Options{}, func(Part) error { return stop })
		require.ErrorIs(t, err, stop)
	})
	t.Run("bad since", func(t *testing.T) {
		e, err := Open(writeCopy(t, map[string]string{"a_hlth_center_fitness_data.csv": good}))
		require.NoError(t, err)
		_, err = e.Convert(Options{Since: "March"}, func(Part) error { return nil })
		require.Error(t, err)
	})
	t.Run("empty data source table", func(t *testing.T) {
		e, err := Open(writeCopy(t, map[string]string{"a_hlth_center_fitness_data.csv": good, "a_hlth_center_data_source.csv": "\n\n"}))
		require.NoError(t, err)
		_, err = e.Convert(Options{}, func(Part) error { return nil })
		require.EqualError(t, err, "a_hlth_center_data_source.csv: empty table")
	})
	t.Run("unreadable header", func(t *testing.T) {
		e, err := Open(writeCopy(t, map[string]string{"a_hlth_center_sport_record.csv": "Uid,Sid\"x,Key\n1,2,3\n"}))
		require.NoError(t, err)
		_, err = e.Convert(Options{}, func(Part) error { return nil })
		require.EqualError(t, err, "a_hlth_center_sport_record.csv: line 1: unreadable header")
	})
	t.Run("missing path", func(t *testing.T) {
		_, err := Open(filepath.Join(t.TempDir(), "absent"))
		require.ErrorIs(t, err, os.ErrNotExist)
	})
}

func TestReadLineTooLong(t *testing.T) {
	long := strings.Repeat("x", maxLineBytes+1) + "\n"
	_, err := readLine(bufio.NewReaderSize(strings.NewReader(long), 1<<20))
	require.ErrorContains(t, err, "a line is longer than")

	s, err := readLine(bufio.NewReaderSize(strings.NewReader("a,b\r\nc"), 16))
	require.NoError(t, err)
	require.Equal(t, "a,b", s)
}
