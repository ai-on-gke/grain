#!/bin/sh
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

# grain hostd's installer for a machine that dials in to a grain daemon
# (`grain hostd -server`), served by that daemon at /api/hostd/install.sh
# (pkg/ui/hostd_install.go). Register the hostd first in Settings -> Hostds,
# which shows its token once, then run this on the machine as root:
#
#   sudo sh -c "$(curl -fsSL <daemon>/api/hostd/install.sh)" grain-hostd-install --name <name>
#
# and paste the token when it asks. Unattended, give the token on stdin
# or in $GRAIN_HOSTD_TOKEN instead -- never on the command line or in the
# URL. It:
#
#   - finds a grain of the daemon's version on this machine, or takes one
#     out of the daemon's image (docker or podman) or downloads one whose
#     SHA-256 you name, into /usr/local/lib/grain/hostd/<version>/grain;
#   - writes the token to /etc/grain/hostd-token, readable by the hostd
#     user alone;
#   - writes /etc/grain/hostd.yaml, hostd's allowlist, with --capacity and
#     --label when given (an existing one is kept as it is);
#   - installs and starts grain-hostd.service, running
#     `grain hostd -server <daemon> -token-file /etc/grain/hostd-token -name <name>`
#     as the hostd user (never root: hostd refuses to run as root).
#
# Re-running it replaces the token and the unit and keeps the allowlist,
# so it is also how a rotated token reaches the machine.

set -eu
# No value this script handles is a pattern: --label's are split on
# spaces, and never expanded as file names.
set -f

# Filled in by the daemon that served this script: the URL it was asked
# for it at, its own build, and the image it runs from ("" when it does
# not know one).
GRAIN_DAEMON_URL='@GRAIN_DAEMON_URL@'
GRAIN_DAEMON_RELEASE='@GRAIN_DAEMON_RELEASE@'
GRAIN_DAEMON_IMAGE='@GRAIN_DAEMON_IMAGE@'

VERSIONS_DIR=/usr/local/lib/grain/hostd
CONFIG_DIR=/etc/grain
ALLOWLIST="$CONFIG_DIR/hostd.yaml"
TOKEN_FILE="$CONFIG_DIR/hostd-token"
UNIT=/etc/systemd/system/grain-hostd.service
SOCKET_DIR=/run/grain-hostd
STATE_DIR=/var/lib/grain-hostd
# The first line of every unit this script writes, so a re-run knows the
# unit is its own and never overwrites the one scripts/setup.sh writes
# for a co-located hostd.
UNIT_MARKER='# Written by grain'"'"'s /api/hostd/install.sh.'

