"""Real Redis + bundled VictoriaMetrics + SDK proxy integration, isolated fixtures only."""
import argparse
import base64
import hashlib
import hmac
import http.server
import json
import os
import pathlib
import platform
import re
import socket
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

ROOT = pathlib.Path(__file__).resolve().parent.parent
SECRET = "monitor-smoke-private-shared-secret-32-chars"
METRICS_PASSWORD = "monitor-smoke-metrics-password"


class Redis:
    def __init__(self, address):
        self.url = urllib.parse.urlsplit(address)

    def command(self, *args):
        def read(stream):
            line = stream.readline()
            if not line:
                raise RuntimeError("Redis connection closed")
            kind, body = chr(line[0]), line[1:-2]
            if kind == "-":
                raise RuntimeError(body.decode())
            if kind in "+:":
                return body.decode()
            if kind == "$":
                size = int(body)
                if size == -1:
                    return None
                value = stream.read(size)
                stream.read(2)
                return value
            if kind == "*":
                return [read(stream) for _ in range(int(body))]
            raise RuntimeError("Unsupported Redis response")

        def send(conn, stream, values):
            pieces = [f"*{len(values)}\r\n".encode()]
            for value in values:
                raw = str(value).encode()
                pieces.extend([f"${len(raw)}\r\n".encode(), raw, b"\r\n"])
            conn.sendall(b"".join(pieces))
            return read(stream)

        with socket.create_connection((self.url.hostname, self.url.port or 6379), 5) as conn:
            stream = conn.makefile("rb")
            if self.url.password:
                creds = ["AUTH"]
                if self.url.username:
                    creds.append(urllib.parse.unquote(self.url.username))
                creds.append(urllib.parse.unquote(self.url.password))
                send(conn, stream, creds)
            send(conn, stream, ["SELECT", self.url.path.strip("/") or "0"])
            return send(conn, stream, args)


def server(handler):
    instance = http.server.ThreadingHTTPServer(("0.0.0.0", 0), handler)
    instance.daemon_threads = True
    threading.Thread(target=instance.serve_forever, daemon=True).start()
    return instance


