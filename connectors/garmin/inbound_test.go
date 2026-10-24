// SPDX-License-Identifier: AGPL-3.0-only

package garmin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/garmin/garminfake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// inbound posts body to target, with the fake connector's token unless
// target names one.
func inbound(t *testing.T, c *Connector, target, body string) ([]connector.InboundEvent, error) {
	t.Helper()
	if !strings.Contains(target, TokenParam+"=") {
		sep := "?"
		if strings.Contains(target, "?") {
			sep = "&"
		}
		target += sep + TokenParam + "=" + testInboundToken
	}
	r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	return c.HandleInbound(context.Background(), r)
}

func TestHandleInbound_PingBecomesPullOfUploadWindow(t *testing.T) {
	c := newFakeGarmin(t).connector()
	body := `{"dailies":[
		{"userId":"u-1","callbackURL":"https://apis.garmin.com/wellness-api/rest/dailies?uploadStartTimeInSeconds=1772406000&uploadEndTimeInSeconds=1772406300"},
		{"userId":"u-1","callbackURL":"https://apis.garmin.com/wellness-api/rest/dailies?uploadStartTimeInSeconds=1772409000&uploadEndTimeInSeconds=1772409300"},
		{"userId":"u-2","callbackURL":"https://apis.garmin.com/wellness-api/rest/dailies?uploadStartTimeInSeconds=1772406000&uploadEndTimeInSeconds=1772406300"}],
		"sleeps":[{"userId":"u-1","callbackURL":"https://apis.garmin.com/wellness-api/rest/sleeps?uploadStartTimeInSeconds=1772433000&uploadEndTimeInSeconds=1772433060"}]}`
	evs, err := inbound(t, c, "/inbound/garmin", body)
	require.NoError(t, err)
	require.Len(t, evs, 3)
	assert.Equal(t, connector.InboundEvent{ProviderUserID: "u-1", DataType: DataTypeDailies,
		From: time.Unix(1772406000, 0).UTC(), To: time.Unix(1772409300, 0).UTC()}, evs[0], "two pings of one user and type merge")
	assert.Equal(t, "u-2", evs[1].ProviderUserID)
	assert.Equal(t, DataTypeSleeps, evs[2].DataType)
	for _, ev := range evs {
		assert.Empty(t, ev.Lines)
	}
}

func TestHandleInbound_PingWithoutWindowOrFutureEnd(t *testing.T) {
	c := newFakeGarmin(t).connector()
	evs, err := inbound(t, c, "/inbound/garmin", `{"allDayRespiration":[{"userId":"u","callbackURL":"https://apis.garmin.com/x"}]}`)
	require.NoError(t, err)
	require.Len(t, evs, 1)
	assert.Equal(t, DataTypeRespiration, evs[0].DataType, "notification key alias")
	assert.Equal(t, fakeNow.Add(-24*time.Hour), evs[0].From)
	assert.Equal(t, fakeNow, evs[0].To)

	far := fakeNow.Add(100 * time.Hour).Unix()
	evs, err = inbound(t, c, "/inbound/garmin", `{"epochs":[{"userId":"u","callbackURL":"https://apis.garmin.com/x?uploadStartTimeInSeconds=1772406000&uploadEndTimeInSeconds=`+itoa(far)+`"}]}`)
	require.NoError(t, err)
	assert.Equal(t, fakeNow, evs[0].To, "never past now")
}

func TestHandleInbound_PushStoresItemsAsLines(t *testing.T) {
	c := newFakeGarmin(t).connector()
	daily := string(garminfake.Summaries(DataTypeDailies)[0])
	act := strings.Replace(string(garminfake.Summaries(DataTypeActivities)[0]), `"userId"`, `"userAccessToken":"legacy-token","userId"`, 1)
	body := `{"dailies":[` + daily + `],"activities":[` + act + `]}`
	evs, err := inbound(t, c, "/inbound/garmin", body)
	require.NoError(t, err)
	require.Len(t, evs, 2)
	assert.Equal(t, DataTypeActivities, evs[0].DataType)
	assert.Equal(t, DataTypeDailies, evs[1].DataType)
	for _, ev := range evs {
		assert.Equal(t, garminfake.UserID, ev.ProviderUserID)
		require.Len(t, ev.Lines, 1)
		assert.NotContains(t, string(ev.Lines[0]), "\n")
		assert.NotContains(t, string(ev.Lines[0]), "legacy-token", "a credential is never stored")
	}
	// The stored line maps like a pulled one.
	res := mapLine(t, DataTypeDailies, evs[1].Lines[0])
	require.Len(t, res.DailySummaries, 1)
	assert.Equal(t, 8000, *res.DailySummaries[0].StepCount)
}

