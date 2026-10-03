# Go services

The services share one Go 1.25 module with independent commands:

- `cmd/fleet-api`: durable fleet inventory and reservation API on `:8080`.
- `cmd/db-init`: applies the ordered SQL migrations in `db/fleet` and `db/ride`, then inserts the six deterministic vehicles once.
- `cmd/ride-api`: accepts durable, idempotent rides and serves ride status on `:8081`.
- `cmd/assignment-worker`: leases assignment jobs, reconciles Fleet reservations, and serves probes/metrics on `:8082`.
- `cmd/scenario-runner`: submits bounded synthetic demand to the local Fleet and Ride APIs.

Run these commands from `services/` after starting PostgreSQL:

```powershell
$env:DATABASE_URL = "postgres://postgres:postgres@localhost:5432/fleet?sslmode=disable"
go run ./cmd/db-init
go run ./cmd/fleet-api
```

Run `ride-api` and `assignment-worker` in separate terminals after `db-init` and
`fleet-api` are running:

```powershell
go run ./cmd/ride-api
go run ./cmd/assignment-worker
```

The Ride API uses `DATABASE_URL` and `HTTP_ADDR` (default `:8081`). The worker
uses `DATABASE_URL`, `FLEET_API_URL` (default `http://localhost:8080`) and
`HTTP_ADDR` (default `:8082`). Both APIs and the worker require the schema
created by `db-init`; they do not apply migrations at startup. With Fleet and
Ride APIs available on their default loopback ports, run a bounded scenario:

```powershell
go run ./cmd/scenario-runner -mode surge -count 10 -interval 1s -observe 60s
```

Scenario URLs default to `http://localhost:8080` and `http://localhost:8081`;
only loopback URLs on those explicit ports are accepted. Submission duration is
limited to 60 seconds and observation to 60 seconds. Fault controls require
`-mode fault` and explicit bounded fault flags.

Set `FLEET_SEED_TIME` to an RFC3339 UTC timestamp to supply a deterministic
scenario clock to the seed command. By default it uses the current UTC time;
vehicle 006 is observed 31 seconds before that time. The published fixed date
in the contract is a fixture reference and is not written as runtime telemetry.
Normal reads never refresh `observed_at`.

The local Compose stack explicitly enables a separate synthetic observation
feed every 10 seconds so the operator journey remains fresh during a long demo.
It updates the five current vehicles from its injected service clock and keeps
vehicle 006 at 31 seconds stale. This is generated mock telemetry, not data
refreshed as a side effect of an API read. Direct API launches leave the feed
off unless `FLEET_SYNTHETIC_OBSERVATIONS=1` is set; its interval is bounded to
1–60 seconds with `FLEET_OBSERVATION_INTERVAL_SECONDS`.

Effective availability first applies the 30-second freshness interval from the
v1 contract. A future or stale observation is `unknown`. For a fresh observation,
a reserved vehicle is `reserved`; stored unavailability, maintenance mode, or
battery below 20 percent is `unavailable`; otherwise it is `available`. The
maintenance and battery columns are synthetic internal controls, not vehicle
response fields. The fixed six-vehicle seed has no maintenance or low-battery
flags. Unit tests cover both policy branches.

The fleet API does not run migrations at startup. `/readyz` checks PostgreSQL
and the fleet schema. Fault injection is disabled by default. Startup settings
`FLEET_FAULT_LATENCY_MS` (0–1000) and `FLEET_FAULT_ERROR_PERCENT` (0–50) are
bounded. Runtime controls are enabled only when `FLEET_FAULT_CONTROL=1`; then
direct loopback callers can POST `{"latency_ms":25,"error_percent":10}` to
`/debug/faults`, and POST to `/debug/faults/reset` to restore normal operation.
In Compose, opt in with `FLEET_FAULT_CONTROL=1` and use
`python scripts/dev.py fault` and `fault-reset`; those commands call the debug
endpoint from inside the Fleet container. This keeps controls inaccessible to
ordinary service-to-service calls. Health, readiness, metrics, and build info
stay available during injected faults.

Run unit tests with `go test ./...`. PostgreSQL integration tests use
`TEST_DATABASE_URL`; when it is not set, those tests report a skip.
