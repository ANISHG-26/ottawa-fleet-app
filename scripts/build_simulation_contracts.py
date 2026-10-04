"""Generate the additive v2 simulation API and semantic fixtures."""
import copy
import json
import re
import sys
from pathlib import Path

OUT = Path(__file__).resolve().parents[1] / "contracts" / "v2"
OUT.mkdir(parents=True, exist_ok=True)

def obj(properties, required=None, **extra):
    return {"type": "object", "additionalProperties": False, "properties": properties,
            "required": list(properties) if required is None else required, **extra}

def enum(*values):
    return {"type": "string", "enum": list(values)}

ID = {"type": "string", "pattern": "^[a-z][a-z0-9-]{2,63}$"}
STAMP = {"type": "string", "format": "date-time", "pattern": "Z$"}
ZONE = enum("centretown", "glebe", "lansdowne", "byward-market")
ASSIGNMENT = enum("queued", "processing", "completed", "failed")
TRIP_STATE = enum("not_started", "in_progress", "completed", "cancelled")
RUN_STATE = enum("scheduled", "running", "stopping", "completed", "stopped", "failed")
OPERATIONAL = enum("available", "reserved", "on_trip", "out_of_service")
POINT = obj({"latitude": {"type": "number", "minimum": 45.0, "maximum": 46.0},
             "longitude": {"type": "number", "minimum": -76.0, "maximum": -75.0}})
POSITION = obj({"vehicle_id": ID, "position": POINT, "observed_at": STAMP, "as_of": STAMP,
                "freshness": enum("fresh", "stale", "future"), "operational_state": OPERATIONAL,
                "vehicle_version": {"type": "integer", "minimum": 1},
                "route_id": {"const": "lansdowne-centretown-v1"}, "route_version": {"const": 1},
                "route_segment": {"type": "integer", "minimum": 0, "maximum": 19},
                "segment_start": POINT, "segment_end": POINT,
                "segment_started_at": STAMP, "segment_ends_at": STAMP},
               required=["vehicle_id", "position", "observed_at", "as_of", "freshness", "operational_state", "vehicle_version"],
               allOf=[{"if": {"properties": {"operational_state": {"const": "on_trip"}}},
                       "then": {"required": ["route_id", "route_version", "route_segment", "segment_start", "segment_end",
                                              "segment_started_at", "segment_ends_at"]}}])
MANIFEST = obj({"scenario_id": enum("route20synthetic"), "fleet_profile": enum("default-six", "route20synthetic"),
                "idempotency_key": {"type": "string", "pattern": "^[A-Za-z0-9_-]{8,128}$"},
                "event_rate_per_second": {"type": "integer", "minimum": 1, "maximum": 4},
                "request_count": {"type": "integer", "minimum": 1, "maximum": 20},
                "execution_event_count": {"type": "integer", "minimum": 2, "maximum": 60},
                "duration_seconds": {"type": "integer", "minimum": 1, "maximum": 60},
                "max_in_flight": {"type": "integer", "minimum": 1, "maximum": 4},
                "route_duration_seconds": {"const": 8}, "route_id": {"const": "lansdowne-centretown-v1"},
                "start_zone": {"const": "lansdowne"}, "end_zone": {"const": "centretown"},
                "seed": {"type": "integer", "minimum": 0, "maximum": 2147483647}},
               required=["scenario_id", "fleet_profile", "idempotency_key", "event_rate_per_second",
                         "request_count", "execution_event_count", "duration_seconds", "max_in_flight", "route_duration_seconds", "route_id",
                         "start_zone", "end_zone", "seed"],
               allOf=[{"if": {"properties": {"request_count": {"const": count}}},
                       "then": {"properties": {"execution_event_count": {"minimum": count * 2}}}}
                      for count in range(1, 21)])
RUN = obj({"run_id": ID, "state": RUN_STATE, "manifest": {"$ref": "#/components/schemas/RunManifest"},
           "created_at": STAMP, "started_at": STAMP, "deadline_at": STAMP,
           "stop_requested_at": STAMP, "drain_deadline_at": STAMP, "completed_at": STAMP,
           "terminal_reason": enum("requested_stop", "deadline", "all_events_complete", "execution_failure"),
           "incomplete_trips": {"type": "integer", "minimum": 0, "maximum": 20},
           "incomplete_requests": {"type": "integer", "minimum": 0, "maximum": 20},
           "incomplete_events": {"type": "integer", "minimum": 0, "maximum": 60},
           "issued_events": {"type": "integer", "minimum": 0, "maximum": 60},
           "completed_events": {"type": "integer", "minimum": 0, "maximum": 60},
           "version": {"type": "integer", "minimum": 1}},
          required=["run_id", "state", "manifest", "created_at", "deadline_at", "drain_deadline_at", "issued_events", "completed_events", "incomplete_trips", "incomplete_requests", "incomplete_events", "version"],
          allOf=[{"if": {"properties": {"state": {"enum": ["completed", "stopped", "failed"]}}},
                  "then": {"required": ["completed_at", "terminal_reason"]}}])
