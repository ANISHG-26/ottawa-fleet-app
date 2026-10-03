#!/usr/bin/env python3
"""Local Compose and validation commands for the Ottawa Fleet application."""

from __future__ import annotations

import argparse
import json
import os
import shutil
import subprocess
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
COMPOSE_FILE = ROOT / "deploy" / "compose.yaml"
PROJECT = "ottawa-fleet-app"
VOLUME = "ottawa-fleet-app_fleet_data"


def compose(*args: str, check: bool = True) -> subprocess.CompletedProcess[str]:
    env = os.environ.copy()
    env["APP_BUILD_CONTEXT"] = str(ROOT)
    if not env.get("POSTGRES_PASSWORD") and not (ROOT / ".env").is_file():
        raise SystemExit(
            "POSTGRES_PASSWORD is required. Copy deploy/.env.example to .env "
            "or set POSTGRES_PASSWORD in the environment."
        )
    return subprocess.run(
        ["docker", "compose", "--project-directory", str(ROOT), "-p", PROJECT, "-f", str(COMPOSE_FILE), *args],
        cwd=ROOT,
        env=env,
        check=check,
        text=True,
    )


def wait_for_stack(timeout_seconds: int = 120) -> None:
    endpoints = [
        "http://127.0.0.1:8080/readyz",
        "http://127.0.0.1:8081/readyz",
        "http://127.0.0.1:8088/",
    ]
    deadline = time.monotonic() + timeout_seconds
    pending = set(endpoints)
    while pending and time.monotonic() < deadline:
        for endpoint in tuple(pending):
            try:
                with urllib.request.urlopen(endpoint, timeout=2) as response:
                    if response.status == 200:
                        pending.remove(endpoint)
            except (urllib.error.URLError, TimeoutError, OSError):
                pass
        if pending:
            time.sleep(1)
    if pending:
        compose("ps", check=False)
        raise SystemExit("Services did not become ready: " + ", ".join(sorted(pending)))


def command_check(_: argparse.Namespace) -> None:
    go = shutil.which("go")
    if not go and os.name == "nt":
        fallback = Path(r"C:\Program Files\Go\bin\go.exe")
        if fallback.exists():
            go = str(fallback)
    if not go:
        raise SystemExit("Go is required; install the Go toolchain and retry.")
    gofmt = shutil.which("gofmt")
    if not gofmt and os.name == "nt":
        fallback_gofmt = Path(r"C:\Program Files\Go\bin\gofmt.exe")
        if fallback_gofmt.exists():
            gofmt = str(fallback_gofmt)
    if not gofmt:
        raise SystemExit("gofmt is required; install the Go toolchain and retry.")
    go_files = sorted(str(path) for path in (ROOT / "services").rglob("*.go") if not {".gocache", ".gomodcache"}.intersection(path.parts))
    formatted = subprocess.run([gofmt, "-l", *go_files], cwd=ROOT, check=True, capture_output=True, text=True)
    if formatted.stdout.strip():
        print(formatted.stdout, file=sys.stderr)
        raise SystemExit("Go source is not gofmt-formatted.")
    subprocess.run([go, "vet", "./..."], cwd=ROOT / "services", check=True)
    subprocess.run([go, "test", "-p", "1", "./..."], cwd=ROOT / "services", check=True)
    if not os.environ.get("TEST_DATABASE_URL"):
        print("PostgreSQL integration tests skipped: TEST_DATABASE_URL is unset.")
    subprocess.run([sys.executable, "scripts/build_contracts.py", "--check"], cwd=ROOT, check=True)
    subprocess.run([sys.executable, "scripts/check_contracts.py"], cwd=ROOT, check=True)
    subprocess.run([sys.executable, "scripts/check_repository.py"], cwd=ROOT, check=True)
    npm = shutil.which("npm")
    if not npm:
        raise SystemExit("Node.js 24 and npm are required for UI checks.")
    npm_run(npm, ROOT / "web", "ci")
    npm_run(npm, ROOT / "web", "audit", "--audit-level=high")
    npm_run(npm, ROOT / "web", "test")
    npm_run(npm, ROOT / "web", "run", "build")
    npm_run(npm, ROOT / "web", "exec", "--", "playwright", "install", "chromium")
    npm_run(npm, ROOT / "web", "run", "test:browser")


