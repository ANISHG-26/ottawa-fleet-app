# Ottawa Fleet Application

A local mock application for learning platform operations: synthetic Ottawa
fleet availability, durable ride requests, assignment work, and an operator UI.
Use the Compose stack to observe demand, backlog, bounded API faults, and worker
recovery without external accounts or infrastructure.

**Phase 1 runtime is implemented:** Fleet and Ride APIs, PostgreSQL migrations,
assignment worker, TypeScript operator UI, bounded scenario CLI, Dockerfiles,
local Compose, and application CI. Follow [local development](docs/local-development.md)
to start and validate the stack. Phase 2 image publishing and Helm release work
remain separate tickets.

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
charts/     # Versioned application Helm package for Phase 2
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
