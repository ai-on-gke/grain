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

#
# Connect to a remote Grain deployment via an SSH port-forwarding tunnel
# and open the Grain web UI in your default browser.
#
# Reconnects automatically if the SSH tunnel drops.
# Works across macOS and Linux.
#
# Usage:
#   ./scripts/client.sh [options] [user@]host [ssh-options...]
#
# Options:
#   -p, --local-port PORT    Local port to bind (default: 4242 or $GRAIN_LOCAL_PORT)
#   -r, --remote-port PORT   Remote Grain UI port (default: 4242 or $GRAIN_REMOTE_PORT)
#   -n, --no-browser         Do not open the browser automatically
#   --path PATH              URL path to open in browser (default: / or $GRAIN_URL_PATH)
#   -k, --keepalive SECS     SSH ServerAliveInterval in seconds (default: 15)
#   -d, --delay SECS         Delay in seconds before reconnecting (default: 2)
#   -h, --help               Show this help message
#
# Environment variables:
#   GRAIN_HOST               Remote host if not given as an argument
#   GRAIN_LOCAL_PORT         Local port to bind (default: 4242)
#   GRAIN_REMOTE_PORT        Remote port (default: 4242)
#   GRAIN_NO_BROWSER         If set to 1 or true, disable browser opening
#   GRAIN_URL_PATH           URL path to open (default: /)
#   BROWSER                  Custom browser executable to open UI

set -uo pipefail

LOCAL_PORT="${GRAIN_LOCAL_PORT:-4242}"
REMOTE_PORT="${GRAIN_REMOTE_PORT:-4242}"
NO_BROWSER="${GRAIN_NO_BROWSER:-0}"
URL_PATH="${GRAIN_URL_PATH:-/}"
KEEPALIVE_INTERVAL=15
RECONNECT_DELAY=2
TARGET_HOST="${GRAIN_HOST:-}"
SSH_EXTRA_ARGS=()

usage() {
  cat <<'EOF'
Usage: client.sh [options] [user@]host [ssh-options...]

Connect to a remote Grain deployment via an SSH port tunnel and open the Grain UI.
Automatically reconnects if the SSH tunnel drops.

Options:
  -p, --local-port PORT    Local port to bind (default: 4242 or $GRAIN_LOCAL_PORT)
  -r, --remote-port PORT   Remote Grain UI port (default: 4242 or $GRAIN_REMOTE_PORT)
  -n, --no-browser         Do not open the browser automatically
  --path PATH              URL path to open in browser (default: / or $GRAIN_URL_PATH)
  -k, --keepalive SECS     SSH ServerAliveInterval in seconds (default: 15)
  -d, --delay SECS         Delay in seconds before reconnecting (default: 2)
  -h, --help               Show this help message

Examples:
  ./scripts/client.sh my-grain-vm
  ./scripts/client.sh -p 8080 -r 80 debian@grain-host   (an older install on 127.0.0.1:80)
  ./scripts/client.sh -r 8420 grain-host               (an older install on 127.0.0.1:8420)
  ./scripts/client.sh --no-browser user@host -i ~/.ssh/id_rsa
EOF
}

