# Give an AI agent read-only access to wearable data over MCP

To give an AI agent read-only access to wearable data, point it at the `/mcp` endpoint of your
Open Health Sync instance with a bearer token: the agent gets tools such as `get_daily`,
`get_sleep` and `get_samples` over every source the instance holds (Oura, WHOOP, Garmin,
Withings, Apple Health and the rest), and no tool can write, delete or connect anything. Open
Health Sync is open source and self-hosted.

This page covers a remote agent that calls a URL. For Claude Desktop with an Apple Health export
on your own computer, follow [Connect Claude to your Apple Health data](apple-health-mcp.md).

## 1. Pick a credential

Every MCP request carries `Authorization: Bearer <secret>`:

| Secret | The agent sees | `user_id` on per-user tools |
|---|---|---|
| User token with scope `read` (`ohs_ut_...`) | one user | omit it; any other user is refused |
| Tenant API key (`ohs_sk_...`) | every user of the tenant | required; `list_users` finds it |

For one person's agent, use a user token. A token whose only scope is `read` may live up to 30
days:

```sh
curl -s -X POST "$OHS_URL/v1/users/$OHS_USER/tokens" \
  -H "Authorization: Bearer $OHS_KEY" \
  -H "Content-Type: application/json" \
  -d '{"scopes": ["read"], "ttl_seconds": 2592000}'
# {"token": "ohs_ut_...", "expires_at": "...", "scopes": ["read"]}
```

The token is shown once and stops at `expires_at`, or as soon as the user is deleted. Revoking
the tenant key that minted it does not end it: revoke the token itself with
`ohs admin key revoke --tenant <id> --key ohs_ut_...`. A user token without `read` gets 403.

## 2. Connect the agent

The endpoint speaks Streamable HTTP. It is stateless (no `Mcp-Session-Id`) and answers in plain
JSON, so it works behind any reverse proxy and any number of `api` instances. Serve it over HTTPS
([Docker Compose and HTTPS](docker-compose-https.md)): the bearer secret is the only credential.

Agents configured with an `mcpServers` block:

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

Claude Code, from the command line:

```sh
claude mcp add --transport http open-health-sync https://ohs.example.org/mcp \
  --header "Authorization: Bearer ohs_ut_..."
```

Cross-origin browser requests (`Sec-Fetch-Site: cross-site`, or an `Origin` that does not match
the host) are refused with 403.

### An agent that only launches commands

`ohs mcp` serves the same tools on stdin and stdout and forwards each call to the instance. It
holds no data and needs no database:

| Variable | Meaning |
|---|---|
| `OHS_MCP_URL` | base URL of the instance (default `OHS_PUBLIC_URL`, then `http://localhost:8080`); `https` except for a loopback host; redirects are not followed, so give the final URL |
| `OHS_MCP_KEY` | a tenant API key or a user token with scope `read` |

`ohs mcp` connects at start and exits with the reason on stderr when the URL or secret is wrong:
401 for a wrong key or an expired token, 403 for a token without `read`, 404 or 405 for a URL that
is not the instance's base URL. The binary ships with each
[release](https://github.com/soma-intelligence/open-health-sync/releases) for Linux and macOS,
or run the image with `docker run --rm -i -e OHS_MCP_URL -e OHS_MCP_KEY ghcr.io/soma-intelligence/open-health-sync:0.3.0 mcp`.

## 3. What the agent can call

| Tool | Returns |
|---|---|
| `get_daily` | daily summaries per local day: the place to start for trends |
| `get_samples`, `get_sample_stats` | quantity samples of one `type`, or hourly and daily statistics of them |
| `get_sleep`, `get_workouts`, `get_workout_route` | sleep sessions (with phases), workouts and their GPS points |
| `get_hrv`, `get_rr_intervals` | HRV derived from heartbeat series, and the beat-to-beat intervals |
| `get_category_samples`, `get_nutrition`, `get_menstrual_cycles`, `get_ecg_recordings` | the other lists; ECG as metadata and a summary, never the voltage samples |
| `get_coverage`, `list_connections` | records per provider per day, pending jobs, and each connection's sync health |
| `get_tags`, `list_quantity_types`, `list_users`, `list_webhooks` | time tags, the type catalog, and (tenant key only) users and webhook endpoints |

Each result is one JSON text, `{"records": [...], "next_cursor": ..., "truncated": ...}`. `limit`
defaults to 100 and stops at 1000; `view` is `merged` (one source per figure) or `all`.

The server also lists twelve prompts, among them `morning_check`, `weekly_training_review`,
`load_and_recovery`, `sleep_habits` and `compare_sources`. Each ends with the same rules: read
the data first, compare the person with their own history, keep to one source per figure, say
when a figure is missing, and stay on wellness, pointing to a clinician when something keeps
looking off.

## Before you connect a hosted model

What the agent reads to answer is sent to whoever runs the model. Records carry strings from
devices and providers, and time tags carry text a person wrote: the agent must read them as
data, never as instructions. Give each agent its own token, so you can revoke one without the
others.

## Related

- [Connect Claude to your Apple Health data](apple-health-mcp.md)
- [Run Open Health Sync with Docker Compose and HTTPS](docker-compose-https.md)
- [Self-host a health data API on a Raspberry Pi 5](raspberry-pi.md)
- [Combine data from several wearables without counting it twice](merge-multiple-wearables.md)
- [MCP server reference](../mcp.md)
- [README](../../README.md)
