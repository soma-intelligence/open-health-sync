// SPDX-License-Identifier: AGPL-3.0-only

// Package connector defines what a provider integration implements and the
// registry the binary uses to find them.
package connector

import (
	"context"
	"errors"
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

// OptionalKeyed is optionally implemented by a KeyedConnector that also
// reads keys it can run without (a webhook secret, a region). The console
// lists them beside RequiredKeys.
type OptionalKeyed interface {
	OptionalKeys() []string
}

// Configurable is optionally implemented by connectors that take their
// operator keys. The app calls Configure for every enabled connector before
// any role starts (an error stops startup), and again whenever the keys
// saved in the console change. Configure must validate before it changes
// anything, so a refused call leaves the connector as it was, and be safe to
// call while the connector is in use.
type Configurable interface {
	Configure(cfg Config) error
}

// publicKeys are the operator keys whose values are not secrets: the console
// shows them back. Every other key is write only.
var publicKeys = map[string]bool{
	"CLIENT_ID":         true,
	"REGION":            true,
	"ENVIRONMENT":       true,
	"WEBHOOK_SIGNATURE": true,
	"SUBSCRIPTION_ID":   true,
}

// SecretKey reports whether the value of an operator key (a FIELD of
// OHS_<SLUG>_<FIELD>) is a secret, never shown once stored. Any key not
// known to be public is a secret.
func SecretKey(field string) bool { return !publicKeys[field] }

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

// NotifiedFetcher is optionally implemented by an OAuthConnector whose
// notifications name what changed (InboundEvent.DataType). FetchNotified
// pulls only what such a notification can concern, usually one or two
// provider calls instead of one per data type, which keeps the app's rate
// limit for what just changed. dataType is a value the connector put in
// InboundEvent.DataType.
type NotifiedFetcher interface {
	FetchNotified(ctx context.Context, c Connection, from, to time.Time, dataType string, emit RawSink) error
}

// StateExchanger is optionally implemented by an OAuthConnector whose
// provider requires PKCE, such as Garmin. AuthorizeURL and the exchange are
// separate requests, so the connector derives the code verifier from the
// OAuth state (for example an HMAC of it under its client secret) instead of
// storing it. The callback hands the state back: when a connector implements
// this interface, ExchangeState is called instead of Exchange.
type StateExchanger interface {
	ExchangeState(ctx context.Context, code, state, redirect string) (Tokens, error)
}

// PasswordConnector is optionally implemented by an OAuthConnector whose
// provider has no consent page for third parties, such as Eight Sleep: the
// user types the account email and password into the sign-in page this
// server serves, and SignIn trades them for Tokens. The core then stores the
// connection exactly as after an OAuth callback. When a connector implements
// it, the core sends the user to its own sign-in page instead of
// AuthorizeURL, and never calls Exchange.
//
// What the connector needs to sign in again later (usually the credentials
// themselves, when the provider issues no refresh token) goes in
// Tokens.Refresh, which the core seals like any refresh token; Refresh then
// signs in again with it.
type PasswordConnector interface {
	SignIn(ctx context.Context, username, password string) (Tokens, error)
}

// ErrBadCredentials is returned by PasswordConnector.SignIn when the provider
// refuses the email and password. The sign-in page asks the user again.
var ErrBadCredentials = errors.New("connector: the provider refused the email or password")

// AsPassword reports whether c signs users in with their provider password.
func AsPassword(c Connector) (PasswordConnector, bool) { p, ok := c.(PasswordConnector); return p, ok }

// AsOAuth reports whether c can be pulled from with OAuth.
func AsOAuth(c Connector) (OAuthConnector, bool) { o, ok := c.(OAuthConnector); return o, ok }

// AsPush reports whether c receives provider notifications.
func AsPush(c Connector) (PushConnector, bool) { p, ok := c.(PushConnector); return p, ok }

// AsDevice reports whether c accepts SDK uploads.
func AsDevice(c Connector) (DeviceConnector, bool) { d, ok := c.(DeviceConnector); return d, ok }
