#!/usr/bin/env bash
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

# Shared environment for docs/test-runs/2026-11-03-hostd-routing.md:
# a real `grain daemon` on loopback with host sandboxes, the co-located
# hostd `local` on a unix socket, and two remote hostds `ws-a` and
# `ws-b` dialing in over /api/hostd/connect, each in its own HOME with its
# own root-owned allowlist (distinct labels: and capacity:). Every hostd's
# agy entry is tests/installer/fakeagy, so a run's agent CLI is a real
# process started by whichever hostd the run was routed to.
#
#   env.sh up            build, register and start everything
#   env.sh daemon-up     (re)start only the daemon
#   env.sh daemon-down   stop only the daemon (SIGTERM)
#   env.sh hostd-up N    (re)start remote hostd N (ws-a, ws-b)
#   env.sh hostd-down N  stop remote hostd N
#   env.sh capacity N C  rewrite N's allowlist with capacity: C
#   env.sh down          stop everything
#
# Sourcing it (". env.sh") just sets GRAIN_SERVER/GRAIN_TOKEN and helpers.
set -euo pipefail

ROOT=${HR_ROOT:-/home/debian/hr}
REPO=${HR_REPO:-/home/debian/work}
BIN=$ROOT/bin
PORT=${HR_PORT:-4300}
export GRAIN_SERVER=http://127.0.0.1:$PORT
DATA=$ROOT/daemon
LOG=$ROOT/logs
mkdir -p "$BIN" "$DATA" "$LOG" "$ROOT/reports"

tok() { cat "$DATA/local-api-token" 2>/dev/null || true; }
GRAIN_TOKEN=$(tok)
export GRAIN_TOKEN
api() { # api METHOD PATH [JSON]
  curl -sS -X "$1" -H "Authorization: Bearer $(tok)" -H 'Content-Type: application/json' \
    ${3:+--data "$3"} "$GRAIN_SERVER$2"
}
db() { sqlite3 -header -column "$DATA/store/grain.db" "$@"; }

allowlist() { # allowlist NAME HOME CAPACITY LABELS-YAML
  local name=$1 home=$2 cap=$3 labels=$4
  cat <<YAML
# hostd ${name} (docs/test-runs/2026-11-03-hostd-routing)
capacity: ${cap}
labels: ${labels}
clis:
  agy:
    path: ${BIN}/fakeagy
    paths_under: [${DATA}, ${ROOT}/sandboxes, ${home}]
    cwd_under: [${DATA}, ${ROOT}/sandboxes, ${home}]
    env: [HOME, AGY_CLI_DISABLE_AUTO_UPDATE, GEMINI_API_KEY, GOOGLE_API_KEY]
    set_env:
      GRAIN_LOCAL_API_TOKEN_FILE: ${DATA}/local-api-token
YAML
}

write_allowlist() { # NAME HOME CAP LABELS
  local f=/etc/grain/hostd-$1.yaml
  sudo mkdir -p /etc/grain
  allowlist "$@" | sudo tee "$f" >/dev/null
  sudo chown root:root "$f"
  sudo chmod 0644 "$f"
}

build() {
  (cd "$REPO" && go build -o "$BIN/grain" ./cmd/grain && go build -o "$BIN/fakeagy" ./tests/installer/fakeagy)
}

daemon_up() {
  daemon_down || true
  mkdir -p "$ROOT/sandboxes"
  setsid nohup "$BIN/grain" daemon -data-dir "$DATA" -sandbox-dir "$ROOT/sandboxes" \
    -ui-addr "127.0.0.1:$PORT" -sandbox-port-base=0 -host-sandboxes \
    -hostd-socket "$ROOT/local/hostd.sock" -agy-run host -agy-path "$BIN/fakeagy" \
    -poll-interval 3s \
    >>"$LOG/daemon.log" 2>&1 &
  echo $! >"$ROOT/daemon.pid"
  for _ in $(seq 60); do
    [ "$(curl -s -o /dev/null -w "%{http_code}" "$GRAIN_SERVER/")" != 000 ] && return 0
    sleep 1
  done
  echo "daemon did not come up; tail of $LOG/daemon.log:" >&2
  tail -30 "$LOG/daemon.log" >&2
  return 1
}

