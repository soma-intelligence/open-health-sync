# Hosted connection page

Connecting a cloud provider takes a consent flow: a button per provider, a call to get the
consent URL, a redirect, and a screen for the result. Open Health Sync can serve that screen for
you. Your server creates a **connect session** for one user and sends the user to the link it
gets back. The page lists the providers, shows which ones the user already connected, and starts
the provider's consent when the user picks one.

If you prefer your own UI, keep using `POST /v1/users/{id}/connections/{provider}` as described
in the [API guide](api.md#connecting-a-provider). Both paths start the same flow and store the
same connections.

## Create a session

From your server, with the tenant API key:

```sh
curl -s -X POST $OHS/v1/users/$USER_ID/connect-sessions -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' \
  -d '{"redirect_uri":"https://app.example.org/connected","app_name":"Sleep Diary",
       "logo_url":"https://app.example.org/logo.png","providers":["withings","oura"]}'
# 201 {"url":"https://ohs.example.org/connect?session=Zm9v...","expires_at":"2026-10-13T10:30:00Z"}
```

Every field is optional:

| Field | Rule | Default |
|---|---|---|
| `redirect_uri` | absolute https URL (http only for localhost), at most 2048 characters | none: the built-in result page |
| `providers` | slugs of OAuth providers enabled on this instance, at most 64, shown in this order | every enabled OAuth provider |
| `app_name` | at most 80 characters, shown as the page title | a generic title |
| `logo_url` | absolute https URL, at most 2048 characters | no logo |
| `ttl_seconds` | 60 to 3600 | 1800 (30 minutes) |

An unknown slug, a device provider (Apple Health, Health Connect) or any other invalid field is a
400. A user of another tenant, an unknown user or a user being deleted is a 404. The list of
providers is fixed when the session is created; `GET /v1/providers` lists what is enabled.

## Send the user

Open `url` in the user's browser: a redirect from your app, a link in an email, or a web view
in a mobile app. The page shows your logo and app name and one row per provider, with what that
source shares (for example "Sleep, readiness, activity and heart rate" for Oura) and the user's
status for it:

- **Connected**: an active connection. The button reconnects, which is harmless.
- **Needs reconnecting**: the provider stopped accepting the grant (`needs_reauth`).
- **Not connected**: no connection yet, or a disconnected one.

To show a single provider, append its slug to the path:
`https://ohs.example.org/connect/withings?session=...`.

The whole row is the button. Beside the list, two figures give how many of the sources are
connected and how many minutes the link has left. The panels sit on a background the page draws
itself (gradients and SVG noise, no image or font is downloaded), so it has no script, loads
nothing but your logo, and fits a phone screen. The space around it follows the light or dark
setting of the device. The list says that the access is read only, and under it the page tells
the user that they sign in on each source's own site.

When the user presses a button, the page sends them to the provider's consent screen. After
consent they land on `redirect_uri?status=connected&provider=withings` (`status=error` on
failure), exactly as with the API route. Without a `redirect_uri` they see the built-in result
page, with a link back to the connection page while the session lasts.

The session can be used for several providers until it expires, so a user can connect Withings,
come back, and connect Oura with the same link. Each button press mints a new single-use OAuth
state that expires after 10 minutes, as the API route does. Create a fresh session each time
you show the page rather than storing links.

An unknown link answers 404 and an expired one 410, as a short page that tells the user to go
back to the app. Expired sessions are purged an hour after they expire, after which the link
answers 404.

## Routes

| Route | Auth | Purpose |
|---|---|---|
| `POST /v1/users/{id}/connect-sessions` | tenant key | Create a session, get the link |
| `GET /connect?session=...` | the token | The page with every provider of the session |
| `GET /connect/{provider}?session=...` | the token | The page with one provider |
| `POST /connect/{provider}` | the token, as form field `session` | Start consent, 303 to the provider |

The full schemas are in [api/openapi.yaml](../api/openapi.yaml).

## Security notes

- The token in the link is the only credential of the page: whoever has the link can connect
  sources to that user's account until it expires. Send it to that user only, over https, and
  keep the TTL short. It is 32 random bytes; Open Health Sync stores only its SHA-256, so a read
  of the database does not give out live links.
- The page never shows data: only provider names and connection statuses.
- The page runs no script. Its Content-Security-Policy is `default-src 'none'`, with inline
  styles, images from https only (your logo), and `form-action` limited to the instance itself
  and the origins of the providers' consent pages (browsers check the redirect that follows a
  form submission too). It cannot be framed (`X-Frame-Options: DENY`,
  `frame-ancestors 'none'`), is never cached (`Cache-Control: no-store`) and sends no referrer.
- The logo is loaded by the user's browser from your URL. Host it somewhere that does not log
  anything you would not want tied to the visit.
- Without a `redirect_uri`, the session token also goes into an `HttpOnly`, `SameSite=Lax`
  cookie scoped to `/oauth/`, so the result page after the provider's callback can link back to
  the connection page. It is `Secure` when `OHS_PUBLIC_URL` is https and expires with the
  session.
- The link starts with `OHS_PUBLIC_URL`. Set it to the public https address of the instance,
  the same one registered as the callback with each provider.
