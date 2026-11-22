// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package settings_test

import (
	"context"
	"encoding/base64"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/crypto"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/settings"
)

func manageLive(t *testing.T) (*settings.Live, *settings.PG) {
	t.Helper()
	url := db.TestDB(t)
	pool, err := db.Open(context.Background(), url)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	key, err := base64.StdEncoding.DecodeString(goodKey)
	require.NoError(t, err)
	box, err := crypto.NewBox(key)
	require.NoError(t, err)
	store := settings.NewPG(pool, box)
	cfg, env, environ := bootEnv(t, url)
	return settings.NewLive(settings.LiveOptions{Store: store, Env: env, Environ: environ, Config: cfg}), store
}

func ptr(s string) *string { return &s }

func TestPatchAtomic(t *testing.T) {
	ctx := context.Background()
	l, store := manageLive(t)
	_, err := l.Patch(ctx, 0, map[string]*string{"OHS_LOG_LEVEL": ptr("debug"), "OHS_RETENTION_DAYS": ptr("abc")}, uuid.New())
	var ie *settings.InvalidError
	require.ErrorAs(t, err, &ie)
	st, err := store.Load(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 0, st.Revision)
	require.Empty(t, st.Rows)
}

func TestPatch412(t *testing.T) {
	ctx := context.Background()
	l, _ := manageLive(t)
	_, err := l.Patch(ctx, 0, map[string]*string{"OHS_LOG_LEVEL": ptr("debug")}, uuid.New())
	require.NoError(t, err)
	_, err = l.Patch(ctx, 0, map[string]*string{"OHS_LOG_LEVEL": ptr("warn")}, uuid.New())
	require.ErrorIs(t, err, settings.ErrRevisionMoved)
}

func TestPatchSecretRoundTrip(t *testing.T) {
	ctx := context.Background()
	l, _ := manageLive(t)
	const tok = "0123456789abcdefSECRET"
	res, err := l.Patch(ctx, 0, map[string]*string{"OHS_METRICS_TOKEN": ptr(tok)}, uuid.New())
	require.NoError(t, err)
	require.Equal(t, []string{"OHS_METRICS_TOKEN"}, res.RestartRequired)
	// The same secret again changes nothing.
	again, err := l.Patch(ctx, res.Revision, map[string]*string{"OHS_METRICS_TOKEN": ptr(tok)}, uuid.New())
	require.NoError(t, err)
	require.Equal(t, res.Revision, again.Revision)
	require.Empty(t, again.Changed)
	out, err := l.Export(ctx)
	require.NoError(t, err)
	require.NotContains(t, string(out), tok)
}

func TestPatchConcurrent(t *testing.T) {
	ctx := context.Background()
	l, _ := manageLive(t)
	var wg sync.WaitGroup
	res := make([]settings.PatchResult, 2)
	errs := make([]error, 2)
	for i, v := range []string{"debug", "warn"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res[i], errs[i] = l.Patch(ctx, 0, map[string]*string{"OHS_LOG_LEVEL": ptr(v)}, uuid.New())
		}()
	}
	wg.Wait()
	won, lost := 0, 0
	for i := range errs {
		if errs[i] == nil {
			won++
			require.EqualValues(t, 1, res[i].Revision)
		} else {
			lost++
			require.ErrorIs(t, errs[i], settings.ErrRevisionMoved)
		}
	}
	require.Equal(t, 1, won)
	require.Equal(t, 1, lost)
	v, err := l.View(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, v.Revision)
}
