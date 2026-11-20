"""Check tracked public files without printing sensitive matches.

Gitleaks supplies the full secret/history scan in CI. This additional check rejects
private artifacts and personal identifiers even when they are low entropy.
"""
from pathlib import Path
import re
import subprocess
import sys

PRIVATE_ROOTS = {".local", "private", "data", "diagnostics", "firmware-prep", "dist", "packages", ".playwright-mcp"}
PRIVATE_NAMES = {"config.json", "config.local.json", "config.private.json", "credentials.json"}
PRIVATE_SUFFIXES = {".db", ".sqlite", ".sqlite3", ".log", ".exe", ".soc", ".pem", ".key", ".zip"}
PATTERNS = {
    "personal mobile number": re.compile(rb"(?<![0-9A-Za-z])(?:\+?86)?1[3-9][0-9]{9}(?![0-9A-Za-z])"),
    "Feishu application identifier": re.compile(rb"\bcli_[0-9a-f]{16}\b"),
    "Feishu private identifier": re.compile(rb"\b(?:ou|oc|on)_[0-9a-f]{32}\b"),
    "SIM identity": re.compile(rb"(?<![0-9A-Za-z])89[0-9]{17,20}(?![0-9A-Za-z])"),
}


def inspect(path: str, data: bytes) -> list[str]:
    p = Path(path)
    failures = []
    if (p.parts[0] in PRIVATE_ROOTS or p.name in PRIVATE_NAMES
            or p.suffix.lower() in PRIVATE_SUFFIXES
            or p.name == ".env" or (p.name.startswith(".env.") and p.name != ".env.example")):
        failures.append("private file type/path")
    failures.extend(label for label, pattern in PATTERNS.items() if pattern.search(data))
    return failures


def main() -> int:
    files = subprocess.check_output(["git", "ls-files", "-z"]).decode().split("\0")
    errors = []
    for name in filter(None, files):
        path = Path(name)
        if path.is_symlink():
            errors.append((name, ["tracked symlink"]))
            continue
        failures = inspect(name, path.read_bytes())
        if failures:
            errors.append((name, failures))
    for name, failures in errors:
        print(f"{name}: {', '.join(failures)}", file=sys.stderr)
    if errors:
        return 1
    print("Public tracked-file audit passed")
    return 0


if __name__ == "__main__":
    sys.exit(main())
