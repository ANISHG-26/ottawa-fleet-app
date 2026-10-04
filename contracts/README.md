# Version 1 application contracts

Issue #1 defines the interfaces for later implementation tickets. The normative
contract is this document plus [OpenAPI 3.1](v1/openapi.json) and the
[assignment job JSON Schema](v1/job.schema.json), version **1.0.0**.
[Examples](v1/fixtures.json) contain synthetic positive and negative cases.
[Freshness examples](v1/freshness.json) exercise the clock boundary and future observations.
These files describe future behavior; no HTTP service or database is implemented.
The additive [v2 simulation, trip, and run contract](v2/README.md) is maintained
independently and does not change these v1 meanings or generated files.

## Validation and changes

```sh
python -m venv .venv-contracts
# Windows: .venv-contracts/Scripts/python.exe; Unix: .venv-contracts/bin/python
.venv-contracts/bin/python -m pip install -r scripts/requirements-contracts.txt
.venv-contracts/bin/python scripts/check_contracts.py
python scripts/check_repository.py
```

Edit `scripts/build_contracts.py` and run `python scripts/build_contracts.py` to
regenerate the checked-in JSON. Consumers use the JSON directly. Validators are
pinned in the requirements file; they originate from PyPI (`jsonschema`, MIT;
`openapi-spec-validator`, Apache-2.0). No external application code is reused.
The check validates OpenAPI and every schema, and requires accepted and rejected
examples for each schema. Temporal and concurrency rules below need service and
database integration tests in issues #2, #5 and #6; JSON validation alone cannot
prove them. Breaking changes use a new major contract directory and coordinated
consumer changes. Additive changes require fixtures and review because objects
reject unknown properties. Job version is immutable; reject unsupported versions.

## HTTP conventions

Fleet API uses local port 8080; Ride API uses 8081. The OpenAPI operation servers
identify the owning service. Both expose `/healthz` (process live) and `/readyz`
(database reachable and migrations compatible). Dependency loss returns 503
from readiness. Worker probes and metrics are specified in its implementation
ticket. No authentication is required for the synthetic local demonstration.

All bodies are UTF-8 JSON with `Content-Type: application/json`; reject bodies
over **4096 bytes** before decoding (413), other content types (415), unknown
fields, duplicate JSON keys, invalid encodings and trailing JSON (400). All
responses, including errors, have `X-Request-ID`. A valid caller ID matches
`^[a-z][a-z0-9-]{2,63}$`; otherwise generate a new ID and return 400 with that ID.
An absent ID is generated. Errors contain the same `request_id`, a stable code
and a bounded human-readable message. The submitting request ID is stored with
the job; worker logs carry it plus ride/job IDs. Each outgoing HTTP call has its
own request ID and logs the originating ID. Do not log whole request bodies.

Zones are `centretown`, `glebe`, `lansdowne`, `byward-market`. Passengers/seats
are integers 1–4. All entity IDs match the request ID pattern. Times are RFC3339
UTC ending in `Z`; service clocks come from an injectable UTC clock, while job
leases use PostgreSQL time to avoid worker clock disagreement. `updated_at` is
never before `created_at`/`reserved_at` and cannot move backwards per record.
Pickup and dropoff may match: this is a mock assignment, not route planning.

| Status | Error code and meaning |
|---|---|
| 400 | `invalid_request`: invalid fields, key, ID, query or cursor |
| 404 | `not_found`: unknown ride, vehicle or reservation on GET |
| 409 | `idempotency_conflict`, `no_capacity`, or `reservation_released` |
| 413 / 415 | `invalid_request`: oversized body / unsupported media type |
| 503 | `temporarily_unavailable`: unavailable DB or injected API outage |
| 500 | `internal_error`: unexpected error with no internal details exposed |

OpenAPI includes representative request/response/error bodies. Error examples
show their shape; this table specifies status-specific codes. A dependency
timeout or 503 does not mean a write was rolled back; use replay/reconciliation.

## Fleet and reservations

