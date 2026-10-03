# Ottawa Fleet Application

A small mock application for learning platform operations: synthetic fleet availability, ride requests, queued assignments and an operator UI. During a Lansdowne demand surge, observe backlog, degrade an API, stop/restart a worker and verify recovery.

**Status: v1 API/job contracts and executable fixtures are defined for review. No runnable API, UI, worker, Docker image, Compose stack or Helm chart exists yet.**

## Ownership

The Application team owns Go services, UI, contracts, migrations, Docker/Compose and the application Helm chart. The [Platform team](https://github.com/ANISHG-26/ottawa-fleet-platform) owns infrastructure, Argo CD, environment values/version pins, KEDA, Istio, Terraform and operating evidence. Both use the [same board](https://github.com/users/ANISHG-26/projects/6).

All business data is synthetic. No real vehicle control, routing optimization, payments or safety decisions are in scope.

## Planned structure

```text
services/   # One Go module: cmd/{fleet-api,ride-api,assignment-worker,scenario-runner}, internal/
web/        # Small TypeScript operator UI; framework chosen in UI ticket
contracts/  # Versioned OpenAPI/job schemas and fixtures
db/         # Service-owned PostgreSQL migrations and synthetic seeds
deploy/     # Application Dockerfiles and local Compose configuration
charts/     # Versioned application Helm package in Phase 2
tests/      # Integration and user-journey checks
scripts/    # Current scaffold validation; future developer commands
docs/       # Application design and links to shared delivery contracts
.github/    # Issue/PR templates and documentation CI
```

The [v1 contract](contracts/README.md) defines API/job behavior and schema validation. Future service unit tests live beside Go packages; cross-service checks live in `tests/`.

## Start here

- [Application architecture](docs/architecture.md)
- [Phase 1 and ticket map](docs/phase-1.md)
- [Contributing](CONTRIBUTING.md)
- [Shared delivery workflow](https://github.com/ANISHG-26/ottawa-fleet-platform/blob/codex/bootstrap-platform/docs/project-management.md)
- [Application/platform release contract](https://github.com/ANISHG-26/ottawa-fleet-platform/blob/codex/bootstrap-platform/docs/application-release-contract.md)

Shared documents currently live on the platform foundation review branch; switch those links to main after that PR merges. Main does not yet contain them.

## Validate this scaffold

```sh
python scripts/check_repository.py
```

This checks documentation and JSON. Follow the [contract validation commands](contracts/README.md#validation-and-changes) for OpenAPI and positive/negative fixtures; CI runs both checks. Runtime application behavior still requires later tickets. Phase 1 delivers local startup and CI. Helm packaging and release publishing follow in Phase 2; Kubernetes, cloud and model access are not prerequisites.
