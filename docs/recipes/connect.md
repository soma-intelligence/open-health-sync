# Connect your accounts

See what feeds your instance, how fresh each source is, and connect a new one in one command.

```sh
go run ./recipes/connect            # what is connected, and what can be
go run ./recipes/connect oura       # opens the Oura sign in page
```

```text
Source          Status        Synced through        Last error
apple_health    fresh         2026-10-12 23:43
oura            fresh         2026-10-12 23:40

Can connect: fitbit, whoop  (go run ./recipes/connect <name>)
```

Allow access on the provider's page and the first data arrives a few seconds later; history
follows in the background.

## Before you connect

Each provider needs its own developer keys on your instance, because nothing is shared between
installs. The page of each [connector](../connectors/README.md) says where to get them and which
redirect address to register. A provider without keys is not offered.

Apple Health has no sign in: import an export of the Health app, see
[Build with your own health data](README.md#set-up-in-five-minutes).

## How it works

`GET /v1/providers` lists the providers this instance can connect,
`GET /v1/users/{id}/freshness` how current each connected one is, and
`POST /v1/users/{id}/connections/{provider}` returns the sign in address to open.
