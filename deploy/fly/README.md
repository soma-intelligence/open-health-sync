# Fly.io

Two Fly apps: `fly.toml` runs ohs (built from `deploy/Dockerfile`, raw store on a volume) and
`postgres/fly.toml` runs Postgres 17 with TimescaleDB on a volume, reachable only on the
private network. Any Postgres that offers the TimescaleDB extension can replace the second app.

The step-by-step commands, the first tenant and the caveats are in
[docs/deploy-platforms.md](../../docs/deploy-platforms.md#flyio).
