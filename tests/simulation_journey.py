"""Live public-API smoke for the opt-in route20synthetic Compose project."""
import json
import time
import urllib.error
import urllib.request
import uuid

FLEET = "http://127.0.0.1:18080"
RIDE = "http://127.0.0.1:18081"
SIM = "http://127.0.0.1:18083"
ROUTE = "lansdowne-centretown-v1"


def call(base, path, method="GET", body=None, timeout=5, extra_headers=None):
    data = None if body is None else json.dumps(body).encode()
    headers = {"Accept": "application/json"}
    headers.update(extra_headers or {})
    if data is not None:
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(base + path, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as response:
            return response.status, json.load(response)
    except urllib.error.HTTPError as error:
        try:
            return error.code, json.load(error)
        finally:
            error.close()


def expect(base, path, status=200, method="GET", body=None):
    code, value = call(base, path, method, body)
    assert code == status, f"{method} {path}: HTTP {code}: {value}"
    return value


def wait_for(fetch, predicate, label, seconds=60):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        value = fetch()
        if predicate(value):
            return value
        if value.get("state") == "failed":
            raise AssertionError(f"{label} failed: {value}")
        time.sleep(0.25)
    raise TimeoutError(f"{label} did not reach expected state in {seconds}s")


def manifest(key, duration=30):
    return {"scenario_id": "route20synthetic", "fleet_profile": "route20synthetic",
            "idempotency_key": key, "event_rate_per_second": 4, "request_count": 1,
            "execution_event_count": 2, "duration_seconds": duration, "max_in_flight": 1,
            "route_duration_seconds": 8, "route_id": ROUTE, "start_zone": "lansdowne",
            "end_zone": "centretown", "seed": 26}


def create_run(value):
    return expect(SIM, "/v2/simulation/runs", 200, "POST", {"manifest": value})


def main():
    route = expect(SIM, "/v2/simulation/routes/" + ROUTE)
    assert len(route["points"]) == 21, f"route has {len(route.get('points', []))} points"
    vehicles, cursor = [], None
    while True:
        suffix = "?limit=100" + ("&cursor=" + cursor if cursor else "")
        page = expect(FLEET, "/v1/fleet" + suffix)
        vehicles.extend(page["items"])
        cursor = page.get("next_cursor")
        if not cursor:
            break
    ids = {v["vehicle_id"] for v in vehicles}
    assert ids == {f"vehicle-{n:03}" for n in range(1, 21)}, f"unexpected seeded fleet: {sorted(ids)}"

    key = "ci-" + uuid.uuid4().hex
    original = manifest(key)
    run = create_run(original)
    run_id = run["run_id"]
    assert create_run(original)["run_id"] == run_id, "exact manifest replay changed run"
    changed = dict(original, seed=original["seed"] + 1)
    code, error = call(SIM, "/v2/simulation/runs", "POST", {"manifest": changed})
    assert code == 409 and error.get("code") == "idempotency_conflict", f"changed replay was not fenced: {error}"
    code, error = call(SIM, "/v2/simulation/runs", "POST", {"manifest": manifest("ci-" + uuid.uuid4().hex)})
    assert code == 409 and error.get("code") == "run_active", f"second active run was accepted: {error}"

    wait_for(lambda: expect(SIM, "/v2/simulation/runs/" + run_id),
             lambda r: r["state"] == "running" and r["issued_events"] >= 1,
             "first trip start")
    events = expect(SIM, "/v2/simulation/runs/" + run_id + "/events?limit=20")["items"]
    start = next(e for e in events if e["kind"] == "trip_start")
    vehicle_id = start["vehicle_id"]
    ride_id = start["ride_id"]
    assert vehicle_id == "vehicle-001", f"first synthetic trip did not use vehicle-001: {vehicle_id}"
    positions = set()
    deadline = time.monotonic() + 60
    position = None
    trip = None
    while time.monotonic() < deadline:
        position = expect(FLEET, f"/v2/fleet/vehicles/{vehicle_id}/position")
        trip = expect(RIDE, f"/v2/rides/{ride_id}/trip")
        if trip.get("trip_state") == "in_progress" and position.get("operational_state") == "on_trip":
            point = position["position"]
            positions.add((round(point["latitude"], 6), round(point["longitude"], 6)))
            if position.get("route_segment", -1) >= 2 and len(positions) >= 2:
                break
        time.sleep(0.25)
    else:
        raise TimeoutError(f"did not observe moving in-progress trip at two positions: {sorted(positions)}")
    done = wait_for(lambda: expect(SIM, "/v2/simulation/runs/" + run_id),
                    lambda r: r["state"] == "completed", "normal run completion")
    trip = expect(RIDE, f"/v2/rides/{ride_id}/trip")
    events = expect(SIM, "/v2/simulation/runs/" + run_id + "/events?limit=20")["items"]
    assert len(events) == 2 and {e["kind"] for e in events} == {"trip_start", "trip_complete"} and all(e["result"] == "applied" for e in events), events
    assert done["issued_events"] == done["completed_events"] == 2 and done["incomplete_trips"] == 0
    assert trip.get("trip_state") == "completed" and trip.get("trip_id") == start["trip_id"]
    fleet_vehicle = expect(FLEET, "/v1/fleet/" + vehicle_id)
    arrived = expect(FLEET, f"/v2/fleet/vehicles/{vehicle_id}/position")
    assert fleet_vehicle["availability"] == "available" and fleet_vehicle["zone"] == "centretown", fleet_vehicle
    assert arrived["operational_state"] == "available" and arrived["position"] == route["points"][-1]
    assert len(vehicles) == 20 and {v["vehicle_id"] for v in expect(FLEET, "/v1/fleet?limit=100")["items"]} == ids

    # Create a new v1 Ride through Ride API, then release its synthetic reservation.
    ride_key = "ci-" + uuid.uuid4().hex
    code, accepted = call(RIDE, "/v1/rides", "POST",
                          {"pickup_zone": "centretown", "dropoff_zone": "lansdowne", "passengers": 1},
                          extra_headers={"Idempotency-Key": ride_key})
    assert code in (200, 201, 202), f"Centretown v1 ride returned HTTP {code}: {accepted}"
    followup_id = accepted.get("ride_id")
    assert followup_id, f"v1 Ride response has no ride_id: {accepted}"
    assignment = wait_for(lambda: expect(RIDE, "/v1/rides/" + followup_id),
                          lambda r: r.get("state") == "completed", "Centretown v1 assignment")
    assert assignment.get("vehicle_id") == vehicle_id, f"arrived vehicle not reused: {assignment}"
    released = expect(FLEET, "/v1/reservations/" + followup_id, 200, "DELETE")
    assert released["state"] == "released", released

    drain_run = create_run(manifest("ci-" + uuid.uuid4().hex, 60))
    drain_id = drain_run["run_id"]
    drain_deadline = time.monotonic() + 60
    active_ride = None
    while time.monotonic() < drain_deadline:
        state = expect(SIM, "/v2/simulation/runs/" + drain_id)
        if state["state"] == "running" and state["issued_events"] >= 1:
            drain_events = expect(SIM, "/v2/simulation/runs/" + drain_id + "/events?limit=20")["items"]
            active_event = next((e for e in drain_events if e["kind"] == "trip_start" and e.get("ride_id")), None)
            if active_event:
                active_trip = expect(RIDE, f"/v2/rides/{active_event['ride_id']}/trip")
                if active_trip.get("trip_state") == "in_progress":
                    active_ride = active_event["ride_id"]
                    break
        time.sleep(0.25)
    assert active_ride, "stop test did not observe an accepted in-progress trip"
    requested = expect(SIM, "/v2/simulation/runs/" + drain_id, 200, "DELETE", {"drain": True})
    assert requested["state"] in ("stopping", "stopped")
    drained = wait_for(lambda: expect(SIM, "/v2/simulation/runs/" + drain_id),
                       lambda r: r["state"] == "stopped", "stop and drain")
    drain_events = expect(SIM, "/v2/simulation/runs/" + drain_id + "/events?limit=20")["items"]
    assert drained["issued_events"] == drained["completed_events"] == 2 and len(drain_events) == 2
    assert {e["kind"] for e in drain_events} == {"trip_start", "trip_complete"}
    assert drained["incomplete_trips"] == 0
    assert drained["incomplete_requests"] == 0 and drained["incomplete_events"] == 0
    assert drained["terminal_reason"] == "requested_stop"
    assert all(event["result"] == "applied" for event in drain_events)
    final_vehicles = expect(FLEET, "/v1/fleet?limit=100")["items"]
    assert {v["vehicle_id"] for v in final_vehicles} == ids, f"physical fleet changed: {len(final_vehicles)}"
    print(json.dumps({"vehicles": len(vehicles), "route_points": len(route["points"]),
                      "normal_run_events": len(events), "normal_state": done["state"],
                      "moving_segment": position["route_segment"], "moving_positions": len(positions),
                      "reused_vehicle": assignment["vehicle_id"], "fleet_after": len(final_vehicles),
                      "drain_state": drained["state"], "drain_incomplete_trips": drained["incomplete_trips"]},
                     sort_keys=True))


if __name__ == "__main__":
    main()