TRIP = obj({"ride_id": ID, "assignment_state": ASSIGNMENT, "trip_state": TRIP_STATE,
            "trip_id": ID, "vehicle_id": ID, "start_zone": ZONE, "destination_zone": ZONE,
            "started_at": STAMP, "completed_at": STAMP, "updated_at": STAMP,
            "version": {"type": "integer", "minimum": 1}},
           required=["ride_id", "assignment_state", "trip_state", "updated_at", "version"],
           allOf=[{"if": {"properties": {"trip_state": {"const": "in_progress"}}},
                   "then": {"required": ["trip_id", "vehicle_id", "started_at"]}},
                  {"if": {"properties": {"trip_state": {"const": "completed"}}},
                   "then": {"required": ["trip_id", "vehicle_id", "started_at", "completed_at"]}},
                  {"if": {"properties": {"trip_state": {"const": "completed"}}},
                   "then": {"properties": {"assignment_state": {"const": "completed"}}}}])
EVENT = obj({"event_id": ID, "run_id": ID, "sequence": {"type": "integer", "minimum": 1, "maximum": 60},
             "kind": enum("trip_start", "position_observation", "trip_complete"), "ride_id": ID,
             "trip_id": ID, "vehicle_id": ID, "expected_trip_version": {"type": "integer", "minimum": 1},
             "expected_vehicle_version": {"type": "integer", "minimum": 1},
             "scheduled_at": STAMP, "issued_at": STAMP, "result": enum("pending", "applied", "replayed", "rejected", "failed")},
            required=["event_id", "run_id", "sequence", "kind", "scheduled_at", "result"])
REQUEST = obj({"manifest": {"$ref": "#/components/schemas/RunManifest"}})
STOP = obj({"drain": {"const": True}})
FLEET_RECEIPT = obj({"event_id": ID, "effect": enum("trip_start", "trip_complete", "position_observation"), "run_id": ID,
                     "ride_id": ID, "trip_id": ID, "vehicle_id": ID, "reservation_id": ID,
                     "payload_fingerprint": {"type": "string", "pattern": "^[a-f0-9]{64}$"},
                     "accepted_vehicle_version": {"type": "integer", "minimum": 1},
                     "stored_at": STAMP, "result": enum("applied", "replayed")})
ROUTE = obj({"route_id": {"const": "lansdowne-centretown-v1"}, "route_version": {"const": 1},
             "start_zone": {"const": "lansdowne"}, "end_zone": {"const": "centretown"},
             "duration_seconds": {"const": 8},
             "points": {"type": "array", "minItems": 21, "maxItems": 21, "items": POINT}})
ERROR = obj({"code": enum("invalid_request", "idempotency_conflict", "not_found", "run_active", "profile_not_ready", "version_conflict", "temporarily_unavailable"),
             "message": {"type": "string", "minLength": 1, "maxLength": 256}, "request_id": ID})
SCHEMAS = {"AssignmentState": ASSIGNMENT, "TripState": TRIP_STATE, "RunState": RUN_STATE,
           "VehicleOperationalState": OPERATIONAL,
           "Position": POSITION, "RunManifest": MANIFEST, "Run": RUN, "Trip": TRIP, "SimulationEvent": EVENT,
           "CreateRunRequest": REQUEST, "StopRunRequest": STOP, "Error": ERROR,
           "FleetEffectReceipt": FLEET_RECEIPT, "Route": ROUTE}

def ref(name):
    return {"$ref": f"#/components/schemas/{name}"}

def op(opid, request=None, response="Run", extra_responses=None):
    responses = {"200": {"description": "Success", "content": {"application/json": {"schema": ref(response)}}},
                 "400": {"description": "Invalid request", "content": {"application/json": {"schema": ref("Error")}}},
                 "409": {"description": "Conflict", "content": {"application/json": {"schema": ref("Error")}}},
                 "503": {"description": "Unavailable", "content": {"application/json": {"schema": ref("Error")}}}}
    if extra_responses:
        responses.update(extra_responses)
    result = {"operationId": opid, "responses": responses}
    if request:
        result["requestBody"] = {"required": True, "content": {"application/json": {"schema": ref(request)}}}
    return result

