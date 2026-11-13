// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"net/http"
	"strings"

	"github.com/soma-intelligence/open-health-sync/internal/auth"
)

// routeScopes maps every route behind an API key or a user token to the one
// key scope it needs (auth.KeyScopes), by its mux pattern. The routes under
// /v1/admin are not listed: routeScope answers for them by their method.
// TestEveryAuthenticatedRouteHasAScope fails when a route is missing here.
var routeScopes = map[string]string{
	"GET /v1/tenant":    auth.ScopeTenantRead,
	"GET /v1/providers": auth.ScopeTenantRead,

	"GET /v1/keys":             auth.ScopeKeysRead,
	"POST /v1/keys":            auth.ScopeKeysWrite,
	"DELETE /v1/keys/{key_id}": auth.ScopeKeysWrite,

	"GET /v1/audit": auth.ScopeAuditRead,

	"GET /v1/users":                                            auth.ScopeUsersRead,
	"GET /v1/users/{id}":                                       auth.ScopeUsersRead,
	"POST /v1/users":                                           auth.ScopeUsersWrite,
	"PATCH /v1/users/{id}":                                     auth.ScopeUsersWrite,
	"GET /v1/users/{id}/consents":                              auth.ScopeUsersRead,
	"GET /v1/users/{id}/consents/history":                      auth.ScopeUsersRead,
	"PUT /v1/users/{id}/consents/{provider}":                   auth.ScopeUsersWrite,
	"DELETE /v1/users/{id}/consents/{provider}":                auth.ScopeUsersWrite,
	"POST /v1/users/{id}/tokens":                               auth.ScopeTokensWrite,
	"DELETE /v1/users/{id}/tokens":                             auth.ScopeTokensWrite,
	"DELETE /v1/users/{id}/tokens/{token_id}":                  auth.ScopeTokensWrite,
	"DELETE /v1/users/{id}":                                    auth.ScopeDataDelete,
	"DELETE /v1/users/{id}/data/{provider}":                    auth.ScopeDataDelete,
	"GET /v1/deletions/{job_id}":                               auth.ScopeDataDelete,
	"GET /v1/users/{id}/samples":                               auth.ScopeDataRead,
	"GET /v1/users/{id}/samples/stats":                         auth.ScopeDataRead,
	"GET /v1/users/{id}/category-samples":                      auth.ScopeDataRead,
	"GET /v1/users/{id}/sleep":                                 auth.ScopeDataRead,
	"GET /v1/users/{id}/sleep/{session_id}/phases":             auth.ScopeDataRead,
	"GET /v1/users/{id}/workouts":                              auth.ScopeDataRead,
	"GET /v1/users/{id}/workouts/{workout_id}/route":           auth.ScopeDataRead,
	"GET /v1/users/{id}/nutrition":                             auth.ScopeDataRead,
	"GET /v1/users/{id}/daily":                                 auth.ScopeDataRead,
	"GET /v1/users/{id}/rr-intervals":                          auth.ScopeDataRead,
	"GET /v1/users/{id}/hrv":                                   auth.ScopeDataRead,
	"GET /v1/users/{id}/menstrual-cycles":                      auth.ScopeDataRead,
	"GET /v1/users/{id}/ecg":                                   auth.ScopeDataRead,
	"GET /v1/users/{id}/ecg/{ecg_id}":                          auth.ScopeDataRead,
	"GET /v1/users/{id}/coverage":                              auth.ScopeDataRead,
	"GET /v1/users/{id}/fhir/Observation":                      auth.ScopeDataRead,
	"GET /v1/users/{id}/fhir/Observation/{observation_id}":     auth.ScopeDataRead,
	"GET /v1/users/{id}/fhir/Patient/{patient_id}":             auth.ScopeDataRead,
	"GET /v1/users/{id}/fhir/Patient/{patient_id}/$everything": auth.ScopeDataRead,
	"GET /v1/users/{id}/fhir/Device":                           auth.ScopeDataRead,
	"GET /v1/users/{id}/fhir/Device/{device_id}":               auth.ScopeDataRead,
	"GET /v1/users/{id}/fhir/Provenance":                       auth.ScopeDataRead,
	"GET /v1/users/{id}/fhir/Provenance/{provenance_id}":       auth.ScopeDataRead,
	"GET /v1/users/{id}/fhir/metadata":                         auth.ScopeDataRead,
	"GET /v1/users/{id}/omh/data-points":                       auth.ScopeDataRead,
	"GET /v1/users/{id}/tags":                                  auth.ScopeDataRead,
	"GET /v1/users/{id}/tags/{tag_id}":                         auth.ScopeDataRead,
	"GET /v1/users/{id}/source-priorities":                     auth.ScopeDataRead,
	"/mcp":                                                     auth.ScopeDataRead,
	"POST /v1/users/{id}/tags":                                 auth.ScopeDataWrite,
	"PATCH /v1/users/{id}/tags/{tag_id}":                       auth.ScopeDataWrite,
	"DELETE /v1/users/{id}/tags/{tag_id}":                      auth.ScopeDataWrite,
	"PUT /v1/users/{id}/source-priorities":                     auth.ScopeDataWrite,
	"POST /v1/users/{id}/self-reports":                         auth.ScopeDataWrite,
	"POST /v1/ingest/apple-health":                             auth.ScopeDataWrite,
	"POST /v1/ingest/health-connect":                           auth.ScopeDataWrite,
	"POST /v1/ingest/xiaomi":                                   auth.ScopeDataWrite,
	"POST /v1/ingest/activity-files":                           auth.ScopeDataWrite,
	"POST /v1/ingest/bluetooth":                                auth.ScopeDataWrite,

	"GET /v1/users/{id}/connections":                             auth.ScopeConnectionsRead,
	"GET /v1/users/{id}/freshness":                               auth.ScopeConnectionsRead,
	"GET /v1/users/{id}/sync-runs":                               auth.ScopeConnectionsRead,
	"GET /v1/users/{id}/sync-runs/{run}":                         auth.ScopeConnectionsRead,
	"GET /v1/pipeline":                                           auth.ScopeConnectionsRead,
	"GET /v1/pipeline/sync-runs":                                 auth.ScopeConnectionsRead,
	"GET /v1/pipeline/etl-jobs":                                  auth.ScopeConnectionsRead,
	"POST /v1/users/{id}/connections/{provider}":                 auth.ScopeConnectionsWrite,
	"DELETE /v1/users/{id}/connections/{provider}":               auth.ScopeConnectionsWrite,
	"POST /v1/users/{id}/connections/{provider}/sync":            auth.ScopeConnectionsWrite,
	"POST /v1/users/{id}/sync-runs/{run}/retry":                  auth.ScopeConnectionsWrite,
	"POST /v1/users/{id}/connections/{provider}/sync-runs/retry": auth.ScopeConnectionsWrite,
	"POST /v1/users/{id}/connect-sessions":                       auth.ScopeConnectionsWrite,
	"POST /v1/pipeline/etl-jobs/retry":                           auth.ScopeConnectionsWrite,
	"POST /v1/pipeline/etl-jobs/{job_id}/retry":                  auth.ScopeConnectionsWrite,

	"GET /v1/webhooks":                                               auth.ScopeWebhooksRead,
	"GET /v1/webhooks/{webhook_id}":                                  auth.ScopeWebhooksRead,
	"GET /v1/webhooks/{webhook_id}/deliveries":                       auth.ScopeWebhooksRead,
	"GET /v1/webhooks/{webhook_id}/deliveries/{delivery_id}":         auth.ScopeWebhooksRead,
	"POST /v1/webhooks":                                              auth.ScopeWebhooksWrite,
	"PATCH /v1/webhooks/{webhook_id}":                                auth.ScopeWebhooksWrite,
	"DELETE /v1/webhooks/{webhook_id}":                               auth.ScopeWebhooksWrite,
	"POST /v1/webhooks/{webhook_id}/rotate-secret":                   auth.ScopeWebhooksWrite,
	"POST /v1/webhooks/{webhook_id}/test":                            auth.ScopeWebhooksWrite,
	"POST /v1/webhooks/{webhook_id}/deliveries/{delivery_id}/retry":  auth.ScopeWebhooksWrite,
	"POST /v1/webhooks/{webhook_id}/deliveries/{delivery_id}/replay": auth.ScopeWebhooksWrite,

	"GET /v1/users/{id}/exports/{dataset}":    auth.ScopeExportsRead,
	"GET /v1/users/{id}/archive":              auth.ScopeExportsRead,
	"GET /v1/users/{id}/shares":               auth.ScopeExportsRead,
	"GET /v1/users/{id}/shares/{share_id}":    auth.ScopeExportsRead,
	"POST /v1/users/{id}/shares":              auth.ScopeExportsWrite,
	"DELETE /v1/users/{id}/shares/{share_id}": auth.ScopeExportsWrite,
}

// routeScope names the key scope the route of a request needs. Under
// /v1/admin a read needs admin:read and anything else admin:write, whatever
// the route does for the tenant it names: an instance admin's power is over
// the instance. A route that declares no scope needs every scope, so only a
// key with full access reaches it until it is listed.
func routeScope(r *http.Request) string {
	method, path, ok := strings.Cut(r.Pattern, " ")
	if !ok {
		method, path = r.Method, r.Pattern
	}
	if strings.HasPrefix(path, "/v1/admin/") {
		if method == http.MethodGet {
			return auth.ScopeAdminRead
		}
		return auth.ScopeAdminWrite
	}
	if scope, ok := routeScopes[r.Pattern]; ok {
		return scope
	}
	return auth.ScopeAll
}
