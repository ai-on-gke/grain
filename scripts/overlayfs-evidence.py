#!/usr/bin/env python3
# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""Read-only, bounded host evidence for task 79. No caller-selected commands/paths."""
import collections
import datetime
import json
import itertools
import os
import re
import selectors
import signal
import subprocess
import sys
import time
from pathlib import Path

SINCE = "2026-11-18T03:10:50Z"
UNTIL = "2026-11-18T03:11:10Z"
MAX_BYTES = 2 * 1024 * 1024
COMMAND_BYTES = 64 * 1024
MAX_PIDS = 1024
MAX_FDS = 64
MAX_CONTAINERS = 16
DOCKER = ["docker", "--host", "unix:///var/run/docker.sock"]
COMMAND_ENV = {"PATH": "/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL": "C", "TZ": "UTC"}


def redact(text):
    # Minimize common credential disclosure; this is not a general secret detector.
    text = re.sub(r"(?im)^\s*Environment=.*$", "Environment=<omitted>", text)
    text = re.sub(
        r"""(?ix)([\w-]*(?:token|password|secret|api[_-]?key)[\w-]*[=:]\s*)
        (?:"[^"]*"|'[^']*'|[^\s]+)""", r"\1<redacted>", text)
    text = re.sub(r"(?i)(Bearer\s+)\S+", r"\1<redacted>", text)
    return re.sub(r"(https?://)[^\s/@]+:[^\s/@]+@", r"\1<redacted>@", text)


def command(argv, timeout, limit=COMMAND_BYTES):
    """Drain one pipe incrementally; kill the whole process group on either limit."""
    try:
        process = subprocess.Popen(argv, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                                   stderr=subprocess.STDOUT, start_new_session=True,
                                   env=COMMAND_ENV)
    except OSError as error:
        return {"status": "unavailable", "error": str(error)}
    data = bytearray()
    deadline = time.monotonic() + timeout
    status = "ok"
    with selectors.DefaultSelector() as selector:
        selector.register(process.stdout, selectors.EVENT_READ)
        while True:
            left = deadline - time.monotonic()
            if left <= 0:
                status = "timeout"
                break
            ready = selector.select(left)
            if not ready:
                status = "timeout"
                break
            block = os.read(process.stdout.fileno(), min(8192, limit + 1 - len(data)))
            if not block:
                break
            data.extend(block)
            if len(data) > limit:
                status = "truncated"
                break
    if status != "ok":
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
    try:
        result = process.wait(timeout=max(0.01, deadline - time.monotonic()))
    except subprocess.TimeoutExpired:
        status = "timeout"
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        result = process.wait()
    process.stdout.close()
    if status == "ok" and result != 0:
        status = "error"
    return {"status": status, "exit_code": result,
            "output": redact(bytes(data[:limit]).decode("utf-8", "replace"))}


