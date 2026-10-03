# Local deployment files

`compose.yaml` builds the Go commands and operator UI for the local Phase 1
stack. PostgreSQL uses the exact `17.11` minor image tag. Go binaries use the
official `golang:1.27.1-alpine` builder and run as UID/GID 10001 on Alpine.
The UI builds with Node 24 and runs on the nginx unprivileged image on port
8080 as UID 101. The API ports and UI port bind only to host loopback; database
and worker have no published ports.

The Go Dockerfile is shared by the API, worker, and one-shot database initializer
through the `APP_CMD` build argument. It embeds service, source commit, and
architecture metadata for API/worker build-info endpoints; `BUILD_DIGEST` is
runtime-injected. Local builds identify themselves as `local`.

Compose starts PostgreSQL, waits for its health check, then runs `db-init` to
apply `db/fleet` and `db/ride` migrations and seed the fleet. APIs wait for that
one-shot to complete. The assignment worker and UI start after API readiness.
The `fleet_data` volume preserves local state during normal `down`; only the
named reset command removes it.

Version source review performed 2026-10-03: [Go release history](https://go.dev/doc/devel/release)
lists Go 1.27.1 as the current stable patch; [PostgreSQL versioning](https://www.postgresql.org/support/versioning/)
lists PostgreSQL 17.11 as supported through November 2029; [Docker Official
Images](https://hub.docker.com/_/postgres/tags?name=17.11) lists the exact
`postgres:17.11` tag. See [local development](../docs/local-development.md)
for startup, validation, fault controls, and reset instructions.
