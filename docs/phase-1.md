# Phase 1: local mock application

The [parent outcome](https://github.com/ANISHG-26/ottawa-fleet-app/issues/4) delivers a local UI/API/worker demonstration. First review [application foundation #3](https://github.com/ANISHG-26/ottawa-fleet-app/issues/3) and [platform foundation #1](https://github.com/ANISHG-26/ottawa-fleet-platform/issues/1). No implementation is marked complete.

| Ticket | Delivery |
|---|---|
| [#1](https://github.com/ANISHG-26/ottawa-fleet-app/issues/1) | Define fleet, ride and job contracts with executable examples |
| [#2](https://github.com/ANISHG-26/ottawa-fleet-app/issues/2) | Implement the Go mock fleet API and deterministic inventory |
| [#5](https://github.com/ANISHG-26/ottawa-fleet-app/issues/5) | Implement durable ride submission and queued work in Go |
| [#6](https://github.com/ANISHG-26/ottawa-fleet-app/issues/6) | Process ride jobs with bounded retries and restart recovery |
| [#7](https://github.com/ANISHG-26/ottawa-fleet-app/issues/7) | Build the operator UI for fleet and ride visibility |
| [#8](https://github.com/ANISHG-26/ottawa-fleet-app/issues/8) | Add repeatable demand and service-failure scenarios |
| [#9](https://github.com/ANISHG-26/ottawa-fleet-app/issues/9) | Package the complete application for local Docker Compose |
| [#10](https://github.com/ANISHG-26/ottawa-fleet-app/issues/10) | Gate application changes with local and CI behavior checks |


## Sequence and acceptance

Start with contract #1. Fleet/ride APIs then support worker and scenarios. UI can start from fixtures; CI checks accompany service implementation. Compose integrates the complete journey. Platform [acceptance #12](https://github.com/ANISHG-26/ottawa-fleet-platform/issues/12) verifies a fresh local start, workload settings, visible backlog, failure/recovery, no duplicate assignments and resource use.

Done requires actual evidence and merged implementation. A scaffold check only proves docs are internally consistent. Each child issue has native blockers; unstarted work stays Todo with no review stage until ready.

## Following phases

[Images #12](https://github.com/ANISHG-26/ottawa-fleet-app/issues/12) and [Helm #11](https://github.com/ANISHG-26/ottawa-fleet-app/issues/11) are P2 packaging work owned here. Platform consumes those artifacts for GitOps. Kubernetes/cloud access, KEDA, Istio, Backstage and AI are not Phase 1 dependencies.