`GET /v1/fleet` lists vehicles; `GET /v1/fleet/{vehicle_id}` returns one.
`observed_at` is the source observation time, never refreshed merely by reading.
List `as_of` is the injected clock at query start; single-vehicle reads expose
the same clock in an RFC3339 `X-As-Of` response header. At that clock time,
age in **[0, 30 seconds]** is fresh. Older or future observations have effective
availability **unknown** even if the stored status is available. The UI derives
freshness using `as_of`/`X-As-Of`, displays unknown explicitly, and labels cached
data with its observation time when polling fails. It never implies an old
observation is live. Reservation eligibility is rechecked against the Fleet
clock inside the reservation transaction.

`POST /v1/reservations` takes ride ID, pickup zone and passenger count. In one
transaction, choose the lowest vehicle ID that is fresh, available, in the
pickup zone, has enough seats, and has no active reservation. Lock candidates
and enforce one active reservation per vehicle plus one permanent record per
ride. First success returns 201. Equal fields for an existing reserved ride
return its original vehicle and timestamps with 200, even if that vehicle's
observation subsequently becomes stale. Changed fields return 409
`idempotency_conflict`. No eligible vehicle returns 409 `no_capacity` and creates
no reservation. Reservations are mock holds until explicitly released/reset.

`GET /v1/reservations/{ride_id}` reconciles uncertain writes.
`DELETE /v1/reservations/{ride_id}` returns 200 and atomically releases a hold.
If none exists, create a permanent `released` tombstone without a vehicle or
`reserved_at`; replay returns that same tombstone. A previously reserved record
retains its vehicle and original timestamp. Any later POST for a released ride
returns 409 `reservation_released`. This fences delayed requests from an expired
worker and prevents orphan holds after terminal failure. Release never deletes
ride identity or makes that ride eligible for another assignment. The Fleet API
owns inventory/reservation storage; callers never write its tables.

## Ride acceptance and pagination

`POST /v1/rides` requires an `Idempotency-Key` of 8–128 ASCII letters, digits,
underscore or hyphen. Validate the body before accepting it. A single Ride API
database transaction inserts the ride (`queued`), unique idempotency record and
exactly one job. Return **202**, `Location: /v1/rides/{ride_id}` and the current
ride only after commit. The unique key is scoped to this endpoint for the entire
local dataset lifetime, with no TTL. Equal normalized fields (not JSON byte
order or whitespace) return the same ride's current representation with 202 and
the same Location. Changed fields return 409 `idempotency_conflict`. Concurrent
equal submissions create one ride/job; concurrent conflicts return 409. Invalid
requests never consume the key. Reads include `GET /v1/rides/{ride_id}` and list
`GET /v1/rides`; a failed worker never undoes durable acceptance.

Both lists accept only `limit` (default 20, 1–100) and optional opaque `cursor`
(1–256 characters). Reject unknown query fields and malformed/foreign cursors.
Fleet is ordered by vehicle ID; rides by `(created_at, ride_id)` ascending.
Cursor binds endpoint, limit, initial `as_of`, and last ordering key. Ride pages
exclude records created after initial `as_of`; all pages return that initial
clock value. State is read at each page request. No database snapshot is held
across requests: a concurrent insertion with exactly the cutoff timestamp can
appear on a later page if its ordering key is after the cursor. Fleet
membership is fixed by the seed; freshness uses initial `as_of` on every page.
`next_cursor: null` ends pagination; empty lists return 200. Deletion is absent
except a named local reset, which invalidates cursors and idempotency records.

## Durable job processing

The job schema describes a logical record, not a migration or queue HTTP API.
Ride API owns ride, idempotency and job tables; worker accesses job/ride tables
through this explicitly shared database contract. `ride_id` is unique across
assignment jobs. Payload copies the accepted request and is immutable, as are
kind/version, IDs, creation time and `max_attempts=5`. Request ID is the original
acceptance correlation ID. Ride and job state changes commit together.

| Current | Event | Next state and effects |
|---|---|---|
| queued | Due claim with attempts <5 | processing; increment attempts, lease for 30 seconds |
| processing | Reservation confirmed, owned lease | completed; set same vehicle ID in ride/job |
| processing | Definitive no capacity or invalid job | failed after confirmed release/tombstone |
| processing | Transient error, attempts <5 | queued; clear lease, schedule bounded backoff |
| processing | Lease expires, attempts <5 | queued; clear lease, schedule retry |
| processing | Fifth attempt fails or expires | processing reconciliation; no sixth assignment attempt |
| processing reconciliation | Reservation exists | completed with original vehicle |
| processing reconciliation | No reservation / released | failed after confirmed tombstone |
| completed / failed | Any later delivery | unchanged; terminal states never requeue |

