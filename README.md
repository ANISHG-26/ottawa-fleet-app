# Ottawa Fleet Application

A local mock application for learning platform operations: synthetic Ottawa
fleet availability, durable ride requests, assignment work, and an operator UI.
Use the Compose stack to observe demand, backlog, bounded API faults, and worker
recovery without external accounts or infrastructure.

**Phase 1 runtime implementation is merged:** Fleet and Ride APIs, PostgreSQL
migrations, assignment worker, TypeScript operator UI, bounded scenario CLI,
Dockerfiles, local Compose, and application CI. Follow
[local development](docs/local-development.md) to start and validate the stack.
Release [v0.1.0](https://github.com/ANISHG-26/ottawa-fleet-app/releases/tag/v0.1.0)
was built from reviewed source commit `f485682`. Its [trusted workflow run](https://github.com/ANISHG-26/ottawa-fleet-app/actions/runs/37131621545)
published six immutable GHCR images and packaged the Helm chart as an Actions
artifact. Local Compose acceptance remains open under application issues [#4](https://github.com/ANISHG-26/ottawa-fleet-app/issues/4)
and [#9](https://github.com/ANISHG-26/ottawa-fleet-app/issues/9); installed Helm
acceptance remains open under [#11](https://github.com/ANISHG-26/ottawa-fleet-app/issues/11).
Platform measurement and the GCP lab remain separate acceptance work tracked by
[platform #12](https://github.com/ANISHG-26/ottawa-fleet-platform/issues/12) and
[platform #15](https://github.com/ANISHG-26/ottawa-fleet-platform/issues/15).

Optional local LGTM telemetry is described in [telemetry setup](docs/telemetry.md).

## Ownership

The Application team owns Go services, UI, contracts, migrations, Docker/Compose,
and the application Helm chart. The [Platform team](https://github.com/ANISHG-26/ottawa-fleet-platform)
owns infrastructure, Argo CD, environment values/version pins, KEDA, Istio,
Terraform, and operating evidence. Both use the [same board](https://github.com/users/ANISHG-26/projects/6).

All business data is synthetic. No real vehicle control, routing optimization,
payments, or safety decisions are in scope.

## Repository layout

```text
services/   # One Go module: fleet-api, ride-api, assignment-worker, scenario-runner
web/        # TypeScript operator UI, fixture and browser checks
contracts/  # Versioned OpenAPI/job schemas and fixtures
db/         # Fleet and ride PostgreSQL migrations
deploy/     # Dockerfiles and local Compose configuration
charts/     # Versioned application Helm package; release acceptance is tracked separately
tests/      # Cross-service checks and run instructions
scripts/    # Contract, repository, and developer commands
docs/       # Architecture, phase plan, and local runbook
.github/    # Repository and application CI
```

The [v1 contract](contracts/README.md) defines API/job behavior and validation.
See [application architecture](docs/architecture.md), [Phase 1 ticket map](docs/phase-1.md),
and [contributing](CONTRIBUTING.md) for the implementation boundaries.

## Validate

```sh
python -m pip install -r scripts/requirements-contracts.txt
python scripts/dev.py check
```

CI runs the same Go formatting/vet/unit/integration checks, contract validation,
UI tests/build, and fixture browser smoke. Database tests use a disposable
PostgreSQL service; local runs skip them explicitly when `TEST_DATABASE_URL` is
unset. Kubernetes, cloud resources, and model access are not Phase 1 needs.
