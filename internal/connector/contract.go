// SPDX-License-Identifier: AGPL-3.0-only

// Package connector defines what a provider integration implements and the
// registry the binary uses to find them.
package connector

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// Connector is what every provider implements.
type Connector interface {
	Slug() string            // "withings", "apple_health"
	Enabled(cfg Config) bool // true when its keys are set
	// Map turns one raw payload into canonical records. Pure, no I/O.
	Map(raw RawRecord) (model.MapResult, error)
}

// OAuthConnector: providers the server pulls from.
type OAuthConnector interface {
	Connector
	AuthorizeURL(state, redirect string) (string, error)
	Exchange(ctx context.Context, code, redirect string) (Tokens, error)
	Refresh(ctx context.Context, t Tokens) (Tokens, error)
	Fetch(ctx context.Context, c Connection, from, to time.Time, emit RawSink) error
}

// PushConnector: providers that notify Open Health Sync. Usually also an OAuthConnector.
type PushConnector interface {
	Connector
	HandleInbound(ctx context.Context, r *http.Request) ([]InboundEvent, error)
}

// DeviceConnector: on-device stores uploaded by an SDK.
type DeviceConnector interface {
	Connector
	ParseUpload(meta UploadMeta, body io.Reader, emit RawSink) error
}

// KeyedConnector is optionally implemented by connectors that need operator
// supplied keys (for example CLIENT_ID and CLIENT_SECRET). The registry treats
// a missing or empty required key as "not configured". Connectors that do not
// implement it (device connectors) are always enabled.
type KeyedConnector interface {
	RequiredKeys() []string
}

// Configurable is optionally implemented by connectors that take their
// operator keys at startup. The app calls Configure once per enabled
// connector before any role starts; an error stops startup.
type Configurable interface {
	Configure(cfg Config) error
}

// Subscriber is optionally implemented by an OAuthConnector whose provider
// pushes notifications to a registered callback URL. callbackURL is
// <OHS_PUBLIC_URL>/inbound/<slug>; the connector may decorate it (signature)
// as long as Unsubscribe names what Subscribe registered. The collect role
// keeps one durable subscription row per connection: it subscribes pending
// rows with fresh tokens and unsubscribes on disconnect or user deletion.
type Subscriber interface {
	Subscribe(ctx context.Context, c Connection, callbackURL string) error
	Unsubscribe(ctx context.Context, c Connection, callbackURL string) error
}

// ChallengeResponder is optionally implemented by a PushConnector whose
// provider validates the callback URL with a GET that must be answered with a
// body, such as the hub.challenge echo of a Strava push subscription. The
// probe route (GET and HEAD /inbound/<slug>) hands it the request. It returns
// the content type and body of a 200 answer (a nil body answers 200 with no
// body, as for any provider), or an error, which the route answers 401 like a
// refused notification. It must not block: providers wait a few seconds.
type ChallengeResponder interface {
	AnswerChallenge(r *http.Request) (contentType string, body []byte, err error)
}

// AsOAuth reports whether c can be pulled from with OAuth.
func AsOAuth(c Connector) (OAuthConnector, bool) { o, ok := c.(OAuthConnector); return o, ok }

// AsPush reports whether c receives provider notifications.
func AsPush(c Connector) (PushConnector, bool) { p, ok := c.(PushConnector); return p, ok }

// AsDevice reports whether c accepts SDK uploads.
func AsDevice(c Connector) (DeviceConnector, bool) { d, ok := c.(DeviceConnector); return d, ok }
