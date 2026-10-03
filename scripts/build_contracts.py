"""Emit the reviewed v1 contract and synthetic examples deterministically.

Run only when editing the contract; generated JSON is the consumer interface.
"""
import copy
import json
import sys
from pathlib import Path

OUT = Path(__file__).resolve().parents[1] / "contracts" / "v1"
OUT.mkdir(parents=True, exist_ok=True)


def obj(properties, required=None, **extra):
    return {"type": "object", "additionalProperties": False, "properties": properties,
            "required": list(properties) if required is None else required, **extra}


def enum(*values):
    return {"type": "string", "enum": list(values)}


ID = {"type": "string", "pattern": "^[a-z][a-z0-9-]{2,63}$"}
STAMP = {"type": "string", "format": "date-time", "pattern": "Z$"}
ZONE = enum("centretown", "glebe", "lansdowne", "byward-market")
COUNT = {"type": "integer", "minimum": 1, "maximum": 4}
REQUEST = obj({"pickup_zone": ZONE, "dropoff_zone": ZONE, "passengers": COUNT})
VEHICLE = obj({"vehicle_id": ID, "zone": ZONE, "seats": COUNT,
               "availability": enum("available", "reserved", "unavailable", "unknown"),
               "observed_at": STAMP})
RESERVATION = obj({"ride_id": ID, "vehicle_id": ID, "state": enum("reserved", "released"),
                   "reserved_at": STAMP, "updated_at": STAMP},
                  required=["ride_id", "state", "updated_at"],
                  allOf=[{"if": {"properties": {"state": {"const": "reserved"}}},
                          "then": {"required": ["vehicle_id", "reserved_at"]}}])
ERROR = obj({"code": enum("invalid_request", "idempotency_conflict", "not_found", "no_capacity",
                         "reservation_released", "temporarily_unavailable", "internal_error"),
             "message": {"type": "string", "minLength": 1, "maxLength": 256}, "request_id": ID})
RIDE = obj({"ride_id": ID, **REQUEST["properties"],
            "state": enum("queued", "processing", "completed", "failed"),
            "created_at": STAMP, "updated_at": STAMP,
            "vehicle_id": ID, "failure_code": enum("no_capacity", "retry_exhausted", "invalid_job")},
           required=["ride_id", *REQUEST["properties"], "state", "created_at", "updated_at"],
           allOf=[
               {"if": {"properties": {"state": {"const": "completed"}}},
                "then": {"required": ["vehicle_id"], "not": {"required": ["failure_code"]}}},
               {"if": {"properties": {"state": {"const": "failed"}}},
                "then": {"required": ["failure_code"], "not": {"required": ["vehicle_id"]}}},
               {"if": {"properties": {"state": {"enum": ["queued", "processing"]}}},
                "then": {"not": {"anyOf": [{"required": ["vehicle_id"]}, {"required": ["failure_code"]}]}}}])


def page(item):
    return obj({"items": {"type": "array", "maxItems": 100, "items": item},
                "next_cursor": {"type": ["string", "null"], "minLength": 1, "maxLength": 256},
                "as_of": STAMP})


SCHEMAS = {"RideRequest": REQUEST, "Vehicle": VEHICLE, "Reservation": RESERVATION,
           "Error": ERROR, "Ride": RIDE, "FleetPage": page(VEHICLE), "RidePage": page(RIDE),
           "ReserveRequest": obj({"ride_id": ID, "pickup_zone": ZONE, "passengers": COUNT}),
           "Probe": obj({"status": enum("ok")})}