def npm_run(npm: str, cwd: Path, *args: str) -> None:
    if os.name == "nt":
        node = shutil.which("node")
        if node:
            npm_cli = Path(npm).resolve().parent / "node_modules" / "npm" / "bin" / "npm-cli.js"
            if npm_cli.is_file():
                subprocess.run([node, str(npm_cli), *args], cwd=cwd, check=True)
                return
        subprocess.run(["cmd.exe", "/d", "/s", "/c", "npm", *args], cwd=cwd, check=True)
    else:
        subprocess.run([npm, *args], cwd=cwd, check=True)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    sub.add_parser("up", help="build the local stack and wait for UI/API readiness")
    sub.add_parser("down", help="stop containers while preserving local database data")
    sub.add_parser("ps", help="show container state")
    logs = sub.add_parser("logs", help="follow service logs")
    logs.add_argument("service", nargs="?", help="optional service name")
    reset = sub.add_parser("reset", help="remove the Compose project and its local data volume")
    reset.add_argument("--yes", action="store_true", help="confirm deletion without an interactive prompt")
    faults = sub.add_parser("fault", help="set bounded Fleet faults through the container-local debug API")
    faults.add_argument("--latency-ms", type=int, default=0, help="0..1000 synthetic latency")
    faults.add_argument("--error-percent", type=int, default=0, help="0..50 synthetic error rate")
    sub.add_parser("fault-reset", help="clear Fleet fault settings")
    sub.add_parser("check", help="run Go, contract, and UI checks")
    sub.add_parser("config", help="validate and print the resolved Compose configuration")
    args = parser.parse_args()

    if args.command == "up":
        compose("up", "--detach", "--build")
        wait_for_stack()
        print("Ready: UI http://localhost:8088, Fleet API http://localhost:8080, Ride API http://localhost:8081")
    elif args.command == "down":
        compose("down", "--remove-orphans")
        print(f"Stopped. Persistent volume {VOLUME} was preserved.")
    elif args.command == "ps":
        compose("ps")
    elif args.command == "logs":
        compose("logs", "--follow", *([args.service] if args.service else []))
    elif args.command == "reset":
        print(f"This removes Compose project {PROJECT!r} and volume {VOLUME!r}.")
        print("Discarded data: synthetic vehicles, reservations, rides, jobs, idempotency keys, and cursor epoch.")
        if not args.yes:
            if not sys.stdin.isatty():
                raise SystemExit("Reset requires an interactive y confirmation or --yes.")
            if input("Type y to permanently remove this local data: ").strip().lower() != "y":
                raise SystemExit("Reset cancelled.")
        compose("down", "--volumes", "--remove-orphans")
    elif args.command == "fault":
        if not 0 <= args.latency_ms <= 1000 or not 0 <= args.error_percent <= 50:
            raise SystemExit("Fault values must be bounded: latency 0..1000 and error_percent 0..50.")
        body = json.dumps({"latency_ms": args.latency_ms, "error_percent": args.error_percent}, separators=(",", ":"))
        compose("exec", "--no-TTY", "fleet-api", "wget", "-qO-", "--header=Content-Type: application/json", f"--post-data={body}", "http://127.0.0.1:8080/debug/faults")
    elif args.command == "fault-reset":
        compose("exec", "--no-TTY", "fleet-api", "wget", "-qO-", "--post-data=", "http://127.0.0.1:8080/debug/faults/reset")
    elif args.command == "check":
        command_check(args)
    elif args.command == "config":
        env = os.environ.copy()
        env["APP_BUILD_CONTEXT"] = str(ROOT)
        result = subprocess.run(
            ["docker", "compose", "--project-directory", str(ROOT), "-p", PROJECT,
             "-f", str(COMPOSE_FILE), "config", "--format", "json"],
            cwd=ROOT, env=env, check=True, capture_output=True, text=True,
        )
        config = json.loads(result.stdout)
        expected = ROOT.resolve()
        mismatches = {
            name: service.get("build", {}).get("context")
            for name, service in config["services"].items()
            if "build" in service and Path(service["build"]["context"]).resolve() != expected
        }
        if mismatches:
            raise SystemExit(f"Compose build contexts must resolve to {expected}: {mismatches}")
        print(f"Compose configuration is valid; all build contexts resolve to {expected}.")


if __name__ == "__main__":
    main()
