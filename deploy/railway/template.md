# Railway template

How to publish the one-click Railway template for Open Health Sync. This is for maintainers:
users deploy with the button, or follow the manual steps in
[docs/deploy-platforms.md](../../docs/deploy-platforms.md#railway).

A Railway button points at a published template (`https://railway.com/deploy/<code>`), not at
this repository. The template is built once in the Railway dashboard, from the values below,
and then generates the secrets for each user at deploy time.

## Create the template

In the Railway dashboard: **Templates, New Template** (the template composer). Add two services.

### Service 1: `ohs-db`

- **Source:** Docker Image,
  `timescale/timescaledb:latest-pg17@sha256:b346edcdb51a1fd6020e3965e0bd1c9f3406fa6d5fbce1e28f4852587ef934e2`
  (the digest of `deploy/docker-compose.yml`; keep them in step).
- **Volume:** mount path `/var/lib/postgresql/data`.
- **Networking:** no public domain. ohs reaches it on the private network.
- **Variables:**

  ```
  POSTGRES_USER=ohs
  POSTGRES_DB=ohs
  POSTGRES_PASSWORD=${{secret(48, "abcdef0123456789")}}
  PGDATA=/var/lib/postgresql/data/pgdata
  ```

  The password is hex, so it goes into a URL without escaping (like `openssl rand -hex 24`).

### Service 2: `ohs`

- **Source:** GitHub repo `soma-intelligence/open-health-sync`, branch `main`, no root
  directory. Railway reads `railway.json` at the repository root (Dockerfile build, health
  check `/readyz`, no sleeping, 35 s draining).
- **Volume:** mount path `/var/lib/ohs/raw`.
- **Networking:** public HTTP domain on port `8080`.
- **Variables:**

  ```
  RAILWAY_RUN_UID=0
  RAILWAY_DOCKERFILE_PATH=deploy/Dockerfile
  OHS_ENCRYPTION_KEY=${{secret(43, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/")}}=
  OHS_DATABASE_URL=postgres://ohs:${{ohs-db.POSTGRES_PASSWORD}}@${{ohs-db.RAILWAY_PRIVATE_DOMAIN}}:5432/ohs?sslmode=disable&pool_max_conns=20
  OHS_PUBLIC_URL=https://${{RAILWAY_PUBLIC_DOMAIN}}
  OHS_RAW_STORE=file:///var/lib/ohs/raw
  OHS_BOOTSTRAP_TENANT=dev
  ```

  Variable descriptions to set in the composer, shown to users at deploy time:

  - `OHS_ENCRYPTION_KEY`: "Seals OAuth tokens and keys. Copy it to your password manager
    after the first deploy: without it, stored tokens cannot be read."
  - `OHS_BOOTSTRAP_TENANT`: "First tenant. Its API key is printed once in the deploy logs."

Why these values:

- `OHS_ENCRYPTION_KEY` must decode from base64 to 32 bytes. 43 characters of the base64
  alphabet plus one `=` is the shape of `openssl rand -base64 32` (258 random bits; Go's
  decoder accepts the non-zero trailing bits).
- `RAILWAY_RUN_UID=0`: Railway mounts volumes owned by root and the image runs as uid 65532
  by default, so ohs could not write its raw store otherwise.
- `RAILWAY_DOCKERFILE_PATH` repeats what `railway.json` says. Railway stops reading
  `railway.json` on 2026-12-01; the variable keeps the build working after that date.

## Publish and add the button

1. Deploy the template once into a scratch project, from the composer. Check that both
   services go green, that the `ohs` logs show `bootstrap tenant created`, and that
   `https://<domain>/readyz` answers 200. Delete the scratch project.
2. Publish the template. Name: `Open Health Sync`. Category: Other. Description: "One API for
   all your health data. Self-hosted, with TimescaleDB."
3. Copy the template URL (`https://railway.com/deploy/<code>`) and add the button next to the
   Render one in `README.md`, and at the top of the Railway section of
   `docs/deploy-platforms.md`:

   ```md
   [![Deploy on Railway](https://railway.com/button.svg)](https://railway.com/deploy/<code>)
   ```

## Keep it in step

The template is a copy: when one of these changes, edit the template in the dashboard too.

- The TimescaleDB digest in `deploy/docker-compose.yml`.
- The variables ohs requires (`internal/config/config.go`).
- The volume paths and the port (`deploy/Dockerfile`).