class Collector:
    def __init__(self, output=sys.stdout, proc=Path("/proc"), runner=command, seconds=45):
        self.output, self.proc, self.runner = output, proc, runner
        self.deadline = time.monotonic() + seconds
        self.bytes = 0
        self.incomplete = False
        self.limited = False

    def emit(self, section, **data):
        if data.get("status", "ok") != "ok":
            self.incomplete = True
        record = json.dumps({"section": section, **data}, ensure_ascii=True) + "\n"
        size = len(record.encode())
        # Always reserve room for the final coverage summary.
        if self.bytes + size > (MAX_BYTES if section == "summary" else MAX_BYTES - 4096):
            self.limited = self.incomplete = True
            return
        self.output.write(record)
        self.output.flush()
        self.bytes += size

    def available(self):
        if time.monotonic() >= self.deadline or self.limited:
            self.limited = self.incomplete = True
            return False
        return True

    def run(self, section, argv, privileged=False, seconds=4):
        if not self.available():
            self.emit(section, status="not_collected", reason="global budget exhausted")
            return {}
        selected = argv
        mode = "direct"
        if privileged and os.geteuid() != 0:
            selected = ["sudo", "-n", "--", *argv]
            mode = "sudo-n"
        result = self.runner(selected, min(seconds, max(0.01, self.deadline - time.monotonic())))
        self.emit(section, argv=selected, access=mode, **result)
        # Failed sudo is evidence, not permission to prompt or install anything.
        return result

    def read(self, path, limit=COMMAND_BYTES):
        try:
            with path.open("rb") as stream:
                data = stream.read(limit + 1)
            return {"status": "truncated" if len(data) > limit else "ok",
                    "output": redact(data[:limit].decode("utf-8", "replace"))}
        except OSError as error:
            return {"status": "unavailable", "error": str(error)}

    def links(self, pid, mount_ids):
        root = self.proc / str(pid)
        links = {}
        for name in ("root", "cwd"):
            try:
                links[name] = os.readlink(root / name)
            except OSError as error:
                links[name] = {"error": str(error)}
        self.emit("process", pid=pid, references=links,
                  status="incomplete" if any(isinstance(v, dict) for v in links.values()) else "ok")
        try:
            fds = sorted(itertools.islice((root / "fdinfo").iterdir(), MAX_FDS + 1), key=lambda p: int(p.name))
        except OSError as error:
            self.emit("fdinfo", pid=pid, status="unavailable", error=str(error))
            return
        if len(fds) > MAX_FDS:
            self.emit("fdinfo_limit", pid=pid, status="truncated", observed_at_least=len(fds), limit=MAX_FDS)
        for fd in fds[:MAX_FDS]:
            if not self.available():
                return
            result = self.read(fd, 4096)
            if result["status"] != "ok":
                self.emit("fdinfo", pid=pid, fd=fd.name, **result)
                continue
            fields = dict(re.findall(r"^(mnt_id|flags):\s*(\S+)", result["output"], re.M))
            if fields.get("mnt_id") in mount_ids:
                try:
                    target = os.readlink(root / "fd" / fd.name)
                except OSError as error:
                    target = str(error)
                self.emit("overlay_fd", pid=pid, fd=fd.name, target=target, **fields)

    def mounts(self):
        """Capture each visible namespace once; cloned mounts retain the same device."""
        namespaces, mounts, errors = {}, [], []
        try:
            pids = sorted(itertools.islice((int(p.name) for p in self.proc.iterdir() if p.name.isdigit()), MAX_PIDS + 1))
        except OSError as error:
            self.emit("mounts", status="unavailable", error=str(error))
            return
        if len(pids) > MAX_PIDS:
            self.emit("pid_limit", status="truncated", observed_at_least=len(pids), limit=MAX_PIDS)
        for pid in pids[:MAX_PIDS]:
            if not self.available():
                break
            try:
                namespace = os.readlink(self.proc / str(pid) / "ns/mnt")
            except OSError as error:
                if len(errors) < 16:
                    errors.append({"pid": pid, "error": str(error)})
                self.incomplete = True
                continue
            if namespace in namespaces:
                namespaces[namespace]["pids"].append(pid)
                continue
            entry = {"pids": [pid], "mount_ids": set()}
            namespaces[namespace] = entry
            result = self.read(self.proc / str(pid) / "mountinfo", 512 * 1024)
            if result["status"] != "ok":
                self.emit("mountinfo", pid=pid, namespace=namespace, **result)
            for line in result.get("output", "").splitlines():
                if " - overlay " not in line:
                    continue
                before, after = line.split(" - ", 1)
                fields = before.split()
                if len(fields) < 6:
                    continue
                options = dict(part.split("=", 1) for part in after.split()[-1].split(",") if "=" in part)
                mount = {"pid": pid, "namespace": namespace, "mount_id": fields[0],
                         "device": fields[2], "line": line,
                         "upperdir": options.get("upperdir"), "workdir": options.get("workdir")}
                mounts.append(mount)
                entry["mount_ids"].add(fields[0])
                self.emit("overlay_mount", **mount)
        if errors:
            self.emit("namespace_visibility", status="incomplete", errors=errors)
        claims = collections.defaultdict(list)
        for mount in mounts:
            for role in ("upperdir", "workdir"):
                if mount[role]:
                    claims[mount[role]].append((mount, role))
        candidates = set()
        for path, entries in claims.items():
            devices = sorted({m["device"] for m, _ in entries})
            if len(devices) > 1:
                self.emit("overlap_candidate", path=path, devices=devices,
                          owners=[{"namespace": m["namespace"], "mount_id": m["mount_id"], "role": r}
                                  for m, r in entries],
                          note="Different devices sharing a directory path; users and inode identity still need assessment.")
                candidates.update(m["namespace"] for m, _ in entries)
        for namespace in sorted(candidates):
            entry = namespaces[namespace]
            for pid in entry["pids"][:32]:
                if self.available():
                    self.links(pid, entry["mount_ids"])
            if len(entry["pids"]) > 32:
                self.emit("reference_limit", status="truncated", namespace=namespace, limit=32)
        self.emit("mount_coverage", namespaces=len(namespaces), overlay_mounts=len(mounts),
                  overlap_namespaces=len(candidates),
                  note="Current snapshot only; no observed overlap does not establish historical safety.")

    def collect(self):
        self.emit("capture", captured_at=datetime.datetime.now(datetime.timezone.utc).isoformat(),
                  hostname=os.uname().nodename, uid=os.getuid(), euid=os.geteuid(),
                  since=SINCE, until=UNTIL, seconds=45, output_limit=MAX_BYTES,
                  note="No environment, cmdline, file contents from fd targets, or full container inspect. Root-only reads use noninteractive sudo.")
        self.run("kernel_version", ["uname", "-r"])
        self.run("docker_version", DOCKER + ["version", "--format", "{{json .Server}}"])
        self.run("docker_storage", DOCKER + ["info", "--format",
                 "{{.Name}} {{.Driver}} {{json .DriverStatus}} {{.DockerRootDir}}"])
        self.run("installed_unit", ["systemctl", "cat", "grain-daemon.service"])
        self.run("effective_unit", ["systemctl", "show", "grain-daemon.service",
                 "-p", "FragmentPath", "-p", "DropInPaths", "-p", "ExecStartPre",
                 "-p", "ExecStart", "-p", "ExecStop", "-p", "TimeoutStopUSec", "-p", "KillMode"])
        self.run("installed_extractor", ["cat", "/usr/local/lib/grain/extract-host-binaries.sh"])
        self.run("retained_boots", ["journalctl", "--list-boots", "--no-pager"], privileged=True)
        window = ["--utc", "--since", "2026-11-18 03:10:50 UTC",
                  "--until", "2026-11-18 03:11:10 UTC", "--no-pager",
                  "--lines", "250", "-o", "short-iso-precise"]
        self.run("runtime_journal", ["journalctl", *window, "-u", "grain-daemon.service",
                 "-u", "docker.service", "-u", "containerd.service",
                 "-u", "grain-hostd-upgrade.service"], privileged=True)
        # A fixed historical window must not inherit -k\'s implicit current-boot filter.
        self.run("kernel_journal", ["journalctl", *window, "_TRANSPORT=kernel"], privileged=True)
        events = self.run("container_events", DOCKER + ["events", "--since", SINCE, "--until", UNTIL,
                          "--filter", "type=container", "--format",
                          '{{json .ID}} {{json .Action}} {{json .Actor.Attributes.name}} {{json .Actor.Attributes.image}}'])
        self.mounts()
        ids = list(dict.fromkeys(re.findall(r'^"([a-f0-9]{64})" ', events.get("output", ""), re.M)))
        if len(ids) > MAX_CONTAINERS:
            self.emit("container_limit", status="truncated", count=len(ids), limit=MAX_CONTAINERS)
        # The named daemon is useful even when Docker's bounded historical event buffer is empty.
        for cid in ["grain-daemon", *ids[:MAX_CONTAINERS]]:
            self.run("container_metadata", DOCKER + ["inspect", "--format",
                     "{{.Id}} {{.Created}} pid={{.State.Pid}} running={{.State.Running}} {{json .GraphDriver}}", cid],
                     seconds=2)
        self.emit("summary", status="incomplete" if self.incomplete else "ok",
                  budget_exhausted=self.limited,
                  note="Missing/empty historical logs or events and clean current mounts cannot establish retention or classify the historical warning.")


def main():
    if len(sys.argv) != 1:
        print("usage: overlayfs-evidence.py (no arguments; fixed incident window)", file=sys.stderr)
        return 2
    collector = Collector()
    collector.collect()
    return 0  # Individual unavailable/incomplete sections are explicitly represented in JSONL.


if __name__ == "__main__":
    sys.exit(main())