usage() {
  cat <<EOF
Usage: grain-hostd-install --name NAME [options]

Installs grain hostd on this machine as grain-hostd.service, dialing in to
the grain daemon at ${GRAIN_DAEMON_URL:-(--server)}. Run as root. The hostd's
token is read from \$GRAIN_HOSTD_TOKEN, or else from stdin (asked for, without
echoing it, when stdin is a terminal).

Options:
  --name NAME          the hostd's name in the daemon's registry (Settings -> Hostds; required)
  --user USER          the account hostd runs as and the CLIs it starts run as
                       (default: \$SUDO_USER; never root)
  --capacity N         how many agent CLI runs at once this hostd takes (hostd.yaml's capacity:)
  --label KEY=VALUE    a label hostd describes itself with (hostd.yaml's labels:); repeatable
  --server URL         the daemon to dial (default: ${GRAIN_DAEMON_URL:-none})
  --grain-bin PATH     a grain binary to install rather than looking for one
  --image REF          the grain image to take grain out of (default: ${GRAIN_DAEMON_IMAGE:-none})
  --binary-url URL     download grain from URL instead; needs --sha256
  --sha256 HEX         the SHA-256 the downloaded binary must have
  --any-version        accept a grain whose version is not the daemon's (${GRAIN_DAEMON_RELEASE:-unstamped})
  --iap-audience AUD   the OAuth client ID of an Identity-Aware Proxy in front of the daemon
  --ca-file PATH       a PEM CA to verify the daemon's certificate against
  --no-start           write everything but do not start grain-hostd.service
  -h, --help           show this help

Environment: GRAIN_HOSTD_TOKEN, GRAIN_HOSTD_NAME, GRAIN_HOSTD_USER, GRAIN_SERVER,
GRAIN_BIN, GRAIN_IMAGE, GRAIN_BINARY_URL, GRAIN_BINARY_SHA256.
EOF
}

log() {
  echo "grain-hostd-install: $*" >&2
}

die() {
  log "$*"
  exit 1
}

need_arg() {
  [ "$2" -ge 2 ] || die "$1 needs a value"
}

NAME="${GRAIN_HOSTD_NAME:-}"
HOSTD_USER="${GRAIN_HOSTD_USER:-${SUDO_USER:-}}"
CAPACITY=""
LABELS=""
SERVER="${GRAIN_SERVER:-$GRAIN_DAEMON_URL}"
GRAIN_BIN="${GRAIN_BIN:-}"
IMAGE="${GRAIN_IMAGE:-$GRAIN_DAEMON_IMAGE}"
BINARY_URL="${GRAIN_BINARY_URL:-}"
BINARY_SHA256="${GRAIN_BINARY_SHA256:-}"
ANY_VERSION=0
IAP_AUDIENCE=""
CA_FILE=""
START=1

parse_args() {
  while [ $# -gt 0 ]; do
    case "$1" in
      -h|--help) usage; exit 0 ;;
      --name) need_arg "$1" $#; NAME="$2"; shift 2 ;;
      --user) need_arg "$1" $#; HOSTD_USER="$2"; shift 2 ;;
      --capacity) need_arg "$1" $#; CAPACITY="$2"; shift 2 ;;
      --label) need_arg "$1" $#; LABELS="$LABELS $2"; shift 2 ;;
      --server) need_arg "$1" $#; SERVER="$2"; shift 2 ;;
      --grain-bin) need_arg "$1" $#; GRAIN_BIN="$2"; shift 2 ;;
      --image) need_arg "$1" $#; IMAGE="$2"; shift 2 ;;
      --binary-url) need_arg "$1" $#; BINARY_URL="$2"; shift 2 ;;
      --sha256) need_arg "$1" $#; BINARY_SHA256="$2"; shift 2 ;;
      --any-version) ANY_VERSION=1; shift ;;
      --iap-audience) need_arg "$1" $#; IAP_AUDIENCE="$2"; shift 2 ;;
      --ca-file) need_arg "$1" $#; CA_FILE="$2"; shift 2 ;;
      --no-start) START=0; shift ;;
      *) usage >&2; die "unknown option: $1" ;;
    esac
  done
}

# --- what was asked for ----------------------------------------------------
#
# Everything below reaches a systemd unit or YAML, so each value is held
# to a plain character set rather than quoted for either.

check_request() {
  [ "$(id -u)" = 0 ] || die "run this as root (sudo): it writes $CONFIG_DIR and $UNIT"
  [ "$(uname -s)" = Linux ] || die "grain hostd's service is a systemd unit: this installs it on Linux only"
  command -v systemctl >/dev/null 2>&1 || die "systemctl not found: grain-hostd.service needs systemd"

  [ -n "$NAME" ] || die "--name is required: the hostd's name in Settings -> Hostds"
  case "$NAME" in
    *[!a-z0-9-]*|-*|*-) die "--name $NAME is not a hostd name: lowercase letters, digits and '-', at most 63" ;;
  esac
  [ "${#NAME}" -le 63 ] || die "--name $NAME is longer than 63 characters"

  case "$SERVER" in
    http://*|https://*) ;;
    "") die "--server is required: the URL of the grain daemon" ;;
    *) die "--server $SERVER is not an http:// or https:// URL" ;;
  esac
  case "$SERVER" in
    *[!]A-Za-z0-9._~:/@[-]*) die "--server $SERVER holds a character grain-hostd.service cannot carry: give the daemon's plain URL" ;;
  esac
  SERVER="${SERVER%/}"

  [ -n "$HOSTD_USER" ] || die "--user is required when not run through sudo: the account hostd runs as"
  case "$HOSTD_USER" in
    *[!A-Za-z0-9._-]*|-*) die "--user $HOSTD_USER is not a plain user name" ;;
  esac
  id -u "$HOSTD_USER" >/dev/null 2>&1 || die "no user $HOSTD_USER on this machine"
  [ "$(id -u "$HOSTD_USER")" != 0 ] || die "--user $HOSTD_USER is root, and grain hostd refuses to run as root"
  HOSTD_GROUP="$(id -gn "$HOSTD_USER")"

  case "$CAPACITY" in
    "") ;;
    *[!0-9]*) die "--capacity $CAPACITY is not a whole number" ;;
  esac

  for label in $LABELS; do
    case "$label" in
      *=*=*|=*|*=) die "--label $label is not KEY=VALUE" ;;
      *=*) ;;
      *) die "--label $label is not KEY=VALUE" ;;
    esac
    case "$label" in
      *[!A-Za-z0-9._/=:@+-]*) die "--label $label: keys and values are letters, digits and ._/:@+-" ;;
    esac
  done

  for value in "$IAP_AUDIENCE" "$CA_FILE"; do
    case "$value" in
      *[!A-Za-z0-9._/:@+-]*) die "$value holds a character grain-hostd.service cannot carry" ;;
    esac
  done
  if [ -n "$CA_FILE" ]; then
    [ -r "$CA_FILE" ] || die "--ca-file $CA_FILE cannot be read"
  fi

  if [ -e "$UNIT" ] && [ "$(head -n1 "$UNIT")" != "$UNIT_MARKER" ]; then
    die "$UNIT is not one this script wrote (scripts/setup.sh's co-located hostd?); remove it first if this machine should dial in instead"
  fi
}