API = {"openapi": "3.1.0", "info": {"title": "Ottawa Fleet Simulation Contracts", "version": "2.0.0",
       "description": "Additive synthetic simulation APIs. Normative semantics: README.md; v1 remains immutable."},
       "servers": [{"url": "http://localhost:8083"}], "paths": {
           "/v2/simulation/runs": {"post": op("createSimulationRun", "CreateRunRequest"),
                                   "get": op("listSimulationRuns", response="RunPage")},
           "/v2/simulation/runs/{run_id}": {"get": op("getSimulationRun"),
                                             "delete": op("stopSimulationRun", "StopRunRequest")},
           "/v2/simulation/runs/{run_id}/events": {"get": op("listSimulationEvents", response="EventPage")},
           "/v2/rides/{ride_id}/trip": {"get": op("getTrip", response="Trip"),
                                         "post": op("startTrip", "TripCommand", response="Trip")},
           "/v2/rides/{ride_id}/trip/completion": {"post": op("completeTrip", "TripCommand", response="Trip")},
           "/v2/fleet/events/{event_id}": {"get": op("getFleetEffectReceipt", response="FleetEffectReceipt")},
           "/v2/fleet/vehicles/{vehicle_id}/trips/start": {"post": op("startFleetTrip", "FleetStartTripCommand", response="FleetEffectReceipt")},
           "/v2/fleet/vehicles/{vehicle_id}/trips/completion": {"post": op("completeFleetTrip", "FleetCompleteTripCommand", response="FleetEffectReceipt")},
           "/v2/fleet/vehicles/{vehicle_id}/position": {"get": op("getVehiclePosition", response="Position"),
                                                          "put": op("observeVehiclePosition", "PositionCommand", response="Position")},
           "/v2/simulation/routes/{route_id}": {"get": op("getSimulationRoute", response="Route")}},
       "components": {"schemas": SCHEMAS}}
API["components"]["schemas"].update({
    "TripCommand": obj({"event_id": ID, "trip_id": ID, "vehicle_id": ID, "reservation_id": ID,
                         "run_id": ID, "route_id": {"const": "lansdowne-centretown-v1"}, "expected_trip_version": {"type": "integer", "minimum": 1},
                         "expected_vehicle_version": {"type": "integer", "minimum": 1}},
                       required=["event_id", "trip_id", "vehicle_id", "reservation_id", "run_id", "route_id",
                                 "expected_trip_version", "expected_vehicle_version"]),
    "FleetStartTripCommand": obj({"event_id": ID, "run_id": ID, "ride_id": ID, "trip_id": ID,
                                   "vehicle_id": ID, "reservation_id": ID, "route_id": {"const": "lansdowne-centretown-v1"},
                                   "expected_vehicle_version": {"type": "integer", "minimum": 1}},
                                  required=["event_id", "run_id", "ride_id", "trip_id", "vehicle_id", "reservation_id", "route_id", "expected_vehicle_version"]),
    "FleetCompleteTripCommand": obj({"event_id": ID, "run_id": ID, "ride_id": ID, "trip_id": ID,
                                      "vehicle_id": ID, "reservation_id": ID, "route_id": {"const": "lansdowne-centretown-v1"},
                                      "expected_vehicle_version": {"type": "integer", "minimum": 1},
                                      "destination_zone": ZONE, "destination_position": POINT},
                                     required=["event_id", "run_id", "ride_id", "trip_id", "vehicle_id", "reservation_id", "route_id",
                                               "expected_vehicle_version", "destination_zone", "destination_position"]),
    "PositionCommand": obj({"event_id": ID, "run_id": ID, "trip_id": ID, "route_id": {"const": "lansdowne-centretown-v1"},
                             "expected_vehicle_version": {"type": "integer", "minimum": 1},
                             "position": POINT, "route_segment": {"type": "integer", "minimum": 0, "maximum": 19}},
                            required=["event_id", "run_id", "trip_id", "route_id", "expected_vehicle_version", "position", "route_segment"]),
    "RunPage": obj({"items": {"type": "array", "maxItems": 100, "items": ref("Run")},
                    "next_cursor": {"type": ["string", "null"], "maxLength": 256}}),
    "EventPage": obj({"items": {"type": "array", "maxItems": 100, "items": ref("SimulationEvent")},
                      "next_cursor": {"type": ["string", "null"], "maxLength": 256}})})
