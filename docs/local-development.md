# Local development

The Phase 1 application runs locally with Docker Compose. It uses synthetic
inventory and ride data; no cloud account or credentials are required.

## Requirements and startup

Install Docker Desktop with the Compose v2 plugin, Python 3.11 or newer, Go
1.27.1, Node.js 24, and npm. From the repository root:

```powershell
Copy-Item deploy/.env.example .env
python scripts/dev.py config
python scripts/dev.py up
```

The sample PostgreSQL credentials in `.env.example` are synthetic and local-only.
Compose requires `POSTGRES_PASSWORD` from the root `.env` or process environment;
there is no password fallback in the service connection URLs. If you change the
sample password, use URL-safe letters, digits, hyphens, or underscores. Keep real
credentials out of this local stack.
`up` builds the Go commands and UI, waits for readiness, and opens these local
endpoints:

| Address | Service |
|---|---|
| http://localhost:8088 | Operator UI |
| http://localhost:8080 | Fleet API |
| http://localhost:8081 | Ride API |

PostgreSQL and the worker remain on the private Compose network. Only the three
listed ports bind to host loopback. Compose starts PostgreSQL, waits for its
health check, runs `db-init` once, then waits for healthy APIs before starting
the assignment worker and UI. `db-init` applies ordered SQL migrations and
creates the dataset epoch before seeding inventory. It is safe to rerun: applied
migrations and existing inventory are retained.

The Fleet API's synthetic observation feed runs every 10 seconds in Compose so
the demonstration stays useful after the original seed observations age out.
It updates seeded vehicles 001–005 from the service clock and maintains 006 at
31 seconds stale. This is an explicit mock telemetry source. API reads do not
refresh telemetry; stopping the API stops the feed. Direct launches leave this
feed disabled unless `FLEET_SYNTHETIC_OBSERVATIONS=1` is set.

## Observe and stop

Open the UI and submit a ride. The worker durably claims jobs, asks the Fleet API
for a reservation, and stores completion or failure. The seeded Lansdowne rides
reserve vehicles 001 then 002; later assignments can fail for no capacity.
Polls show database state without holding a cross-request snapshot.

To run the bounded default traffic from the host, while Compose is up:

```powershell
Push-Location services
go run ./cmd/scenario-runner -mode surge -count 10 -interval 1s -observe 60s
Pop-Location
```

`python scripts/dev.py logs`, `ps`, and `down` inspect or stop the stack. Normal
shutdown preserves PostgreSQL data in the named volume
`ottawa-fleet-app_fleet_data`; restarting recovers rides, jobs, reservations,
idempotency records, and the cursor epoch.

Fault injection is disabled by default. For a runtime fault run, set
`FLEET_FAULT_CONTROL=1` in `.env` and run `python scripts/dev.py up` to recreate
Fleet with the local control endpoint enabled. Set faults with
`python scripts/dev.py fault --latency-ms 25 --error-percent 10`, run a bounded
surge scenario from the host, then run `python scripts/dev.py fault-reset`,
including after an interrupted run. Return `FLEET_FAULT_CONTROL` to `0` and run
`up` again when finished. Health, readiness, and metrics bypass injected
faults. Ordinary service-to-service requests cannot reach the debug endpoint
because their source address is not loopback.

## Opt-in route simulation profile

The simulation profile is a separate local Compose project with its own
project-named PostgreSQL volume and bridge network. It seeds the synthetic
`route20synthetic` fleet profile only when that new project's database is
empty. It does not reset or convert an existing database. Keep the normal
Phase 1 project running if desired; use different loopback ports for the
simulation project when those defaults are occupied.

From the repository root, set a project name and optional published ports, then
start both Compose files:

```powershell
$env:SIMULATION_PROJECT_NAME = 'ottawa-fleet-simulation'
$env:SIMULATION_FLEET_API_PORT = '18080'
$env:SIMULATION_RIDE_API_PORT = '18081'
$env:SIMULATION_API_PORT = '18083'
$env:SIMULATION_WEB_PORT = '18088'
docker compose --project-directory . -p $env:SIMULATION_PROJECT_NAME -f deploy/compose.yaml -f deploy/compose.simulation.yaml config --quiet
docker compose --project-directory . -p $env:SIMULATION_PROJECT_NAME -f deploy/compose.yaml -f deploy/compose.simulation.yaml --profile simulation up --build -d
docker compose --project-directory . -p $env:SIMULATION_PROJECT_NAME -f deploy/compose.yaml -f deploy/compose.simulation.yaml --profile simulation ps
```

