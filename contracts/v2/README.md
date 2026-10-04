# Simulation, trip, and run contracts (v2.0.0)

This is an additive contract version. The v1 Fleet/Ride API and assignment job
meanings remain unchanged. In particular, a Ride with `assignment_state=completed`
has a vehicle assignment; it does not mean the passenger trip has completed.
The v2 `trip_state` owns that lifecycle separately.

`openapi.json` defines the HTTP surface and strict JSON schemas. `schema-fixtures.json`
contains a valid and invalid representative payload for every named schema.
`semantic-fixtures.json` contains executable examples for bounded manifests, replay,
event fencing, freshness, and stop/drain behavior. `scripts/build_simulation_contracts.py`
regenerates the contract and fixtures. The validator's hand-coded reference evaluator
checks the examples against the written rules; it is not runtime or database proof.
Consumers must opt into `/v2`; they must not reinterpret a v1 representation.

## Ownership and APIs

The Simulation Controller owns immutable run manifests, run state, scheduled event
identities, event results, and bounded run history. It may call owner APIs only; it
never writes Fleet or Ride tables. Ride API owns trip records and the trip state
machine. Fleet API owns operational vehicle state, position observations and the
existing reservation records. Assignment jobs remain short-lived v1 work records;
the controller schedules trip events after assignment and does not turn a passenger
waiting for a vehicle into a runnable assignment job.

The Controller on port 8083 exposes `POST/GET /v2/simulation/runs`, `GET /v2/simulation/runs/{run_id}`,
`DELETE /v2/simulation/runs/{run_id}` (stop), and cursor-paged
`GET /v2/simulation/runs/{run_id}/events`, plus `GET /v2/simulation/routes/{route_id}`.
Ride API on port 8081 exposes `GET/POST /v2/rides/{ride_id}/trip` and
`POST /v2/rides/{ride_id}/trip/completion`. Fleet API on port 8080 exposes
`GET /v2/fleet/vehicles/{vehicle_id}/position`, explicit trip effect commands
`POST /v2/fleet/vehicles/{vehicle_id}/trips/start` and
`POST /v2/fleet/vehicles/{vehicle_id}/trips/completion`, and
`GET /v2/fleet/events/{event_id}` to reconcile a stored owner receipt. Each API
operation declares its owning port in OpenAPI. Every list page
contains at most 100 entries; cursors are opaque and scoped to the run and endpoint.
Every write uses an event identity and returns the stored result on an exact replay.
Reuse of an identity with changed content is a conflict.

Run creation is idempotent by manifest `idempotency_key` for the local dataset
lifetime. The same normalized manifest returns the original run; changed settings
conflict. At most one run may be scheduled, running, or stopping. A run cannot be
edited after creation. Each manifest has at most 20 trip requests, 60 execution
events (start, position, and completion), 4 scheduled events per second, 60 seconds,
and 4 in flight. The route example `route20synthetic` is
opt-in and uses a separate 20-vehicle synthetic fleet profile; default seeding
continues to produce the original six-vehicle fleet. Each route trip takes exactly
8 simulated seconds from Lansdowne to Centretown. The manifest names route
`lansdowne-centretown-v1`; its catalog response contains 21 synthetic points defining
20 segments. The fixture geometry is a simple interpolated line, not a road route.
Local initialization must explicitly select the route20 profile, verify its 20
synthetic vehicle IDs and route catalog, and must not reset existing data. Run
creation fails with `profile_not_ready` if those preconditions fail; it fails with
`run_active` when another run is scheduled/running/stopping. There is no reset API.
These limits constrain generated work, not HTTP calls or wall-clock maintenance.

The server stamps `created_at`, `started_at`, `deadline_at`, `issued_at` and terminal
times from its injected UTC clock. The route clock controls only scheduled simulation
events and position interpolation. It never advances database lease time, the 5-second
HTTP timeout, or Fleet observation clocks. All route segment and observation timestamps
remain server-clock UTC values even when event dispatch is accelerated. Position reads return vehicle ID, `observed_at`,
`as_of`, vehicle version, operational state, and current point. On-trip vehicles also
return route ID/version, segment endpoints, and server-stamped segment start/end times;
stationary vehicles do not claim a route segment. Position commands contain no observation time; Fleet stamps
`as_of` on acceptance. A position is fresh when its age at server `as_of` is between
0 and 30 seconds inclusive; older observations are stale, and future observations
are future-dated. UI interpolation uses only the returned segment and time window,
clamps to the endpoints, and stops at the last observed point when stale.

Ride trip commands carry event, run, route, ride, trip, vehicle and reservation IDs,
plus separate `expected_trip_version` and `expected_vehicle_version`. Starting requires
assignment completion and a current reservation matching all those IDs. Ride changes
its trip record with a compare-and-set on the trip version. Fleet's explicit start
command atomically verifies the same reservation and expected vehicle version, marks
the vehicle on-trip, and stores a receipt with a SHA-256 fingerprint of the normalized
command payload in the same transaction. Fleet completion atomically verifies the
active trip and version, marks the trip effect complete, relocates operational
position/state to the destination, releases the reservation, and stores its receipt.
Release preserves the v1 tombstone.

Fleet checks for an existing event ID before checking the current vehicle version.
An identical event/payload returns its original stored receipt even after the vehicle
version advances; reuse with changed payload returns 409. An unseen event with a stale
version or mismatched active trip is rejected before effects. Thus an event from trip
A cannot move or release a vehicle after trip B becomes active. The owner receipt GET
supports reconciliation when an HTTP response is lost.

Ride and Fleet cannot commit atomically. The Controller stores each scheduled
event before dispatch, retries an owner call with the same event ID, and reads both
owners to reconcile an uncertain response before marking the event terminal. Ride
and Fleet expose event-id replay and version/identity compare-and-set semantics.
An event is complete only when both owner effects are confirmed; partial success
remains pending for reconciliation. A controller crash resumes from the durable
event ledger. Schema fixtures exercise these semantics but do not prove DB race
behavior; concurrent acceptance, owner fencing, and crash recovery need service and
database tests.

Stopping a run is idempotent. The first stop changes it to `stopping`, stamps
`stop_requested_at`, sets `drain_deadline_at` to ten server-clock seconds later, and prevents new
requests or events. Work already issued drains under its existing identities; all
owner receipts must be reconciled before closing. Once all events and trips are
finished, a user-stopped run is `stopped`, a normally exhausted run is `completed`,
and an execution failure is `failed`, each with a terminal reason. At the drain
deadline the run becomes `stopped` with `incomplete_trips`, `incomplete_requests`, and
`incomplete_events` recorded. Accepted trips are not evicted or falsely marked completed; their
Ride state and Fleet reservation remain visible for later reconciliation. No stop
operation cancels a committed trip or releases a reservation without the corresponding
owner API call.

Passenger waiting, cancellation, battery and charging, incidents, and detours are
future explicit contract additions. They require new versioned states/events and
owner responsibilities. Passenger waiting stays distinct from runnable assignment
work; cancellation cannot silently erase a reservation, and physical conditions
cannot be inferred from route position alone. No runtime simulator or real vehicle
control is defined here.
