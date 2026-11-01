"""Verify packaged digests and run the real Restic backup/download drill."""
import hashlib
import json
import os
from pathlib import Path
import platform
import subprocess
import sys

root = Path(__file__).resolve().parents[1]
release_dir = Path(sys.argv[1]).resolve()
manifest = json.loads((release_dir / "manifest.json").read_text())
for group in ("artifacts", "resticArtifacts"):
    for entry in manifest[group].values():
        assert hashlib.sha256((release_dir / entry["file"]).read_bytes()).hexdigest() == entry["sha256"]
target = ("windows" if os.name == "nt" else "linux") + ("-arm64" if platform.machine().lower() in ("aarch64", "arm64") else "-amd64")
executable = release_dir / manifest["resticArtifacts"][target]["file"]
version = subprocess.check_output([str(executable), "version"], text=True)
assert version.startswith("restic " + manifest["resticVersion"] + " "), version
env = os.environ.copy()
env.update({"VIGIRA_RESTIC_DRILL": "1", "VIGIRA_RESTIC_EXECUTABLE": str(executable)})
subprocess.run(["go", "test", "./internal/restic", "-run", "TestManagedResticRoundTrip", "-count=1", "-v"], cwd=root, env=env, check=True)
print("Verified release digests, pinned restic version, backup, browsing, and downloads.")
