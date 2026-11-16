// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package vault

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/vault"
)

// goldenOutputs reads the golden files by output name.
func goldenOutputs(t *testing.T) map[string][]byte {
	t.Helper()
	entries, err := os.ReadDir(goldenDir)
	require.NoError(t, err)
	out := map[string][]byte{}
	for _, e := range entries {
		body, err := os.ReadFile(filepath.Join(goldenDir, e.Name()))
		require.NoError(t, err)
		out[strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))] = body
	}
	require.NotEmpty(t, out, "no golden file: run TestGoldenCapture on the commit before the vault")
	return out
}

// sealedTables are the tables the demo person has rows in through the
// writer: on a sealed instance none of their rows may be clear.
var sealedTables = []string{
	"quantity_samples", "category_samples", "sleep_sessions", "sleep_phases",
	"workouts", "daily_summaries", "series_blobs",
}

// TestOutputsMatchGolden proves that the vault changes no read output: the
// demo data set, stored sealed, answers every captured read with the bytes
// captured before the vault existed. It runs twice: on rows the writer
// sealed from the start, and on rows written clear and converted by the
// seal command.
func TestOutputsMatchGolden(t *testing.T) {
	want := goldenOutputs(t)
	for name, mode := range map[string]fixtureMode{"sealed from the start": fixtureSealed, "sealed later": fixtureSealedLater} {
		t.Run(name, func(t *testing.T) {
			f := seedFixture(t, mode, nil)
			ctx := context.Background()

			// The fixture really is sealed: the comparison below would pass
			// on clear rows too.
			for _, table := range sealedTables {
				var clear, sealed int
				require.NoError(t, f.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE sealed IS NULL),
					count(*) FILTER (WHERE sealed IS NOT NULL) FROM ohs_data.`+table+`
					WHERE tenant_id = $1 AND user_id = $2`, f.tenant, f.user).Scan(&clear, &sealed))
				assert.Zero(t, clear, "%s: rows left clear", table)
				assert.NotZero(t, sealed, "%s: no sealed row", table)
			}
			// The heartbeats and routes the writer stored are series blobs.
			// The ECG voltages the fixture inserts as rows by hand stay rows
			// until the seal command moves them: read from the rows when
			// sealed from the start, from blobs when sealed later.
			for _, table := range vault.PerPoint {
				if table == "ecg_samples" && mode == fixtureSealed {
					continue
				}
				var n int
				require.NoError(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM ohs_data.`+table+`
					WHERE tenant_id = $1 AND user_id = $2`, f.tenant, f.user).Scan(&n))
				assert.Zero(t, n, "%s: per-point rows left", table)
			}
			if mode == fixtureSealedLater {
				st, err := vault.Status(ctx, f.pool)
				require.NoError(t, err)
				assert.Zero(t, st.ClearRows(), "the seal command left clear rows: %+v", st)
			}

			got := captureOutputs(t, f.srv, f.key, f.user)
			for name, body := range want {
				out, ok := got[name]
				if !assert.True(t, ok, "%s: the output is no longer captured", name) {
					continue
				}
				if string(body) != string(out) {
					assert.Fail(t, name+": the output differs from the golden file", firstDifference(body, out))
				}
			}
			for name := range got {
				// The coverage report counts what each write landed and is
				// not part of the golden set.
				if _, ok := want[name]; !ok && name != "coverage" {
					assert.Fail(t, name+": captured but has no golden file")
				}
			}
		})
	}
}

// firstDifference shows where two bodies part: the line number and the two
// lines.
func firstDifference(want, got []byte) string {
	w, g := strings.Split(string(want), "\n"), strings.Split(string(got), "\n")
	for i := 0; i < len(w) || i < len(g); i++ {
		var a, b string
		if i < len(w) {
			a = w[i]
		}
		if i < len(g) {
			b = g[i]
		}
		if a != b {
			return fmt.Sprintf("line %d\n  golden: %s\n  got:    %s\n(%d golden lines, %d got)", i+1, a, b, len(w), len(g))
		}
	}
	return "no difference found"
}

// TestSealedListsKeepIdsOrderAndPaging covers what the golden files cannot:
// they mask row ids and cursors. On sealed rows every list returns the rows
// of the table, with their own ids, in the order of (time, id), and reading
// it page by page gives the same rows as reading it at once.
func TestSealedListsKeepIdsOrderAndPaging(t *testing.T) {
	f := seedFixture(t, fixtureSealed, nil)
	ctx := context.Background()
	base := "/v1/users/" + f.user.String()

	get := func(path string) (ids []int64, next string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, f.srv.URL+path, nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+f.key)
		res, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer res.Body.Close()
		body, err := io.ReadAll(res.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, res.StatusCode, path+": "+string(body))
		var env struct {
			Data []struct {
				ID int64 `json:"id"`
			} `json:"data"`
			Next *string `json:"next_cursor"`
		}
		require.NoError(t, json.Unmarshal(body, &env))
		for _, d := range env.Data {
			ids = append(ids, d.ID)
		}
		if env.Next != nil {
			next = *env.Next
		}
		return ids, next
	}

	for _, c := range []struct {
		name, path, table, where, order string
		page                            int
	}{
		{"samples", base + "/samples?type=heart_rate&view=all", "quantity_samples", "AND quantity_type = 'heart_rate'", "start_date, id", 97},
		{"category samples", base + "/category-samples?view=all", "category_samples", "", "start_date, id", 2},
		{"sleep", base + "/sleep?view=all", "sleep_sessions", "", "bedtime_start, id", 3},
		{"workouts", base + "/workouts?view=all", "workouts", "", "start_date, id", 2},
		{"daily", base + "/daily?view=all", "daily_summaries", "", "summary_date, id", 4},
	} {
		t.Run(c.name, func(t *testing.T) {
			rows, err := f.pool.Query(ctx, `SELECT id FROM ohs_data.`+c.table+`
				WHERE tenant_id = $1 AND user_id = $2 `+c.where+` ORDER BY `+c.order, f.tenant, f.user)
			require.NoError(t, err)
			var stored []int64
			for rows.Next() {
				var id int64
				require.NoError(t, rows.Scan(&id))
				stored = append(stored, id)
			}
			require.NoError(t, rows.Err())
			require.NotEmpty(t, stored)

			var paged []int64
			cursor := ""
			for pages := 0; ; pages++ {
				require.Less(t, pages, 1000, "paging does not end")
				path := fmt.Sprintf("%s&limit=%d", c.path, c.page)
				if !strings.Contains(c.path, "?") {
					path = fmt.Sprintf("%s?limit=%d", c.path, c.page)
				}
				if cursor != "" {
					path += "&cursor=" + cursor
				}
				ids, next := get(path)
				assert.LessOrEqual(t, len(ids), c.page)
				paged = append(paged, ids...)
				if next == "" {
					break
				}
				cursor = next
			}
			assert.Equal(t, stored, paged, "the pages do not hold the stored rows in order")
		})
	}
}
