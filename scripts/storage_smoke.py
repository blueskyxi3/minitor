"""Native storage path, exclusivity and explicit failure checks; no external services."""
import argparse
import json
import os
import pathlib
import platform
import socket
import subprocess
import tempfile
import time
import urllib.request

parser = argparse.ArgumentParser()
native = platform.system().lower() + "-" + {"aarch64": "arm64", "x86_64": "amd64"}.get(platform.machine(), platform.machine())
parser.add_argument("--binary", default=str(pathlib.Path(__file__).resolve().parent.parent / "dist" / native / "monitor"))
args = parser.parse_args()
binary = str(pathlib.Path(args.binary).resolve())

with tempfile.TemporaryDirectory(prefix="monitor-storage-") as temporary:
    root = pathlib.Path(temporary)
    settings = root / "settings/config.yaml"
    settings.parent.mkdir()
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        port = listener.getsockname()[1]
    host = root / "host.yaml"
    host.write_text(f'plugin:\n  id: monitor\nserver:\n  address: "127.0.0.1:{port}"\nhost:\n  base_url: "http://127.0.0.1:3000"\n  secret: "storage-smoke-shared-secret-over-32-characters"\n')

    def configure(path):
        settings.write_text("version: 1\nstorage:\n  data_path: " + json.dumps(str(path)) + "\n  retention_days: 30\n")

    def launch():
        output = tempfile.TemporaryFile()
        process = subprocess.Popen([binary, "--config", str(host)], cwd=tempfile.gettempdir(), env={**os.environ, "MONITOR_CONFIG_PATH": str(settings)}, stdout=output, stderr=output)
        return process, output

    def stop(process, output):
        if process.poll() is None:
            process.terminate()
        process.wait(timeout=20)
        output.seek(0)
        text = output.read().decode()
        output.close()
        return text

    def ready(process):
        until = time.monotonic() + 30
        while time.monotonic() < until:
            if process.poll() is not None:
                raise AssertionError("monitor exited before becoming healthy")
            try:
                with urllib.request.urlopen(f"http://127.0.0.1:{port}/healthz", timeout=1) as response:
                    if response.status == 200:
                        return
            except OSError:
                pass
            time.sleep(0.1)
        raise AssertionError("monitor did not become healthy")

    configure("relative-db")
    process, output = launch()
    try:
        ready(process)
        database = settings.parent / "relative-db"
        assert (database / "data").is_dir(), "relative data path ignored the configuration directory"
        duplicate, log = launch()
        duplicate.wait(timeout=10)
        assert duplicate.returncode != 0 and "already in use" in stop(duplicate, log), "database exclusivity failed"
    finally:
        stop(process, output)

    absolute = root / "absolute-db"
    configure(absolute)
    process, output = launch()
    try:
        ready(process)
        assert (absolute / "data").is_dir() and (database / "data").is_dir(), "changing paths moved or reused the old directory"
    finally:
        stop(process, output)

    blocked = root / "not-writable"
    blocked.mkdir(mode=0o500)
    configure(blocked)
    process, output = launch()
    try:
        process.wait(timeout=10)
        assert process.returncode != 0
        message = stop(process, output)
        assert "not writable" in message or "permission denied" in message, message
    finally:
        blocked.chmod(0o700)
        if not output.closed:
            stop(process, output)

print(json.dumps({"relative_path": "passed", "absolute_path": "passed", "directory_change_without_migration": "passed", "exclusive_lock": "passed", "unwritable_directory": "passed"}))
