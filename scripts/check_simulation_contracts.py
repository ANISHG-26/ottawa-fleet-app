"""Validate v2 schemas and execute the reference contract evaluator (not runtime proof)."""
import json
import sys
import copy
from datetime import datetime
from pathlib import Path

from jsonschema import Draft202012Validator, FormatChecker
from openapi_spec_validator import validate

ROOT = Path(__file__).resolve().parents[1] / "contracts" / "v2"


def outcome(case):
    kind = case["kind"]
    if kind == "manifest":
        m = case["manifest"]
        valid = (1 <= m["event_rate_per_second"] <= 4 and 1 <= m["request_count"] <= 20
                 and 1 <= m["execution_event_count"] <= 60
                 and 1 <= m["duration_seconds"] <= 60 and 1 <= m["max_in_flight"] <= 4
                 and m["route_duration_seconds"] == 8 and m["route_id"] == "lansdowne-centretown-v1" and m["start_zone"] == "lansdowne"
                 and m["end_zone"] == "centretown" and m["fleet_profile"] == m["scenario_id"])
        return "accept" if valid else "reject"
    if kind == "run_replay":
        return "replay" if case["first"] == case["replay"] else "conflict"
    if kind == "fleet_effect":
        if case["stored_payload"] is not None:
            if case["stored_receipt"]["event_id"] != case["event_id"]:
                return "reject"
            if case["stored_payload"] == case["replay_payload"]:
                assert case["returned_receipt"] == case["stored_receipt"], case["name"]
                return "replay"
            return "conflict"
        payload = case["replay_payload"]
        return "applied" if (payload["trip_id"] == case["active_trip_id"]
                             and payload["expected_vehicle_version"] == case["current_vehicle_version"]) else "reject"
    if kind == "freshness":
        age = (datetime.fromisoformat(case["as_of"].replace("Z", "+00:00"))
               - datetime.fromisoformat(case["observed_at"].replace("Z", "+00:00"))).total_seconds()
        return "fresh" if 0 <= age <= 30 else "stale" if age > 30 else "future"
    if kind == "stop":
        if case.get("drain_deadline_expired"):
            assert case["terminal_state"] == "stopped"
            return "stopped_unfinished" if any(case[name] > 0 for name in
                                                ("incomplete_trips", "incomplete_requests", "incomplete_events")) else "stopped"
        if case["state"] == "stopping" and case["issued"] == case["completed"]:
            return "closed"
        return "draining"
    raise AssertionError(f"Unknown semantic fixture kind: {kind}")


def main():
    if "--check-generated" in sys.argv:
        import subprocess
        subprocess.run([sys.executable, str(Path(__file__).with_name("build_simulation_contracts.py")), "--check"], check=True)
    api = json.loads((ROOT / "openapi.json").read_text(encoding="utf-8"))
    validate(api)
    schemas = api["components"]["schemas"]
    for schema in schemas.values():
        Draft202012Validator.check_schema(schema)
    fixtures = json.loads((ROOT / "semantic-fixtures.json").read_text(encoding="utf-8"))
    assert fixtures and {"accept", "reject"} <= {c["expected"] for c in fixtures}
    for case in fixtures:
        actual = outcome(case)
        assert actual == case["expected"], f'{case["name"]}: expected {case["expected"]}, got {actual}'
    assert {c["kind"] for c in fixtures} >= {"manifest", "run_replay", "fleet_effect", "freshness", "stop"}
    assert {"fleet_effect"} <= {c["kind"] for c in fixtures}
    schema_fixtures = json.loads((ROOT / "schema-fixtures.json").read_text(encoding="utf-8"))
    schema_names = set(schemas)
    covered = set()
    def relocate_refs(node):
        if isinstance(node, dict):
            for key, value in list(node.items()):
                if key == "$ref" and isinstance(value, str):
                    node[key] = value.replace("#/components/schemas/", "#/$defs/")
                else:
                    relocate_refs(value)
        elif isinstance(node, list):
            for value in node:
                relocate_refs(value)
    definitions = copy.deepcopy(schemas)
    relocate_refs(definitions)
    for case in schema_fixtures:
        assert case["schema"] in schemas, case["name"]
        target = copy.deepcopy(definitions[case["schema"]])
        validator_schema = {"$schema": "https://json-schema.org/draft/2020-12/schema",
                            "$defs": definitions, "allOf": [target]}
        errors = list(Draft202012Validator(validator_schema, format_checker=FormatChecker()).iter_errors(case["value"]))
        assert bool(errors) != case["valid"], f'{case["name"]}: {[e.message for e in errors]}'
        covered.add((case["schema"], case["valid"]))
    for name in schema_names:
        assert (name, True) in covered and (name, False) in covered, f"Missing schema fixtures: {name}"
    route_schema = {"$schema": "https://json-schema.org/draft/2020-12/schema", "$defs": definitions,
                    "allOf": [definitions["Route"]]}
    routes = json.loads((ROOT / "routes.json").read_text(encoding="utf-8"))
    for route in routes:
        Draft202012Validator(route_schema, format_checker=FormatChecker()).validate(route)
    assert "delete" in api["paths"]["/v2/simulation/runs/{run_id}"]
    print(f"Simulation contract checks passed: OpenAPI, {len(schemas)} schemas, {len(schema_fixtures)} schema fixtures, {len(fixtures)} semantic fixtures.")


if __name__ == "__main__":
    main()
