// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/logging"
	"github.com/soma-intelligence/open-health-sync/internal/logutil"
	"github.com/soma-intelligence/open-health-sync/internal/webhook"
)

type createWebhookRequest struct {
	URL         string   `json:"url"`
	Description string   `json:"description"`
	EventTypes  []string `json:"event_types"`
}

// webhookJSON is an endpoint as the API shows it: the stored endpoint and
// the delivery state an operator needs to tell why nothing arrives. The
// three fields are read only: the delivery worker and the secret rotation
// set them.
type webhookJSON struct {
	webhook.Endpoint
	// ConsecutiveFailures counts the failed attempts since the last success.
	ConsecutiveFailures int `json:"consecutive_failures"`
	// PausedUntil is when a paused endpoint is tried again, null when it is
	// not paused.
	PausedUntil *time.Time `json:"paused_until"`
	// PreviousSecretExpiresAt is until when the secret the last rotation
	// replaced also signs, null outside a rotation's grace period.
	PreviousSecretExpiresAt *time.Time `json:"previous_secret_expires_at"`
}

func toWebhookJSON(e webhook.Endpoint) webhookJSON {
	return webhookJSON{Endpoint: e, ConsecutiveFailures: e.ConsecutiveFailures,
		PausedUntil: e.PausedUntil, PreviousSecretExpiresAt: e.PreviousSecretExpiresAt}
}

// webhookWithSecret is an endpoint with its signing secret, returned by the
// create call only.
type webhookWithSecret struct {
	webhookJSON
	Secret string `json:"secret"`
}

type updateWebhookRequest struct {
	URL         *string   `json:"url"`
	Description *string   `json:"description"`
	EventTypes  *[]string `json:"event_types"`
	Enabled     *bool     `json:"enabled"`
}

// rotateSecretRequest is the optional body of the rotate route.
type rotateSecretRequest struct {
	// ExpirePrevious ends the replaced secret at once instead of after the
	// grace period: the rotation after a leak.
	ExpirePrevious bool `json:"expire_previous"`
}

type secretResponse struct {
	Secret string `json:"secret"`
	// PreviousSecretExpiresAt is until when the replaced secret also signs,
	// null when it stopped at once.
	PreviousSecretExpiresAt *time.Time `json:"previous_secret_expires_at"`
}

// testWebhookRequest is the optional body of the test route.
type testWebhookRequest struct {
	EventType string `json:"event_type"`
}

func (s *server) mountWebhooks(mux *http.ServeMux) {
	if s.d.Webhooks == nil {
		return
	}
	// Not wrapped in idempotent: a replay would store the plaintext secret.
	s.tenantRoute(mux, "POST /v1/webhooks", s.createWebhook)
	s.tenantRoute(mux, "GET /v1/webhooks", s.listWebhooks)
	s.tenantRoute(mux, "GET /v1/webhooks/{webhook_id}", s.getWebhook)
	s.tenantRoute(mux, "PATCH /v1/webhooks/{webhook_id}", s.updateWebhook)
	s.tenantRoute(mux, "DELETE /v1/webhooks/{webhook_id}", s.deleteWebhook)
	s.tenantRoute(mux, "POST /v1/webhooks/{webhook_id}/rotate-secret", s.rotateWebhookSecret)
	s.tenantRoute(mux, "POST /v1/webhooks/{webhook_id}/test", s.testWebhook)
	s.tenantRoute(mux, "GET /v1/webhooks/{webhook_id}/deliveries", s.listWebhookDeliveries)
	s.tenantRoute(mux, "GET /v1/webhooks/{webhook_id}/deliveries/{delivery_id}", s.getWebhookDelivery)
	s.tenantRoute(mux, "POST /v1/webhooks/{webhook_id}/deliveries/{delivery_id}/retry", s.retryWebhookDelivery)
	s.tenantRoute(mux, "POST /v1/webhooks/{webhook_id}/deliveries/{delivery_id}/replay", s.replayWebhookDelivery)
}

// pathWebhook parses {webhook_id}. A malformed id is an endpoint that does
// not exist.
func pathWebhook(w http.ResponseWriter, r *http.Request) (auth.Principal, uuid.UUID, bool) {
	p, _ := auth.From(r.Context())
	id, err := uuid.Parse(r.PathValue("webhook_id"))
	if err != nil {
		notFound(w, "webhook")
		return p, uuid.Nil, false
	}
	return p, id, true
}

