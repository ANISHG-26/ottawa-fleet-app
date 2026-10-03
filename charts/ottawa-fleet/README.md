# Ottawa Fleet Helm chart

Chart `ottawa-fleet` owns the Fleet API, Ride API, assignment worker, operator UI, one migration Job, and an optional disposable PostgreSQL StatefulSet. It creates no namespace, policy, ingress controller, autoscaler, CRD, or platform resource. Install one release per namespace because the in-cluster API names (`fleet-api`, `ride-api`, and `postgres`) are stable for the UI proxy and worker.

## Before installation

The default values intentionally contain no image digest or source commit, so lint and template commands fail closed until values are supplied. Provide real registry digests and matching source commits for all six images from the trusted release workflow before installing. `ci-values.yaml` contains synthetic test-only pins and must never be used to install workloads. The chart schema rejects image tags, missing digests, all-zero placeholders, and missing source commits.

For an existing PostgreSQL database, create a secret outside Helm containing the complete URL:

```powershell
kubectl create secret generic ottawa-fleet-database `
  --namespace <namespace> `
  --from-literal=DATABASE_URL='<database-url-from-your-secret-manager>'
```

Set `database.existingSecret` and `database.secretKey` to those secret references. The URL is never stored in chart values or release metadata.

For an explicitly disposable lab database, set `database.disposable.enabled=true`. Create `credentialsSecret` with the keys named by `usernameKey` and `passwordKey`, and create `connectionSecret` with the matching PostgreSQL URL. The chart creates one PostgreSQL StatefulSet and a persistent volume claim. Normal stop or Helm uninstall retains that claim; deleting the claim discards the lab database. This option is for disposable local/lab use, not a production database recommendation.

## Install and upgrade

Chart 0.1.1 initializes the disposable database in `/var/lib/postgresql/data/pgdata`, below the disk mount root, so a freshly formatted disk's `lost+found` directory does not prevent startup. Before upgrading a disposable database created with an older chart, check for `PG_VERSION` at the mount root. An existing root-layout database requires an explicit data migration or an authorized disposable reset; switching the path alone would create a separate empty database.

After replacing the six image digests and matching source metadata in a values file:

```sh
helm lint charts/ottawa-fleet -f release-values.yaml
helm template ottawa-fleet charts/ottawa-fleet \
  --namespace <platform-owned-namespace> \
  -f release-values.yaml
helm upgrade --install ottawa-fleet charts/ottawa-fleet \
  --namespace <platform-owned-namespace> \
  -f release-values.yaml --wait --wait-for-jobs
```

The `db-init` Job is the only migration owner. Its name includes the Helm release revision and a hash of the migration image/source and database secret references. That makes Helm create a new Job on upgrades and makes Argo's fixed render revision react to migration artifact or secret-reference changes. It applies ordered, transactional SQL files and records them in PostgreSQL. Argo CD runs it as a Sync hook at wave `-1`; the service account and optional disposable PostgreSQL resources are earlier waves, and APIs/workloads remain at wave `0`. `BeforeHookCreation,HookSucceeded` removes the previous hook Job on each sync. Helm ignores these Argo annotations and treats the Job as a normal release resource, so `helm upgrade --install --wait --wait-for-jobs` continues to wait for migration and service readiness, including with the disposable database option. The current migration set creates the v1 Fleet and ride/job schema. Future migrations must state compatibility with the previous image before they are added. An image rollback never reverses database changes; roll back only to an image verified against the already-applied schema. Helm uninstall leaves database data intact.

Fleet and Ride API ports are contract-fixed at 8080 and 8081; the worker probe port is 8082 and the web service port is 8080. Probe timing, CPU/memory requests and limits, secret references, image digests, and image-pull secrets are values. Workloads run without a service-account token, as non-root identities, with a read-only root filesystem (the UI receives temporary writable mounts), dropped Linux capabilities, and the RuntimeDefault seccomp profile. API/worker termination grace is 10 seconds; worker shutdown stops new claims and lets unfinished leases expire.

Set `worker.autoscaling.enabled=true` when a platform-owned autoscaler manages replicas. This omits `spec.replicas` from the Deployment and the chart creates no HPA or KEDA resource. Backlog remains visible at Ride API `/metrics`, independent of worker replica count. Fault controls are disabled in chart values; scenario fault mode requires Fleet to be started with its explicit control gate.

## Validation

Use the repository-pinned Helm binary or a matching Helm 4.3.0 binary. The synthetic CI pins below are for rendering only:

```sh
helm lint charts/ottawa-fleet -f charts/ottawa-fleet/ci-values.yaml
helm template ottawa-fleet charts/ottawa-fleet --namespace test \
  -f charts/ottawa-fleet/ci-values.yaml
helm template ottawa-fleet charts/ottawa-fleet --namespace test \
  -f charts/ottawa-fleet/ci-values.yaml --set worker.autoscaling.enabled=true
```

These commands validate the templates and schema. A disposable-cluster smoke test, real registry digests, migration rollback compatibility evidence, and platform promotion evidence remain separate release acceptance work.
