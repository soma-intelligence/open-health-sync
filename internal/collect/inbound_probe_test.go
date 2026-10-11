// SPDX-License-Identifier: AGPL-3.0-only

package collect

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

type probePush struct{ slug string }

func (p probePush) Slug() string                                   { return p.slug }
func (probePush) Enabled(connector.Config) bool                    { return true }
func (probePush) Map(connector.RawRecord) (model.MapResult, error) { return model.MapResult{}, nil }
func (probePush) HandleInbound(context.Context, *http.Request) ([]connector.InboundEvent, error) {
	return nil, nil
}

// probeChallenge echoes the "challenge" query parameter when "token" is
// "good", answers a plain probe with no body, and refuses anything else.
type probeChallenge struct{ probePush }

func (probeChallenge) AnswerChallenge(r *http.Request) (string, []byte, error) {
	q := r.URL.Query()
	switch {
	case q.Get("challenge") == "":
		return "", nil, nil
	case q.Get("token") == "good":
		return "application/json", []byte(`{"c":"` + q.Get("challenge") + `"}`), nil
	default:
		return "", nil, errors.New("bad token")
	}
}

func probe(t *testing.T, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	byID := map[string]connector.Connector{
		"plain": probePush{"plain"},
		"chal":  probeChallenge{probePush{"chal"}},
	}
	h := &Handlers{d: Deps{Lookup: func(s string) (connector.Connector, bool) { c, ok := byID[s]; return c, ok }}}
	mux := http.NewServeMux()
	mux.Handle("GET /inbound/{provider}", h.InboundProbe())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func TestInboundProbe_PlainPushAnswersEmpty200(t *testing.T) {
	rec := probe(t, http.MethodGet, "/inbound/plain?challenge=x&token=good")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, rec.Body.String())
}

func TestInboundProbe_ChallengeResponderAnswers(t *testing.T) {
	rec := probe(t, http.MethodGet, "/inbound/chal?challenge=abc&token=good")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.Equal(t, `{"c":"abc"}`, rec.Body.String())

	rec = probe(t, http.MethodHead, "/inbound/chal")
	assert.Equal(t, http.StatusOK, rec.Code, "a plain reachability probe still answers 200")
	assert.Empty(t, rec.Body.String())

	rec = probe(t, http.MethodGet, "/inbound/chal?challenge=abc&token=bad")
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.NotContains(t, rec.Body.String(), "abc")
}

func TestInboundProbe_UnknownProvider(t *testing.T) {
	assert.Equal(t, http.StatusNotFound, probe(t, http.MethodGet, "/inbound/nope").Code)
}