// checkWebhookFields validates what create and update share; it writes the
// problem and returns false on a refusal.
func (s *server) checkWebhookFields(w http.ResponseWriter, url, description *string, types *[]string) bool {
	if url != nil {
		if err := webhook.CheckURL(*url, s.d.WebhookAllowPrivate); err != nil {
			badRequest(w, err.Error())
			return false
		}
	}
	if description != nil && len(*description) > webhook.MaxDescription {
		badRequest(w, "description is at most 255 characters")
		return false
	}
	if types != nil {
		clean, err := webhook.CheckTypes(*types)
		if err != nil {
			badRequest(w, "event_types: unknown type; known types are "+strings.Join(webhook.Types, ", "))
			return false
		}
		*types = clean
	}
	return true
}

func (s *server) createWebhook(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.From(r.Context())
	var req createWebhookRequest
	if !decodeJSON(w, r, &req, false) {
		return
	}
	if req.URL == "" {
		badRequest(w, "url is required")
		return
	}
	if req.EventTypes == nil {
		req.EventTypes = []string{}
	}
	if !s.checkWebhookFields(w, &req.URL, &req.Description, &req.EventTypes) {
		return
	}
	e, secret, err := s.d.Webhooks.Create(r.Context(), p.TenantID, req.URL, req.Description, req.EventTypes)
	switch {
	case errors.Is(err, webhook.ErrTooMany):
		problem(w, http.StatusConflict, "this tenant already has 20 webhooks: delete one first")
		return
	case err != nil:
		internalError(w, r, "create_webhook_failed", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Location", "/v1/webhooks/"+e.ID.String())
	writeJSON(w, http.StatusCreated, webhookWithSecret{webhookJSON: toWebhookJSON(e), Secret: secret})
}

func (s *server) listWebhooks(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.From(r.Context())
	list, err := s.d.Webhooks.List(r.Context(), p.TenantID)
	if err != nil {
		internalError(w, r, "list_webhooks_failed", err)
		return
	}
	out := make([]webhookJSON, len(list))
	for i, e := range list {
		out[i] = toWebhookJSON(e)
	}
	writeJSON(w, http.StatusOK, newPage(out, ""))
}

func (s *server) getWebhook(w http.ResponseWriter, r *http.Request) {
	p, id, ok := pathWebhook(w, r)
	if !ok {
		return
	}
	e, err := s.d.Webhooks.Get(r.Context(), p.TenantID, id)
	if !webhookErr(w, r, err, "get_webhook_failed") {
		return
	}
	writeJSON(w, http.StatusOK, toWebhookJSON(e))
}

func (s *server) updateWebhook(w http.ResponseWriter, r *http.Request) {
	p, id, ok := pathWebhook(w, r)
	if !ok {
		return
	}
	var req updateWebhookRequest
	if !decodeJSON(w, r, &req, false) {
		return
	}
	if !s.checkWebhookFields(w, req.URL, req.Description, req.EventTypes) {
		return
	}
	e, err := s.d.Webhooks.Update(r.Context(), p.TenantID, id, webhook.Patch{
		URL: req.URL, Description: req.Description, EventTypes: req.EventTypes, Enabled: req.Enabled,
	})
	if !webhookErr(w, r, err, "update_webhook_failed") {
		return
	}
	writeJSON(w, http.StatusOK, toWebhookJSON(e))
}

func (s *server) deleteWebhook(w http.ResponseWriter, r *http.Request) {
	p, id, ok := pathWebhook(w, r)
	if !ok {
		return
	}
	if !webhookErr(w, r, s.d.Webhooks.Delete(r.Context(), p.TenantID, id), "delete_webhook_failed") {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) rotateWebhookSecret(w http.ResponseWriter, r *http.Request) {
	p, id, ok := pathWebhook(w, r)
	if !ok {
		return
	}
	var req rotateSecretRequest
	if !decodeJSON(w, r, &req, true) {
		return
	}
	var (
		secret string
		until  *time.Time
		err    error
	)
	if req.ExpirePrevious {
		secret, until, err = s.d.Webhooks.RotateSecretWithGrace(r.Context(), p.TenantID, id, 0)
	} else {
		// The store's grace period (OHS_WEBHOOK_ROTATION_GRACE_HOURS).
		secret, err = s.d.Webhooks.RotateSecret(r.Context(), p.TenantID, id)
	}
	if !webhookErr(w, r, err, "rotate_webhook_secret_failed") {
		return
	}
	if !req.ExpirePrevious {
		// Read back when the replaced secret stops. The rotation is done:
		// a failure here must not hide the new secret, shown only now.
		if e, gerr := s.d.Webhooks.Get(r.Context(), p.TenantID, id); gerr == nil {
			until = e.PreviousSecretExpiresAt
		} else {
			logutil.Err(*logging.Ctx(r.Context()), gerr, zerolog.WarnLevel).Msg("rotate_webhook_secret_grace_failed")
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, secretResponse{Secret: secret, PreviousSecretExpiresAt: until})
}

func (s *server) testWebhook(w http.ResponseWriter, r *http.Request) {
	p, id, ok := pathWebhook(w, r)
	if !ok {
		return
	}
	var req testWebhookRequest
	if !decodeJSON(w, r, &req, true) {
		return
	}
	if req.EventType != "" && req.EventType != webhook.Test && !webhook.KnownType(req.EventType) {
		badRequest(w, "event_type: unknown type; known types are "+strings.Join(webhook.Types, ", ")+", "+webhook.Test)
		return
	}
	d, err := s.d.Webhooks.SendSample(r.Context(), p.TenantID, id, req.EventType)
	if !webhookErr(w, r, err, "test_webhook_failed") {
		return
	}
	writeJSON(w, http.StatusAccepted, d)
}

var deliveryStatuses = map[string]bool{"": true, "pending": true, "running": true, "done": true, "parked": true}

func (s *server) listWebhookDeliveries(w http.ResponseWriter, r *http.Request) {
	p, id, ok := pathWebhook(w, r)
	if !ok {
		return
	}
	qs := r.URL.Query()
	q := webhook.DeliveryQuery{Status: qs.Get("status"), Cursor: qs.Get("cursor")}
	if !deliveryStatuses[q.Status] {
		badRequest(w, "status must be one of pending, running, done, parked")
		return
	}
	if v := qs.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > webhook.MaxDeliveryLimit {
			badRequest(w, "limit must be an integer between 1 and 200")
			return
		}
		q.Limit = n
	}
	list, next, err := s.d.Webhooks.ListDeliveries(r.Context(), p.TenantID, id, q)
	if errors.Is(err, webhook.ErrCursor) {
		badRequest(w, "the cursor is not valid")
		return
	}
	if !webhookErr(w, r, err, "list_webhook_deliveries_failed") {
		return
	}
	writeJSON(w, http.StatusOK, newPage(list, next))
}

// pathDelivery parses {webhook_id} and {delivery_id}. A malformed id is a
// delivery that does not exist.
func pathDelivery(w http.ResponseWriter, r *http.Request) (auth.Principal, uuid.UUID, int64, bool) {
	p, id, ok := pathWebhook(w, r)
	if !ok {
		return p, id, 0, false
	}
	delivery, err := strconv.ParseInt(r.PathValue("delivery_id"), 10, 64)
	if err != nil || delivery <= 0 {
		notFound(w, "delivery")
		return p, id, 0, false
	}
	return p, id, delivery, true
}

func (s *server) getWebhookDelivery(w http.ResponseWriter, r *http.Request) {
	p, id, delivery, ok := pathDelivery(w, r)
	if !ok {
		return
	}
	d, err := s.d.Webhooks.GetDelivery(r.Context(), p.TenantID, id, delivery)
	if !deliveryErr(w, r, err, "get_webhook_delivery_failed") {
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *server) retryWebhookDelivery(w http.ResponseWriter, r *http.Request) {
	p, id, delivery, ok := pathDelivery(w, r)
	if !ok {
		return
	}
	d, err := s.d.Webhooks.Retry(r.Context(), p.TenantID, id, delivery)
	if !deliveryErr(w, r, err, "retry_webhook_delivery_failed") {
		return
	}
	writeJSON(w, http.StatusAccepted, d)
}

func (s *server) replayWebhookDelivery(w http.ResponseWriter, r *http.Request) {
	p, id, delivery, ok := pathDelivery(w, r)
	if !ok {
		return
	}
	d, err := s.d.Webhooks.Replay(r.Context(), p.TenantID, id, delivery)
	if !deliveryErr(w, r, err, "replay_webhook_delivery_failed") {
		return
	}
	w.Header().Set("Location", "/v1/webhooks/"+id.String()+"/deliveries/"+strconv.FormatInt(d.ID, 10))
	writeJSON(w, http.StatusAccepted, d)
}

// deliveryErr answers the errors of the delivery routes. It returns true
// when err is nil.
func deliveryErr(w http.ResponseWriter, r *http.Request, err error, event string) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, webhook.ErrNotRetryable):
		problem(w, http.StatusConflict, "this delivery has not finished yet: only done or parked deliveries are sent again")
	case errors.Is(err, webhook.ErrTooManyReplays):
		problem(w, http.StatusTooManyRequests, "this webhook already has "+strconv.Itoa(webhook.MaxQueuedReplays)+
			" replays waiting to be sent: wait for them to finish")
	case errors.Is(err, webhook.ErrNotFound):
		notFound(w, "delivery")
	default:
		internalError(w, r, event, err)
	}
	return false
}

// webhookErr answers 404 for ErrNotFound and 500 for any other error. It
// returns true when err is nil.
func webhookErr(w http.ResponseWriter, r *http.Request, err error, event string) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, webhook.ErrNotFound):
		notFound(w, "webhook")
	default:
		internalError(w, r, event, err)
	}
	return false
}
