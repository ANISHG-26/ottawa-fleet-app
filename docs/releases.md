# Application release workflow

The application repository owns images, migrations, and the versioned Helm chart. The platform repository selects reviewed chart and image digests; this workflow does not deploy or promote them.

## Trusted release path

The `Trusted application release` workflow runs only for pushed `v*.*.*` tags. Create a release tag from a reviewed commit after the repository checks and local PostgreSQL behavior suite pass. The read-only `verify` job runs Go tests against an ephemeral PostgreSQL service, builds the UI, lints/renders/packages the chart, and does not receive package-write permission. Image publishing runs only after verification, from the tag event, with `packages: write` scoped to those jobs. Pull request events cannot run these publishing jobs.

It builds one `linux/amd64` image for each Go command (`db-init`, `fleet-api`, `ride-api`, `assignment-worker`, `scenario-runner`) and one UI image. The workflow publishes version and source-SHA tags for discovery, OCI source/revision/service/version labels, and records the registry-returned immutable digest, full source commit, and architecture in one JSON artifact per image. Deployment values must use `repository@sha256:…`; tags are never deployment pins. Go API and worker binaries expose `/buildinfo` with service, source commit, architecture, and the configured image digest. The image digest is supplied at runtime because the registry assigns it after the image is built. OCI labels and release metadata identify the UI image.

The workflow also uploads the versioned `.tgz` chart as an artifact. It does not publish the chart to a registry or create a platform PR. The trusted tag/release administrator must ensure the tag points to an approved commit and cannot be moved under repository tag protections.

## Applying a release

Download the workflow artifacts, verify their tag and source commit, and copy each image digest into the corresponding `images.*.digest` value. Keep `sourceCommit` and `architecture` aligned with the metadata artifact. Replace every synthetic value in `ci-values.yaml` before any installation; those values exist only for lint and rendering. Then use the chart's documented `helm lint`, `helm template`, and `helm upgrade --install --wait --wait-for-jobs` commands with an externally managed database URL secret.

The `db-init` Helm Job is the single migration owner. Its name includes the Helm release revision and a hash of the migration image/source and database secret references, so Helm upgrades create a fresh Job and Argo's fixed render revision reacts to migration artifact/reference changes. Argo CD runs it as a Sync hook at wave `-1`, after the service account and optional disposable PostgreSQL have been applied at earlier waves and before API workloads at wave `0`. `BeforeHookCreation,HookSucceeded` removes the previous hook Job on each sync. These Argo annotations do not change Helm's normal Job behavior: Helm `--wait --wait-for-jobs` still waits for this Job and the workloads, avoiding a pre-install hook dependency cycle with the chart's disposable database. Migrations are not reversed by Helm uninstall or image rollback. Before rolling an image back, check whether that image is compatible with every migration already recorded in `app_schema_migrations`; otherwise restore a compatible database backup only through a separately reviewed procedure. The initial v1 schema is covered by the application migration tests. No disposable-cluster rollout, promotion, image signature, registry retention, rollback, or resource baseline is claimed by this workflow.

## Local release checks

```powershell
$env:GOCACHE = 'services/.gocache'
$env:GOMODCACHE = 'services/.gomodcache'
$env:TEST_DATABASE_URL = '<disposable PostgreSQL URL>'
Push-Location services
go test -p 1 ./...
Pop-Location
npm --prefix web ci
npm --prefix web run build
helm lint charts/ottawa-fleet -f charts/ottawa-fleet/ci-values.yaml
helm template ottawa-fleet charts/ottawa-fleet --namespace release-check -f charts/ottawa-fleet/ci-values.yaml
```

Use the project's pinned Helm 4.3.0 binary for local chart checks. The local PostgreSQL URL must point at a throwaway test database; the integration tests create and remove a uniquely named schema inside it.
