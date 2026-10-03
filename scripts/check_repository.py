"""Dependency-free scaffold validation; not an application test suite."""
from pathlib import Path
import json
import re
import sys
from urllib.parse import unquote

ROOT = Path(__file__).resolve().parents[1]
REQUIRED = (
    "README.md",
    "AGENTS.md",
    "CONTRIBUTING.md",
    "docs/architecture.md",
    "docs/phase-1.md",
    "services/README.md",
    "web/README.md",
    "contracts/README.md",
    "db/README.md",
    "deploy/README.md",
    "charts/README.md",
    "tests/README.md",
    ".github/workflows/repository-checks.yml",
)
errors = []
for name in REQUIRED:
    if not (ROOT / name).is_file():
        errors.append(f"Missing required file: {name}")
for path in ROOT.rglob("*.md"):
    if any(part in {".git", ".venv", ".venv-contracts", ".gocache", ".gomodcache", ".local-data", "node_modules"} for part in path.parts):
        continue
    for target in re.findall(r"\[[^\]]*\]\(([^\s)]+)\)", path.read_text(encoding="utf-8")):
        if target.startswith(("https://", "http://", "mailto:", "#")):
            continue
        target = unquote(target.split("#", 1)[0])
        candidate = (path.parent / target).resolve()
        if not candidate.is_relative_to(ROOT) or not candidate.exists():
            errors.append(f"{path.relative_to(ROOT)}: invalid local link {target}")
for path in ROOT.rglob("*.json"):
    if any(part in {".git", ".venv", ".venv-contracts", ".gocache", ".gomodcache", ".local-data", "node_modules"} for part in path.parts):
        continue
    try:
        json.loads(path.read_text(encoding="utf-8"))
    except (ValueError, UnicodeError) as exc:
        errors.append(f"{path.relative_to(ROOT)}: {exc}")
if errors:
    print("\n".join(errors), file=sys.stderr)
    sys.exit(1)
print("Repository checks passed: required files, local Markdown links and JSON.")
