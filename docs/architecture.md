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

Phase 1 delivers application Dockerfiles and local Compose with explicit initialization, startup/readiness and reset instructions. Separate service unit tests from database integration and live user-journey tests.

Phase 2 adds immutable images and a versioned application Helm chart. Platform owns environment values, deployment pins, namespace policy and controllers. Never duplicate application templates into platform. Chart defaults must later allow an autoscaler to own replicas.

No executable contract, runtime service, container or chart exists yet. The [Phase 1 plan](phase-1.md) tracks delivery and evidence.