func TestHandleInbound_RejectsMalformed(t *testing.T) {
	c := newFakeGarmin(t).connector()
	for name, body := range map[string]string{
		"not json":          `nope`,
		"array":             `[{"userId":"u"}]`,
		"empty object":      `{}`,
		"not a list":        `{"dailies":{"userId":"u"}}`,
		"item not object":   `{"dailies":["u"]}`,
		"no userId":         `{"dailies":[{"callbackURL":"https://x"}]}`,
		"userId not string": `{"dailies":[{"userId":5,"callbackURL":"https://x"}]}`,
		"push no summaryId": `{"dailies":[{"userId":"u","steps":5}]}`,
		"callback not text": `{"dailies":[{"userId":"u","callbackURL":5}]}`,
		"dereg no userId":   `{"deregistrations":[{}]}`,
	} {
		_, err := inbound(t, c, "/inbound/garmin", body)
		assert.Error(t, err, name)
	}
}

func TestHandleInbound_AcknowledgesDeregistrationsAndUnknownTypes(t *testing.T) {
	c := newFakeGarmin(t).connector()
	evs, err := inbound(t, c, "/inbound/garmin", `{"deregistrations":[{"userId":"u","userAccessToken":"t"}],
		"userPermissionsChange":[{"userId":"u","summaryId":"x","permissions":["ACTIVITY_EXPORT"],"changeTimeInSeconds":1772406000}],
		"menstrualCycleTracking":[{"userId":"u","summaryId":"m"}]}`)
	require.NoError(t, err)
	assert.Empty(t, evs)

	for _, m := range []string{http.MethodGet, http.MethodHead} {
		evs, err := c.HandleInbound(context.Background(), httptest.NewRequest(m, "/inbound/garmin", nil))
		require.NoError(t, err)
		assert.Empty(t, evs)
	}
}

func TestHandleInbound_InboundToken(t *testing.T) {
	c := newFakeGarmin(t).connector(func(o *Options) { o.InboundToken = "s3cret" })
	body := `{"dailies":[{"userId":"u","callbackURL":"https://x"}]}`
	_, err := inbound(t, c, "/inbound/garmin", body)
	assert.ErrorIs(t, err, ErrBadToken)
	_, err = inbound(t, c, "/inbound/garmin?token=wrong", body)
	assert.ErrorIs(t, err, ErrBadToken)
	evs, err := inbound(t, c, "/inbound/garmin?token=s3cret", body)
	require.NoError(t, err)
	assert.Len(t, evs, 1)
}

// Without OHS_GARMIN_INBOUND_TOKEN nothing tells a forged push from Garmin's,
// so every notification is refused, a ping included.
func TestHandleInbound_RefusedWithoutAConfiguredToken(t *testing.T) {
	c := newFakeGarmin(t).connector(func(o *Options) { o.InboundToken = "" })
	for _, body := range []string{
		`{"dailies":[{"userId":"u","callbackURL":"https://x"}]}`,
		`{"dailies":[{"userId":"u","summaryId":"s","steps":10}]}`,
	} {
		_, err := inbound(t, c, "/inbound/garmin?token=", body)
		assert.ErrorIs(t, err, ErrNoInboundToken)
		assert.ErrorIs(t, err, ErrBadToken, "answered 401 like a wrong token")
	}
	r := httptest.NewRequest(http.MethodHead, "/inbound/garmin", nil)
	_, err := c.HandleInbound(context.Background(), r)
	assert.NoError(t, err, "a HEAD check of the URL still answers")
}

func TestHandleInbound_CapsBody(t *testing.T) {
	c := newFakeGarmin(t).connector()
	big := `{"dailies":[{"userId":"u","summaryId":"s","pad":"` + strings.Repeat("a", maxInboundBody) + `"}]}`
	_, err := inbound(t, c, "/inbound/garmin", big)
	assert.ErrorContains(t, err, "exceeds")

	items := make([]string, maxItems+1)
	for i := range items {
		items[i] = `{"userId":"u","callbackURL":"https://x"}`
	}
	_, err = inbound(t, c, "/inbound/garmin", `{"dailies":[`+strings.Join(items, ",")+`]}`)
	assert.ErrorContains(t, err, "more than")
}

func TestHandleInbound_EventsAreDeterministic(t *testing.T) {
	c := newFakeGarmin(t).connector()
	body := `{"sleeps":[{"userId":"b","callbackURL":"https://x"}],"dailies":[{"userId":"a","callbackURL":"https://x"}]}`
	a, err := inbound(t, c, "/inbound/garmin", body)
	require.NoError(t, err)
	b, err := inbound(t, c, "/inbound/garmin", body)
	require.NoError(t, err)
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	assert.JSONEq(t, string(ja), string(jb))
	assert.Equal(t, DataTypeDailies, a[0].DataType)
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
