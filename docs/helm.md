# Kubernetes with Helm

The chart in [`deploy/helm/ohs`](../deploy/helm/ohs) runs Open Health Sync on Kubernetes. It runs
the published image (`ghcr.io/soma-intelligence/open-health-sync`, the chart's `appVersion` by
default) and expects a Postgres 17 with TimescaleDB that you run yourself: a managed service, an
operator in the cluster, or a server outside it. The chart is not published to a chart
repository yet: install it from a checkout of this repository.

## What it creates

- A **Deployment** of one pod running `ohs serve` with the three roles (`OHS_ROLES=api,collect,etl`),
  as the compose file does. With `splitRoles.enabled`, three Deployments instead (`-api`,
  `-collect`, `-etl`), one role each, so they scale apart. Only the `api` role serves HTTP.
- A **Job** that runs `ohs migrate up` before every install and upgrade (a Helm `pre-install` and
  `pre-upgrade` hook), so the pods start on an up to date schema and never migrate themselves.
- A **Service** on port 8080. Pods that serve HTTP are probed on `/healthz` (startup and liveness)
  and `/readyz` (readiness: the database and the raw store answer).
- A **ConfigMap** with the non-secret `OHS_*` settings, and a **Secret** with
  `OHS_DATABASE_URL` and `OHS_ENCRYPTION_KEY` (or your own Secret, see `existingSecret`).
- A **PersistentVolumeClaim** for the raw store at `/var/lib/ohs/raw`, unless the raw store is S3.
- Optional, all off by default: Ingress, ServiceMonitor, NetworkPolicy, HorizontalPodAutoscaler
  and PodDisruptionBudget.

The pods run as user 65532 with a read-only root filesystem, no capabilities and no service
account token.

### One pod, or several

The default raw store is a `ReadWriteOnce` volume, which one pod at a time can mount. So the
default is one replica, and the Deployment uses the `Recreate` strategy: on an upgrade the old pod
stops before the new one starts. The chart refuses to render more than one pod
(`replicaCount` above 1, `autoscaling.enabled` or `splitRoles.enabled`) on that volume. To run
more, keep raw payloads where every pod can reach them: an S3 bucket (`rawStore.type=s3`), or a
`ReadWriteMany` volume (`rawStore.persistence.accessMode=ReadWriteMany`).

## Requirements

- Kubernetes 1.25 or later, Helm 3.
- Postgres 17 with the TimescaleDB extension available. The first migration runs
  `CREATE EXTENSION IF NOT EXISTS timescaledb`: give the chart's database user the right to
  create it, or create it once yourself as a superuser.
