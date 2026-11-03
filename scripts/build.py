"""Produce a standalone, platform-matched plugin binary and marketplace ZIP."""
import argparse
import json
import os
import pathlib
import subprocess
import sys
import zipfile

root = pathlib.Path(__file__).resolve().parent.parent
parser = argparse.ArgumentParser()
parser.add_argument("--platform", default="-".join(subprocess.check_output(["go", "env", "GOOS", "GOARCH"], text=True).split()))
args = parser.parse_args()
subprocess.run([sys.executable, "scripts/prepare_vm.py", "--platform", args.platform], cwd=root, check=True)
if not (root / "node_modules/.bin/vite").exists():
    subprocess.run(["npm", "ci", "--ignore-scripts", "--no-audit", "--no-fund"], cwd=root, check=True)
subprocess.run(["npm", "run", "build"], cwd=root, check=True)
goos, goarch = args.platform.split("-", 1)
destination = root / "dist" / args.platform
destination.mkdir(parents=True, exist_ok=True)
executable = destination / "monitor"
subprocess.run(["go", "build", "-trimpath", "-ldflags=-s -w", "-o", str(executable), "."], cwd=root,
               env={**os.environ, "GOOS": goos, "GOARCH": goarch, "CGO_ENABLED": "0"}, check=True)
manifest = json.loads((root / "plugin.json").read_text())
package = root / "dist" / f"monitor-{manifest['version']}-{args.platform}.zip"
# The SDK bootstrap config is separate from the monitor's persistent config.yaml.
files = [(executable, "monitor"), (root / "plugin.json", "plugin.json"), (root / "host-config.example.yaml", "config.yaml")]
if sum(path.stat().st_size for path, _ in files) > 150 * 1024 * 1024:
    raise SystemExit("Marketplace package exceeds the 150 MiB unpacked limit")
with zipfile.ZipFile(package, "w", zipfile.ZIP_DEFLATED) as archive:
    for path, name in files:
        archive.write(path, name)
print(package)
