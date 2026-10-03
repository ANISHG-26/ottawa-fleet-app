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
