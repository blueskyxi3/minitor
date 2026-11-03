"""Download and verify the pinned official VictoriaMetrics release at build time."""
import argparse
import hashlib
import io
import json
import pathlib
import subprocess
import tarfile
import urllib.request

root = pathlib.Path(__file__).resolve().parent.parent
parser = argparse.ArgumentParser()
parser.add_argument("--platform", default="-".join(subprocess.check_output(["go", "env", "GOOS", "GOARCH"], text=True).split()))
args = parser.parse_args()
lock = json.loads((root / "versions.lock.json").read_text())
if args.platform not in lock["platforms"]:
    raise SystemExit("Unsupported platform: " + args.platform)
expected = lock["platforms"][args.platform]
bundle = root / "internal/vm/bundle"
archive = bundle / "victoria-metrics.tar.gz"
name = f"victoria-metrics-{args.platform}-{lock['version']}.tar.gz"
if archive.exists() and hashlib.sha256(archive.read_bytes()).hexdigest() == expected["archive_sha256"]:
    payload = archive.read_bytes()
else:
    url = f"https://github.com/VictoriaMetrics/VictoriaMetrics/releases/download/{lock['version']}/{name}"
    with urllib.request.urlopen(url, timeout=120) as response:
        payload = response.read(150 * 1024 * 1024 + 1)
if hashlib.sha256(payload).hexdigest() != expected["archive_sha256"]:
    raise SystemExit("VictoriaMetrics archive checksum mismatch")
with tarfile.open(fileobj=io.BytesIO(payload), mode="r:gz") as tar:
    entry = tar.getmember("victoria-metrics-prod")
    if not entry.isfile() or entry.size > 150 * 1024 * 1024:
        raise SystemExit("Invalid VictoriaMetrics executable")
    binary = tar.extractfile(entry).read()
if hashlib.sha256(binary).hexdigest() != expected["binary_sha256"]:
    raise SystemExit("VictoriaMetrics executable checksum mismatch")
bundle.mkdir(parents=True, exist_ok=True)
archive.write_bytes(payload)
(bundle / "metadata.json").write_text(json.dumps({"version": lock["version"], "platform": args.platform, **expected}))
license_url = f"https://raw.githubusercontent.com/VictoriaMetrics/VictoriaMetrics/{lock['version']}/LICENSE"
license_path = root / "licenses/VictoriaMetrics-LICENSE"
if not license_path.exists():
    license_path.parent.mkdir(parents=True, exist_ok=True)
    license_path.write_bytes(urllib.request.urlopen(license_url, timeout=30).read())
print(f"Verified {name}: {len(binary)} executable bytes")
