# Local application architecture

## User journey

An operator views synthetic Ottawa vehicles, submits a ride request and sees it move through queued work to an assignment result. A Lansdowne scenario increases demand. Stopping workers grows backlog; restarting drains recoverable work. API degradation is visible without presenting old observations as fresh facts.

## Components

| Component | Responsibility | Boundary |
|---|---|---|
| Fleet API (Go) | Seeded inventory, observed availability and idempotent mock reservation/release | HTTP; owns fleet tables/migrations |
| Ride API (Go) | Validate ride submissions and idempotency; persist ride/job atomically; expose status | HTTP; owns ride/job tables/migrations |
| Assignment worker (Go) | Lease jobs, call Fleet API, commit outcomes and retry within bounds | Documented job database contract and HTTP Fleet API |
| Scenario CLI (Go) | Seeded bounded traffic and explicit fault scenarios | Calls application APIs; no arbitrary-target load mode |
| Operator UI (TypeScript) | Fleet status, ride submission/history and degradation visibility | HTTP contracts; no database access |
| PostgreSQL | One local instance with explicit table ownership | Durable jobs and inventory; no initial message broker |

One Go module under `services/` with commands under `cmd/` keeps tooling small while preserving independently deployable processes. Shared code lives under `internal/` only where semantics are shared. UI framework/tool versions are selected in bounded tickets.

## Contracts before services

The first Phase 1 ticket specifies exact OpenAPI routes, schemas, examples and state transitions. Resolve input bounds, timestamp/freshness semantics, pagination/errors, idempotency-key conflicts, job leases/retry caps and deterministic fixture behavior.

A worker crash after Fleet API reservation must not create another assignment when the job retries. Use ride identity for idempotent cross-service effects and test concurrent workers. Durable acceptance, visible terminal failure and bounded shutdown/recovery matter even when business logic is mocked.

No full event-sourcing/projector system, routing optimizer, payments, identity product or realtime streaming UI is required. Polling and a few bounded fixtures are sufficient. Start with basic structured logs, request IDs, probes and stable metrics; platform monitoring comes later.

## Packaging and operations

Phase 1 implementation includes application Dockerfiles and local Compose with explicit initialization, startup/readiness and reset instructions. Service unit tests, database integration tests and live user-journey acceptance are separate evidence. The code and preparation are merged; the full local run and platform measurement remain open in application issues [#4](https://github.com/ANISHG-26/ottawa-fleet-app/issues/4), [#9](https://github.com/ANISHG-26/ottawa-fleet-app/issues/9) and platform [#12](https://github.com/ANISHG-26/ottawa-fleet-platform/issues/12).

Docker and Helm preparation has merged. Tagged build [v0.1.0](https://github.com/ANISHG-26/ottawa-fleet-app/tree/v0.1.0), built from reviewed source commit `f485682`, has six immutable GHCR images; [trusted workflow run 37131621545](https://github.com/ANISHG-26/ottawa-fleet-app/actions/runs/37131621545) also packaged chart `0.1.0` as an Actions artifact. Current chart source is `0.1.1` and includes the disposable PostgreSQL fix; it is separate from that published build. See [release status](releases.md#published-build-and-current-source). Artifact publication does not establish accepted installed Helm, cloud lifecycle or Compose outcomes. Those gates remain open under application [#9](https://github.com/ANISHG-26/ottawa-fleet-app/issues/9) and [#11](https://github.com/ANISHG-26/ottawa-fleet-app/issues/11), and platform [#12](https://github.com/ANISHG-26/ottawa-fleet-platform/issues/12) and [#15](https://github.com/ANISHG-26/ottawa-fleet-platform/issues/15). Platform owns environment values, deployment pins, namespace policy and controllers. Never duplicate application templates into platform. Chart defaults allow an autoscaler to own replicas.

The [v1 contract and fixtures](../contracts/README.md) define API, freshness, reservation and job recovery rules. Merged application work delivered the runtime and its Docker/Compose and Helm preparation (PRs #16–#19); v0.1.0 now provides the first image and chart build. Published artifacts do not establish measured Phase 1 acceptance or an installed Phase 2 release. The [Phase 1 plan](phase-1.md) tracks remaining evidence.
