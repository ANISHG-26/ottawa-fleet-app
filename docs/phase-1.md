# Phase 1: local mock application

The [parent outcome](https://github.com/ANISHG-26/ottawa-fleet-app/issues/4) tracks a local UI/API/worker demonstration. Core implementation and Docker/Helm preparation have merged in application PRs #16–#19. Full Compose acceptance [#9](https://github.com/ANISHG-26/ottawa-fleet-app/issues/9) remains open pending evidence; platform measurement is separately tracked by [platform #12](https://github.com/ANISHG-26/ottawa-fleet-platform/issues/12).

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

The contract, APIs, worker, scenarios, UI, service checks and Compose integration are implemented. Remaining acceptance evidence includes a fresh local start, workload settings, visible backlog, failure/recovery, no duplicate assignments and resource use. Platform [acceptance #12](https://github.com/ANISHG-26/ottawa-fleet-platform/issues/12) records that workload evidence.

Done requires actual evidence and merged implementation. A scaffold check only proves docs are internally consistent. Each child issue has native blockers; unstarted work stays Todo with no review stage until ready.

## Following phases

The first immutable image publication and chart package are available from the [tagged v0.1.0 build](https://github.com/ANISHG-26/ottawa-fleet-app/tree/v0.1.0) and [trusted workflow run 37131621545](https://github.com/ANISHG-26/ottawa-fleet-app/actions/runs/37131621545). See [published build versus current chart source](releases.md#published-build-and-current-source) before selecting a release. Verified Helm installation [#11](https://github.com/ANISHG-26/ottawa-fleet-app/issues/11), Compose acceptance [#9](https://github.com/ANISHG-26/ottawa-fleet-app/issues/9), and platform GCP and workload acceptance [#15](https://github.com/ANISHG-26/ottawa-fleet-platform/issues/15) and [#12](https://github.com/ANISHG-26/ottawa-fleet-platform/issues/12) remain pending. Platform consumes accepted artifacts for GitOps. Kubernetes/cloud access, KEDA, Istio, Backstage and AI are not Phase 1 dependencies.
