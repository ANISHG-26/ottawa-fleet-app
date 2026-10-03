"""Validate OpenAPI, JSON Schema and positive/negative contract examples."""
import json
from datetime import datetime
from pathlib import Path
from jsonschema import Draft202012Validator, FormatChecker
from openapi_spec_validator import validate

ROOT = Path(__file__).resolve().parents[1] / "contracts" / "v1"


def main():
    api = json.loads((ROOT / "openapi.json").read_text())
    validate(api)
    assert "X-As-Of" in api["paths"]["/v1/fleet/{vehicle_id}"]["get"]["responses"]["200"]["headers"], "Single vehicle freshness needs a clock"
    assert "Location" in api["paths"]["/v1/rides"]["post"]["responses"]["202"]["headers"], "Accepted ride needs a status URL"
    job = json.loads((ROOT / "job.schema.json").read_text())
    Draft202012Validator.check_schema(job)
    schemas = api["components"]["schemas"]
    for schema in schemas.values():
        Draft202012Validator.check_schema(schema)
    for methods in api["paths"].values():
        for operation in methods.values():
            messages = list(operation["responses"].values())
            if "requestBody" in operation:
                messages.append(operation["requestBody"])
            for message in messages:
                media = message["content"]["application/json"]
                name = media["schema"]["$ref"].rsplit("/", 1)[-1]
                values = [media["example"]] if "example" in media else [example["value"] for example in media["examples"].values()]
                for value in values:
                    Draft202012Validator(schemas[name], format_checker=FormatChecker()).validate(value)
    cases = json.loads((ROOT / "fixtures.json").read_text())
    assert {c["valid"] for c in cases} == {True, False}
    covered = set()
    for case in cases:
        schema = job if case["schema"] == "Job" else schemas[case["schema"]]
        bundle = {**schema, "$defs": schemas}
        errors = list(Draft202012Validator(bundle, format_checker=FormatChecker()).iter_errors(case["value"]))
        assert bool(errors) != case["valid"], f'{case["name"]}: {[e.message for e in errors]}'
        covered.add((case["schema"], case["valid"]))
    for name in [*schemas, "Job"]:
        assert (name, True) in covered and (name, False) in covered, f"Missing positive/negative coverage: {name}"
    freshness = json.loads((ROOT / "freshness.json").read_text())
    assert len(freshness) == 4
    for case in freshness:
        age = (datetime.fromisoformat(case["as_of"]) - datetime.fromisoformat(case["observed_at"])).total_seconds()
        effective = case["stored"] if 0 <= age <= 30 else "unknown"
        assert effective == case["effective"], case["name"]
    print(f"Contract checks passed: OpenAPI, job schema, {len(cases)} examples.")


if __name__ == "__main__":
    main()
