# Project 4: Ask an AI about your training

**Goal:** an AI assistant such as Claude answering questions about your sleep, heart and training
from your real numbers, through a token that can only read.

**Time:** 10 minutes. **Level:** copy and paste. **Needs:** [project 1](first-sync.md), and an AI
client that speaks MCP (Claude Desktop, Claude Code, or any other MCP client).

## 1. Get a read-only token and the lines for your client

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

Paste the lines for your client and restart it. Claude Desktop starts the `ohs` command you
installed in project 1, so it needs to be on your `PATH`.

The token reads your data and nothing else: it cannot write, delete or connect an account, and it
stops working after 30 days. **Before you connect it, know where your data goes:** what the
assistant reads to answer you is sent to the company that runs the model, like anything you type
to it. Your instance itself stays on your machine.

## 2. Ask your first questions

Start broad, so the assistant finds out what you have:

> What health data can you see for me? List each source, each kind of data and the dates it
> covers.

Then ask a real question:

> Compare my sleep on the nights after I trained with the nights after rest days, over the last
> eight weeks. Show the averages and how many nights each one is based on.

> Has my resting heart rate changed since I started running more in September? Show it week by
> week.

The assistant calls the server's tools (`get_sleep`, `get_workouts`, `get_daily`, `get_samples`
and the others) and works from what they return. Ask it to show its numbers: you can check any of
them with the scripts of projects 2 and 3.

## 3. Keep the good questions

When a question gives you something useful, save it. The [prompt library](prompts.md) has a
dozen to start from, written for people who train: a morning check, a weekly review, race week,
coming back from a break.

## Make it yours

- Ask for a chart: most assistants can turn the numbers into a picture or an HTML page.
- Ask it to write a recipe: "write a Python script like `recipes/sleep-report.py` that shows
  my workouts per week". Then run it and read it.
- Shorten the token's life: `python3 recipes/ai-agent.py --days 1` for a one-off session.

## Share it

The best question you asked, and what made the answer useful, in
[Show and tell](https://github.com/soma-intelligence/open-health-sync/discussions/categories/show-and-tell).
Good prompts make it into the library.

**Next:** [Project 5, Does training cost you sleep?](training-vs-sleep.md).