for path, methods in API["paths"].items():
    names = re.findall(r"\{([^}]+)\}", path)
    owner_port = "8080" if path.startswith("/v2/fleet/") else "8081" if path.startswith("/v2/rides/") else "8083"
    for method in methods.values():
        method["parameters"] = [{"name": name, "in": "path", "required": True, "schema": ID} for name in names]
        method["servers"] = [{"url": f"http://localhost:{owner_port}"}]

def write(name, value):
    rendered = json.dumps(value, indent=2) + "\n"
    path = OUT / name
    if "--check" in sys.argv:
        if not path.exists() or path.read_text(encoding="utf-8") != rendered:
            raise SystemExit(f"Generated contract drift: {name}; run python scripts/build_simulation_contracts.py")
    else:
        path.write_text(rendered, encoding="utf-8")

T = "2026-10-03T12:00:00Z"
manifest = {"scenario_id": "route20synthetic", "fleet_profile": "route20synthetic", "idempotency_key": "run_demo_001",
            "event_rate_per_second": 2, "request_count": 20, "execution_event_count": 60,
            "duration_seconds": 20, "max_in_flight": 2, "route_duration_seconds": 8,
            "route_id": "lansdowne-centretown-v1", "start_zone": "lansdowne", "end_zone": "centretown", "seed": 7}
fixtures = [
    {"name": "valid bounded route manifest", "kind": "manifest", "expected": "accept", "manifest": copy.deepcopy(manifest)},
    {"name": "manifest exceeds finite request bound", "kind": "manifest", "expected": "reject", "manifest": {**manifest, "request_count": 21}},
    {"name": "manifest exceeds execution event bound", "kind": "manifest", "expected": "reject", "manifest": {**manifest, "execution_event_count": 61}},
    {"name": "same run replay", "kind": "run_replay", "expected": "replay", "first": manifest, "replay": copy.deepcopy(manifest)},
    {"name": "conflicting run replay", "kind": "run_replay", "expected": "conflict", "first": manifest, "replay": {**manifest, "seed": 8}},
    {"name": "duplicate event after vehicle version advances", "kind": "fleet_effect", "expected": "replay", "event_id": "event-001", "stored_payload": {"trip_id": "trip-001", "vehicle_id": "vehicle-001", "expected_vehicle_version": 2}, "replay_payload": {"trip_id": "trip-001", "vehicle_id": "vehicle-001", "expected_vehicle_version": 2}, "stored_receipt": {"event_id": "event-001", "accepted_vehicle_version": 3}, "current_vehicle_version": 4, "active_trip_id": "trip-002"},
    {"name": "conflicting duplicate event payload", "kind": "fleet_effect", "expected": "conflict", "event_id": "event-001", "stored_payload": {"trip_id": "trip-001", "vehicle_id": "vehicle-001", "expected_vehicle_version": 2}, "replay_payload": {"trip_id": "trip-001", "vehicle_id": "vehicle-002", "expected_vehicle_version": 4}, "stored_receipt": {"event_id": "event-001", "accepted_vehicle_version": 3}, "current_vehicle_version": 4, "active_trip_id": "trip-002"},
    {"name": "late unseen event fenced after later trip", "kind": "fleet_effect", "expected": "reject", "event_id": "event-old-001", "stored_payload": None, "replay_payload": {"trip_id": "trip-001", "vehicle_id": "vehicle-001", "expected_vehicle_version": 2}, "stored_receipt": None, "current_vehicle_version": 4, "active_trip_id": "trip-002"},
    {"name": "fresh position boundary", "kind": "freshness", "expected": "fresh", "as_of": T, "observed_at": "2026-10-03T11:59:30Z"},
    {"name": "stale position", "kind": "freshness", "expected": "stale", "as_of": T, "observed_at": "2026-10-03T11:59:29Z"},
    {"name": "future position", "kind": "freshness", "expected": "future", "as_of": T, "observed_at": "2026-10-03T12:00:01Z"},
    {"name": "stop drains issued events", "kind": "stop", "expected": "draining", "state": "running", "issued": 3, "completed": 1},
    {"name": "stop rejects new events", "kind": "stop", "expected": "closed", "state": "stopping", "issued": 3, "completed": 3},
    {"name": "drain deadline records unfinished trips", "kind": "stop", "expected": "stopped_unfinished", "state": "stopping", "issued": 6, "completed": 5, "drain_deadline_expired": True, "terminal_state": "stopped", "incomplete_trips": 1, "incomplete_requests": 1, "incomplete_events": 1},
]
for case in fixtures:
    if case["kind"] == "fleet_effect" and case["stored_payload"] is not None:
        case["stored_receipt"] = {"event_id": case["event_id"], "effect": "trip_start", "run_id": "run-demo-001",
                                  "ride_id": "ride-001", "trip_id": case["stored_payload"]["trip_id"],
                                  "vehicle_id": case["stored_payload"]["vehicle_id"], "reservation_id": "reservation-001",
                                  "payload_fingerprint": "a" * 64, "accepted_vehicle_version": 3,
                                  "stored_at": T, "result": "applied"}
        case["returned_receipt"] = copy.deepcopy(case["stored_receipt"]) if case["expected"] == "replay" else None
    else:
        case["stored_receipt"] = None
        case["returned_receipt"] = None