# --- the token ---------------------------------------------------------------

read_token() {
  if [ -n "${GRAIN_HOSTD_TOKEN:-}" ]; then
    TOKEN="$GRAIN_HOSTD_TOKEN"
  elif [ -t 0 ]; then
    printf 'grain hostd token for %s (Settings -> Hostds; not echoed): ' "$NAME" >&2
    stty -echo
    trap 'stty echo' EXIT
    trap 'stty echo; exit 130' INT
    IFS= read -r TOKEN || TOKEN=""
    stty echo
    trap - EXIT INT
    echo >&2
  else
    # Piped into sh (curl | sh) stdin is this script itself, read to its
    # end before main runs: the read finds nothing, and says so.
    IFS= read -r TOKEN || true
  fi
  TOKEN="$(printf '%s' "${TOKEN:-}" | tr -d ' \t\r\n')"
  [ -n "$TOKEN" ] || die "no token: give it on stdin or in \$GRAIN_HOSTD_TOKEN (not as an argument), or run this from a terminal to be asked"
}

# --- grain -------------------------------------------------------------------

# build_of prints the build a grain binary says it is (`grain hostd
# -build`), and fails for one that is not a grain with a hostd.
build_of() {
  "$1" hostd -build 2>/dev/null
}

# usable reports whether grain binary $1 is one to install: a grain hostd
# of the daemon's version, or any grain hostd with --any-version or a
# daemon that carries no version of its own.
usable() {
  [ -x "$1" ] || return 1
  build="$(build_of "$1")" || return 1
  if [ "$ANY_VERSION" = 1 ] || [ -z "$GRAIN_DAEMON_RELEASE" ] || [ "$build" = "$GRAIN_DAEMON_RELEASE" ]; then
    return 0
  fi
  log "$1 is grain ${build:-(unstamped)}, not the daemon's $GRAIN_DAEMON_RELEASE"
  return 1
}

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    openssl dgst -sha256 -r "$1" | awk '{print $1}'
  fi
}

# from_image takes /usr/local/bin/grain out of $IMAGE, with docker or podman.
from_image() {
  [ -n "$IMAGE" ] || return 1
  case "$IMAGE" in
    *[!A-Za-z0-9._/:@-]*) die "--image $IMAGE is not an image reference" ;;
  esac
  runtime=""
  for r in docker podman; do
    if command -v "$r" >/dev/null 2>&1; then
      runtime="$r"
      break
    fi
  done
  if [ -z "$runtime" ]; then
    log "neither docker nor podman is installed, so grain cannot be taken out of $IMAGE"
    return 1
  fi
  log "taking grain out of $IMAGE with $runtime"
  cid="$("$runtime" create "$IMAGE")" || return 1
  if ! "$runtime" cp "$cid:/usr/local/bin/grain" "$WORK/grain" >/dev/null; then
    "$runtime" rm "$cid" >/dev/null 2>&1 || true
    return 1
  fi
  "$runtime" rm "$cid" >/dev/null 2>&1 || true
  chmod 0755 "$WORK/grain"
  usable "$WORK/grain"
}

