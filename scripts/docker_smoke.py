"""Disposable Docker lifecycle test with a real embedded database and named volume."""
import argparse
import json
import re
import subprocess
import time
import urllib.parse
import urllib.request
import uuid

parser = argparse.ArgumentParser()
parser.add_argument("--image", default="ai-hub-monitor:0.0.1")
args = parser.parse_args()
prefix = "monitor-smoke-" + uuid.uuid4().hex[:10]
volume, name, duplicate = prefix + "-data", prefix, prefix + "-duplicate"
containers = []


def docker(*arguments):
    return subprocess.check_output(["docker", *arguments], text=True, stderr=subprocess.STDOUT if arguments[0] == "logs" else subprocess.PIPE).strip()


def launch(container, publish=True):
    containers.append(container)
    flags = ["run", "-d", "--name", container, "--mount", "source=" + volume + ",target=/data/plugin",
             "-e", "MONITOR_SERVER_ADDRESS=0.0.0.0:3013", "-e", "MONITOR_HOST_SECRET=docker-smoke-shared-secret-over-32-characters"]
    if publish:
        flags.extend(["-p", "127.0.0.1::3013"])
    docker(*flags, args.image)
    if not publish:
        return
    port = int(docker("port", container, "3013/tcp").rsplit(":", 1)[1])
    until = time.monotonic() + 35
    while time.monotonic() < until:
        try:
            with urllib.request.urlopen(f"http://127.0.0.1:{port}/healthz", timeout=1) as response:
                if response.status == 200:
                    return
        except OSError:
            pass
        time.sleep(0.2)
    raise AssertionError("container failed to become healthy: " + docker("logs", container))


def internal_base():
    processes = docker("exec", name, "ps", "-o", "pid,args")
    return "http://127.0.0.1:" + re.search(r"-httpListenAddr=127\.0\.0\.1:(\d+)", processes).group(1)


def sample_exists():
    target = internal_base() + "/api/v1/query?" + urllib.parse.urlencode({"query": "last_over_time(monitor_persistence_probe[1h])"})
    result = json.loads(docker("exec", name, "wget", "-qO-", target))
    rows = result.get("data", {}).get("result", [])
    return bool(rows) and float(rows[0]["value"][1]) == 42


try:
    docker("volume", "create", volume)
    launch(name)
    assert docker("exec", name, "id", "-u") == "10001", "container does not run as its configured unprivileged user"
    assert docker("exec", name, "stat", "-c", "%a", "/data/plugin/config.yaml") == "600"
    docker("exec", name, "wget", "-qO-", "--post-data", "monitor_persistence_probe 42", internal_base() + "/api/v1/import/prometheus")
    until = time.monotonic() + 15
    while not sample_exists():
        assert time.monotonic() < until, "test sample was not ingested"
        time.sleep(0.3)
    launch(duplicate, publish=False)
    exit_code = docker("wait", duplicate)
    assert exit_code != "0" and "already in use" in docker("logs", duplicate), "shared volume was not locked"
    docker("stop", "-t", "20", name)
    docker("rm", name)
    launch(name)
    assert sample_exists(), "database sample was lost after container recreation"
    assert docker("exec", name, "test", "-d", "/data/plugin/victoria-metrics-data/data") == ""
    print(json.dumps({"docker": "passed", "non_root": "passed", "config_permissions": "passed", "exclusive_volume": "passed", "historical_sample_after_recreate": "passed"}))
finally:
    for container in set(containers):
        subprocess.run(["docker", "rm", "-f", container], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    subprocess.run(["docker", "volume", "rm", volume], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
