#!/usr/bin/env python3
"""Build fixed agent targets and write a SHA-256 manifest for server provisioning."""
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import shutil
import re


ROOT = Path(__file__).resolve().parents[1]
TARGETS = {
    "windows-amd64": ("windows", "amd64", "vigara-agent-windows-amd64.exe"),
    "linux-amd64": ("linux", "amd64", "vigara-agent-linux-amd64"),
    "linux-arm64": ("linux", "arm64", "vigara-agent-linux-arm64"),
}


def main() -> int:
    if len(sys.argv) not in (3, 4):
        print("usage: package_agent_releases.py VERSION OUTPUT_DIRECTORY [SOURCE_REVISION]", file=sys.stderr)
        return 2
    version = sys.argv[1].strip()
    if not version or len(version) > 80 or any(ord(c) < 32 or c.isspace() for c in version):
        print("invalid release version", file=sys.stderr)
        return 2
    output = Path(sys.argv[2]).resolve()
    output.mkdir(parents=True, exist_ok=True)
    revision = sys.argv[3] if len(sys.argv) == 4 else subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
    if not re.fullmatch(r"[a-f0-9]{40}", revision):
        print("invalid source revision", file=sys.stderr)
        return 2
    restic_version = re.search(r'const PinnedVersion = "([^"\n]+)"', (ROOT / "internal/restic/runner.go").read_text()).group(1)
    protocol_version = re.search(r'const CurrentVersion = "([^"\n]+)"', (ROOT / "internal/protocol/protocol.go").read_text()).group(1)
    manifest = {"version": version, "sourceRevision": revision, "protocolVersion": protocol_version, "artifacts": {}, "resticVersion": restic_version, "resticArtifacts": {}}
    for target, (goos, goarch, filename) in TARGETS.items():
        path = output / filename
        env = os.environ.copy()
        env.update({"GOOS": goos, "GOARCH": goarch, "CGO_ENABLED": "0"})
        subprocess.run(["go", "build", "-trimpath", "-ldflags", f"-X github.com/deranjer/vigara/apps/agent/internal/runtime.AgentVersion={version}", "-o", str(path), "./apps/agent/cmd/vigira-agent"], cwd=ROOT, env=env, check=True)
        restic_name = f"restic-{target}" + (".exe" if goos == "windows" else "")
        # Build the pinned upstream module through Go's checksum database.
        # No host package manager or unverified release download is involved.
        with tempfile.TemporaryDirectory(prefix="vigara-restic-") as build_dir:
            Path(build_dir, "go.mod").write_text(f"module vigara-restic-release\n\ngo 1.25.8\n\nrequire github.com/restic/restic v{restic_version}\n")
            restic_env = env.copy()
            restic_env.update({"GOWORK": "off", "GOTOOLCHAIN": "auto", "GOSUMDB": "sum.golang.org", "GOPRIVATE": "", "GONOSUMDB": ""})
            subprocess.run(["go", "build", "-mod=mod", "-trimpath", "-o", str(output / restic_name), "github.com/restic/restic/cmd/restic"], cwd=build_dir, env=restic_env, check=True)
            module = json.loads(subprocess.check_output(["go", "mod", "download", "-json", f"github.com/restic/restic@v{restic_version}"], cwd=build_dir, env=restic_env, text=True))
            shutil.copyfile(Path(module["Dir"], "LICENSE"), output / "restic.LICENSE")
        manifest["resticArtifacts"][target] = {"file": restic_name, "sha256": hashlib.sha256((output / restic_name).read_bytes()).hexdigest()}
        digest = hashlib.sha256(path.read_bytes()).hexdigest()
        manifest["artifacts"][target] = {"file": filename, "sha256": digest}
    manifest["resticLicenseSha256"] = hashlib.sha256((output / "restic.LICENSE").read_bytes()).hexdigest()
    (output / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    print(f"Wrote {output / 'manifest.json'}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