daemon_down() {
  [ -f "$ROOT/daemon.pid" ] || return 0
  local pid; pid=$(cat "$ROOT/daemon.pid")
  kill "$pid" 2>/dev/null || return 0
  for _ in $(seq 30); do kill -0 "$pid" 2>/dev/null || return 0; sleep 1; done
  kill -9 "$pid" 2>/dev/null || true
}

local_hostd_up() {
  mkdir -p "$ROOT/local"
  write_allowlist local "$ROOT/local" 0 '{site: local}'
  pkill -f "hostd -socket $ROOT/local/hostd.sock" 2>/dev/null || true
  setsid nohup "$BIN/grain" hostd -socket "$ROOT/local/hostd.sock" -allowlist /etc/grain/hostd-local.yaml \
    -state-dir "$ROOT/local/state" >>"$LOG/hostd-local.log" 2>&1 &
  echo $! >"$ROOT/local/pid"
  sleep 1
}

hostd_home() { echo "$ROOT/$1/home"; }

hostd_up() { # NAME
  local n=$1 home; home=$(hostd_home "$n")
  hostd_down "$n" || true
  mkdir -p "$home"
  HOME=$home setsid nohup "$BIN/grain" hostd -server "$GRAIN_SERVER" -name "$n" -token-file "$ROOT/$n/token" \
    -socket "$ROOT/$n/hostd.sock" -allowlist "/etc/grain/hostd-$n.yaml" -state-dir "$ROOT/$n/state" \
    >>"$LOG/hostd-$n.log" 2>&1 &
  echo $! >"$ROOT/$n/pid"
}

hostd_down() {
  [ -f "$ROOT/$1/pid" ] || return 0
  kill "$(cat "$ROOT/$1/pid")" 2>/dev/null || true
  sleep 1
}

register() { # NAME OWNER LABEL...
  local n=$1 owner=$2; shift 2
  mkdir -p "$ROOT/$n"
  [ -s "$ROOT/$n/token" ] && return 0
  local args=()
  for l in "$@"; do args+=(-label "$l"); done
  GRAIN_TOKEN=$(tok) "$BIN/grain" hostds register -owner "$owner" "${args[@]}" -token-file "$ROOT/$n/token" "$n"
}

capacity() { # NAME CAP
  case $1 in
  ws-a) write_allowlist ws-a "$(hostd_home ws-a)" "$2" '{gpu: "yes", zone: a}' ;;
  ws-b) write_allowlist ws-b "$(hostd_home ws-b)" "$2" '{gpu: "yes", zone: b}' ;;
  esac
}

up() {
  build
  local_hostd_up
  daemon_up
  register ws-a alice@example.com gpu=yes zone=a
  register ws-b bob@example.com gpu=yes zone=b
  capacity ws-a "${WS_A_CAPACITY:-1}"
  capacity ws-b "${WS_B_CAPACITY:-2}"
  hostd_up ws-a
  hostd_up ws-b
  sleep 3
  GRAIN_TOKEN=$(tok) "$BIN/grain" hostds list
}

down() {
  hostd_down ws-a; hostd_down ws-b; daemon_down
  if [ -f "$ROOT/local/pid" ]; then kill "$(cat "$ROOT/local/pid")" 2>/dev/null || true; fi
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  cmd=${1:-up}; shift || true
  case $cmd in
  up) up ;;
  down) down ;;
  daemon-up) daemon_up ;;
  daemon-down) daemon_down ;;
  hostd-up) hostd_up "$1" ;;
  hostd-down) hostd_down "$1" ;;
  capacity) capacity "$1" "$2" ;;
  *) echo "unknown: $cmd" >&2; exit 2 ;;
  esac
fi
