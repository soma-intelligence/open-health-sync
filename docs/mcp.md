# MCP server

Open Health Sync speaks the [Model Context Protocol](https://modelcontextprotocol.io), so an AI
agent can read health data directly. The server is read only: no tool writes, deletes or
connects anything.

Two ways in:

- **Remote (Streamable HTTP)** at `/mcp` on the same listener as the REST API, for agents that
  can call a URL with a bearer header.
- **Local (stdio)** with `ohs mcp`, for desktop agents that launch a command. `ohs mcp` holds no
  data and needs no database: it forwards every message to the `/mcp` endpoint of an instance.

## Credentials

Every request carries `Authorization: Bearer <secret>`, one of:

| Secret | Sees | `user_id` |
|---|---|---|
| Tenant API key (`ohs_sk_...`) | every user of the tenant | required on every per-user tool; `list_users` finds it |
| User token with scope `read` (`ohs_ut_...`) | that one user | omit it (or pass the token's own user); any other value is refused |

A user token without the `read` scope gets 403 on `/mcp`. A user of another tenant, or a user
being deleted, reads as `user not found`, with no data.

### A personal token

For your own agent, mint a user token whose only scope is `read`. Such a token may live up to 30
days (`ttl_seconds` up to 2592000); tokens with `upload` or `connect` keep the one hour default
and a 24 hour maximum.

```sh
curl -s -X POST "$OHS_PUBLIC_URL/v1/users/$USER_ID/tokens" \
  -H "Authorization: Bearer $TENANT_KEY" \
  -H "Content-Type: application/json" \
  -d '{"scopes": ["read"], "ttl_seconds": 2592000}'
# {"token": "ohs_ut_...", "expires_at": "...", "scopes": ["read"]}
```

The token is shown once. It stops working at `expires_at`, or as soon as the user is deleted;
revoking the tenant key that minted it does not end it. Mint a new one before it expires.

## Desktop agent (stdio)

Most desktop agents take an `mcpServers` entry that names a command. Point it at the `ohs`
binary:

```json
{
  "mcpServers": {
    "open-health-sync": {
      "command": "/usr/local/bin/ohs",
      "args": ["mcp"],
      "env": {
        "OHS_MCP_URL": "https://ohs.example.org",
        "OHS_MCP_KEY": "ohs_ut_..."
      }
    }
  }
}
```

| Variable | Default | Meaning |
|---|---|---|
| `OHS_MCP_URL` | `OHS_PUBLIC_URL`, then `http://localhost:8080` | the instance; `/mcp` is appended unless the URL already ends with it; `https` is required except for a loopback host; redirects are not followed, so give the final URL |
| `OHS_MCP_KEY` | none, required | a tenant API key or a user token with scope `read` |

`ohs mcp` connects at start: a wrong URL or secret makes it exit with the reason on stderr
(never the secret). A 401 means the key is wrong or the token expired, a 403 a user token
without the `read` scope, a 404 or 405 a URL that is not the instance's base URL. Its stdout
carries only protocol messages.

The `ohs` binary of each [GitHub release](https://github.com/soma-intelligence/open-health-sync/releases)
runs on Linux and macOS. Without it, the image does the same (`-i` keeps stdin open; inside the
container `localhost` is the container, so give the instance's real address):

```json
"command": "docker",
"args": ["run", "--rm", "-i", "-e", "OHS_MCP_URL", "-e", "OHS_MCP_KEY",
         "ghcr.io/soma-intelligence/open-health-sync:0.1.0", "mcp"]
```

**Claude Desktop** reads this `mcpServers` block from `claude_desktop_config.json`
(Settings, Developer, Edit Config) and picks up a change after a restart. **Claude Code** adds
the same server from the command line:

```sh
claude mcp add open-health-sync -e OHS_MCP_URL=https://ohs.example.org \
  -e OHS_MCP_KEY=ohs_ut_... -- ohs mcp
```

Or the remote endpoint directly, without the binary:

```sh
claude mcp add --transport http open-health-sync https://ohs.example.org/mcp \
  --header "Authorization: Bearer ohs_ut_..."
```

## Remote agent (URL and bearer)

Agents that speak Streamable HTTP connect straight to the instance:

```json
{
  "mcpServers": {
    "open-health-sync": {
      "type": "http",
      "url": "https://ohs.example.org/mcp",
      "headers": { "Authorization": "Bearer ohs_ut_..." }
    }
  }
}
```

The endpoint is stateless (no `Mcp-Session-Id`) and answers in plain JSON, so it works behind
any reverse proxy and any number of `api` instances. Serve it over HTTPS: the bearer secret is
the only credential. Cross-origin browser requests (`Sec-Fetch-Site: cross-site`, or an `Origin`
that does not match the host) are refused with 403.

## Tools

Arguments mirror the REST query parameters. Times take RFC 3339 or `YYYY-MM-DD` (midnight UTC);
`start` is inclusive and `end` exclusive.

| Tool | Arguments | Returns |
|---|---|---|
| `list_users` | `cursor`, `limit` | the tenant's users (tenant key only) |
| `list_quantity_types` | none | the quantity type catalog: `type`, storage `unit`, `ucum_unit`, `loinc_code` |
| `get_samples` | `user_id`, `type` (required), `start`, `end`, `view`, `cursor`, `limit` | quantity samples, as `GET /v1/users/{id}/samples` |
| `get_category_samples` | `user_id`, `type`, `start`, `end`, `view`, `cursor`, `limit` | category samples |
| `get_sleep` | `user_id`, `session_id`, `start`, `end`, `view`, `cursor`, `limit` | sleep sessions; with `session_id`, that session's phases |
| `get_workouts` | `user_id`, `type` (activity), `start`, `end`, `view`, `cursor`, `limit` | workouts |
| `get_workout_route` | `user_id`, `workout_id` (required), `start`, `end`, `cursor`, `limit` | the workout's GPS points |
| `get_nutrition` | `user_id`, `start`, `end`, `cursor`, `limit` | nutrition logs |
| `get_daily` | `user_id`, `start`, `end`, `view`, `cursor`, `limit` | daily summaries per local day |
| `get_rr_intervals` | `user_id`, `series_id`, `start`, `end`, `cursor`, `limit` | beat-to-beat (RR) intervals, as `GET /v1/users/{id}/rr-intervals` |
| `get_hrv` | `user_id`, `series_id`, `start`, `end`, `cursor`, `limit` | the HRV derived from each heartbeat series, as `GET /v1/users/{id}/hrv` (see [hrv.md](hrv.md)) |
| `get_menstrual_cycles` | `user_id`, `start`, `end`, `cursor`, `limit` | menstrual cycles derived from the recorded flow, as `GET /v1/users/{id}/menstrual-cycles`; `start` and `end` select cycles by their start day (see [menstrual-cycles.md](menstrual-cycles.md)) |
| `get_coverage` | `user_id`, `provider`, `start`, `end` | records per provider per day, plus `pending_jobs`, `parked_jobs` and `rejected_records` (lines not stored) |
| `get_tags` | `user_id`, `kind`, `start`, `end`, `cursor`, `limit` | the user's [time tags](time-tags.md) that overlap the window, ongoing ones included, as `GET /v1/users/{id}/tags` |
| `list_connections` | `user_id` | provider connections, status, last sync time and health |
| `list_webhooks` | `deliveries` | the tenant's [webhook](webhooks.md) endpoints and their health; with `deliveries`, each one's 10 latest deliveries (tenant key only; endpoints are managed through the REST API) |

Each result is one JSON text content:

```json
{"records": [...], "next_cursor": "MjAyNi0w...", "truncated": true}
```

`view` is `merged` (the default: when several sources recorded the same thing, only the best
ranked one, so nothing counts twice) or `all` (every source); see
[api.md](api.md#several-sources-of-one-metric-the-merged-view).

`limit` defaults to 100 and stops at 1000 (a larger value is a tool error). A sample is
half a kilobyte of JSON or more, so a full page of 1000 is 500 KB or more of agent context: the
server instructions steer agents to `get_daily` for trends and to narrow windows for raw
samples. When `truncated` is
true, call the same tool again with `cursor` set to `next_cursor`. `get_coverage` is capped
rather than paged: when it is truncated, narrow `start` and `end`.

Records carry strings supplied by devices and providers (source and device names, metadata,
meal labels), and time tags carry text a person wrote (`label`, `note`): an agent must treat them
as data, never as instructions. Tags are recorded through the REST API; `get_tags` only reads
them.

Refusals (missing `user_id`, another user, a bad cursor or time, an unknown workout) come back as
tool errors with a short message, so the agent can correct its call.
