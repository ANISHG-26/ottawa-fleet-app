# Application repository working agreements

This repository belongs to the Application team. Own Go services, UI, contracts, database migrations, Dockerfiles, Compose and application Helm templates/releases. Platform bootstrap, environment values/release pins, Argo CD, KEDA, Istio, Terraform and operational integrations belong in `ANISHG-26/ottawa-fleet-platform`.

- Read README, `docs/architecture.md`, `docs/phase-1.md` and linked shared delivery/release contracts before implementation.
- Work through one bounded GitHub issue and a focused `codex/` branch/PR. Do not merge without user authorization. Keep implementation in the current phase.
- Phase 1 is local. Do not install cloud infrastructure, controllers, Backstage or AI runtimes to implement application tickets.
- Use synthetic data only. No real vehicle commands, safety decisions or real user/payment data.
- Write failing behavior tests before meaningful implementation. Docs/scaffolds need proportional validation.
- Use one Go module with separate commands, explicit API/job contracts, idempotency, bounded retries and crash recovery. Avoid a repository per service.
- Application Helm packages are namespace-portable and do not install platform controllers/policies. Honor the shared release contract.
- Keep credentials, account identifiers and private planning outside source and logs. PR CI has minimal permissions, timeouts/concurrency and no cloud/publish credentials.
- Do not introduce destructive reset behavior without clearly naming affected local data. No cloud resource is provisioned by this repository's foundation.
- Record license/dependency provenance before reusing external code. No existing private source is implicitly authorized for copying.
- If subagents are authorized, assign non-overlapping files and bounded outcomes and review their work.