- A StorageClass for the raw volume (the cluster default is used), or an S3 bucket that supports
  conditional writes (`If-None-Match`), see [S3 compatible raw store](self-hosting.md#s3-compatible-raw-store).

## Install with an external Postgres

1. Create the database and the extension (as a superuser, once):

   ```sql
   CREATE ROLE ohs LOGIN PASSWORD 'change-me';
   CREATE DATABASE ohs OWNER ohs;
   \c ohs
   CREATE EXTENSION IF NOT EXISTS timescaledb;
   ```

2. Get the chart:

   ```sh
   git clone https://github.com/soma-intelligence/open-health-sync
   cd open-health-sync
   ```

3. Write your values. Keep this file out of version control: it holds the database password.

   ```yaml
   # ohs-values.yaml
   database:
     url: postgres://ohs:change-me@db.example.org:5432/ohs?sslmode=require&pool_max_conns=20
   config:
     publicURL: https://health.example.org
   ```

   Size the pool with `pool_max_conns` in the URL, as described under
   [Database pool size](self-hosting.md#database-pool-size). With split roles each pod opens its
   own pool: keep the sum over all pods under the server's `max_connections`.

4. Install:

   ```sh
   kubectl create namespace ohs
   helm install ohs deploy/helm/ohs -n ohs -f ohs-values.yaml --wait
   ```

   The migration Job runs first. If it fails, `helm install` stops and the Job stays for its logs:
   `kubectl -n ohs logs job/ohs-migrate`.

5. Check the instance and create a first tenant (its API key is printed once):

   ```sh
   kubectl -n ohs exec deploy/ohs -- /ohs doctor
   kubectl -n ohs exec deploy/ohs -- /ohs admin tenant create --name main
   ```

   With `splitRoles.enabled`, use `deploy/ohs-api`.

6. Put it behind TLS: enable the chart's Ingress (below) or route to the `ohs` Service on port
   8080 from your own, and set `config.publicURL` to that https address. OAuth callbacks are
   built from it: `<publicURL>/oauth/<slug>/callback`.

### The encryption key

`OHS_ENCRYPTION_KEY` seals the OAuth tokens stored in the database. Leave `encryptionKey` empty
and the chart generates one on the first install, then reads it back from the cluster on every
upgrade, so it never changes. The Secret is created as a Helm hook (it has to exist before the
migration Job), so `helm uninstall` keeps it. Back it up with the database:

```sh
kubectl -n ohs get secret ohs-secret -o jsonpath='{.data.OHS_ENCRYPTION_KEY}' | base64 -d
```

A tool that renders the chart without access to the cluster (`helm template`, some GitOps
setups) cannot read the key back and would generate a new one on each render. In that case set
`encryptionKey` (`openssl rand -base64 32`), or keep every secret in a Secret of your own:

```sh
kubectl -n ohs create secret generic ohs-env \
  --from-literal=OHS_DATABASE_URL='postgres://ohs:change-me@db.example.org:5432/ohs?sslmode=require&pool_max_conns=20' \
  --from-literal=OHS_ENCRYPTION_KEY="$(openssl rand -base64 32)" \
  --from-literal=OHS_WITHINGS_CLIENT_ID=... \
  --from-literal=OHS_WITHINGS_CLIENT_SECRET=...
helm install ohs deploy/helm/ohs -n ohs --set existingSecret=ohs-env \
  --set config.publicURL=https://health.example.org --wait
```

Every key of that Secret becomes an environment variable of the pods, so provider keys and S3
credentials go there too.

### Provider keys

Without `existingSecret`, put provider keys in `secretEnv`; they land in the chart's Secret:

```yaml
secretEnv:
  OHS_WITHINGS_CLIENT_ID: "..."
  OHS_WITHINGS_CLIENT_SECRET: "..."
```

Non-secret provider settings (for example `OHS_DEXCOM_REGION`) go in `config.extra`. Every pod
gets the same environment, which covers the rule that each process running `collect` or `etl`
needs the provider keys.

### S3 raw store

```yaml
rawStore:
  type: s3
  s3:
    url: s3://my-ohs-raw?region=eu-west-3
    accessKeyId: "..."        # or AWS_ACCESS_KEY_ID in existingSecret
    secretAccessKey: "..."    # or AWS_SECRET_ACCESS_KEY in existingSecret
```

For another S3 compatible service add `endpoint=` and `path_style=true` to the URL, as in
[the compose setup](self-hosting.md#s3-compatible-raw-store). No volume is created.

## Values

| Value | Default | Meaning |
|---|---|---|
| `image.repository` | `ghcr.io/soma-intelligence/open-health-sync` | Image |
| `image.tag` | chart `appVersion` | Image tag; there is no `latest` while the version is 0.x |
| `image.digest` | empty | `sha256:...`, wins over the tag |
| `image.pullPolicy` | `IfNotPresent` | |
| `imagePullSecrets` | `[]` | |
| `database.url` | empty, required | `OHS_DATABASE_URL`, unless `existingSecret` is set |
| `existingSecret` | empty | Your Secret, holding at least `OHS_DATABASE_URL` and `OHS_ENCRYPTION_KEY`; all its keys become environment variables |
| `encryptionKey` | generated once | `OHS_ENCRYPTION_KEY`, 32 random bytes in base64 |
| `secretEnv` | `{}` | More keys for the chart's Secret (provider keys) |
| `config.publicURL` | `http://localhost:8080` | `OHS_PUBLIC_URL`: set your https address |
| `config.logLevel` | `info` | `OHS_LOG_LEVEL` |
| `config.metrics` | `true` | `OHS_METRICS`: `/metrics` on port 8080 |
| `config.retentionDays` | `0` | `OHS_RETENTION_DAYS`; 0 keeps everything |
| `config.ingestConcurrency` | `8` | `OHS_INGEST_CONCURRENCY` |
| `config.webhookAllowPrivate` | `false` | `OHS_WEBHOOK_ALLOW_PRIVATE` (0.1.0 and later) |
| `config.bootstrapTenant` | empty | `OHS_BOOTSTRAP_TENANT` |
| `config.extra` | `{}` | Any other non-secret variable |
| `rawStore.type` | `file` | `file` (a volume) or `s3` |
| `rawStore.s3.url` | empty | `s3://bucket?region=...`, required with `s3` |
| `rawStore.s3.accessKeyId`, `secretAccessKey` | empty | Written to the chart's Secret as `AWS_*` |
| `rawStore.persistence.enabled` | `true` | `false` mounts an emptyDir: raw payloads are lost with the pod |
| `rawStore.persistence.existingClaim` | empty | Use this claim instead of creating one |
| `rawStore.persistence.storageClass` | empty | Empty uses the default StorageClass |
| `rawStore.persistence.accessMode` | `ReadWriteOnce` | `ReadWriteMany` allows more than one pod |
| `rawStore.persistence.size` | `10Gi` | |
| `rawStore.persistence.keep` | `true` | Keep the claim on uninstall |
| `replicaCount` | `1` | Pods of the single Deployment |
| `resources` | 100m CPU, 256Mi requested; 1Gi memory limit | Also the default of each split role |
| `splitRoles.enabled` | `false` | One Deployment per role |
| `splitRoles.<role>.replicaCount` | api 2, collect 1, etl 1 | |
| `splitRoles.<role>.resources` | `{}` | Empty uses `resources` |
| `migrations.enabled` | `true` | The `ohs migrate up` Job; disabled, run it yourself before each upgrade |
| `migrations.backoffLimit`, `activeDeadlineSeconds` | `2`, `600` | |
| `env`, `extraEnvFrom` | `[]` | Extra environment, as Kubernetes `env` and `envFrom` entries |
| `service.type`, `service.port` | `ClusterIP`, `8080` | |
| `startupProbe`, `livenessProbe`, `readinessProbe` | `/healthz`, `/healthz`, `/readyz` | Probes of the pods that serve HTTP |
| `terminationGracePeriodSeconds` | `35` | `ohs` gives work in flight 25 s on SIGTERM |
| `serviceAccountName` | empty | An existing ServiceAccount |
| `podSecurityContext`, `securityContext` | non-root 65532, read-only root | |
| `podAnnotations`, `podLabels`, `nodeSelector`, `tolerations`, `affinity`, `topologySpreadConstraints`, `priorityClassName` | empty | Pod placement |
| `ingress.enabled` | `false` | With `className`, `annotations`, `hosts`, `tls` |
| `serviceMonitor.enabled` | `false` | Needs the Prometheus Operator CRDs; `interval`, `scrapeTimeout`, `labels` |
| `networkPolicy.enabled` | `false` | Ingress on 8080 from `ingressFrom`; egress to DNS, the database (`databasePort`, `databaseTo`), HTTPS (`allowHTTPS`) and `extraEgress` |
| `autoscaling.enabled` | `false` | CPU based, on the pods that serve HTTP; `minReplicas`, `maxReplicas`, `targetCPUUtilizationPercentage` |
| `podDisruptionBudget.enabled` | `false` | On the pods that serve HTTP; `minAvailable` |

[`values.yaml`](../deploy/helm/ohs/values.yaml) explains each one in place.

### Optional resources

- **Ingress.** Uploads carry up to 64 MiB of body: raise your controller's limit (with ingress-nginx,
  `nginx.ingress.kubernetes.io/proxy-body-size: 64m`). The Ingress routes every path, `/metrics`
  included: block it at the controller, or set `config.metrics=false`
  (see [Operating notes](self-hosting.md#operating-notes)).
- **ServiceMonitor.** Scrapes `/metrics` from the Service.
- **NetworkPolicy.** Applies to every pod of the release, the migration Job included. HTTPS on
  443 covers the provider APIs, AWS S3 and most webhook endpoints; add an `extraEgress` rule for
  an S3 endpoint or a webhook receiver on another port.
- **HorizontalPodAutoscaler and PodDisruptionBudget.** Both only make sense with more than one
  pod, so with a shared raw store. A PodDisruptionBudget with `minAvailable: 1` on a single pod blocks node drains.

## Upgrading

Read the [changelog](../CHANGELOG.md) and take a backup first (the database, the raw store and
the encryption key, see [Backup and restore](self-hosting.md#backup-and-restore)). Then:

```sh
git pull
helm upgrade ohs deploy/helm/ohs -n ohs -f ohs-values.yaml --wait
kubectl -n ohs exec deploy/ohs -- /ohs version
```

The migration Job runs before the new pods start. To run another release than the chart's
`appVersion`, set `image.tag`. There is no step by step downgrade: to go back, run the old
version on a restored backup.

## Uninstalling

```sh
helm uninstall ohs -n ohs
```

This removes the Deployments, the Service, the ConfigMap and the optional resources. It keeps,
on purpose:

- the Secret `ohs-secret` with the encryption key (a hook resource),
- the raw volume claim `ohs-raw` (`rawStore.persistence.keep`),
- the database, which the chart never touches beyond the migrations.

Installing again under the same release name reuses the Secret and the claim. To remove
everything, delete them by hand once you no longer need the data:

```sh
kubectl -n ohs delete secret ohs-secret
kubectl -n ohs delete pvc ohs-raw
```
