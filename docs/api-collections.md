# API collections

[`api/collections/postman.json`](../api/collections/postman.json) is a Postman collection
(format v2.1) of the API, to try the routes from a desktop client. Postman, Bruno and Insomnia
all import it. It is generated from [`api/openapi.yaml`](../api/openapi.yaml) by the same
generator as the [server SDKs](server-sdks.md), so it matches the API of the same commit; a
test fails when it is out of date.

It holds one folder per tag of the document (users, webhooks, connections, read, fhir...) and
one request per operation, with example parameters and bodies. Optional query parameters are
there but disabled: tick the ones you want. The routes providers call, `/mcp` and `/metrics`
are left out, as in the SDKs.

## Variables

Set them on the collection after the import:

| Variable | Value |
|---|---|
| `baseUrl` | The instance, without a trailing slash. `http://localhost:8080` by default |
| `apiKey` | A tenant API key, `ohs_sk_...`. Sent as a bearer token by every request |
| `userToken` | A user token with the `upload` scope, `ohs_ut_...`, for the two ingest requests only |
| `userId` | A user id, from **Create an end user** |
| `webhookId`, `jobId`, `provider`, ... | The other path parameters, filled in where an example exists |

Authentication is set once at the collection level (bearer `{{apiKey}}`). The ingest requests
override it with `{{userToken}}`, and `healthz` and `readyz` send none.

## Postman

1. **Import**, then pick `api/collections/postman.json`.
2. Open the collection, **Variables** tab: set `baseUrl` and `apiKey`, save.
3. Run **users > Create an end user**, copy its `id` into `userId`.

## Bruno

1. **Import Collection**, choose **Postman Collection**, pick the file and a folder to keep
   the collection in.
2. Collection settings, **Vars** (or an environment): set `baseUrl` and `apiKey`.

Bruno converts the collection into its own `.bru` files; import again after an API change.

## Insomnia

1. **Import**, **From File**, pick the file. Insomnia recognises the Postman v2.1 format.
2. Set `baseUrl` and `apiKey` in the collection's base environment.

## From the OpenAPI document instead

All three can also import the OpenAPI document itself, which every instance serves at
`GET /openapi.yaml` (readable from any origin). The import then follows the instance you point
it at, but you set the variables and the authentication yourself. The collection is the
quicker start.

## Regenerate

```sh
make sdk
```

The collection, the SDKs and the webhook test vectors are written together; commit them with
the change to `api/openapi.yaml`.