JOB = obj({"schema_version": {"const": "1.0.0"}, "job_id": ID, "ride_id": ID,
           "request_id": ID, "kind": {"const": "assign_ride"}, "payload": REQUEST,
           "state": enum("queued", "processing", "completed", "failed"),
           "attempts": {"type": "integer", "minimum": 0, "maximum": 5},
           "max_attempts": {"const": 5}, "created_at": STAMP, "updated_at": STAMP,
           "available_at": STAMP, "lease_owner": ID, "lease_token": ID,
           "lease_expires_at": STAMP, "vehicle_id": ID,
           "failure_code": enum("no_capacity", "retry_exhausted", "invalid_job")},
          required=["schema_version", "job_id", "ride_id", "request_id", "kind", "payload", "state",
                    "attempts", "max_attempts", "created_at", "updated_at", "available_at"],
          allOf=[
              {"if": {"properties": {"state": {"const": "processing"}}},
               "then": {"required": ["lease_owner", "lease_token", "lease_expires_at"],
                        "properties": {"attempts": {"minimum": 1}}},
               "else": {"not": {"anyOf": [{"required": [k]} for k in
                                          ["lease_owner", "lease_token", "lease_expires_at"]]}}},
              {"if": {"properties": {"state": {"const": "queued"}}},
               "then": {"properties": {"attempts": {"maximum": 4}}}},
              *RIDE["allOf"]])
JOB = {"$schema": "https://json-schema.org/draft/2020-12/schema",
       "$id": "https://anishg-26.github.io/ottawa-fleet-app/contracts/v1/job.schema.json",
       "title": "Assignment job v1 (logical record, not DDL)", **JOB}

T = "2026-10-03T12:00:00Z"
request = {"pickup_zone": "lansdowne", "dropoff_zone": "centretown", "passengers": 2}
ride = {"ride_id": "ride-001", **request, "state": "queued", "created_at": T, "updated_at": T}
vehicle = {"vehicle_id": "vehicle-001", "zone": "lansdowne", "seats": 4,
           "availability": "available", "observed_at": T}
reservation = {"ride_id": "ride-001", "vehicle_id": "vehicle-001", "state": "reserved",
               "reserved_at": T, "updated_at": T}
error = {"code": "invalid_request", "message": "passengers must be between 1 and 4", "request_id": "req-001"}
VALUES = {"RideRequest": request, "Vehicle": vehicle, "Reservation": reservation,
          "Error": error, "Ride": ride, "FleetPage": {"items": [vehicle], "next_cursor": None, "as_of": T},
          "RidePage": {"items": [ride], "next_cursor": None, "as_of": T},
          "ReserveRequest": {"ride_id": "ride-001", "pickup_zone": "lansdowne", "passengers": 2},
          "Probe": {"status": "ok"},
          "Job": {"schema_version": "1.0.0", "job_id": "job-001", "ride_id": "ride-001",
                  "request_id": "req-001", "kind": "assign_ride", "payload": request,
                  "state": "queued", "attempts": 0, "max_attempts": 5,
                  "created_at": T, "updated_at": T, "available_at": T}}

cases = []


def case(name, schema, value, valid=True):
    cases.append({"name": name, "schema": schema, "valid": valid, "value": copy.deepcopy(value)})


for name, value in VALUES.items():
    case(f"{name}: valid", name, value)
    case(f"{name}: rejects unknown field", name, {**value, "unexpected": True}, False)
for state in ["processing", "completed", "failed"]:
    r = {**ride, "state": state}
    j = {**VALUES["Job"], "state": state, "attempts": 1}
    if state == "processing":
        j.update(lease_owner="worker-001", lease_token="lease-001", lease_expires_at="2026-10-03T12:00:30Z")
    if state == "completed":
        r["vehicle_id"] = j["vehicle_id"] = "vehicle-001"
    if state == "failed":
        r["failure_code"] = j["failure_code"] = "no_capacity"
    case(f"ride {state}", "Ride", r)
    case(f"job {state}", "Job", j)
