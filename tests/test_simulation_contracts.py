"""Behavior checks for the v2 simulation contract and its semantic fixtures."""
import json
import unittest
from pathlib import Path
from jsonschema import Draft202012Validator, FormatChecker


ROOT = Path(__file__).resolve().parents[1] / "contracts" / "v2"


class SimulationContractTests(unittest.TestCase):
    def test_manifest_reserves_start_and_completion_for_every_request(self):
        api = json.loads((ROOT / "openapi.json").read_text(encoding="utf-8"))
        fixture = next(c["manifest"] for c in json.loads((ROOT / "semantic-fixtures.json").read_text()) if c["kind"] == "manifest" and c["expected"] == "accept")
        validator = Draft202012Validator(api["components"]["schemas"]["RunManifest"])
        for count in (1, 4, 20):
            with self.subTest(count=count):
                self.assertFalse(list(validator.iter_errors({**fixture, "request_count": count, "execution_event_count": count * 2})))
                self.assertTrue(list(validator.iter_errors({**fixture, "request_count": count, "execution_event_count": count * 2 - 1})))

    def test_openapi_declares_distinct_trip_and_assignment_states(self):
        api = json.loads((ROOT / "openapi.json").read_text(encoding="utf-8"))
        schemas = api["components"]["schemas"]
        self.assertEqual(schemas["AssignmentState"]["enum"], ["queued", "processing", "completed", "failed"])
        self.assertEqual(schemas["TripState"]["enum"], ["not_started", "in_progress", "completed", "cancelled"])
        self.assertEqual(schemas["VehicleOperationalState"]["enum"], ["available", "reserved", "on_trip", "out_of_service"])
        self.assertIn("freshness", schemas["Position"]["properties"])
        self.assertIn("operational_state", schemas["Position"]["properties"])
        self.assertIn("/v2/simulation/runs", api["paths"])
        self.assertIn("/v2/rides/{ride_id}/trip", api["paths"])

    def test_semantic_fixtures_include_replay_fencing_freshness_and_drain(self):
        fixtures = json.loads((ROOT / "semantic-fixtures.json").read_text(encoding="utf-8"))
        names = {item["name"] for item in fixtures}
        self.assertTrue({"same run replay", "conflicting run replay", "duplicate event after vehicle version advances",
                         "conflicting duplicate event payload", "late unseen event fenced after later trip",
                         "fresh position boundary", "stale position", "stop drains issued events",
                         "stop rejects new events", "drain deadline records unfinished trips"} <= names)
        self.assertTrue(any(item["expected"] == "accept" for item in fixtures))
        self.assertTrue(any(item["expected"] == "reject" for item in fixtures))

    def test_bounded_opt_in_route_and_default_fleet_are_explicit(self):
        docs = (ROOT / "README.md").read_text(encoding="utf-8")
        self.assertIn("route20synthetic", docs)
        self.assertIn("8 simulated seconds", docs)
        self.assertIn("six-vehicle", docs)
        self.assertIn("assignment completion", docs)
        self.assertIn("profile_not_ready", docs)
        self.assertIn("There is no reset API", docs)
        self.assertIn("ten server-clock seconds", docs)

    def test_manifest_schema_accepts_bounded_and_rejects_over_limit_fixture(self):
        api = json.loads((ROOT / "openapi.json").read_text(encoding="utf-8"))
        schema = api["components"]["schemas"]["RunManifest"]
        validator = Draft202012Validator(schema, format_checker=FormatChecker())
        fixtures = json.loads((ROOT / "semantic-fixtures.json").read_text(encoding="utf-8"))
        for case in (c for c in fixtures if c["kind"] == "manifest"):
            errors = list(validator.iter_errors(case["manifest"]))
            accepted = not errors
            self.assertEqual(accepted, case["expected"] == "accept", case["name"])

    def test_operations_use_service_owners(self):
        api = json.loads((ROOT / "openapi.json").read_text(encoding="utf-8"))
        self.assertEqual(api["servers"], [{"url": "http://localhost:8083"}])
        self.assertEqual(api["paths"]["/v2/fleet/events/{event_id}"]["get"]["servers"], [{"url": "http://localhost:8080"}])
        self.assertEqual(api["paths"]["/v2/rides/{ride_id}/trip"]["post"]["servers"], [{"url": "http://localhost:8081"}])
        self.assertIn("/v2/fleet/vehicles/{vehicle_id}/trips/start", api["paths"])
        self.assertIn("/v2/fleet/vehicles/{vehicle_id}/trips/completion", api["paths"])
        self.assertEqual(api["paths"]["/v2/fleet/events/{event_id}"]["get"]["servers"], [{"url": "http://localhost:8080"}])

    def test_fenced_owner_commands_and_position_are_server_stamped(self):
        api = json.loads((ROOT / "openapi.json").read_text(encoding="utf-8"))
        schemas = api["components"]["schemas"]
        ride_command = schemas["TripCommand"]["properties"]
        self.assertTrue({"expected_trip_version", "expected_vehicle_version", "reservation_id", "run_id", "route_id"} <= set(ride_command))
        start = api["paths"]["/v2/fleet/vehicles/{vehicle_id}/trips/start"]["post"]["requestBody"]["content"]["application/json"]["schema"]["$ref"]
        fleet_command = schemas[start.rsplit("/", 1)[-1]]["properties"]
        self.assertIn("reservation_id", fleet_command)
        self.assertIn("event_id", fleet_command)
        position = schemas["Position"]["properties"]
        self.assertTrue({"vehicle_id", "observed_at", "as_of", "route_id", "segment_started_at", "segment_ends_at"} <= set(position))
        self.assertNotIn("observed_at", schemas["PositionCommand"]["properties"])
        self.assertIn("payload_fingerprint", schemas["FleetEffectReceipt"]["properties"])

    def test_run_records_bounded_drain_and_unfinished_trips(self):
        api = json.loads((ROOT / "openapi.json").read_text(encoding="utf-8"))
        schemas = api["components"]["schemas"]
        self.assertTrue({"stopped", "completed", "failed"} <= set(schemas["RunState"]["enum"]))
        self.assertTrue({"drain_deadline_at", "terminal_reason", "incomplete_trips"} <= set(schemas["Run"]["properties"]))
        manifest = schemas["RunManifest"]["properties"]
        self.assertEqual(manifest["request_count"]["maximum"], 20)
        self.assertEqual(manifest["execution_event_count"]["maximum"], 60)

    def test_validator_schema_validates_every_named_fixture(self):
        api = json.loads((ROOT / "openapi.json").read_text(encoding="utf-8"))
        fixtures = json.loads((ROOT / "schema-fixtures.json").read_text(encoding="utf-8"))
        self.assertTrue(fixtures)
        covered = set()
        for case in fixtures:
            covered.add((case["schema"], case["valid"]))
        self.assertEqual({name for name in api["components"]["schemas"]}, {name for name, _ in covered})
        self.assertTrue(all((name, True) in covered and (name, False) in covered for name in api["components"]["schemas"]))
        names = {case["name"] for case in fixtures}
        self.assertTrue({"stationary vehicle has no route segment", "moving vehicle has timed segment",
                         "moving vehicle missing segment is invalid"} <= names)


if __name__ == "__main__":
    unittest.main()
