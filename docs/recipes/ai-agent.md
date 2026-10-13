# Ask an AI about your health data

Give an AI assistant read-only access to your data, then ask it what you want to know: "how did I
sleep the weeks I trained the most?", "has my resting heart rate changed since I started
running?". It reads through the Model Context Protocol (MCP).

```sh
python3 recipes/ai-agent.py
```

```text
Read only token, valid until 2026-11-11. Keep it private.

Claude Code:

  claude mcp add --transport http open-health-sync http://localhost:8080/mcp \
    --header "Authorization: Bearer ohs_ut_..."

Claude Desktop, or any client that starts a local command (claude_desktop_config.json):

{ "mcpServers": { "open-health-sync": { "command": "ohs", "args": ["mcp"], ... } } }
```

Paste the lines for your client and restart it.

## What the token can do

It reads your samples, sleep, workouts, daily summaries and coverage through the MCP tools. It
cannot write, delete or connect anything, it sees only your user, and it stops working after 30
days (`--days` sets fewer). To revoke it sooner, pass the whole token to
`ohs admin key revoke --tenant <id> --key ohs_ut_...`; the tenant id is in the first line of
`~/.ohs.env`.

## How it works

`POST /v1/users/{id}/tokens` with `{"scopes": ["read"]}` mints the token with your tenant key.
The [AI agents guide](../mcp.md) covers the other clients and the tools the server offers.