Claims use one atomic transaction with row locking (`FOR UPDATE SKIP LOCKED` or
equivalent), eligible `available_at <= database_now`, and a fresh unique lease
token/worker owner. A worker may own at most one job at a time. Only updates with
matching token and `lease_expires_at > database_now` may mutate processing state;
every completion, retry and failure is a compare-and-set transaction. Expiry at
exactly the deadline loses ownership. A new claim never shares a token. Expired
owners discard results. Each HTTP call times out after 5 seconds. Worker shutdown
stops claiming and waits at most 10 seconds; unfinished claims expire normally.

Retry delays after attempts 1–4 are **1, 2, 4, 8 seconds**, without jitter in
this deterministic mock. Retry transport errors, timeouts, 503 and 500. Do not
retry malformed payloads, idempotency conflicts or no capacity as new assignment
attempts. Such conflicts map to `invalid_job`; no capacity to `no_capacity`.
On every reclaimed job, first GET its reservation: reuse a reserved result;
only 404 permits POST. A Fleet outage must never be treated as 404.

After attempt 5, a recovery owner can acquire a new 30-second reconciliation
lease without incrementing attempts or issuing POST. It GETs the reservation.
If absent/released, DELETE ensures a tombstone before recording `retry_exhausted`.
DELETE races safely with an in-flight old POST: whichever wins, release ends
the hold and prevents later recreation. For definitive failures use the same
cleanup before recording their failure code. If reconciliation/cleanup is
unavailable, keep processing, let the lease expire and try maintenance again
after 8 seconds; never spin or make a sixth assignment attempt. Maintenance is
bounded per lease to one GET and one DELETE, but its elapsed duration can extend
until Fleet returns. The UI shows processing during this outage. This is a
deliberate distinction between the five assignment attempts and cleanup needed
to safely reach a terminal state; expose recovery failures in logs/metrics.

Crash examples: before acceptance commit, retry the key; after acceptance commit,
replay returns the existing ride. After reservation but before job commit, lease
recovery finds the original vehicle. After completion commit, redelivery does
nothing. Two workers can race in Fleet, but the permanent ride uniqueness and
job lease fencing preserve at most one assignment. Failed jobs have no active
reservation. Completed rides may be released for the mock scenario; their
historical vehicle/result remains completed. Cross-service atomicity is achieved
by these idempotent effects and reconciliation, not a distributed transaction.

## Concrete UI journey and bounded default scenario

Use a fixed clock **2026-10-03T12:00:00Z** for fixtures. Seed six vehicles,
`vehicle-001` through `vehicle-006`: two in lansdowne, two in centretown, one in
glebe and one in byward-market. All have four seats. Vehicles 001–005 are
available observed at the fixed clock; 006 is stored available but observed
31 seconds earlier and therefore displays unknown. No random inventory is
required. Runtime seeding uses a supplied scenario clock; it must not use the
fixed fixture date as live telemetry. Reset only via the later documented local
database reset, naming inventory, reservations, rides, jobs and idempotency data.

Operator opens fleet, sees five available and one unknown; submits a two-person
lansdowne→centretown ride with key `demo_ride_001`; sees queued then processing
then completed with vehicle-001. Replaying that key shows the same ride; changing
passengers gives a conflict. Poll fleet and ride status every 2 seconds, stop
polling when the UI closes, and label fetch failures/cached observations visibly.
States may advance between polls, so the UI must not require seeing processing.

Default scenario: **10** two-person lansdowne→centretown submissions, one per
second over **10 seconds**, keys `surge_0001`…`surge_0010`, one worker, no automatic
release. In a healthy stack the first two reserve vehicles 001 and 002; the
remaining eight fail `no_capacity`. Stop after 10 submissions; observe up to
60 seconds and report unfinished rides instead of generating further load.
Only explicit localhost Fleet/Ride ports are allowed for the later scenario CLI.
For recovery evidence in #6/#8, pause a worker after its first successful
reservation and before commit, terminate it, restart after lease expiry, and
assert one completed ride with one reservation. Faults are opt-in; restore API
availability and stop the runner/worker after observation. No external resources
are provisioned by this contract or default scenario.
