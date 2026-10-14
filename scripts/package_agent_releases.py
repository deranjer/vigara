#!/usr/bin/env python3
"""Build fixed agent targets and write a SHA-256 manifest for server provisioning."""
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys


ROOT = Path(__file__).resolve().parents[1]
TARGETS = {
    "windows-amd64": ("windows", "amd64", "vigira-agent-windows-amd64.exe"),
    "linux-amd64": ("linux", "amd64", "vigira-agent-linux-amd64"),
    "linux-arm64": ("linux", "arm64", "vigira-agent-linux-arm64"),
}


def main() -> int:
    if len(sys.argv) != 3:
        print("usage: package_agent_releases.py VERSION OUTPUT_DIRECTORY", file=sys.stderr)
        return 2
    version = sys.argv[1].strip()
    if not version or len(version) > 80 or any(ord(c) < 32 or c.isspace() for c in version):
        print("invalid release version", file=sys.stderr)
        return 2
    output = Path(sys.argv[2]).resolve()
    output.mkdir(parents=True, exist_ok=True)
    manifest = {"version": version, "artifacts": {}}
    for target, (goos, goarch, filename) in TARGETS.items():
        path = output / filename
        env = os.environ.copy()
        env.update({"GOOS": goos, "GOARCH": goarch, "CGO_ENABLED": "0"})
        subprocess.run(["go", "build", "-trimpath", "-ldflags", f"-X github.com/deranjer/vigara/apps/agent/internal/runtime.AgentVersion={version}", "-o", str(path), "./apps/agent/cmd/vigira-agent"], cwd=ROOT, env=env, check=True)
        digest = hashlib.sha256(path.read_bytes()).hexdigest()
        manifest["artifacts"][target] = {"file": filename, "sha256": digest}
    (output / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    print(f"Wrote {output / 'manifest.json'}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
