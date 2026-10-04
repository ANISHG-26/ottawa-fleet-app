# Contributing to the mock application

Use the shared board's Application team and relevant Phase filter. Start with one issue that has an agreed contract, resolved blockers, acceptance criteria and evidence requirements. Claim an assignee when starting; keep one implementation issue active per logical team.

1. Read the architecture and the issue's parent/dependencies.
2. Branch from current main as `codex/<issue>-<purpose>`.
3. Write a failing behavior test, then implement the bounded outcome. Introduce useful local checks as the first component lands; the CI ticket consolidates them.
4. Validate locally using the same commands as CI. Note cross-repo release/configuration effects.
5. Open a focused draft PR and move review stage to In review. Record actual evidence and limitations.
6. Wait for human merge authorization. Close only after acceptance is demonstrated and delivery is merged.

Documentation/scaffolding uses `python scripts/check_repository.py`; it does not prove application behavior. Service checks include Go unit tests, contract validation and UI fixture tests. Database integration tests use a disposable PostgreSQL instance and are skipped explicitly when `TEST_DATABASE_URL` is unset. Compose and platform acceptance tickets verify the full live journey; merged implementation does not substitute for that evidence.

## Bounded agent delivery

Keep one accepted outcome active, with implementation slices small enough to check
and review independently. Use Luna low/medium for settled, small tasks, Luna high
or Sol low for features with interacting behavior, and a fresh reviewer for
correctness and security. Give each agent a compact brief containing the exact
base revision, owned paths, observable outcome, exclusions, test commands and
stop conditions. Assign shared databases, ports and Compose resources to one
coordinator; worktrees do not isolate them.

After three to five minutes, capture the finding, changed paths, red/green
evidence and remaining blocker, then reassess the slice. Keep progress and
diagnostics outside public source. Before an allowance or time boundary, commit
and push a clearly labeled checkpoint and record the branch/revision, resource
ownership, passing checks, unresolved findings and exact next action. Treat a
reset as continuation from that checkpoint. An elapsed wait never grants approval.

Review the combined behavior after integration. Re-run passing checks only when
new changes, failures or unresolved concerns justify it; exercise the affected
regression after each review fix. Build the final source before recording the
real service journey, and distinguish fixture checks, measured acceptance,
reviewable delivery and merge. Agree time boundaries and allowance checkpoints
at the start, and update them when the owner changes the constraints.
