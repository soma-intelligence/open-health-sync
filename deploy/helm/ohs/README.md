# ohs Helm chart

Runs [Open Health Sync](https://github.com/soma-intelligence/open-health-sync) on Kubernetes
against a Postgres 17 with TimescaleDB that you provide. The full guide, with every value, is
[docs/helm.md](../../../docs/helm.md).

```sh
helm install ohs deploy/helm/ohs -n ohs --create-namespace \
  --set database.url='postgres://ohs:change-me@db.example.org:5432/ohs?sslmode=require&pool_max_conns=20' \
  --set config.publicURL=https://health.example.org \
  --wait
kubectl -n ohs exec deploy/ohs -- /ohs doctor
```

- One Deployment runs the three roles (`api`, `collect`, `etl`) in one process;
  `splitRoles.enabled` runs one Deployment per role.
- A `pre-install`/`pre-upgrade` Job runs `ohs migrate up` before the pods start.
- Raw payloads go to a `ReadWriteOnce` volume by default, which allows one pod. Use an S3 bucket
  (`rawStore.type=s3`) or a `ReadWriteMany` volume to run more.
- The encryption key is generated on the first install and kept across upgrades and uninstalls,
  unless you pass `encryptionKey` or your own Secret (`existingSecret`).
- Ingress, ServiceMonitor, NetworkPolicy, HorizontalPodAutoscaler and PodDisruptionBudget are
  available and off by default.

The chart's `appVersion` is the image tag it runs by default. Set `image.tag` to run another
release.
