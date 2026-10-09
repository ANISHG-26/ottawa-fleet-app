"""Behavior checks for the optional simulation controller chart workload."""

from __future__ import annotations

import os
import pathlib
import subprocess
import unittest

import yaml


ROOT = pathlib.Path(__file__).resolve().parents[1]
CHART = ROOT / "charts" / "ottawa-fleet"
HELM = os.environ.get("HELM", "helm")


def render(*args: str, check: bool = True) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        [HELM, "template", "ottawa-fleet", str(CHART), "--namespace", "chart-test",
         "-f", str(CHART / "ci-values.yaml"), *args],
        text=True,
        capture_output=True,
        check=check,
    )


try:
    HELM_AVAILABLE = subprocess.run([HELM, "version", "--short"], capture_output=True).returncode == 0
except OSError:
    HELM_AVAILABLE = False


@unittest.skipUnless(HELM_AVAILABLE, "Helm is required for chart render checks (set HELM to its path)")
class ClusterChartTests(unittest.TestCase):
    def documents(self, output: str) -> list[dict]:
        return [document for document in yaml.safe_load_all(output) if document]

    def find(self, documents: list[dict], kind: str, name: str) -> dict:
        matches = [doc for doc in documents if doc.get("kind") == kind and doc.get("metadata", {}).get("name") == name]
        self.assertEqual(len(matches), 1, f"expected one {kind}/{name}, found {len(matches)}")
        return matches[0]

    def test_default_keeps_controller_off_and_requires_seventh_image_pin(self) -> None:
        output = self.documents(render().stdout)
        deployments = [doc for doc in output if doc.get("kind") == "Deployment"]
        images = [container["image"] for doc in deployments
                  for container in doc["spec"]["template"]["spec"]["containers"]]
        self.assertEqual(len(images), 4)
        self.assertFalse(any(doc.get("metadata", {}).get("name") == "simulation-controller" for doc in output))
        ci_values = yaml.safe_load((CHART / "ci-values.yaml").read_text())
        self.assertRegex(ci_values["images"]["simulationController"]["digest"], r"^sha256:[a-f0-9]{64}$")
        self.assertIn("version: 0.2.1", (CHART / "Chart.yaml").read_text())
        self.assertIn('appVersion: "0.2.1"', (CHART / "Chart.yaml").read_text())

    def test_enabled_route20_renders_bounded_controller_and_profile_migration_key(self) -> None:
        output = self.documents(render("--set", "simulation.enabled=true",
                                       "--set", "simulation.fleetProfile=route20synthetic",
                                       "--set", "images.simulationController.digest=sha256:" + "2" * 64,
                                       "--set", "images.simulationController.sourceCommit=" + "2" * 40).stdout)
        controller = self.find(output, "Deployment", "simulation-controller")
        pod_spec = controller["spec"]["template"]["spec"]
        container = pod_spec["containers"][0]
        self.assertEqual(controller["spec"]["replicas"], 1)
        self.assertEqual(pod_spec["terminationGracePeriodSeconds"], 15)
        self.assertEqual(container["ports"][0]["containerPort"], 8083)
        env = {item["name"]: item for item in container["env"]}
        self.assertEqual(env["SIMULATION_ENABLED"]["value"], "1")
        self.assertEqual(env["FLEET_API_URL"]["value"], "http://fleet-api:8080")
        self.assertEqual(env["RIDE_API_URL"]["value"], "http://ride-api:8081")
        self.assertIn("secretKeyRef", env["DATABASE_URL"]["valueFrom"])
        self.assertTrue(container["securityContext"]["readOnlyRootFilesystem"])
        self.assertEqual(container["resources"]["limits"], {"cpu": "500m", "memory": "256Mi"})
        self.assertEqual(container["readinessProbe"]["httpGet"]["path"], "/readyz")
        self.assertEqual(container["livenessProbe"]["httpGet"]["path"], "/healthz")
        service = self.find(output, "Service", "simulation-controller")
        self.assertEqual(service["spec"]["ports"][0]["port"], 8083)
        migration = next(doc for doc in output if doc.get("kind") == "Job")
        migration_env = {item["name"]: item for item in migration["spec"]["template"]["spec"]["containers"][0]["env"]}
        self.assertEqual(migration_env["FLEET_PROFILE"]["value"], "route20synthetic")
        default_job = next(doc for doc in self.documents(render().stdout) if doc.get("kind") == "Job")
        self.assertNotEqual(default_job["metadata"]["name"], migration["metadata"]["name"],
                            "fleet profile must participate in the migration Job key")

    def test_ride_api_uses_the_fixed_fleet_api_url(self) -> None:
        documents = self.documents(render().stdout)
        ride = self.find(documents, "Deployment", "ride-api")
        env = {item["name"]: item for item in ride["spec"]["template"]["spec"]["containers"][0]["env"]}
        self.assertEqual(env["FLEET_API_URL"]["value"], "http://fleet-api:8080")

    def test_enabled_with_default_six_profile_is_rejected(self) -> None:
        result = render("--set", "simulation.enabled=true", check=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("fleetProfile", result.stderr)

    def test_missing_simulation_controller_digest_is_rejected(self) -> None:
        result = render("--set-string", "images.simulationController.digest=", check=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("/images/simulationController/digest", result.stderr)

    def test_blue_ui_background_is_passed_to_runtime(self) -> None:
        blue = self.documents(render("--set", "web.backgroundColor=blue").stdout)
        green = self.documents(render().stdout)
        blue_web = self.find(blue, "Deployment", "web")["spec"]["template"]["spec"]["containers"][0]
        green_web = self.find(green, "Deployment", "web")["spec"]["template"]["spec"]["containers"][0]
        self.assertEqual({item["name"]: item["value"] for item in blue_web["env"]}["APP_BACKGROUND_COLOR"], "blue")
        self.assertEqual({item["name"]: item["value"] for item in green_web["env"]}["APP_BACKGROUND_COLOR"], "green")


if __name__ == "__main__":
    unittest.main()
