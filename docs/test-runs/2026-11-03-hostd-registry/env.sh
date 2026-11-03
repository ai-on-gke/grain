#!/bin/bash
# Shared environment for the 2026-11-03 hostd-registry walkthrough (§1).
# Source it: it defines the paths and helpers the other scripts use.
#
#   R=/var/tmp/hr . docs/test-runs/2026-11-03-hostd-registry/env.sh
#   start_daemon [extra daemon flags...]   # grain daemon + co-located hostd "local"
#   register ws-a owner@example.com gpu=yes zone=a
#   start_remote ws-a                      # grain hostd -server ... -name ws-a in HOME=$R/ws-a
#   stop_remote ws-a
#
# Needs: grain on PATH (built with the embedded UI), sudo (allowlists must
# be root-owned), curl, jq.
set -u
R=${R:-/var/tmp/hr}
PORT=${PORT:-4310}
export GRAIN_SERVER=http://127.0.0.1:$PORT
DATA=$R/daemon/data
mkdir -p "$R/daemon"

# allowlist DIR LABELS CAPACITY: a root-owned allowlist offering /bin/sh.
allowlist() {
  local dir=$1 labels=$2 cap=$3
  mkdir -p "$dir"
  sudo tee "$dir/allowlist.yaml" >/dev/null <<EOF
capacity: $cap
labels: {$labels}
clis:
  sh:
    path: /bin/sh
EOF
  sudo chown root:root "$dir/allowlist.yaml"
  sudo chmod 0644 "$dir/allowlist.yaml"
}

# start_daemon: the co-located hostd "local" on a socket, then the daemon.
# NO_LOCAL=1 leaves the daemon no socket (the shared -hostd-token-file needs that).
start_daemon() {
  allowlist "$R/local" "role: local" 4
  HOME=$R/local nohup grain hostd -socket "$R/local/hostd.sock" -allowlist "$R/local/allowlist.yaml" \
    -state-dir "$R/local/state" >"$R/local/hostd.log" 2>&1 &
  echo $! >"$R/local/pid"
  for _ in $(seq 50); do [ -S "$R/local/hostd.sock" ] && break; sleep 0.1; done
  local sock=$R/local/hostd.sock
  [ -n "${NO_LOCAL:-}" ] && sock=
  HOME=$R/daemon nohup grain daemon -data-dir "$DATA" -ui-addr 127.0.0.1:$PORT -sandbox-port-base=0 \
    -hostd-socket="$sock" -poll-interval 2s -disable-native-sandboxes "$@" >"$R/daemon/daemon.log" 2>&1 &
  echo $! >"$R/daemon/pid"
  for _ in $(seq 120); do
    curl -fsS -o /dev/null "$GRAIN_SERVER/api/health" 2>/dev/null && break
    sleep 0.5
  done
  export GRAIN_TOKEN
  GRAIN_TOKEN=$(access_code)
}

stop_daemon() {
  kill "$(cat "$R/daemon/pid")" 2>/dev/null
  kill "$(cat "$R/local/pid")" 2>/dev/null
  sleep 2
}

# access_code: the daemon's UI access code.
access_code() {
  local f
  for f in "$DATA/local-api-token"; do
    [ -f "$f" ] && { tr -d '\n' <"$f"; return; }
  done
  grep -o 'access code[^ ]* [A-Za-z0-9_-]\{16,\}' "$R/daemon/daemon.log" | head -1 | awk '{print $NF}'
}

# register NAME OWNER LABEL...: grain hostds register, token to $R/NAME/token.
register() {
  local name=$1 owner=$2; shift 2
  mkdir -p "$R/$name"
  local args=()
  for l in "$@"; do args+=(-label "$l"); done
  grain hostds register "$name" ${owner:+-owner "$owner"} "${args[@]}" -token-file "$R/$name/token"
}

# start_remote NAME [extra hostd flags]: a remote hostd in HOME=$R/NAME with
# its own allowlist (labels name=NAME, capacity 1), dialing the daemon
# (or $HOSTD_SERVER).
start_remote() {
  local name=$1; shift
  [ -f "$R/$name/allowlist.yaml" ] || allowlist "$R/$name" "name: $name" 1
  HOME=$R/$name nohup grain hostd -server "${HOSTD_SERVER:-$GRAIN_SERVER}" -name "${HOSTD_NAME:-$name}" \
    -token-file "${HOSTD_TOKEN_FILE:-$R/$name/token}" -socket "$R/$name/hostd.sock" \
    -allowlist "$R/$name/allowlist.yaml" -state-dir "$R/$name/state" "$@" >>"$R/$name/hostd.log" 2>&1 &
  echo $! >"$R/$name/pid"
}

stop_remote() { kill "$(cat "$R/$1/pid")" 2>/dev/null; sleep 1; }

api() { # api METHOD PATH [JSON]: status line then body
  local m=$1 p=$2 d=${3:-}
  curl -sS -X "$m" -H "Authorization: Bearer $GRAIN_TOKEN" -H 'Content-Type: application/json' \
    ${d:+--data "$d"} -w '\nHTTP %{http_code}\n' "$GRAIN_SERVER$p"
}

# connect TOKEN [NAME] [extra curl args]: a bare WebSocket-upgrade attempt at
# /api/hostd/connect, printing the status and body (101 means admitted).
connect() {
  local tok=$1 name=${2:-}; shift; [ $# -gt 0 ] && shift
  curl -sS -m 3 -o - -w '\nHTTP %{http_code}\n' -H 'Upgrade: websocket' -H 'Connection: Upgrade' \
    -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' -H 'Sec-WebSocket-Version: 13' \
    -H "X-Grain-Hostd-Token: $tok" ${name:+-H "X-Grain-Hostd-Name: $name"} "$@" \
    "${CONNECT_BASE:-$GRAIN_SERVER}/api/hostd/connect" 2>&1 | head -c 600
}

hostds() { grain hostds list; }

export GRAIN_DATA_DIR=$DATA
[ -f "$DATA/local-api-token" ] && export GRAIN_TOKEN=$(access_code)
