#!/usr/bin/env bash
# Stream concise live progress during `go test` while capturing the full raw
# output to a log file and printing a structured failure summary at the end.
#
# Usage:
#   scripts/run-go-tests.sh [go test] [go test flags and packages...]
#   scripts/run-go-tests.sh <compiled-test-binary> [test binary flags...]
#
# Why this script exists:
#   1. Several packages in this module write noisy output to os.Stdout during
#      passing tests (e.g., cmd/grain CLI dumps and daemon logs). Dumping raw
#      `go test -v` output directly to the step's stdout exceeds the 256 KiB
#      ci.maxStepOutput ceiling (pkg/ci/live.go), causing middle-elision to
#      drop `--- FAIL:` diagnostics. Redirecting `go test > /tmp/gotest.log`
#      avoided elision but produced zero live output for 15-35 minutes.
#   2. Single-package or few-package steps (`postgres`, `run-host`,
#      `package-cache`) running `go test` without `-json` buffer all output
#      until the package finishes, leaving the live CI log blank for minutes.
#
# How it works:
#   - Runs `go test -json` (or a pre-compiled test binary) and processes the
#     output stream incrementally as lines arrive.
#   - Writes all raw text output to GRAIN_GOTEST_LOG (default: /tmp/gotest.log)
#     so post-run assertions (such as `grep --- PASS:` in package-cache) and
#     the failure tail have the complete log.
#   - Streams only concise progress lines to stdout while tests run:
#       * each package completion line (`ok`, `FAIL`, `?`)
#       * immediate `[go test] --- FAIL:`, `panic:`, and `WARNING: DATA RACE`
#         notifications
#       * when `-v` / `-test.v` is explicitly requested (or when running a
#         compiled test binary directly), `--- PASS:` and `--- SKIP:` lines
#         along with any `t.Skipf(...)` diagnostic output
#       * build error lines
#       * periodic heartbeat lines every GRAIN_GOTEST_HEARTBEAT_SECONDS
#         (default: 20s) naming the packages and tests currently in flight
#   - When `go test` finishes, prints the structured summary sections
#     (`=== failing tests ===`, `=== panics ===`, `=== data races ===`,
#     `=== per-package results ===`, and on failure `=== last 200 lines of the
#     raw run ===`) and exits with `go test`'s exit status.

set -uo pipefail

if [ "$#" -eq 0 ]; then
  echo "usage: $0 [go test] [go test args...]" >&2
  exit 2
fi

log_file="${GRAIN_GOTEST_LOG:-/tmp/gotest.log}"
heartbeat_seconds="${GRAIN_GOTEST_HEARTBEAT_SECONDS:-20}"

if [ "$1" = "go" ] && [ "${2:-}" = "test" ]; then
  shift 2
  has_json=0
  for arg in "$@"; do
    if [ "$arg" = "-json" ] || [ "$arg" = "--json" ]; then
      has_json=1
      break
    fi
  done
  if [ "$has_json" -eq 1 ]; then
    cmd=(go test "$@")
  else
    cmd=(go test -json "$@")
  fi