The local endpoints are the operator UI at `http://localhost:18088`, Fleet at
`http://localhost:18080`, Ride at `http://localhost:18081`, and the controller
at `http://localhost:18083`. The browser reaches the controller through the
same-origin `/simulation-api/` proxy. The simulation controls are opt-in; the
route profile must be ready before a run is accepted. If readiness reports
`profile_not_ready`, preserve that project's data and start a new isolated
project name with a fresh volume rather than resetting the existing one.

The standard project keeps its six-vehicle v1 seed. The opt-in simulation
profile provides the 20-vehicle `route20synthetic` fixture in a separate,
fresh project. Fleet, Ride, and simulation database migrations are additive and
retain existing v1 tables and records. An existing default-six project volume
is not converted into the simulation profile. Use a dedicated project name and
fresh volume for simulation acceptance.

In the UI, enable the opt-in checkbox, keep the request count, event rate,
duration and concurrency within the displayed bounds, then start the pinned
Lansdowne-to-Centretown scenario. The controller owns run scheduling and trip
start/completion effects; each trip takes eight simulated seconds. Moving dots
are rendered from Fleet position reads and stop interpolating when observations
are stale or future-dated. The UI does not complete a ride from animation or
send trip-completion writes. Use **Stop and drain** to prevent new work while
accepted effects reconcile. Wait for the displayed run state and bounded event
history to become terminal before stopping Compose.

The create request is replay-safe. For a run ID shown by the UI, fetch its exact
manifest and resubmit it unchanged (including its idempotency key); it should
resolve to the same run ID:

```powershell
$runId = 'run-id-shown-in-the-ui'
$run = Invoke-RestMethod "http://localhost:18083/v2/simulation/runs/$runId"
$manifest = @{ manifest = $run.manifest } | ConvertTo-Json -Depth 8
$replay = Invoke-RestMethod -Method Post -Uri 'http://localhost:18083/v2/simulation/runs' -ContentType 'application/json' -Body $manifest
$replay.run_id
Invoke-RestMethod "http://localhost:18083/v2/simulation/runs/$runId/events?limit=20"
Invoke-RestMethod http://localhost:18083/readyz
```

Stop only this simulation project without deleting its persistent data:

```powershell
docker compose --project-directory . -p $env:SIMULATION_PROJECT_NAME -f deploy/compose.yaml -f deploy/compose.simulation.yaml --profile simulation down
```

The project-specific volume is retained for later inspection or restart. Do not
add `--volumes` when stopping a run.

## Public API simulation journey

With the dedicated simulation Compose project running and ready, run the
bounded public-API journey from the repository root:

```powershell
python tests/simulation_journey.py
```

The journey checks the route and 20-vehicle fixture, exact-manifest replay,
conflicting and concurrent run rejection, moving server-reported positions,
normal trip completion, location-aware v1 reservation reuse, and Stop with
drain. It uses the default loopback ports shown above. It creates run and ride
records in the project's persistent database, so use a fresh project name and
volume when you need a clean run. The script does not reset data. Inspect the
run state and event history through the controller API before stopping the
project.

This API journey does not replace browser operator acceptance. The UI delivery
in [issue #30](https://github.com/ANISHG-26/ottawa-fleet-app/issues/30) records
refresh, stale-position, tile-failure and responsive browser checks.

## Validation and local data reset

Install the pinned contract validators once, then run the same checks as CI:

```powershell
python -m pip install -r scripts/requirements-contracts.txt
python scripts/dev.py check
```

The PostgreSQL integration tests run when `TEST_DATABASE_URL` points to a
disposable database; otherwise Go reports those tests as skipped. Hosted CI
starts its own ephemeral PostgreSQL service.

To discard local data, run `python scripts/dev.py reset`. It identifies the
Compose project and volume, lists the discarded tables/data, and asks for
confirmation. `python scripts/dev.py reset --yes` skips the prompt. Reset
removes only this Compose project's containers and named volume; the next start
creates a new database, dataset epoch, and synthetic seed. No other project or
external database is affected.
