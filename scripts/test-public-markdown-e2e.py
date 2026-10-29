#!/usr/bin/env python3
"""Opt-in public Go HTTP -> strict Worker HTTP -> actor -> billing -> download.

Only synthetic data and disposable PostgreSQL, Redis and local storage are used.
Run with the installed Worker's Python environment. No dotenv, hosted services,
application doubles, dependency installation, or shared databases are used.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import signal
import socket
import subprocess
import sys
import tempfile
import time
from urllib.request import ProxyHandler, build_opener

import fitz
import psutil
from redis import Redis
from redis.exceptions import ConnectionError as RedisConnectionError

BACKEND = Path(__file__).resolve().parents[1]


def port():
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return listener.getsockname()[1]


def until(probe, seconds=45):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        if probe():
            return
        time.sleep(.05)
    raise AssertionError("private fixture did not become ready")


def stop(process):
    descendants = psutil.Process(process.pid).children(recursive=True) if process.poll() is None else []
    if process.poll() is None:
        process.send_signal(signal.SIGTERM)
        try:
            process.wait(timeout=8)
        except subprocess.TimeoutExpired:
            os.killpg(process.pid, signal.SIGKILL)
            process.wait(timeout=5)
    _, alive = psutil.wait_procs(descendants, timeout=3)
    for child in alive:
        if child.is_running() and child.status() != psutil.STATUS_ZOMBIE:
            child.kill()
    _, alive = psutil.wait_procs(alive, timeout=3)
    assert all(not p.is_running() or p.status() == psutil.STATUS_ZOMBIE for p in alive)
    return process.returncode


def qualify(worker, redis_binary, report):
    assert (worker / "app/main.py").is_file()
    assert redis_binary.is_file()
    bindir = Path(subprocess.check_output(["pg_config", "--bindir"], text=True).strip())
    gopath = subprocess.check_output(["go", "env", "GOPATH"], text=True).strip()
    cache = subprocess.check_output(["go", "env", "GOCACHE"], text=True).strip()
    report["backend_sha"] = subprocess.check_output(["git", "-C", str(BACKEND), "rev-parse", "HEAD"], text=True).strip()
    report["worker_sha"] = subprocess.check_output(["git", "-C", str(worker), "rev-parse", "HEAD"], text=True).strip()
    report["source_files_sha256"] = {
        name: hashlib.sha256((BACKEND / name).read_bytes()).hexdigest()
        for name in ("internal/conversion/pdfToMarkdown.go", "internal/tasks/durable.go", "internal/worker/durability.go", "internal/conversion/pdfToMarkdown_public_e2e_test.go")
    }
    report["worker_files_sha256"] = {
        name: hashlib.sha256((worker / name).read_bytes()).hexdigest()
        for name in ("app/jobs/actors.py", "app/jobs/router.py", "app/core/security.py", "app/core/native_domain.py")
    }
    report["result"] = "FAILED"
    processes, handles = [], []
    postgres_started = False
    with tempfile.TemporaryDirectory(prefix="platen-public-markdown-") as owned:
        root = Path(owned)
        for name in ("objects", "scratch", "redis", "metrics", "home"):
            (root / name).mkdir(mode=0o700)
        redis_port, http_port, metrics_port = port(), port(), port()
        assert len({redis_port, http_port, metrics_port}) == 3
        environment = {
            "PATH": os.environ["PATH"], "LANG": "C.UTF-8", "APP_ENV": "test", "LOCAL": "true",
            "HOME": str(root / "home"), "GOPATH": gopath, "GOCACHE": cache,
            "GOMAXPROCS": "2", "GOPROXY": "off", "GOSUMDB": "off",
            "PGPASSFILE": str(root / "pgpass"), "PYTHONPATH": str(worker), "PYTHON_DOTENV_DISABLED": "1",
            "PYTHONDONTWRITEBYTECODE": "1", "STORAGE_MODE": "local", "LOCAL_STORAGE_DIR": str(root / "objects"),
            "TMPDIR": str(root / "scratch"), "REDIS_URL": f"redis://127.0.0.1:{redis_port}/0",
            "DATABASE_URL": f"host={root} port=55449 user=platen_test dbname=public_markdown_test sslmode=disable",
            "WORKER_SHARED_SECRET": "public-markdown-synthetic-secret", "JWT_SECRET": "public-markdown-synthetic-jwt",
            "PDFNEST_WORKER_URL": f"http://127.0.0.1:{http_port}", "FILE_ENCRYPTION_KEY": "",
            "ENABLE_PERSISTENT_RENDER_POOL": "false", "ACTOR_HEARTBEAT_REQUIRED": "false",
            "GLOBAL_HEAVY_EXECUTION_LIMIT": "1", "PER_IDENTITY_HEAVY_EXECUTION_LIMIT": "1",
            "OMP_NUM_THREADS": "1", "OPENBLAS_NUM_THREADS": "1", "PDF_TO_MARKDOWN_ENGINE": "internal",
            "F15_ACTOR_EVENTS": str(root / "actor-events.jsonl"), "dramatiq_prom_host": "127.0.0.1",
            "dramatiq_prom_port": str(metrics_port), "dramatiq_prom_db": str(root / "metrics"),
            "PLATEN_PUBLIC_MARKDOWN_E2E": "1", "PLATEN_PUBLIC_MARKDOWN_ROOT": str(root),
        }
        (root / "pgpass").touch(mode=0o600)

        def run(argv, *, cwd=BACKEND, timeout=90):
            return subprocess.run([str(value) for value in argv], cwd=cwd, env=environment,
                                  check=True, text=True, capture_output=True, timeout=timeout)

        def launch(name, argv):
            handle = open(root / (name + ".log"), "w+")
            handles.append(handle)
            process = subprocess.Popen([str(value) for value in argv], cwd=worker, env=environment,
                                       start_new_session=True, stdout=handle, stderr=subprocess.STDOUT)
            processes.append((name, process))
            return process

        redis = None
        try:
            run([bindir / "initdb", "-D", root / "pgdata", "-A", "trust", "-U", "platen_test", "--encoding=UTF8"])
            run([bindir / "pg_ctl", "-D", root / "pgdata", "-l", root / "postgres.log", "-o",
                 f"-h '' -k {root} -p 55449 -F -c shared_buffers=32MB -c max_connections=50", "-w", "start"])
            postgres_started = True
            run([bindir / "createdb", "-h", root, "-p", "55449", "-U", "platen_test", "public_markdown_test"])
            launch("redis", [redis_binary, "--bind", "127.0.0.1", "--protected-mode", "yes", "--port", redis_port,
                             "--save", "", "--appendonly", "no", "--dir", root / "redis", "--maxmemory", "32mb", "--maxmemory-policy", "noeviction"])
            redis = Redis(host="127.0.0.1", port=redis_port, db=0, socket_timeout=2, socket_connect_timeout=1)
            def redis_ready():
                try:
                    return redis.ping()
                except RedisConnectionError:
                    return False
            until(redis_ready)
            assert redis.dbsize() == 0
            api = launch("api", [sys.executable, "-m", "uvicorn", "app.main:app", "--host", "127.0.0.1", "--port", http_port, "--log-level", "warning", "--no-access-log"])
            opener = build_opener(ProxyHandler({}))
            def api_ready():
                assert api.poll() is None, "private Worker API exited"
                try:
                    with opener.open(environment["PDFNEST_WORKER_URL"] + "/health/live", timeout=1) as response:
                        return response.status == 200
                except OSError:
                    return False
            until(api_ready)
            with fitz.open() as pdf:
                for text in ("PLATEN PUBLIC ALPHA", "PLATEN PUBLIC BETA"):
                    page = pdf.new_page(width=300, height=150)
                    page.insert_text((20, 45), text, fontsize=12)
                pdf.save(root / "synthetic.pdf")
            report["input_sha256"] = hashlib.sha256((root / "synthetic.pdf").read_bytes()).hexdigest()
            command = ["go", "test", "-race", "./internal/conversion", "-run", "^Test(PDFToMarkdownLocalStorageDoesNotRequireR2|PublicMarkdownQueuedLifecycle)$", "-count=1", "-v", "-timeout=6m"]
            go_log = open(root / "go-test.log", "w+")
            handles.append(go_log)
            go_process = subprocess.Popen(command, cwd=BACKEND, env=environment, text=True,
                                          start_new_session=True, stdout=go_log, stderr=subprocess.STDOUT)
            processes.append(("go-test", go_process))
            def cancelled_before_actor():
                assert go_process.poll() is None, "Go fixture exited before deterministic cancellation checkpoint"
                return (root / "start-actor").is_file()
            until(cancelled_before_actor, seconds=120)
            launch("actor", [sys.executable, "-m", "dramatiq", "tests.f15_queued_actor:broker", "--processes", "1", "--threads", "1", "--queues", "conversion", "--worker-shutdown-timeout", "5000"])
            go_exit = go_process.wait(timeout=300)
            go_log.flush()
            output = (root / "go-test.log").read_text()
            report["command"] = command
            report["go_exit_code"] = go_exit
            report["go_output"] = output
            print(report["go_output"], flush=True)
            assert go_exit == 0, "public route E2E failed"
            report["cases"] = [json.loads(line.split("PUBLIC_MARKDOWN_E2E ", 1)[1]) for line in output.splitlines() if "PUBLIC_MARKDOWN_E2E " in line]
            assert len(report["cases"]) == 5
            events = [json.loads(line) for line in (root / "actor-events.jsonl").read_text().splitlines()]
            ids = {case["task_id"] for case in report["cases"]}
            native = [event for event in events if event["event"] == "native_start" and event["job_id"] in ids]
            assert len(native) == 4
            cancelled_ids = {case["task_id"] for case in report["cases"] if case["identity"] == "guest_cancelled"}
            assert not any(event["job_id"] in cancelled_ids for event in native)
            assert all(event["pid"] != os.getpid() and event["pid"] != api.pid for event in native)
            until(lambda: not redis.llen("dramatiq:conversion") and redis.hlen("dramatiq:conversion.msgs") == 0)
            assert redis.zcard("pdfnest:limiter:active") == 0
            report["native_events"] = native
            report["cleanup"] = {"queue_messages": 0, "broker_payloads": 0, "active_leases": 0}
            report["result"] = "LOCAL_PUBLIC_MARKDOWN_FREE_PAID_PASS"
        finally:
            report["log_tails"] = {path.stem: path.read_text(errors="replace")[-8000:] for path in root.glob("*.log")}
            if redis is not None:
                redis.close()
            report["process_exits"] = {name: stop(process) for name, process in reversed(processes)}
            for handle in handles:
                handle.close()
            if postgres_started:
                run([bindir / "pg_ctl", "-D", root / "pgdata", "-m", "fast", "-w", "stop"])
    report["temporary_root_removed"] = not root.exists()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--worker-root", type=Path, required=True)
    parser.add_argument("--redis-server", type=Path, required=True)
    parser.add_argument("--report", type=Path, required=True)
    args = parser.parse_args()
    report = {}
    try:
        qualify(args.worker_root.resolve(), args.redis_server.absolute(), report)
    finally:
        args.report.write_text(json.dumps(report, indent=2) + "\n")
        print(json.dumps({key: value for key, value in report.items() if key not in {"log_tails", "go_output"}}, indent=2))


if __name__ == "__main__":
    main()