# Parse options
while [ $# -gt 0 ]; do
  case "$1" in
    -h|--help)
      usage
      exit 0
      ;;
    -p|--local-port)
      if [ $# -lt 2 ]; then
        echo "client.sh: option $1 requires a port argument" >&2
        exit 2
      fi
      LOCAL_PORT="$2"
      shift 2
      ;;
    --local-port=*)
      LOCAL_PORT="${1#*=}"
      shift
      ;;
    -r|--remote-port)
      if [ $# -lt 2 ]; then
        echo "client.sh: option $1 requires a port argument" >&2
        exit 2
      fi
      REMOTE_PORT="$2"
      shift 2
      ;;
    --remote-port=*)
      REMOTE_PORT="${1#*=}"
      shift
      ;;
    -n|--no-browser)
      NO_BROWSER=1
      shift
      ;;
    --path)
      if [ $# -lt 2 ]; then
        echo "client.sh: option $1 requires a path argument" >&2
        exit 2
      fi
      URL_PATH="$2"
      shift 2
      ;;
    --path=*)
      URL_PATH="${1#*=}"
      shift
      ;;
    -k|--keepalive)
      if [ $# -lt 2 ]; then
        echo "client.sh: option $1 requires a seconds argument" >&2
        exit 2
      fi
      KEEPALIVE_INTERVAL="$2"
      shift 2
      ;;
    --keepalive=*)
      KEEPALIVE_INTERVAL="${1#*=}"
      shift
      ;;
    -d|--delay)
      if [ $# -lt 2 ]; then
        echo "client.sh: option $1 requires a seconds argument" >&2
        exit 2
      fi
      RECONNECT_DELAY="$2"
      shift 2
      ;;
    --delay=*)
      RECONNECT_DELAY="${1#*=}"
      shift
      ;;
    --)
      shift
      if [ -z "$TARGET_HOST" ] && [ $# -gt 0 ]; then
        TARGET_HOST="$1"
        shift
      fi
      while [ $# -gt 0 ]; do
        SSH_EXTRA_ARGS+=("$1")
        shift
      done
      break
      ;;
    -*)
      echo "client.sh: unrecognized option: $1" >&2
      echo "Run './scripts/client.sh --help' for usage." >&2
      exit 2
      ;;
    *)
      TARGET_HOST="$1"
      shift
      # All arguments following the target host are treated as SSH arguments
      while [ $# -gt 0 ]; do
        SSH_EXTRA_ARGS+=("$1")
        shift
      done
      break
      ;;
  esac
done

if [ -z "$TARGET_HOST" ]; then
  echo "client.sh: missing target host" >&2
  echo "Run './scripts/client.sh --help' for usage." >&2
  exit 2
fi

# Ensure URL_PATH begins with /
case "$URL_PATH" in
  /*) ;;
  *) URL_PATH="/$URL_PATH" ;;
esac

# Normalize NO_BROWSER
case "$NO_BROWSER" in
  1|true|TRUE|yes|YES) NO_BROWSER=1 ;;
  *) NO_BROWSER=0 ;;
esac

# Check if TARGET_HOST has host:port format (and remote port wasn't overridden)
if [[ "$TARGET_HOST" =~ ^(.*):([0-9]+)$ ]]; then
  TARGET_HOST="${BASH_REMATCH[1]}"
  REMOTE_PORT="${BASH_REMATCH[2]}"
fi

# Function to test whether the local port is open/listening
is_port_open() {
  local port="$1"
  # 1. Try bash built-in /dev/tcp
  if (exec 3<>/dev/tcp/127.0.0.1/"$port") 2>/dev/null; then
    exec 3>&- 2>/dev/null || true
    exec 3<&- 2>/dev/null || true
    return 0
  fi
  # 2. Try nc (netcat)
  if command -v nc >/dev/null 2>&1; then
    if nc -z -w 1 127.0.0.1 "$port" >/dev/null 2>&1; then
      return 0
    fi
  fi
  # 3. Try curl
  if command -v curl >/dev/null 2>&1; then
    if curl -s -m 1 -o /dev/null "http://127.0.0.1:$port" >/dev/null 2>&1; then
      return 0
    fi
  fi
  return 1
}

# Function to open URL in browser
open_browser() {
  local url="$1"
  if [ "$NO_BROWSER" -eq 1 ]; then
    return 0
  fi

  # Custom BROWSER variable
  if [ -n "${BROWSER:-}" ] && command -v "$BROWSER" >/dev/null 2>&1; then
    "$BROWSER" "$url" >/dev/null 2>&1 &
    return 0
  fi

  # macOS
  if [ "$(uname -s 2>/dev/null)" = "Darwin" ] && command -v open >/dev/null 2>&1; then
    open "$url" >/dev/null 2>&1 &
    return 0
  fi

  # Linux / FreeDesktop
  if command -v xdg-open >/dev/null 2>&1; then
    xdg-open "$url" >/dev/null 2>&1 &
    return 0
  fi

  # Fallback Linux browsers
  for b in sensible-browser x-www-browser google-chrome chromium firefox; do
    if command -v "$b" >/dev/null 2>&1; then
      "$b" "$url" >/dev/null 2>&1 &
      return 0
    fi
  done

  echo "[grain-client] Could not detect a browser to launch. Please open: $url"
  return 0
}

CURRENT_SSH_PID=""
CURRENT_SLEEP_PID=""
INTERRUPTED=0

cleanup() {
  local sig="${1:-EXIT}"
  trap - INT TERM EXIT HUP
  if [ -n "$CURRENT_SSH_PID" ] && kill -0 "$CURRENT_SSH_PID" 2>/dev/null; then
    kill -TERM "$CURRENT_SSH_PID" 2>/dev/null || true
    wait "$CURRENT_SSH_PID" 2>/dev/null || true
  fi
  if [ -n "$CURRENT_SLEEP_PID" ] && kill -0 "$CURRENT_SLEEP_PID" 2>/dev/null; then
    kill -TERM "$CURRENT_SLEEP_PID" 2>/dev/null || true
  fi
  if [ "$sig" = "INT" ] || [ "$sig" = "TERM" ] || [ "$INTERRUPTED" -eq 1 ]; then
    echo -e "\n[grain-client] Stopped."
    exit 130
  fi
}

trap 'INTERRUPTED=1; cleanup INT' INT
trap 'INTERRUPTED=1; cleanup TERM' TERM
trap 'cleanup EXIT' EXIT

TARGET_URL="http://localhost:${LOCAL_PORT}${URL_PATH}"
browser_opened=0

echo "[grain-client] Forwarding localhost:${LOCAL_PORT} -> ${TARGET_HOST}:${REMOTE_PORT}"
echo "[grain-client] Target UI: ${TARGET_URL}"
echo "[grain-client] Press Ctrl+C to disconnect and stop."

while [ "$INTERRUPTED" -eq 0 ]; do
  # Build SSH command
  ssh_cmd=(
    ssh
    -N
    -o "ExitOnForwardFailure=yes"
    -o "ServerAliveInterval=${KEEPALIVE_INTERVAL}"
    -o "ServerAliveCountMax=3"
    -o "TCPKeepAlive=yes"
    -L "${LOCAL_PORT}:localhost:${REMOTE_PORT}"
  )
  if [ ${#SSH_EXTRA_ARGS[@]} -gt 0 ]; then
    ssh_cmd+=("${SSH_EXTRA_ARGS[@]}")
  fi
  ssh_cmd+=("$TARGET_HOST")

  echo "[grain-client] Establishing SSH tunnel to ${TARGET_HOST}..."

  # Start SSH in background
  "${ssh_cmd[@]}" &
  CURRENT_SSH_PID=$!

  # Poll for port readiness (up to 10s)
  ready=0
  for _ in $(seq 1 50); do
    if ! kill -0 "$CURRENT_SSH_PID" 2>/dev/null; then
      # SSH died immediately
      break
    fi
    if is_port_open "$LOCAL_PORT"; then
      ready=1
      break
    fi
    sleep 0.2
  done

  if [ "$ready" -eq 1 ]; then
    if [ "$browser_opened" -eq 0 ]; then
      echo "[grain-client] Tunnel established! Opening browser: ${TARGET_URL}"
      open_browser "$TARGET_URL"
      browser_opened=1
    else
      echo "[grain-client] Tunnel re-established at ${TARGET_URL}"
    fi
  fi

  # Wait for SSH process to exit
  wait "$CURRENT_SSH_PID"
  ssh_status=$?
  CURRENT_SSH_PID=""

  if [ "$INTERRUPTED" -eq 1 ]; then
    break
  fi

  echo "[grain-client] SSH tunnel disconnected (exit status: ${ssh_status})."
  echo "[grain-client] Reconnecting in ${RECONNECT_DELAY}s... (Press Ctrl+C to cancel)"

  # Interruptible sleep
  sleep "$RECONNECT_DELAY" &
  CURRENT_SLEEP_PID=$!
  wait "$CURRENT_SLEEP_PID" 2>/dev/null || true
  CURRENT_SLEEP_PID=""
done