# from_url downloads $BINARY_URL and refuses it unless it has $BINARY_SHA256.
from_url() {
  [ -n "$BINARY_URL" ] || return 1
  [ -n "$BINARY_SHA256" ] || die "--binary-url needs --sha256: a downloaded grain is installed only once its checksum matches"
  log "downloading grain from $BINARY_URL"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --retry 2 -o "$WORK/grain" "$BINARY_URL" || return 1
  elif command -v wget >/dev/null 2>&1; then
    wget -q -O "$WORK/grain" "$BINARY_URL" || return 1
  else
    die "neither curl nor wget is installed to download $BINARY_URL"
  fi
  got="$(sha256_of "$WORK/grain")"
  want="$(printf '%s' "$BINARY_SHA256" | tr 'A-F' 'a-f')"
  [ "$got" = "$want" ] || die "$BINARY_URL has SHA-256 $got, not $want: not installing it"
  chmod 0755 "$WORK/grain"
  usable "$WORK/grain"
}

find_grain() {
  if [ -n "$GRAIN_BIN" ]; then
    usable "$GRAIN_BIN" || die "--grain-bin $GRAIN_BIN is not a grain hostd this daemon takes (--any-version to install it anyway)"
    FOUND="$GRAIN_BIN"
    return 0
  fi
  for candidate in "$VERSIONS_DIR/current/grain" "$(command -v grain 2>/dev/null || true)" /usr/local/bin/grain; do
    if [ -n "$candidate" ] && usable "$candidate"; then
      FOUND="$candidate"
      return 0
    fi
  done
  if from_image || from_url; then
    FOUND="$WORK/grain"
    return 0
  fi
  die "found no grain ${GRAIN_DAEMON_RELEASE:-hostd} to install: name one with --grain-bin, an image with --image, or --binary-url with --sha256"
}

install_grain() {
  FOUND=""
  find_grain
  BUILD="$(build_of "$FOUND")"
  case "$BUILD" in
    ""|*[!A-Za-z0-9._+-]*) BUILD=unstamped ;;
  esac

  # A root-owned copy in its own version directory, and current pointed at
  # it by rename, the layout scripts/setup.sh gives a co-located hostd.
  install -d -m0755 -o root -g root "$VERSIONS_DIR" "$VERSIONS_DIR/$BUILD"
  if [ "$FOUND" != "$VERSIONS_DIR/$BUILD/grain" ]; then
    install -m0755 -o root -g root "$FOUND" "$VERSIONS_DIR/$BUILD/.grain.new"
    mv -f "$VERSIONS_DIR/$BUILD/.grain.new" "$VERSIONS_DIR/$BUILD/grain"
  fi
  ln -sfn "$BUILD" "$VERSIONS_DIR/.current.new"
  mv -fT "$VERSIONS_DIR/.current.new" "$VERSIONS_DIR/current"
  GRAIN="$VERSIONS_DIR/current/grain"
  log "grain $BUILD is at $VERSIONS_DIR/$BUILD/grain"
}

# --- /etc/grain ----------------------------------------------------------------

write_token() {
  install -d -m0755 -o root -g root "$CONFIG_DIR"
  [ ! -L "$TOKEN_FILE" ] || die "$TOKEN_FILE is a symlink; not writing the token through it"
  # Created 0600 under a 077 umask and handed to the hostd user before it
  # is renamed into place, so nobody else can read it at any moment.
  tmp="$(umask 077; mktemp "$CONFIG_DIR/.hostd-token.XXXXXX")"
  (umask 077; printf '%s\n' "$TOKEN" > "$tmp")
  chown "$HOSTD_USER:$HOSTD_GROUP" "$tmp"
  chmod 0400 "$tmp"
  mv -f "$tmp" "$TOKEN_FILE"
  TOKEN=""
  log "the token is in $TOKEN_FILE, readable by $HOSTD_USER alone"
}

