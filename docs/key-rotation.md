# Rotating the encryption key

`OHS_ENCRYPTION_KEY` seals the secrets Open Health Sync keeps in the database: the OAuth access
and refresh tokens of every provider connection (`ohs_sync.connections`) and the signing secret
of every webhook endpoint (`ohs_core.webhook_endpoints`). This page explains how to replace that
key on a running instance without losing them.

## Why rotate

- **The key leaked**, or may have: a `.env` file in a ticket, a backup copied to the wrong place,
  a former administrator. Rotate at once (see [if the key is compromised](#if-the-key-is-compromised)).
- **Policy**: your organization replaces keys on a schedule.
- **Hygiene**: moving the key into a secret manager is a good moment to make a fresh one.

Health records are not sealed with this key, and API keys and user tokens are stored as hashes:
rotating it affects neither.

## How it works

Each sealed value starts with a format version and a short id of the key that sealed it (the
first 4 bytes of the key's SHA-256, which does not reveal the key). A process seals with
`OHS_ENCRYPTION_KEY`, the active key, and opens with the active key or any key listed in
`OHS_PREVIOUS_ENCRYPTION_KEYS`, comma separated. Previous keys never seal anything new.

Values written before this format (releases up to 0.0.2) have no header: they are opened by
trying each configured key, the active one first. `ohs admin rekey` gives them the header.

`ohs admin rekey` re-seals every value that is not yet under the active key, row by row in short
transactions, each value bound to the same row as before. Serving processes keep running
meanwhile. Once nothing uses the previous key, it can go.

## Procedure

The commands use the compose stack of [self-hosting](self-hosting.md); with another setup, run
the same `ohs` commands with the instance's environment.

```sh
DC="docker compose --env-file .env -f deploy/docker-compose.yml"
```

1. **Back up** the database and `.env` ([backup](self-hosting.md#backup-and-restore)). Keep the
   old key until the end of the procedure: it is the only way back.
2. **Generate a new key.**

   ```sh
   openssl rand -base64 32
   ```

3. **Move the old key into `OHS_PREVIOUS_ENCRYPTION_KEYS`** and put the new one in
   `OHS_ENCRYPTION_KEY`, in `.env` (or your secret manager):

   ```sh
   OHS_ENCRYPTION_KEY=<new key>
   OHS_PREVIOUS_ENCRYPTION_KEYS=<old key>
   ```

   If a rotation is already under way, keep its keys in the list: `<old key>,<older key>`.
4. **Restart every process** that reads the database, with the new environment: every `ohs`
   container or service, whatever its `OHS_ROLES`, and any one off `ohs` command you run.

   ```sh
   $DC up -d --wait
   ```

   From now on new tokens are sealed with the new key, and tokens sealed with the old one still
   open.
5. **Re-seal the stored secrets.**

   ```sh
   $DC exec ohs /ohs admin rekey
   ```

   It prints, for each sealed column, how many values it rewrote, how many were already under
   the new key, and how many no configured key opens (`failed`, each named by its row and left
   as it is). It is safe to run again, and on a large instance it can be stopped and restarted:
   `--batch <rows>` sets how many rows it reads at once (default 100).
6. **Check with `ohs doctor`.**

   ```sh
   $DC run --rm ohs doctor
   ```

   When every value is under the new key, it says
   `warn  encryption keys: no sealed value uses a previous key any more: you can remove OHS_PREVIOUS_ENCRYPTION_KEYS ...`.
   If it still counts values under a previous key, run `ohs admin rekey` again: a process that
   was not restarted in step 4 may have sealed some with the old key.
7. **Remove the old key**: delete `OHS_PREVIOUS_ENCRYPTION_KEYS` (or the old key from it),
   restart every process again, and destroy the old key wherever you kept it, backups of `.env`
   included, once you no longer need those backups.

### Several processes, no failed requests

Between steps 3 and 4 the processes do not all run with the same keys. A process still on the old
configuration cannot open a token another process already sealed with the new key: a pull or a
webhook delivery fails, and is retried later. With a single compose service that window is a few
seconds. To avoid it entirely on a split deployment, add the new key in two passes: first
`OHS_PREVIOUS_ENCRYPTION_KEYS=<new key>` everywhere, with the old key still active, and restart
every process; then swap them (`OHS_ENCRYPTION_KEY=<new key>`,
`OHS_PREVIOUS_ENCRYPTION_KEYS=<old key>`) and restart again. Every process can then open both
forms at every moment.

Every process must run a release that reads the new format before step 4: an older release
cannot open values with a key id header.

## If you skip a step

| Skipped | What happens | Fix |
|---|---|---|
| Old key not moved to `OHS_PREVIOUS_ENCRYPTION_KEYS` (step 3) | Stored tokens and webhook secrets no longer open: pulls fail and connections stop syncing, webhook deliveries fail saying the secret cannot be opened. `ohs doctor` counts values that open with no configured key. | Add the old key to `OHS_PREVIOUS_ENCRYPTION_KEYS` and restart. Nothing was lost as long as you still have it. |
| A process not restarted (step 4) | It keeps sealing with the old key, and cannot open what the others seal with the new one. | Restart it, then run `ohs admin rekey` again. |
| `ohs admin rekey` not run (step 5) | Nothing breaks, but the old key stays needed: removing it later loses every value it still seals. | Run it before step 7. |
| Old key removed before `ohs doctor` agrees (step 7) | Values still under the old key no longer open; `ohs admin rekey` reports them as `failed` and leaves them as they are. | Put the old key back in `OHS_PREVIOUS_ENCRYPTION_KEYS`, restart, run `ohs admin rekey`. |
| The old key is lost | The values it sealed cannot be recovered. | Each affected user connects the provider again (the connection is replaced), and each affected webhook endpoint gets a new secret (`POST /v1/webhooks/{webhook_id}/rotate-secret`). Then `ohs admin rekey` and `ohs doctor` report no failure. |

`ohs admin rekey` never deletes a row. A value no key opens is reported, and the command exits
with status 1 so a script notices.

## If the key is compromised

Someone holding the key and a copy of the database (a dump, a backup, read access to Postgres)
can open every token sealed with it. Rotating the key protects the tokens from then on, but a
copy taken before still opens with the old key. So:

1. Rotate the key at once, steps 2 to 7 above, without waiting between them. Remove the old key
   as soon as `ohs doctor` agrees.
2. Treat the provider tokens as exposed: revoke them at the providers where you can (the
   developer console of each provider app, or by asking users to disconnect and connect again),
   and rotate the provider client secrets (`OHS_<PROVIDER>_CLIENT_SECRET`) if the same leak may
   have exposed `.env`.
3. Rotate the webhook signing secrets (`POST /v1/webhooks/{webhook_id}/rotate-secret`) and tell the
   receivers.
4. Find out how the key leaked, and replace whatever else lived next to it: tenant API keys
   (`ohs admin key create`, then `key revoke`), `POSTGRES_PASSWORD`, storage credentials. See
   [rotating secrets](self-hosting.md#rotating-secrets).

Old backups still hold values sealed with the old key. Destroy the old key and those backups
together, or keep both under the same protection.