class Quiet(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def send(self, body, status=200, content_type="application/json"):
        if not isinstance(body, bytes):
            body = body.encode()
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        try:
            self.wfile.write(body)
        except (BrokenPipeError, ConnectionResetError):
            pass


def histogram(name, labels, count, bounds, fractions, average):
    labels = labels + "," if labels else ""
    rows = [f"# TYPE {name} histogram"]
    for bound, fraction in zip(bounds, fractions):
        rows.append(f'{name}_bucket{{{labels}le="{bound}"}} {count * fraction}')
    rows += [f'{name}_bucket{{{labels}le="+Inf"}} {count}', f'{name}_count{{{labels[:-1]}}} {count}', f'{name}_sum{{{labels[:-1]}}} {count * average}']
    return rows


def metric_handler(number, gateway, worker, rps, control):

    class Metrics(Quiet):
        def do_GET(self):
            expected = "Basic " + base64.b64encode(f"metrics:{METRICS_PASSWORD}".encode()).decode()
            if self.path != "/api/metrics" or self.headers.get("Authorization") != expected:
                self.send("", 404)
                return
            if control.get("empty"):
                self.send("# TYPE unrelated_metric gauge\nunrelated_metric 1\n", content_type="text/plain; version=0.0.4")
                return
            elapsed = time.monotonic() - control["started"]
            rows = ["# TYPE go_goroutines gauge", "go_goroutines 12", "# TYPE process_resident_memory_bytes gauge", "process_resident_memory_bytes 10485760", "# TYPE process_cpu_seconds_total counter", f"process_cpu_seconds_total {elapsed * .1}", "# TYPE db_pool_open_connections gauge", "db_pool_open_connections 5", "# TYPE db_pool_in_use_connections gauge", "db_pool_in_use_connections 2", "# TYPE db_pool_wait_seconds_total counter", f"db_pool_wait_seconds_total {elapsed * .01}"]
            if gateway:
                rows += ["# TYPE http_requests_total counter", f'http_requests_total{{method="POST",path="/v1/chat/completions",code="200"}} {elapsed * rps}', "# TYPE ai_hub_llm_requests_in_flight gauge"]
                for model in ["model-a", "model-b"]:
                    n = elapsed * rps / 2
                    labels = f'channel_type="1",channel_id="{number + 1}",model="{model}",api_mode="chat_completions",stream="true"'
                    rows += histogram("gateway_processing_seconds", labels, n, [.005, .01, .025, .05, .1, .25, .5, 1, 2, 5, 10, 30], [0, 0, .2, .8, 1, 1, 1, 1, 1, 1, 1, 1], .04)
                    fast = number == 0
                    rows += histogram("ttft_seconds", labels, n, [.1, .25, .5, 1, 2, 5, 10, 30, 60, 120, 300], [0, 1 if fast else 0, 1 if fast else 0, 1, 1, 1, 1, 1, 1, 1, 1], .2 if fast else .8)
                    rows += histogram("tpot_seconds", labels, n, [.005, .01, .02, .05, .1, .2, .5, 1, 2], [0, 0, .2, .4, .6, .8, .9, .9, .9], .4)
                    rows += histogram("e2e_latency_seconds", labels, n, [.1, .25, .5, 1, 2, 5, 10, 30, 60, 120, 300], [0, 0, 0, 0, .1, .8, 1, 1, 1, 1, 1], 5)
                    rows += [f'ai_hub_llm_requests_in_flight{{model="{model}",instance_id="mock-{number}"}} 3', "# TYPE provider_requests_total counter", f'provider_requests_total{{channel_type="1",channel_id="{number + 1}",model="{model}",type="200"}} {n}']
            if worker:
                bounds = [.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60, 120, 300]
                for stage in ["event_queue", "event_apply", "database_budget", "settlement", "wallet_lock", "financial_commit", "reconciliation"]:
                    rows += histogram("ai_hub_billing_stage_duration_seconds", f'stage="{stage}",event="completed",result="success"', elapsed * 10, bounds, [0, 0, .1, .5, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1], .02)
                rows += histogram("ai_hub_billing_batch_size", 'stage="wallet_settlement"', elapsed * 10, [1, 2, 4, 8, 16, 32, 64, 128], [0, 0, 1, 1, 1, 1, 1, 1], 4)
                rows += ["# TYPE ai_hub_billing_pipeline gauge", 'ai_hub_billing_pipeline{state="healthy_backlog"} 17', 'ai_hub_billing_pipeline{state="oldest_seconds"} 3', 'ai_hub_billing_pipeline{state="deferred"} 2', 'ai_hub_billing_pipeline{state="isolated"} 1', "# TYPE ai_hub_billing_health_sampled_at_seconds gauge"]
                for kind in ["pipeline", "billing_stream", "media_usage_stream", "orders"]:
                    rows.append(f'ai_hub_billing_health_sampled_at_seconds{{kind="{kind}"}} {control.get("observed_at", int(time.time()))}')
                for name in ["billing_stream", "media_usage_stream"]:
                    rows += [f"# TYPE {name} gauge"]
                    for state, value in [("pending", 4), ("length", 20), ("dlq", 1), ("oldest_pending_seconds", 2)]:
                        rows.append(f'{name}{{state="{state}"}} {value}')
                rows += ["# TYPE billing_orders gauge", 'billing_orders{state="retrying"} 2', "# TYPE ai_hub_billing_backlog_sampled_at_seconds gauge", f"ai_hub_billing_backlog_sampled_at_seconds {int(time.time())}", "# TYPE ai_hub_billing_backlog_snapshot gauge", 'ai_hub_billing_backlog_snapshot{kind="settlement",state="count"} 17', 'ai_hub_billing_backlog_snapshot{kind="reconciliation",state="count"} 5']
            self.send("\n".join(rows) + "\n", content_type="text/plain; version=0.0.4")
    return Metrics


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--redis-url", required=True, help="isolated disposable test Redis")
    native = platform.system().lower() + "-" + {"aarch64": "arm64", "x86_64": "amd64"}.get(platform.machine(), platform.machine())
    parser.add_argument("--binary", default=str(ROOT / "dist" / native / "monitor"))
    parser.add_argument("--browser", action="store_true")
    parser.add_argument("--instances", type=int, default=2)
    parser.add_argument("--faults", action="store_true", help="also check reset, missing metrics, stale snapshots, discovery/auth failure and scaling")
    args = parser.parse_args()
    redis = Redis(args.redis_url)
    suffix = uuid.uuid4().hex[:10]
    reader, password = f"monitor_reader_{suffix}", uuid.uuid4().hex
    redis.command("ACL", "SETUSER", reader, "on", ">" + password, "~ai-hub:monitor:instances", "~ai-hub:monitor:instance:*", "+@connection", "+smembers", "+mget")
    original = urllib.parse.urlsplit(args.redis_url)
    redis_url = urllib.parse.urlunsplit((original.scheme, f"{reader}:{password}@{original.hostname}:{original.port or 6379}", original.path, "", ""))
    ids, servers = [], []
    stop = threading.Event()
    process = None
    state = {"port": 0, "access": "manage", "requests": []}
    temporary = tempfile.TemporaryDirectory(prefix="ai-hub-monitor-smoke-")
    temp = pathlib.Path(temporary.name)

    class Host(Quiet):
        def do_GET(self):
            self.route()

        def do_PUT(self):
            self.route()

        def route(self):
            if self.path == "/api/user/self":
                assert self.headers.get("Authorization") == "Bearer smoke-delegation"
                self.send(json.dumps({"success": True, "data": {"id": 9, "role": 100, "page_access": {"monitor": state["access"]}}}))
                return
            if self.path == "/":
                self.send((ROOT / "scripts/browser.html").read_bytes(), content_type="text/html; charset=utf-8")
                return
            vendor = {"/vendor/react.js": ROOT / "node_modules/react/umd/react.development.js", "/vendor/react-dom.js": ROOT / "node_modules/react-dom/umd/react-dom.development.js", "/vendor/mui.js": ROOT / "node_modules/@mui/material/umd/material-ui.development.js"}
            if self.path in vendor:
                self.send(vendor[self.path].read_bytes(), content_type="text/javascript")
                return
            prefix = "/api/plugin-bridge/monitor"
            if not self.path.startswith(prefix + "/"):
                self.send("", 404)
                return
            state["requests"].append(self.path)
            path = self.path[len(prefix):]
            body = self.rfile.read(int(self.headers.get("Content-Length", "0"))) if self.command == "PUT" else None
            claims = {"user_id": 9, "plugin_id": "monitor", "method": self.command, "uri": path, "expires_at": int(time.time()) + 30}
            encoded = base64.urlsafe_b64encode(json.dumps(claims).encode()).rstrip(b"=")
            signature = base64.urlsafe_b64encode(hmac.new(SECRET.encode(), encoded, hashlib.sha256).digest()).rstrip(b"=")
            request = urllib.request.Request(f"http://127.0.0.1:{state['port']}" + path, data=body, method=self.command,
                headers={"X-AIHub-Identity": (encoded + b"." + signature).decode(), "X-AIHub-Host-Authorization": "smoke-delegation", "Content-Type": "application/json"})
            try:
                response = urllib.request.urlopen(request, timeout=40)
            except urllib.error.HTTPError as error:
                response = error
            self.send(response.read(), response.status, response.headers.get("Content-Type", "application/json"))

    host = server(Host)
    servers.append(host)
    proxy = f"http://127.0.0.1:{host.server_port}"

    def call(path, payload=None):
        request = urllib.request.Request(proxy + "/api/plugin-bridge/monitor/api/v1" + path, data=json.dumps(payload).encode() if payload is not None else None, method="PUT" if payload is not None else "GET")
        with urllib.request.urlopen(request, timeout=50) as response:
            result = json.load(response)
            assert result["success"], result
            return result["data"]

    try:
        records, sources, excluded = [], [], set()
        for number in range(args.instances + 1):
            gateway, worker = number < args.instances, number in [0, args.instances]
            rps = 300 if number == 0 else 700 / max(1, args.instances - 1)
            control = {"started": time.monotonic() - 10}
            sources.append(control)
            metrics = server(metric_handler(number, gateway, worker, rps, control))
            servers.append(metrics)
            instance_id = f"monitor-smoke-{suffix}-{number}"
            ids.append(instance_id)
            records.append({"id": instance_id, "hostname": f"fixture-{number}", "node_role": "master" if number == 0 else "slave", "billing_worker_mode": "embedded" if number == 0 else "only" if worker else "disabled", "worker_enabled": worker, "worker_ready": worker, "metrics_url": f"http://127.0.0.1:{metrics.server_port}/api/metrics"})

        def heartbeat():
            while not stop.is_set():
                for record in records:
                    if record["id"] in excluded:
                        continue
                    record["last_heartbeat_at"] = int(time.time())
                    redis.command("SET", "ai-hub:monitor:instance:" + record["id"], json.dumps(record), "EX", "35")
                    redis.command("SADD", "ai-hub:monitor:instances", record["id"])
                stop.wait(5)
        heartbeat_thread = threading.Thread(target=heartbeat, daemon=True)
        heartbeat_thread.start()
        settings = {"version": 1, "discovery": {"redis_url": redis_url}, "metrics": {"username": "metrics", "password": METRICS_PASSWORD}, "collection": {"interval_seconds": 5, "timeout_seconds": 1}, "storage": {"data_path": "./custom-tsdb", "retention_days": 30}}
        settings_path = temp / "settings/config.yaml"
        settings_path.parent.mkdir()
        settings_path.write_text(json.dumps(settings))
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            state["port"] = sock.getsockname()[1]
        bootstrap = temp / "host-config.yaml"
        bootstrap.write_text(json.dumps({"plugin": {"id": "monitor"}, "server": {"address": f"127.0.0.1:{state['port']}"}, "host": {"base_url": proxy, "secret": SECRET}}))
        log = open(temp / "monitor.log", "wb")

        def launch():
            return subprocess.Popen([args.binary, "--config", str(bootstrap), "--monitor-config", str(settings_path)], cwd="/tmp", stdout=log, stderr=log)

        def ready():
            for _ in range(250):
                if process.poll() is not None:
                    raise RuntimeError((temp / "monitor.log").read_text())
                try:
                    with urllib.request.urlopen(f"http://127.0.0.1:{state['port']}/healthz", timeout=1) as response:
                        if response.status == 200:
                            return
                except (OSError, urllib.error.URLError):
                    pass
                time.sleep(.1)
            raise RuntimeError("monitor readiness timed out")

        process = launch()
        ready()
        deadline = time.time() + 35
        while True:
            health = call("/status")
            if health["complete"]:
                break
            if time.time() > deadline:
                raise RuntimeError(health)
            time.sleep(1)
        time.sleep(11)
        deadline = time.monotonic() + 30
        while True:
            gateway = call("/series?scope=gateway&grain_seconds=15")
            metrics = {m["key"]: m for m in gateway["metrics"]}
            if any(point["values"].get("p95") is not None for point in metrics["ttft"]["points"]):
                break
            assert time.monotonic() < deadline, "no trend observations after sampling warm-up"
            time.sleep(2)
        p95 = metrics["ttft"]["summary"]["p95"]
        assert p95 is not None and 850 < p95 <= 1000, p95
        assert metrics["tpot"]["lower_bounds"].get("p99"), metrics["tpot"]
        assert metrics["http_error_rate"]["summary"]["value"] == 0, metrics["http_error_rate"]
        assert 900 < metrics["http_rps"]["summary"]["value"] < 1100, metrics["http_rps"]
        model = call("/series?scope=model")
        assert {row["model"] for row in model["models"]} == {"model-a", "model-b"}
        billing = call("/series?scope=billing")
        billing_metrics = {m["key"]: m for m in billing["metrics"]}
        assert billing_metrics["billing_backlog"]["summary"]["value"] == 17, billing_metrics["billing_backlog"]
        assert billing_metrics["sql_settlement_backlog"]["summary"]["value"] == 17
        assert billing_metrics["billing_settled_orders"]["summary"]["value"] > 0
        filtered = call("/series?scope=model&model_name=model-a")
        assert not next(m for m in filtered["metrics"] if m["key"] == "http_rps")["supported"]
        configuration = call("/settings")
        assert configuration["resolved_data_path"] == str(settings_path.parent / "custom-tsdb")
        assert METRICS_PASSWORD not in json.dumps(configuration) and password not in json.dumps(configuration)
        call("/settings", {"metrics": {"username": "metrics"}})
        state["access"] = ""
        try:
            call("/series")
            raise AssertionError("revoked permission accepted")
        except urllib.error.HTTPError as error:
            assert error.code == 403
        state["access"] = "manage"
        try:
            urllib.request.urlopen(f"http://127.0.0.1:{state['port']}/api/v1/status")
            raise AssertionError("unsigned request accepted")
        except urllib.error.HTTPError as error:
            assert error.code == 401
        if args.browser:
            subprocess.run(["node", str(ROOT / "scripts/browser.mjs"), proxy], check=True)
        if args.faults:
            def wait_status(predicate):
                deadline = time.monotonic() + 30
                while True:
                    status = call("/status")
                    if predicate(status):
                        return status
                    assert time.monotonic() < deadline, status
                    time.sleep(1)

            for source in sources:
                source["started"] = time.monotonic()
            time.sleep(12)
            reset = call("/series?scope=gateway&grain_seconds=15")
            reset_metrics = {m["key"]: m for m in reset["metrics"]}
            assert 700 < reset_metrics["http_rps"]["summary"]["value"] < 1300, "counter reset corrupted throughput"
            assert 850 < reset_metrics["ttft"]["summary"]["p95"] <= 1000
            call("/settings", {"metrics": {"password": "intentionally-incorrect"}})
            wait_status(lambda status: any(row["health"] == "down" for row in status["instances"]))
            call("/settings", {"metrics": {"password": METRICS_PASSWORD}})
            wait_status(lambda status: status["complete"])
            sources[0]["empty"] = True
            wait_status(lambda status: any(row["id"] == ids[0] and row["health"] == "missing" for row in status["instances"]))
            sources[0]["empty"] = False
            wait_status(lambda status: status["complete"])
            for n in [0, args.instances]:
                sources[n]["observed_at"] = int(time.time()) - 300
            wait_status(lambda status: any(row["health"] == "stale" for row in status["instances"]))
            stale = call("/series?scope=billing")
            backlog = next(m for m in stale["metrics"] if m["key"] == "billing_backlog")
            assert backlog["summary"]["value"] is None and backlog["stale"], "expired global backlog was displayed as current"
            for source in sources:
                source.pop("observed_at", None)
            wait_status(lambda status: status["complete"])
            redis.command("ACL", "SETUSER", reader, "off")
            wait_status(lambda status: bool(status["discovery_error"]) and not status["complete"] and not status["instances"])
            redis.command("ACL", "SETUSER", reader, "on")
            wait_status(lambda status: status["complete"])
            excluded.add(ids[1])
            time.sleep(5)
            redis.command("DEL", "ai-hub:monitor:instance:" + ids[1])
            redis.command("SREM", "ai-hub:monitor:instances", ids[1])
            wait_status(lambda status: len(status["instances"]) == args.instances)
            excluded.clear()
            wait_status(lambda status: status["complete"] and len(status["instances"]) == args.instances + 1)
            print(json.dumps({"counter_reset": "passed", "metrics_auth_failure": "passed", "missing_metrics": "passed", "stale_billing": "passed", "redis_failure_recovery": "passed", "instance_scale_in_out": "passed"}))
        # A hard-killed parent must not leave a database that blocks a new owner.
        process.kill()
        process.wait(timeout=10)
        time.sleep(2)
        process = launch()
        ready()
        history = call("/series?scope=gateway")
        assert next(m for m in history["metrics"] if m["key"] == "ttft")["summary"]["count"] > 0
        process.terminate()
        process.wait(timeout=20)
        assert (settings_path.parent / "custom-tsdb/data").exists()
        result = {"instances": args.instances + 1, "simulated_gateway_rps": 1000, "cluster_ttft_p95_ms": p95, "backlog_not_multiplied": 17, "proxy_requests": len(state["requests"]), "config_path_resolution": "passed", "permission_revocation": "passed", "parent_sigkill_recovery": "passed", "historical_restart": "passed"}
        (ROOT / "dist").mkdir(exist_ok=True)
        (ROOT / "dist/smoke-report.json").write_text(json.dumps(result, indent=2))
        print(json.dumps(result))
    except BaseException:
        print((temp / "monitor.log").read_text() if (temp / "monitor.log").exists() else "fixture setup failed")
        if process and process.poll() is None:
            try:
                print("Collection status:", json.dumps(call("/status")))
                print("Available labels:", json.dumps(call("/options")))
                commands = subprocess.check_output(["ps", "-axo", "command"], text=True).splitlines()
                address = next(re.search(r"-httpListenAddr=(127\.0\.0\.1:\d+)", line).group(1) for line in commands if str(temp) in line and "-httpListenAddr=" in line)
                for expression in ['ttft_seconds_count', 'sum by(le)(increase(ttft_seconds_bucket[60s]))', 'label_replace(vector(1),"monitor_key","probe","__name__",".*")']:
                    result = urllib.request.urlopen("http://" + address + "/api/v1/query?" + urllib.parse.urlencode({"query": expression}), timeout=5).read().decode()
                    print("Query probe:", expression, result[:5000])
            except Exception as error:
                print("Diagnostic query failed:", type(error).__name__)
        raise
    finally:
        if process and process.poll() is None:
            process.terminate()
            try:
                process.wait(timeout=20)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
        stop.set()
        for fixture in servers:
            fixture.shutdown()
            fixture.server_close()
        for instance_id in ids:
            redis.command("SREM", "ai-hub:monitor:instances", instance_id)
            redis.command("DEL", "ai-hub:monitor:instance:" + instance_id)
        redis.command("ACL", "DELUSER", reader)
        temporary.cleanup()


if __name__ == "__main__":
    main()