allowlist_content() {
  cat <<YAML
# grain hostd's allowlist: every program the grain daemon at
# $SERVER may start on this machine, and what each request for it may
# carry. Written once by the daemon's /api/hostd/install.sh; a re-run keeps
# your edits. hostd re-reads it on every request, and loads it only while
# it is owned by root and writable by nobody else.
YAML
  if [ -n "$CAPACITY" ]; then
    echo "# How many agent CLI runs at once this hostd takes; 0 is no limit."
    echo "capacity: $CAPACITY"
  fi
  if [ -n "$LABELS" ]; then
    echo "# What this hostd says about itself, beside the registry's labels for it."
    echo "labels:"
    for label in $LABELS; do
      printf '  "%s": "%s"\n' "${label%%=*}" "${label#*=}"
    done
  fi
  cat <<'YAML'
# An agent CLI this machine runs for the daemon, for example:
#  agy:
#    path: /usr/local/bin/agy
#    paths_under: [/var/lib/grain]
#    cwd_under: [/var/lib/grain]
#    env: [HOME]
clis: {}
# A stdio capability server a capability config starts with run: host;
# grain's own are added below by `grain hostd -ensure-builtins`.
capabilities:
YAML
}

write_allowlist() {
  [ ! -L "$ALLOWLIST" ] || die "$ALLOWLIST is a symlink; grain hostd refuses one, and this script will not write through it"
  if [ -e "$ALLOWLIST" ]; then
    if [ -n "$CAPACITY$LABELS" ]; then
      log "keeping $ALLOWLIST as it is: set capacity: and labels: in it by hand (--capacity and --label only write a new one)"
    else
      log "keeping $ALLOWLIST as it is"
    fi
  else
    tmp="$(mktemp "$ALLOWLIST.XXXXXX")"
    allowlist_content > "$tmp"
    chown root:root "$tmp"
    chmod 0644 "$tmp"
    mv -f "$tmp" "$ALLOWLIST"
    log "wrote $ALLOWLIST"
  fi
  chown root:root "$ALLOWLIST"
  chmod 0644 "$ALLOWLIST"
  "$GRAIN" hostd -ensure-builtins -allowlist "$ALLOWLIST" -command "$GRAIN" >&2 ||
    log "WARNING: could not add grain's own capabilities entries to $ALLOWLIST"
  if ! "$GRAIN" hostd -check -allowlist "$ALLOWLIST" >/dev/null; then
    log "WARNING: grain hostd will refuse $ALLOWLIST as it stands: fix it before host work reaches this machine"
  fi
}

# --- grain-hostd.service ---------------------------------------------------------

write_unit() {
  ARGS="-server $SERVER -token-file $TOKEN_FILE -name $NAME -allowlist $ALLOWLIST -socket $SOCKET_DIR/hostd.sock -state-dir $STATE_DIR"
  [ -z "$IAP_AUDIENCE" ] || ARGS="$ARGS -iap-audience $IAP_AUDIENCE"
  [ -z "$CA_FILE" ] || ARGS="$ARGS -ca-file $CA_FILE"

  tmp="$(mktemp "$UNIT.XXXXXX")"
  cat > "$tmp" <<UNIT
$UNIT_MARKER
# Re-run it to change anything here.
[Unit]
Description=grain hostd: host work for the grain daemon at $SERVER
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=0

[Service]
Type=simple
User=$HOSTD_USER
ExecStart=$GRAIN hostd $ARGS
RuntimeDirectory=$(basename "$SOCKET_DIR")
RuntimeDirectoryMode=0700
StateDirectory=$(basename "$STATE_DIR")
StateDirectoryMode=0700
OOMScoreAdjust=-900
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
UNIT
  chmod 0644 "$tmp"
  mv -f "$tmp" "$UNIT"
  systemctl daemon-reload
  log "wrote $UNIT: $GRAIN hostd -server $SERVER -name $NAME, as $HOSTD_USER"
}

start_unit() {
  if [ "$START" = 1 ]; then
    systemctl enable grain-hostd.service >/dev/null 2>&1
    systemctl restart grain-hostd.service
    log "grain-hostd.service started; it shows as connected in Settings -> Hostds once it has dialed in (journalctl -u grain-hostd -f)"
  else
    log "not starting grain-hostd.service (--no-start): systemctl enable --now grain-hostd.service"
  fi
}

main() {
  parse_args "$@"
  check_request
  TOKEN=""
  read_token
  WORK="$(mktemp -d)"
  trap 'rm -rf "$WORK"' EXIT
  FOUND=""
  install_grain
  write_token
  write_allowlist
  write_unit
  start_unit
}

# Everything above only defines functions, so by the time this line runs
# sh has read the whole script: piped into sh (curl | sh), stdin is then
# at its end, and read_token finds nothing there rather than eating the
# script's own next line as a token.
main "$@"
