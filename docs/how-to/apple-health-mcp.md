# Connect Claude to your Apple Health data

To use Apple Health data in Claude through MCP, load your Health app export into Open Health
Sync and add its read-only MCP server to Claude with a token that can only read one person's
data. Open Health Sync is open source and runs on your own computer.

Claude, or any other assistant that speaks the Model Context Protocol (MCP), can answer questions
about your Apple Health data from your real numbers: "how did I sleep the weeks I ran the most?",
"has my resting heart rate changed since March?". This guide loads your Health app export into
Open Health Sync on your own computer, then gives the assistant a token that can only read it.
It takes about 20 minutes.

## How it works

Open Health Sync holds your data in its own database and serves it through a read-only MCP
server. The assistant calls its tools (`get_daily`, `get_sleep`, `get_workouts`, `get_samples`
and others) and works from what they return. The token you give it cannot write, delete or
connect anything, sees only your user, and stops working after 30 days.

**Know where your data goes:** what the assistant reads to answer you is sent to the company
that runs the model, like anything you type to it. Your instance and the rest of your data stay
on your machine.

## 1. Load your Apple Health data

Follow steps 1 to 4 of [Import Apple Health data into PostgreSQL](apple-health-to-postgres.md):
start the stack with Docker Compose, create your user with `ohs admin personal setup`, export
from the Health app on your iPhone, and run `ohs import apple-health export.zip`. Install the
`ohs` command while you are there: Claude Desktop starts it.

## 2. Mint a read-only token

From the repository folder:

```sh
go run ./recipes/ai-agent
```

```text
Read only token, valid until 2026-11-17. Keep it private.

Claude Code:

  claude mcp add --transport http open-health-sync http://localhost:8080/mcp \
    --header "Authorization: Bearer ohs_ut_..."

Claude Desktop, or any client that starts a local command (claude_desktop_config.json):

{ "mcpServers": { "open-health-sync": { "command": "ohs", "args": ["mcp"], ... } } }
```

The script reads `~/.ohs.env` and calls `POST /v1/users/{id}/tokens` with the `read` scope. To
do it by hand, see [a personal token](../mcp.md#a-personal-token).

## 3. Add it to your assistant

**Claude Desktop.** Open Settings, Developer, Edit Config, and add the server to
`claude_desktop_config.json`, with the path of your `ohs` binary and the token from step 2:

```json
{
  "mcpServers": {
    "open-health-sync": {
      "command": "/usr/local/bin/ohs",
      "args": ["mcp"],
      "env": {
        "OHS_MCP_URL": "http://localhost:8080",
        "OHS_MCP_KEY": "ohs_ut_..."
      }
    }
  }
}
```

Restart Claude Desktop. `ohs mcp` forwards each request to your instance and holds no data
itself.

**Claude Code.** Paste the `claude mcp add` line the script printed.

**Other clients.** Any MCP client that starts a command or calls a URL with a bearer header
works; the [MCP server reference](../mcp.md) has both forms.

## 4. Ask your first questions

Start broad, so the assistant finds out what you have:

> What health data can you see for me? List each kind of data and the dates it covers.

Then ask real questions:

> Compare my sleep on the nights after a workout with the nights after rest days, over the last
> eight weeks. Show the averages and how many nights each one is based on.

> Show my resting heart rate and HRV week by week since January, and tell me when they moved
> together.

Ask it to show its numbers, so you can check them. When an iPhone and an Apple Watch both count
steps, the server keeps one source per figure, so nothing is counted twice
([the merged view](../api.md#several-sources-of-one-metric-the-merged-view)).

## Next

- The [prompt library](../learn/prompts.md) has a dozen questions to start from.
- Add your ring or band (Oura, WHOOP, Withings, Fitbit and others): every
  [connector](../connectors/README.md) lands in the same tools.
- [Ask an AI about your training](../learn/ask-an-ai.md) goes further with workouts.
