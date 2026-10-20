# Ottawa Fleet Application

A local mock application for learning platform operations: synthetic Ottawa
fleet availability, durable ride requests, assignment work, and an operator UI.
Use the Compose stack to observe demand, backlog, bounded API faults, and worker
recovery without external accounts or infrastructure.

## Two-minute refresher

**Back after a week or two? Start with the [quick guide](QUICK_GUIDE.md).**
It explains the screens and the main ideas without requiring a code deep dive.

| Watch a short captioned clip | What it teaches |
|---|---|
| [Fleet and freshness](videos/01-fleet-and-freshness.webm) | See 20 synthetic vehicles and recognize stale data. |
| [Trip and refresh](videos/02-trip-and-refresh.webm) | Follow one trip and recover the same run after refresh. |
| [Stop and drain](videos/03-stop-and-drain.webm) | Stop demand while accepted work finishes. |

Open [WATCH.html](WATCH.html) from a local clone for inline video players.
The [capture metadata](videos/recordings.json) identifies the release shown.

**Current snapshot — October 20, 2026:** Fleet/Ride APIs, durable assignment,
route simulation, operator UI, PostgreSQL migrations, Compose and CI are built.
[v0.2.1](https://github.com/ANISHG-26/ottawa-fleet-app/tree/v0.2.1) has seven
published images and a versioned Helm chart. Live trip, vehicle reuse,
refresh recovery and Stop-and-drain journeys have passed; see
[local simulation evidence](docs/simulation-acceptance.md) and
[cloud acceptance](https://github.com/ANISHG-26/ottawa-fleet-platform/blob/main/docs/acceptance/first-application-deployment.md).
Broader recovery measurements, GitOps drift correction and compatible rollback
remain platform acceptance work. Older runbooks contain dated release history;
use their dates when interpreting status.

For startup, follow [local development](docs/local-development.md).

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
QUICK_GUIDE.md # Plain-language tour and links to recorded scenarios
WATCH.html  # Local video gallery; open in a browser
videos/     # Short synthetic Playwright recordings, posters and capture metadata
services/   # One Go module: APIs, assignment worker, scenario runner, simulation controller
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
