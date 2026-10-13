# Deploy on Render, Fly.io or Railway

These templates run Open Health Sync on a hosting platform, without a server of your own. Each
one builds the image from `deploy/Dockerfile` in this repository, so it works whether or not the
published image is reachable from the platform (see [the published image](#the-published-image)
to pull it instead).

| Platform | Template | Database |
| --- | --- | --- |
| Render | `render.yaml` (Blueprint, one button) | TimescaleDB private service, created by the Blueprint |
| Fly.io | `deploy/fly/fly.toml` | TimescaleDB as a second app, `deploy/fly/postgres/fly.toml` |
| Railway | `railway.json` | TimescaleDB service from its Docker image, added by hand |

Costs depend on the plan and the platform's current pricing: check it before you deploy. Every
setup below needs paid resources, because a persistent disk or volume is part of each.

## What every platform needs

- **Postgres with the TimescaleDB extension.** The first migration runs
  `CREATE EXTENSION timescaledb`. The managed Postgres offered by these platforms does not ship
  it, so the templates run the same `timescale/timescaledb` image as the compose stack, pinned by
  the same digest. Any managed PostgreSQL with the TimescaleDB extension works too: put its URL
  in `OHS_DATABASE_URL` and skip the database part of each section.
- **A persistent raw store.** Raw payloads are the source of truth for re-processing. The
  container's own file system is wiped on every deploy, so `/var/lib/ohs/raw` must be a
  persistent disk or volume, or `OHS_RAW_STORE` must point at an S3 compatible bucket (see
  [S3 instead of a volume](#s3-instead-of-a-volume)).
- **The required variables.** `OHS_ENCRYPTION_KEY` (32 random bytes, base64),
  `OHS_DATABASE_URL`, and `OHS_PUBLIC_URL` set to the https address the platform gives the
  service (OAuth callbacks are built from it). The rest is optional, see
  [Configuration](self-hosting.md#configuration).

The service runs `ohs up`: migrations, then the API, the collectors and the ETL in one process.
Its health check is `/readyz` (database and raw store).

### The persistent storage caveat

- **One instance.** A service with a disk or volume cannot be scaled out on any of the three
  platforms. One instance with all roles is what the templates run.
- **Short downtime on each deploy.** The old instance stops before the new one mounts the
  volume. Device uploads that hit that window get an error and retry.
- **Disk snapshots are not a database backup.** Render advises against restoring a disk snapshot
  that backs a database: it may come back corrupted. Take backups as described in
  [Backup and restore](backup-and-restore.md), and keep `OHS_ENCRYPTION_KEY` with
  them: without it, stored OAuth tokens cannot be read.
- **The container runs as root.** Render and Fly.io mount volumes owned by root, and ohs must
  write to its raw store, so `render.yaml` and `deploy/fly/fly.toml` build with
  `IMAGE_UID=0` (a build argument of `deploy/Dockerfile`, default `65532`, the distroless
  nonroot user). Railway gets the same with its `RAILWAY_RUN_UID=0` variable.

### The first tenant

The image has no shell, so a platform's interactive shell (Render Shell, `railway ssh`) has
nothing to open. Two ways in:

- **`OHS_BOOTSTRAP_TENANT`.** Set it to a tenant name before the first start. ohs creates the
  tenant and writes its API key once to the logs, as a `WARN` line
  `bootstrap tenant created: store this API key now` with the key in `api_key`. Copy it from
  the platform's log view. The key stays in the platform's log history: if that matters to you,
  create a second key and revoke the first once you have admin access (below).
- **`ohs admin` commands.** On Fly.io, `fly ssh console -C` runs the binary directly:

  ```sh
  fly ssh console --app <app> -C "/ohs admin tenant create --name dev"
  ```

  Elsewhere, run a [release binary](https://github.com/soma-intelligence/open-health-sync/releases)
  of the same version on your machine with `OHS_DATABASE_URL` and `OHS_ENCRYPTION_KEY` pointing
  at the instance's database through a tunnel the platform provides (Railway: a TCP proxy on the
  database service, removed afterwards). On Render the database has no public address: run the
  command as a one-off job of the `ohs` service (`/ohs admin tenant create --name dev`); its
  output is in the job's logs.

Then follow the [quickstart](../README.md#quickstart) from the API key on.

## Render

[![Deploy to Render](https://render.com/images/deploy-to-render-button.svg)](https://render.com/deploy?repo=https://github.com/soma-intelligence/open-health-sync)

The button reads `render.yaml` at the repository root (deploy from your fork to change it) and
creates:

- `ohs-db`, a private service running `timescale/timescaledb:latest-pg17` (pinned digest) with a
  10 GB disk on `/var/lib/postgresql/data`. Its `POSTGRES_PASSWORD` is generated.
- `ohs`, a web service built from `deploy/Dockerfile`, with a 5 GB disk on `/var/lib/ohs/raw`,
  health check `/readyz`, and a generated `OHS_ENCRYPTION_KEY` (Render generates a base64
  256-bit value, which is the format ohs requires).

Steps:

1. Click the button and sign in. Render asks for the two variables marked `sync: false`:
   - `OHS_PUBLIC_URL`: `https://ohs.onrender.com` if that name is free, otherwise leave it
     empty for now.
   - `OHS_BOOTSTRAP_TENANT`: a tenant name such as `dev`, or empty.
2. Wait for both services to deploy. The first build compiles ohs and takes a few minutes.
3. Open the `ohs` service. Copy its URL (`https://<name>.onrender.com`, or your custom domain)
   into `OHS_PUBLIC_URL` if it is not set yet; saving redeploys the service.
4. Copy `OHS_ENCRYPTION_KEY` from the service's environment into your password manager.
5. Read the API key from the logs (see [the first tenant](#the-first-tenant)).

How the database URL is built: a Blueprint cannot paste a generated password into a URL (it
may contain `/`, `+` or `=`), so `OHS_DATABASE_URL` is `postgres://ohs@/ohs?...` with no host
and no password, and the Blueprint sets `PGHOST` (the private host of `ohs-db`) and
`PGPASSWORD` (its `POSTGRES_PASSWORD`). The Postgres driver reads both from the environment.
To use a managed PostgreSQL with the TimescaleDB extension instead, delete the `ohs-db` service
and the two `PG` variables from your fork's `render.yaml`, and set `OHS_DATABASE_URL` in full.

The Blueprint uses Render's default region and instance type. Set `region` and `plan` on both
services in your fork before the first deploy if you need others: the region cannot change
afterwards, and Postgres with TimescaleDB is more comfortable with more than the default memory.

## Fly.io

Two apps: ohs (`deploy/fly/fly.toml`) and its database (`deploy/fly/postgres/fly.toml`). Fly's
own Postgres offers do not include TimescaleDB, so the second app runs the TimescaleDB image on
a volume, reachable only on the private network as `<db app>.internal`. Use a managed
PostgreSQL with the TimescaleDB extension instead by skipping steps 2 to 4 and putting its URL
in `OHS_DATABASE_URL`.

Needs `flyctl` and `openssl`. Run every command from the repository root. App names are global
on Fly: pick your own.

1. Pick names and a region, and put the region in `primary_region` of both `fly.toml` files.

   ```sh
   fly auth login
   APP=ohs-yourname DB_APP=ohs-db-yourname REGION=ams
   ```

2. Create the database app and its volume. `fly apps create` registers the app without
   deploying, like `fly launch --no-deploy`, but leaves the `fly.toml` in the repository as it is.

   ```sh
   fly apps create "$DB_APP"
   fly volumes create pgdata --app "$DB_APP" --region "$REGION" --size 10
   ```

3. Generate the database password (hex, so it fits in a URL unescaped) and store it.

   ```sh
   PGPASS=$(openssl rand -hex 24)
   fly secrets set --app "$DB_APP" --stage POSTGRES_PASSWORD="$PGPASS"
   ```

4. Deploy it as a single machine (`--ha=false`: one volume, one machine).

   ```sh
   fly deploy --config deploy/fly/postgres/fly.toml --app "$DB_APP" --ha=false
   ```

5. Create the ohs app and its raw store volume.

   ```sh
   fly apps create "$APP"
   fly volumes create ohs_raw --app "$APP" --region "$REGION" --size 3
   ```

6. Generate the encryption key and keep a copy: Fly secrets cannot be read back.

   ```sh
   KEY=$(openssl rand -base64 32)
   echo "$KEY"   # into your password manager
   fly secrets set --app "$APP" --stage \
     OHS_ENCRYPTION_KEY="$KEY" \
     OHS_DATABASE_URL="postgres://ohs:$PGPASS@$DB_APP.internal:5432/ohs?sslmode=disable&pool_max_conns=20" \
     OHS_PUBLIC_URL="https://$APP.fly.dev"
   ```

   Add `OHS_BOOTSTRAP_TENANT=dev` to the same command to create a tenant at first start, and
   provider keys (`OHS_WITHINGS_CLIENT_ID=...`) as you enable connectors.

7. Build and deploy ohs. The `.` makes the repository the build context.

   ```sh
   fly deploy . --config deploy/fly/fly.toml --dockerfile deploy/Dockerfile --app "$APP" --ha=false
   ```

8. Check it and create the first tenant.

   ```sh
   fly ssh console --app "$APP" -C "/ohs doctor"
   fly ssh console --app "$APP" -C "/ohs admin tenant create --name dev"
   ```

The ohs app keeps one machine running at all times (`auto_stop_machines = "off"`): background
sync pulls providers on a schedule and would stop with the machine. With your own domain, run
`fly certs add <domain> --app "$APP"` and set `OHS_PUBLIC_URL` to it.

## Railway

`railway.json` at the repository root configures the ohs service: build from
`deploy/Dockerfile`, health check `/readyz`, a volume required on `/var/lib/ohs/raw`, never
sleeping, 35 s to drain on redeploy. The database is a second service, added from the
TimescaleDB image. In the Railway dashboard:

1. **New project, Deploy from GitHub repo**, and pick this repository or your fork. Rename the
   service `ohs`. Cancel the first deploy if it starts: it fails without a volume and variables.
2. **Add the database.** In the project, **Create, Docker Image**, with
   `timescale/timescaledb:latest-pg17@sha256:b346edcdb51a1fd6020e3965e0bd1c9f3406fa6d5fbce1e28f4852587ef934e2`
   (the digest of `deploy/docker-compose.yml`). Name it `ohs-db`, attach a volume on
   `/var/lib/postgresql/data`, and set its variables:

   ```
   POSTGRES_USER=ohs
   POSTGRES_DB=ohs
   POSTGRES_PASSWORD=<output of: openssl rand -hex 24>
   PGDATA=/var/lib/postgresql/data/pgdata
   ```

   Do not give it a public domain.
3. **Prepare `ohs`.** Attach a volume on `/var/lib/ohs/raw`. Under **Settings, Networking**,
   generate a domain for port 8080.
4. **Set the variables of `ohs`** (Raw Editor). The `${{...}}` references are filled in by
   Railway:

   ```
   RAILWAY_RUN_UID=0
   OHS_ENCRYPTION_KEY=<output of: openssl rand -base64 32>
   OHS_DATABASE_URL=postgres://ohs:${{ohs-db.POSTGRES_PASSWORD}}@${{ohs-db.RAILWAY_PRIVATE_DOMAIN}}:5432/ohs?sslmode=disable&pool_max_conns=20
   OHS_PUBLIC_URL=https://${{RAILWAY_PUBLIC_DOMAIN}}
   OHS_RAW_STORE=file:///var/lib/ohs/raw
   OHS_BOOTSTRAP_TENANT=dev
   ```

   Keep a copy of the encryption key elsewhere.
5. **Deploy** `ohs`, then read the API key from its logs (see
   [the first tenant](#the-first-tenant)).

Railway has announced that it stops reading `railway.json` files (its "config as code") on
2026-12-01, in favour of a newer configuration format; `railway config migrate` converts the
file. If your service ignores `railway.json`, set the same in the service settings: Dockerfile
path `deploy/Dockerfile` (or the variable `RAILWAY_DOCKERFILE_PATH=deploy/Dockerfile`),
healthcheck path `/readyz`, no serverless sleeping.

## S3 instead of a volume

Point `OHS_RAW_STORE` at a bucket and drop the raw volume (and `IMAGE_UID` or `RAILWAY_RUN_UID`,
which only exist so ohs can write to it):

```
OHS_RAW_STORE=s3://<bucket>?region=<region>
AWS_ACCESS_KEY_ID=...
AWS_SECRET_ACCESS_KEY=...
```

For an S3 compatible service other than AWS, add `&endpoint=https://...` and, if it needs it,
`&path_style=true`. The store must support conditional writes (`If-None-Match`): see
[S3 compatible raw store](self-hosting.md#s3-compatible-raw-store). The database still needs
its volume.

## The published image

Once `ghcr.io/soma-intelligence/open-health-sync` is public, the platforms can pull a pinned
version instead of building: Render with `runtime: image` and `image.url`, Fly.io with
`fly deploy --image ghcr.io/soma-intelligence/open-health-sync:0.1.0`, Railway with a Docker
Image service. The published image runs as uid 65532, so on Render and Fly.io pair it with an
S3 raw store; on Railway, `RAILWAY_RUN_UID=0` still applies.