write("openapi.json", API)
write("semantic-fixtures.json", fixtures)

def sample(schema, root=None):
    root = SCHEMAS | API["components"]["schemas"] if root is None else root
    if "$ref" in schema:
        return sample(root[schema["$ref"].rsplit("/", 1)[-1]], root)
    if "const" in schema:
        return schema["const"]
    if "enum" in schema:
        return schema["enum"][0]
    typ = schema.get("type")
    if isinstance(typ, list):
        typ = next(t for t in typ if t != "null")
    if typ == "object":
        return {key: sample(schema["properties"][key], root) for key in schema.get("required", [])}
    if typ == "array":
        return [sample(schema["items"], root) for _ in range(schema.get("minItems", 0))]
    if typ == "string":
        if schema.get("format") == "date-time": return "2026-10-03T12:00:00Z"
        if "[a-f0-9]{64}" in schema.get("pattern", ""): return "a" * 64
        if schema.get("pattern", "").startswith("^run_"): return "run_demo_001"
        return "vehicle-001"
    if typ == "integer": return max(1, schema.get("minimum", 0))
    if typ == "number": return schema.get("minimum", 0)
    if typ == "boolean": return True
    raise ValueError(f"Cannot make schema sample: {schema}")

schema_fixtures = []
for name, schema in API["components"]["schemas"].items():
    positive = sample(schema)
    negative = copy.deepcopy(positive)
    if isinstance(negative, dict):
        negative["unexpected_fixture_field"] = True
    elif isinstance(negative, list):
        negative.append(None)
    elif isinstance(negative, bool):
        negative = "not-a-boolean"
    elif isinstance(negative, int):
        negative = -1
    elif isinstance(negative, (float, str)):
        negative = "invalid-fixture-value"
    schema_fixtures.extend([
        {"name": f"{name} representative valid", "schema": name, "valid": True, "value": positive},
        {"name": f"{name} rejects invalid fixture", "schema": name, "valid": False, "value": negative},
    ])
stationary = {"vehicle_id": "vehicle-001", "position": {"latitude": 45.399, "longitude": -75.684},
              "observed_at": T, "as_of": T, "freshness": "fresh", "operational_state": "available", "vehicle_version": 1}
moving = {**stationary, "operational_state": "on_trip", "route_id": "lansdowne-centretown-v1", "route_version": 1,
          "route_segment": 0, "segment_start": {"latitude": 45.399, "longitude": -75.684},
          "segment_end": {"latitude": 45.40005, "longitude": -75.68445},
          "segment_started_at": T, "segment_ends_at": "2026-10-03T12:00:00.400Z"}
schema_fixtures.extend([
    {"name": "stationary vehicle has no route segment", "schema": "Position", "valid": True, "value": stationary},
    {"name": "moving vehicle has timed segment", "schema": "Position", "valid": True, "value": moving},
    {"name": "moving vehicle missing segment is invalid", "schema": "Position", "valid": False,
     "value": {k: v for k, v in moving.items() if k not in ("route_segment", "segment_start", "segment_end", "segment_started_at", "segment_ends_at", "route_id", "route_version")}},
])
write("schema-fixtures.json", schema_fixtures)
points = [{"latitude": round(45.399 + (45.420 - 45.399) * i / 20, 6),
           "longitude": round(-75.684 + (-75.693 + 75.684) * i / 20, 6)} for i in range(21)]
write("routes.json", [{"route_id": "lansdowne-centretown-v1", "route_version": 1,
                       "start_zone": "lansdowne", "end_zone": "centretown",
                       "duration_seconds": 8, "points": points}])