case("passenger ceiling", "RideRequest", {**request, "passengers": 5}, False)
case("unknown zone", "RideRequest", {**request, "pickup_zone": "toronto"}, False)
case("completed ride lacks assignment", "Ride", {**ride, "state": "completed"}, False)
case("failed ride lacks reason", "Ride", {**ride, "state": "failed"}, False)
case("processing job lacks lease", "Job", {**VALUES["Job"], "state": "processing", "attempts": 1}, False)
case("retry ceiling", "Job", {**VALUES["Job"], "attempts": 6}, False)
case("exhausted job cannot queue", "Job", {**VALUES["Job"], "attempts": 5}, False)
case("queued job cannot retain lease", "Job", {**VALUES["Job"], "lease_token": "lease-001"}, False)
case("timestamp requires timezone", "Vehicle", {**vehicle, "observed_at": "2026-10-03T12:00:00"}, False)
case("invalid calendar date", "Vehicle", {**vehicle, "observed_at": "2026-02-30T12:00:00Z"}, False)
case("stale vehicle is unknown", "Vehicle", {**vehicle, "availability": "unknown", "observed_at": "2026-10-03T11:59:29Z"})
case("completed job cannot also fail", "Job", {**VALUES["Job"], "state": "completed", "attempts": 1,
                                                "vehicle_id": "vehicle-001", "failure_code": "no_capacity"}, False)
case("released reservation", "Reservation", {**reservation, "state": "released"})
case("release before reservation creates tombstone", "Reservation", {"ride_id": "ride-001", "state": "released", "updated_at": T})
case("reserved requires vehicle", "Reservation", {"ride_id": "ride-001", "state": "reserved", "updated_at": T}, False)

headers = {"X-Request-ID": {"description": "Echo valid caller ID or generated ID; see contract rules.",
                             "schema": ID}}


def response(schema, status, description=None):
    return {"description": description or status, "headers": copy.deepcopy(headers),
            "content": {"application/json": {"schema": {"$ref": f"#/components/schemas/{schema}"},
                                              "example": VALUES[schema]}}}


request_id = {"name": "X-Request-ID", "in": "header", "required": False, "schema": ID}
cursor = {"name": "cursor", "in": "query", "schema": {"type": "string", "minLength": 1, "maxLength": 256}}
limit = {"name": "limit", "in": "query", "schema": {"type": "integer", "minimum": 1, "maximum": 100, "default": 20}}
key = {"name": "Idempotency-Key", "in": "header", "required": True,
       "schema": {"type": "string", "pattern": "^[A-Za-z0-9_-]{8,128}$"}}


def operation(opid, schema, success="200", body=None, params=None, errors=("400", "503", "500")):
    result = {"operationId": opid, "parameters": [request_id, *(params or [])],
              "responses": {success: response(schema, success),
                            **{code: response("Error", code) for code in errors}}}
    if body:
        result["requestBody"] = {"required": True, "content": {"application/json": {
            "schema": {"$ref": f"#/components/schemas/{body}"}, "example": VALUES[body]}}}
        result["responses"]["413"] = response("Error", "Payload exceeds 4096 bytes")
        result["responses"]["415"] = response("Error", "Content-Type must be application/json")
    for code, reply in result["responses"].items():
        if int(code) >= 400:
            error_code = {"404": "not_found", "409": "idempotency_conflict",
                          "503": "temporarily_unavailable", "500": "internal_error"}.get(code, "invalid_request")
            reply["content"]["application/json"]["example"] = {
                "code": error_code, "message": f"Example {error_code} response", "request_id": "req-001"}
    return result


paths = {
    "/v1/fleet": {"get": operation("listFleet", "FleetPage", params=[cursor, limit])},
    "/v1/fleet/{vehicle_id}": {"get": operation("getVehicle", "Vehicle", params=[
        {"name": "vehicle_id", "in": "path", "required": True, "schema": ID}], errors=("400", "404", "503", "500"))},
    "/v1/reservations": {"post": operation("reserveVehicle", "Reservation", "201", "ReserveRequest", errors=("400", "409", "503", "500"))},
    "/v1/reservations/{ride_id}": {
        "get": operation("getReservation", "Reservation", params=[{"name": "ride_id", "in": "path", "required": True, "schema": ID}], errors=("400", "404", "503", "500")),
        "delete": operation("releaseReservation", "Reservation", params=[{"name": "ride_id", "in": "path", "required": True, "schema": ID}], errors=("400", "503", "500"))},
    "/v1/rides": {"post": operation("submitRide", "Ride", "202", "RideRequest", [key], errors=("400", "409", "503", "500")),
                  "get": operation("listRides", "RidePage", params=[cursor, limit])},
    "/v1/rides/{ride_id}": {"get": operation("getRide", "Ride", params=[{"name": "ride_id", "in": "path", "required": True, "schema": ID}], errors=("400", "404", "503", "500"))},
    "/healthz": {"get": operation("liveness", "Probe", errors=("503",))},
    "/readyz": {"get": operation("readiness", "Probe", errors=("503",))}}
