# Contributing to the mock application

Use the shared board's Application team and relevant Phase filter. Start with one issue that has an agreed contract, resolved blockers, acceptance criteria and evidence requirements. Claim an assignee when starting; keep one implementation issue active per logical team.

1. Read the architecture and the issue's parent/dependencies.
2. Branch from current main as `codex/<issue>-<purpose>`.
3. Write a failing behavior test, then implement the bounded outcome. Introduce useful local checks as the first component lands; the CI ticket consolidates them.
4. Validate locally using the same commands as CI. Note cross-repo release/configuration effects.
5. Open a focused draft PR and move review stage to In review. Record actual evidence and limitations.
6. Wait for human merge authorization. Close only after acceptance is demonstrated and delivery is merged.

Documentation/scaffolding uses `python scripts/check_repository.py`; it does not prove application behavior. No runtime tests exist yet. UI fixture tests may be developed before the services; the Compose and platform acceptance tickets verify the full live journey.