elif [[ "$1" == /* || "$1" == ./* || "$1" == *.test ]]; then
  cmd=("$@")
else
  has_json=0
  for arg in "$@"; do
    if [ "$arg" = "-json" ] || [ "$arg" = "--json" ]; then
      has_json=1
      break
    fi
  done
  if [ "$has_json" -eq 1 ]; then
    cmd=(go test "$@")
  else
    cmd=(go test -json "$@")
  fi
fi

python3 - "$log_file" "$heartbeat_seconds" "${cmd[@]}" <<'PY'
import codecs
import collections
import json
import os
import re
import select
import signal
import subprocess
import sys
import time

log_path = sys.argv[1]
try:
    heartbeat_interval = float(sys.argv[2])
except ValueError:
    heartbeat_interval = 20.0
if heartbeat_interval <= 0:
    heartbeat_interval = 20.0
cmd = sys.argv[3:]

MODULE_PREFIX = "github.com/ai-on-gke/grain/"

is_go_test_cmd = len(cmd) >= 2 and cmd[0] == "go" and cmd[1] == "test"
verbose_mode = (not is_go_test_cmd) or any(
    a in ("-v", "-test.v") or a.startswith("-test.v=") for a in cmd
)


def short_pkg(pkg: str) -> str:
    if pkg.startswith(MODULE_PREFIX):
        return pkg[len(MODULE_PREFIX):]
    if pkg == "github.com/ai-on-gke/grain":
        return "."
    return pkg


def fmt_duration(seconds: float) -> str:
    secs = max(0, int(round(seconds)))
    if secs < 60:
        return f"{secs}s"
    mins, rem = divmod(secs, 60)
    if mins < 60:
        return f"{mins}m{rem:02d}s"
    hrs, mins = divmod(mins, 60)
    return f"{hrs}h{mins:02d}m{rem:02d}s"


def emit(line: str) -> None:
    sys.stdout.write(line + "\n")
    sys.stdout.flush()


os.makedirs(os.path.dirname(os.path.abspath(log_path)), exist_ok=True)
log_fp = open(log_path, "a", encoding="utf-8", errors="replace")
ci_default_log_path = "/tmp/gotest.log"
ci_mirror_fp = None
if os.environ.get("GRAIN_CI") == "true" and os.path.abspath(log_path) != ci_default_log_path:
    try:
        os.makedirs(os.path.dirname(ci_default_log_path), exist_ok=True)
        ci_mirror_fp = open(ci_default_log_path, "a", encoding="utf-8", errors="replace")
    except OSError:
        ci_mirror_fp = None


def write_log(text: str) -> None:
    log_fp.write(text)
    log_fp.flush()
    if ci_mirror_fp is not None:
        try:
            ci_mirror_fp.write(text)
            ci_mirror_fp.flush()
        except OSError:
            pass


start_time = time.monotonic()
last_heartbeat = start_time

# Display the command without the internal -json flag so logs read naturally.
display_cmd = [a for a in cmd if a != "-json"]
emit(f"=== running: {' '.join(display_cmd)} ===")

in_flight_pkgs = collections.OrderedDict()   # pkg -> start_monotonic
in_flight_tests = collections.OrderedDict()  # (pkg, test) -> start_monotonic
paused_tests = set()                         # (pkg, test)

ok_pkgs = 0
fail_pkgs = 0
skip_pkgs = 0
pass_tests = 0
fail_tests = 0

# Per-(pkg, test) buffered output lines while test is running:
# - test_diag_lines retains early file.go:line: diagnostic lines (up to 25) so
#   noisy subsequent log output cannot evict an earlier t.Errorf location.
# - test_tail_lines retains the most recent 40 lines before --- FAIL:/--- SKIP:
#   so the final t.Fatalf/t.Skipf reason is never lost when a test prints >80
#   lines of daemon/CLI output.
test_diag_lines = collections.defaultdict(list)
test_tail_lines = collections.defaultdict(lambda: collections.deque(maxlen=40))
recorded_test_fails = set()                        # (pkg, test)
current_plain_test = ""
in_plain_build_block = False
last_logged_pkg = ""

# Diagnostic capture windows: list of [lines_list, remaining_after_count, pkg_key, test_key]
fail_blocks = []
active_fail_windows = []

panic_blocks = []
active_panic_windows = []

race_blocks = []
active_race_windows = []

pkg_results = []
tail_ring = collections.deque(maxlen=200)

PKG_SUMMARY_RE = re.compile(r"^(ok|FAIL|\?)\s+(\S+)")
RUN_RE = re.compile(r"^===\s+(RUN|PAUSE|CONT)\s+(\S+)")
RESULT_RE = re.compile(r"^\s*---\s+(PASS|FAIL|SKIP):\s+(\S+)")
GO_DIAG_RE = re.compile(r"^\s*[a-zA-Z0-9_\-./]+\.go:\d+:")


def reset_test_buffer(key) -> None:
    test_diag_lines.pop(key, None)
    test_tail_lines.pop(key, None)
    recorded_test_fails.discard(key)


def append_test_buffer(key, line: str) -> None:
    diag = test_diag_lines[key]
    if len(diag) < 25:
        if GO_DIAG_RE.match(line) or (diag and line.startswith("        ")):
            diag.append(line)
    test_tail_lines[key].append(line)


def pop_test_output(key) -> list:
    diag = test_diag_lines.pop(key, [])
    tail = list(test_tail_lines.pop(key, ()))
    if not diag:
        return tail
    tail_set = set(tail)
    combined = [l for l in diag if l not in tail_set]
    combined.extend(tail)
    return combined


def record_raw_line(line: str) -> None:
    write_log(line + "\n")
    tail_ring.append(line)


def is_boundary_line(stripped: str, line: str) -> bool:
    if RUN_RE.match(stripped):
        return True
    if RESULT_RE.match(line):
        return True
    if PKG_SUMMARY_RE.match(line):
        return True
    if line in ("PASS", "FAIL"):
        return True
    return False


def is_pkg_or_run_boundary(stripped: str, line: str) -> bool:
    if RUN_RE.match(stripped):
        return True
    if RESULT_RE.match(line):
        return True
    if PKG_SUMMARY_RE.match(line):
        return True
    if line in ("PASS", "FAIL"):
        return True
    return False


def feed_windows(
    windows,
    line: str,
    stripped: str,
    pkg: str,
    test: str,
    stop_on_boundary: bool,
    stop_on_pkg_or_run: bool = False,
) -> None:
    still_active = []
    for block, rem, win_pkg, win_test in windows:
        if win_pkg and pkg and win_pkg != pkg:
            still_active.append([block, rem, win_pkg, win_test])
            continue
        if win_test and test and win_test != test:
            still_active.append([block, rem, win_pkg, win_test])
            continue
        if stop_on_boundary and is_boundary_line(stripped, line):
            continue
        if stop_on_pkg_or_run and is_pkg_or_run_boundary(stripped, line):
            continue
        block.append(line)
        if rem > 1:
            still_active.append([block, rem - 1, win_pkg, win_test])
    windows[:] = still_active


def handle_text_line(raw_line: str, pkg: str, test: str, from_json: bool, is_build: bool) -> None:
    global ok_pkgs, fail_pkgs, skip_pkgs, pass_tests, fail_tests, current_plain_test, in_plain_build_block, last_logged_pkg
    line = raw_line.rstrip("\r\n")
    stripped = line.lstrip()
    now = time.monotonic()

    if from_json and pkg and pkg != last_logged_pkg:
        write_log(f"=== PACKAGE {pkg}\n")
        last_logged_pkg = pkg
    record_raw_line(line)

    if from_json or is_boundary_line(stripped, line):
        in_plain_build_block = False
    elif line.startswith("# "):
        in_plain_build_block = True

    # Feed active panic and race capture windows first, stopping at package/test boundaries.
    feed_windows(
        active_panic_windows,
        line,
        stripped,
        pkg,
        test="",
        stop_on_boundary=False,
        stop_on_pkg_or_run=True,
    )
    feed_windows(
        active_race_windows,
        line,
        stripped,
        pkg,
        test="",
        stop_on_boundary=False,
        stop_on_pkg_or_run=True,
    )

    if is_build or line.startswith("# ") or in_plain_build_block:
        emit(line)
        if not fail_blocks or fail_blocks[-1][0] != "build":
            fail_blocks.append(["build", [line]])
        else:
            fail_blocks[-1][1].append(line)
        return

    # Plain-text test lifecycle tracking when input is not `go test -json`.
    if not from_json:
        m_run = RUN_RE.match(stripped)
        if m_run:
            verb, tname = m_run.group(1), m_run.group(2)
            key = (pkg, tname)
            if verb == "RUN":
                in_flight_tests[key] = now
                paused_tests.discard(key)
                reset_test_buffer(key)
                current_plain_test = tname
            elif verb == "PAUSE":
                paused_tests.add(key)
                in_flight_tests.pop(key, None)
                if current_plain_test == tname:
                    current_plain_test = ""
            elif verb == "CONT":
                paused_tests.discard(key)
                in_flight_tests[key] = now
                current_plain_test = tname
            feed_windows(active_fail_windows, line, stripped, pkg, tname, stop_on_boundary=True)
            return

    # Check for --- PASS / --- FAIL / --- SKIP lines.
    m_res = RESULT_RE.match(line)
    if m_res:
        verdict, tname = m_res.group(1), m_res.group(2)
        key = (pkg, tname)
        in_flight_tests.pop(key, None)
        paused_tests.discard(key)
        if not from_json and current_plain_test == tname:
            current_plain_test = ""

        # Close any previous fail window for this test/package before handling the new verdict.
        feed_windows(active_fail_windows, line, stripped, pkg, tname, stop_on_boundary=True)

        if verdict == "PASS":
            pass_tests += 1
            reset_test_buffer(key)
            if verbose_mode:
                emit(line)
        elif verdict == "SKIP":
            prior = pop_test_output(key)
            if verbose_mode:
                for pline in prior:
                    emit(pline)
                emit(line)
        elif verdict == "FAIL":
            fail_tests += 1
            recorded_test_fails.add(key)
            summary_header = f"{line} [{short_pkg(pkg)}]" if pkg and pkg not in line else line
            emit(f"[go test] {summary_header.lstrip()}")
            prior = pop_test_output(key)
            block = [summary_header] + prior
            fail_blocks.append(["test", block])
            active_fail_windows.append([block, 30, pkg, tname])
        return

    # Feed active post-FAIL capture windows (scoped to matching pkg and test).
    feed_windows(active_fail_windows, line, stripped, pkg, test, stop_on_boundary=True)

    # Check for panic:
    if line.startswith("panic:") or stripped.startswith("panic:"):
        live_line = f"{line} [{short_pkg(pkg)}]" if pkg and pkg not in line else line
        emit(live_line)
        pblock = [live_line]
        panic_blocks.append(pblock)
        active_panic_windows.append([pblock, 30, pkg, ""])

    # Check for WARNING: DATA RACE
    if stripped.startswith("WARNING: DATA RACE"):
        live_line = f"{line} [{short_pkg(pkg)}]" if pkg and pkg not in line else line
        emit(live_line)
        rblock = [live_line]
        race_blocks.append(rblock)
        active_race_windows.append([rblock, 30, pkg, ""])

    # Check for per-package summary lines (ok / FAIL / ?).
    m_pkg = PKG_SUMMARY_RE.match(line)
    if m_pkg:
        kind, pkg_name = m_pkg.group(1), m_pkg.group(2)
        in_flight_pkgs.pop(pkg_name, None)
        if pkg:
            in_flight_pkgs.pop(pkg, None)
        if kind == "ok":
            ok_pkgs += 1
        elif kind == "FAIL":
            fail_pkgs += 1
        elif kind == "?":
            skip_pkgs += 1
        pkg_results.append(line)
        emit(line)
        return

    # Buffer per-test diagnostic lines while a test is running.
    tkey = None
    if from_json and test:
        tkey = (pkg, test)
    elif not from_json and current_plain_test:
        tkey = (pkg, current_plain_test)
    if tkey is not None:
        if not RUN_RE.match(stripped) and stripped not in ("PASS", "FAIL"):
            append_test_buffer(tkey, line)


def handle_raw_input_line(raw_line: str) -> None:
    stripped = raw_line.strip()
    if stripped.startswith("{") and stripped.endswith("}"):
        try:
            evt = json.loads(stripped)
        except Exception:
            evt = None
        if isinstance(evt, dict) and ("Action" in evt or "Output" in evt):
            action = evt.get("Action", "")
            pkg = evt.get("Package") or evt.get("ImportPath") or ""
            test = evt.get("Test") or ""
            output = evt.get("Output") or ""
            now = time.monotonic()

            if action == "start" and pkg:
                in_flight_pkgs.setdefault(pkg, now)
            elif test:
                key = (pkg, test)
                if action == "run":
                    in_flight_pkgs.setdefault(pkg, now)
                    in_flight_tests[key] = now
                    paused_tests.discard(key)
                    reset_test_buffer(key)
                elif action == "pause":
                    paused_tests.add(key)
                    in_flight_tests.pop(key, None)
                elif action == "cont":
                    in_flight_pkgs.setdefault(pkg, now)
                    paused_tests.discard(key)
                    in_flight_tests[key] = now
                elif action in ("pass", "skip"):
                    in_flight_tests.pop(key, None)
                    paused_tests.discard(key)
                    reset_test_buffer(key)
                elif action == "fail":
                    in_flight_tests.pop(key, None)
                    paused_tests.discard(key)
                    if key not in recorded_test_fails:
                        recorded_test_fails.add(key)
                        prior = pop_test_output(key)
                        header = f"--- FAIL: {test} [{short_pkg(pkg)}]" if pkg else f"--- FAIL: {test}"
                        emit(f"[go test] {header}")
                        write_log(header + "\n")
                        fail_blocks.append(["test", [header] + prior])

            if action in ("output", "build-output") and output:
                lines = output.splitlines()
                if not lines and output.endswith("\n"):
                    lines = [""]
                for subline in lines:
                    handle_text_line(
                        subline,
                        pkg=pkg,
                        test=test,
                        from_json=True,
                        is_build=(action == "build-output"),
                    )

            if pkg and not test and action in ("pass", "fail", "skip", "build-fail"):
                in_flight_pkgs.pop(pkg, None)
                for k in [k for k in list(in_flight_tests.keys()) if k[0] == pkg]:
                    in_flight_tests.pop(k, None)
                    if action == "fail" and k not in recorded_test_fails:
                        recorded_test_fails.add(k)
                        prior = pop_test_output(k)
                        header = f"--- FAIL: {k[1]} (incomplete) [{short_pkg(pkg)}]"
                        emit(f"[go test] {header}")
                        write_log(header + "\n")
                        fail_blocks.append(["test", [header] + prior])
                for k in [k for k in list(paused_tests) if k[0] == pkg]:
                    paused_tests.discard(k)
            return

    handle_text_line(raw_line, pkg="", test="", from_json=False, is_build=False)


def maybe_emit_heartbeat(now: float) -> None:
    global last_heartbeat
    if now - last_heartbeat < heartbeat_interval:
        return
    last_heartbeat = now
    elapsed = fmt_duration(now - start_time)

    # Summarize active tests and packages, longest-running first.
    active_items = []
    covered_pkgs = set()
    for (pkg, tname), tstart in sorted(in_flight_tests.items(), key=lambda kv: kv[1]):
        dur = fmt_duration(now - tstart)
        label = f"{short_pkg(pkg)}:{tname} ({dur})" if pkg else f"{tname} ({dur})"
        active_items.append(label)
        if pkg:
            covered_pkgs.add(pkg)

    for pkg, pstart in sorted(in_flight_pkgs.items(), key=lambda kv: kv[1]):
        if pkg in covered_pkgs:
            continue
        dur = fmt_duration(now - pstart)
        active_items.append(f"{short_pkg(pkg)} ({dur})")

    counts = f"{ok_pkgs} pkg ok, {fail_pkgs} fail"
    if pass_tests > 0 or fail_tests > 0:
        counts += f", {pass_tests} tests pass"
    if active_items:
        shown = ", ".join(active_items[:5])
        if len(active_items) > 5:
            shown += f", +{len(active_items) - 5} more"
        emit(f"[go test +{elapsed}] {counts}; in flight: {shown}")
    else:
        emit(f"[go test +{elapsed}] {counts}; running...")


proc = subprocess.Popen(
    cmd,
    stdout=subprocess.PIPE,
    stderr=subprocess.STDOUT,
    close_fds=True,
)


def forward_signal(signum, _frame):
    try:
        proc.send_signal(signum)
    except OSError:
        pass


signal.signal(signal.SIGTERM, forward_signal)
signal.signal(signal.SIGINT, forward_signal)

fd = proc.stdout.fileno()
os.set_blocking(fd, False)
poll_timeout = min(0.25, max(0.02, heartbeat_interval / 4.0))
decoder = codecs.getincrementaldecoder("utf-8")(errors="replace")
pending = ""

while True:
    try:
        ready, _, _ = select.select([fd], [], [], poll_timeout)
    except InterruptedError:
        ready = []

    if ready:
        try:
            chunk = os.read(fd, 65536)
        except BlockingIOError:
            chunk = b""
        if not chunk:
            break
        pending += decoder.decode(chunk)
        while "\n" in pending:
            raw_line, pending = pending.split("\n", 1)
            handle_raw_input_line(raw_line)
            maybe_emit_heartbeat(time.monotonic())
    else:
        if proc.poll() is not None:
            # Drain any remaining bytes on the pipe.
            try:
                while True:
                    rem_chunk = os.read(fd, 65536)
                    if not rem_chunk:
                        break
                    pending += decoder.decode(rem_chunk)
            except BlockingIOError:
                pass
            break
        maybe_emit_heartbeat(time.monotonic())

pending += decoder.decode(b"", final=True)
while "\n" in pending:
    raw_line, pending = pending.split("\n", 1)
    handle_raw_input_line(raw_line)
if pending:
    for rem_line in pending.splitlines():
        handle_raw_input_line(rem_line)

status = proc.wait()

if status != 0 and in_flight_tests:
    for (pkg, tname) in list(in_flight_tests.keys()):
        key = (pkg, tname)
        if key in recorded_test_fails:
            continue
        recorded_test_fails.add(key)
        prior = pop_test_output(key)
        header = f"--- FAIL: {tname} (incomplete) [{short_pkg(pkg)}]" if pkg else f"--- FAIL: {tname} (incomplete)"
        write_log(header + "\n")
        for pline in prior:
            write_log(pline + "\n")
        fail_blocks.append(["test", [header] + prior])

log_fp.close()
if ci_mirror_fp is not None:
    try:
        ci_mirror_fp.close()
    except OSError:
        pass

emit("=== failing tests ===")
fail_lines_printed = 0
for _kind, block in fail_blocks:
    for line in block:
        if fail_lines_printed >= 500:
            break
        emit(line)
        fail_lines_printed += 1
    if fail_lines_printed >= 500:
        break

emit("=== panics ===")
panic_lines_printed = 0
for block in panic_blocks:
    for line in block:
        if panic_lines_printed >= 200:
            break
        emit(line)
        panic_lines_printed += 1
    if panic_lines_printed >= 200:
        break

emit("=== data races ===")
race_lines_printed = 0
for block in race_blocks:
    for line in block:
        if race_lines_printed >= 200:
            break
        emit(line)
        race_lines_printed += 1
    if race_lines_printed >= 200:
        break

emit("=== per-package results ===")
for line in pkg_results[:200]:
    emit(line)

if status != 0:
    emit("=== last 200 lines of the raw run ===")
    for line in tail_ring:
        emit(line)

if status < 0:
    sys.exit(128 + (-status))
sys.exit(status)
PY