paths["/v1/reservations"]["post"]["responses"]["200"] = response("Reservation", "Replay existing reservation")
conflict_media = paths["/v1/reservations"]["post"]["responses"]["409"]["content"]["application/json"]
del conflict_media["example"]
conflict_media["examples"] = {code: {"value": {"code": code, "message": message, "request_id": "req-001"}}
                             for code, message in [
                                 ("idempotency_conflict", "Ride reservation parameters differ"),
                                 ("no_capacity", "No fresh eligible vehicle in pickup zone"),
                                 ("reservation_released", "Ride reservation is permanently released")]}
paths["/v1/fleet/{vehicle_id}"]["get"]["responses"]["200"]["headers"]["X-As-Of"] = {
    "description": "UTC clock used to derive effective freshness.", "schema": STAMP, "example": T}
paths["/v1/rides"]["post"]["responses"]["202"]["headers"]["Location"] = {
    "description": "Relative status URL for the durably accepted ride, including replay.",
    "schema": {"type": "string", "pattern": "^/v1/rides/[a-z][a-z0-9-]{2,63}$"},
    "example": "/v1/rides/ride-001"}
for path, methods in paths.items():
    service = "Ride API" if "rides" in path else "Fleet API"
    for method in methods.values():
        method["tags"] = [service] if path not in ("/healthz", "/readyz") else ["Probes"]
        if path not in ("/healthz", "/readyz"):
            method["servers"] = [{"url": "http://localhost:8081" if service == "Ride API" else "http://localhost:8080"}]
api = {"openapi": "3.1.0", "info": {"title": "Ottawa mock fleet contracts", "version": "1.0.0",
        "description": "Synthetic local APIs. Normative semantics: ../README.md. No services exist in this contract ticket."},
       "servers": [{"url": "http://localhost:8080"}], "paths": paths,
       "components": {"schemas": SCHEMAS}}
fleet = []
for i, zone in enumerate(["lansdowne", "lansdowne", "centretown", "centretown", "glebe", "byward-market"], 1):
    fleet.append({**vehicle, "vehicle_id": f"vehicle-{i:03d}", "zone": zone,
                  "availability": "unknown" if i == 6 else "available",
                  "observed_at": "2026-10-03T11:59:29Z" if i == 6 else T})
case("UI initial fleet", "FleetPage", {"items": fleet, "next_cursor": None, "as_of": T})
freshness = [{"name": name, "as_of": T, "observed_at": observed, "stored": "available", "effective": effective}
             for name, observed, effective in [
                 ("fresh", T, "available"),
                 ("boundary included", "2026-10-03T11:59:30Z", "available"),
                 ("stale", "2026-10-03T11:59:29Z", "unknown"),
                 ("future", "2026-10-03T12:00:01Z", "unknown")]]
for name, value in [("openapi.json", api), ("job.schema.json", JOB), ("fixtures.json", cases),
                    ("freshness.json", freshness)]:
    rendered = json.dumps(value, indent=2) + "\n"
    if "--check" in sys.argv:
        if (OUT / name).read_text(encoding="utf-8") != rendered:
            raise SystemExit(f"Generated contract drift: {name}; run python scripts/build_contracts.py")
    else:
        (OUT / name).write_text(rendered, encoding="utf-8")
