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

# migrate-local-to-gke.sh -- turn a local install (scripts/setup.sh or
# `grain init`: /var/lib/grain, grain-daemon.service, grain-hostd.service,
# /etc/grain/hostd.yaml) into an in-cluster grain controller on GKE
# (deploy/kubernetes/grain-granule.yaml, the kubernetes-granule backend)
# with this host's grain hostd dialing out to it.
#
# docs/grain-init-platforms.md ("Migrating a local install to GKE") says
# what each stage does and why; docs/k8s-granule-design.md ("Migrating a
# local install onto this platform") covers the manifest changes. Every
# stage is idempotent: re-running after a failure picks up where the
# last run stopped, and a stage whose work is already done says so and
# moves on.
#
# This keeps the store as it is -- SQLite on the grain-data PVC, or the
# Postgres the local daemon already uses -- and copies the whole data
# directory, so the controller that comes up in the cluster is the same
# deployment with the same history, secrets and state repository, and
# only the place it runs has changed.
#
# Tested by scripts/migrate-local-to-gke_test.sh (stubbed kubectl,
# systemctl, gcloud, docker, curl and grain), run in CI by
# tests/deploy/migrate_local_to_gke_test.go.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

# Host paths. Overridable through the environment so the test suite can
# point every one of them into a scratch directory; an operator has no
# reason to.
GRAIN_ROOT_DIR="${GRAIN_ROOT_DIR:-/var/lib/grain}"
GRAIN_ETC_DIR="${GRAIN_ETC_DIR:-/etc/grain}"
SYSTEMD_DIR="${GRAIN_SYSTEMD_DIR:-/etc/systemd/system}"
BACKUP_DIR="${GRAIN_MIGRATE_BACKUP_DIR:-/var/backups/grain}"

DATA_DIR="${GRAIN_DATA_DIR:-}"
KUBECONFIG_PATH="${GRAIN_MIGRATE_KUBECONFIG:-}"
PROJECT=""
LOCATION=""
CLUSTER=""
PROVISION=false
NAMESPACE="grain"
IMAGE=""
SANDBOX_IMAGE=""
TARGET_REPO=""
NODE_SELECTOR=""
MANIFEST="${REPO_ROOT}/deploy/kubernetes/grain-granule.yaml"
TRANSFER_IMAGE="${GRAIN_MIGRATE_TRANSFER_IMAGE:-}"
SERVER_URL=""
FORWARD_PORT=4280
TOKEN_FILE=""
API_TOKEN_FILE=""
DRAIN_WAIT=0
VERIFY_TIMEOUT=300
RECOPY=false
SYNC_BACK=true
DRY_RUN=false
ROLLBACK=false
PRINT_MANIFEST=false

DEFAULT_IMAGE="ghcr.io/bwsalmon/grain/grain:main"
DAEMON_UNIT=grain-daemon.service
# Where Stage 2 moves DAEMON_UNIT's file so that it can be masked:
# systemctl mask refuses ("File ... already exists") a unit whose own
# file is in /etc/systemd/system, which is where setup.sh installs it.
ASIDE_SUFFIX=.pre-gke
HOSTD_UNIT=grain-hostd.service
FORWARD_UNIT=grain-gke-forward.service
# MODEL_UNIT is the grain model-server unit an earlier version of this
# script installed beside hostd; remote executors are retired, and
# --rollback still removes the unit where one is left.
MODEL_UNIT=grain-model-server.service
TRANSFER_POD=grain-data-transfer
SECRET_NAME=grain-migration
SECRET_MOUNT=/etc/grain-secrets

usage() {
  cat <<'EOF'
Usage: scripts/migrate-local-to-gke.sh [options]

Migrate this host's local grain install to an in-cluster grain controller on
GKE (kubernetes-granule sandboxes), keeping grain hostd running here and
dialing out to it. Run as root on the host the local install lives on.

Where the cluster is:
  --kubeconfig <path>       Admin kubeconfig for the cluster (default: the one
                            --cluster/--project/--location fetches)
  --cluster <name>          GKE cluster name
  --project <id>            GCP project of the cluster
  --location <zone|region>  Zone or region of the cluster
  --provision               Create the cluster, nested-virt pool, granule
                            device plugin and RBAC first if they are missing
                            (grain init -platform=gke-sandboxes)
  --namespace <name>        Namespace (default: grain)

What runs there:
  --image <ref>             grain controller image (default: the image the
                            local grain-daemon.service runs, else
                            ghcr.io/bwsalmon/grain/grain:main)
  --sandbox-image <ref>     Packed guest image for -k8s-granule-image (default:
                            the local daemon's -sandbox-image)
  --target-repo <owner/name>
                            -default-target-repo (default: the local daemon's)
  --node-selector <k=v>     -k8s-node-selector for the nested-virt pool
  --manifest <path>         Manifest to render (default:
                            deploy/kubernetes/grain-granule.yaml)
  --transfer-image <ref>    Image of the pod the data directory is copied
                            through; needs sh and tar (default: the
                            controller image, which is Debian-based)

This host:
  --data-dir <dir>          Local data directory (default: GRAIN_DATA_DIR from
                            /etc/grain/cli.env, else /var/lib/grain/data, else
                            /var/lib/grain)
  --server-url <url>        How hostd reaches the controller
                            (a Gateway/IAP URL). Default: a local
                            grain-gke-forward.service running kubectl
                            port-forward to http://127.0.0.1:<forward-port>
  --forward-port <port>     Local port of that forward (default: 4280)
  --token-file <path>       Shared hostd token (default:
                            /etc/grain/hostd.token; created when missing)
  --api-token-file <path>   Bearer token for the controller's API in the
                            readiness checks (default: <data-dir>/local-api-token,
                            which the controller accepts over the port-forward)
  --drain-wait <seconds>    After pausing dispatch, wait this long for in-flight
                            runs before stopping the local daemon (default: 0)
  --verify-timeout <secs>   How long to wait for hostd to attach (default: 300)
  --recopy                  Copy the data directory again even though an
                            earlier run already did (refused while the cluster
                            controller runs)

Modes:
  --dry-run                 Print every check and command; change nothing
  --rollback                Stop the cluster controller, copy its data
                            directory back (unless --no-sync-back), point hostd
                            back at the local socket and restart the local
                            grain-daemon.service
  --no-sync-back            With --rollback: keep the local data directory as
                            it is rather than copying the cluster's back
  --print-manifest          Print the rendered manifest and exit
  -h, --help                Show this help
EOF
}

log() { printf '[migrate-local-to-gke] %s\n' "$*"; }
warn() { printf '[migrate-local-to-gke] WARNING: %s\n' "$*" >&2; }
die() {
  printf '[migrate-local-to-gke] ERROR: %s\n' "$*" >&2
  exit 1
}

# run prints a command and, unless this is a dry run, runs it.
run() {
  if [[ "$1" == "kc" ]]; then
    shift
    set -- kubectl --kubeconfig="${KUBECONFIG_PATH}" -n "${NAMESPACE}" "$@"
  fi
  printf '+'
  printf ' %q' "$@"
  printf '\n'
  [[ "${DRY_RUN}" == "true" ]] && return 0
  "$@"
}

# kc is kubectl against the migration's cluster and namespace.
kc() {
  kubectl --kubeconfig="${KUBECONFIG_PATH}" -n "${NAMESPACE}" "$@"
}

# write_file PATH MODE: stdin to PATH, or what would be written on a dry
# run.
write_file() {
  local path="$1" mode="$2" content
  content="$(cat)"
  if [[ "${DRY_RUN}" == "true" ]]; then
    printf '+ write %s (mode %s):\n' "${path}" "${mode}"
    printf '%s\n' "${content}" | sed 's/^/    /'
    return 0
  fi
  mkdir -p "$(dirname "${path}")"
  printf '%s\n' "${content}" >"${path}.tmp"
  chmod "${mode}" "${path}.tmp"
  mv -f "${path}.tmp" "${path}"
}

parse_args() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --kubeconfig) KUBECONFIG_PATH="$2"; shift ;;
      --kubeconfig=*) KUBECONFIG_PATH="${1#*=}" ;;
      --cluster) CLUSTER="$2"; shift ;;
      --cluster=*) CLUSTER="${1#*=}" ;;
      --project) PROJECT="$2"; shift ;;
      --project=*) PROJECT="${1#*=}" ;;
      --location) LOCATION="$2"; shift ;;
      --location=*) LOCATION="${1#*=}" ;;
      --provision) PROVISION=true ;;
      --namespace) NAMESPACE="$2"; shift ;;
      --namespace=*) NAMESPACE="${1#*=}" ;;
      --image) IMAGE="$2"; shift ;;
      --image=*) IMAGE="${1#*=}" ;;
      --sandbox-image) SANDBOX_IMAGE="$2"; shift ;;
      --sandbox-image=*) SANDBOX_IMAGE="${1#*=}" ;;
      --target-repo) TARGET_REPO="$2"; shift ;;
      --target-repo=*) TARGET_REPO="${1#*=}" ;;
      --node-selector) NODE_SELECTOR="$2"; shift ;;
      --node-selector=*) NODE_SELECTOR="${1#*=}" ;;
      --manifest) MANIFEST="$2"; shift ;;
      --manifest=*) MANIFEST="${1#*=}" ;;
      --transfer-image) TRANSFER_IMAGE="$2"; shift ;;
      --transfer-image=*) TRANSFER_IMAGE="${1#*=}" ;;
      --data-dir) DATA_DIR="$2"; shift ;;
      --data-dir=*) DATA_DIR="${1#*=}" ;;
      --server-url) SERVER_URL="$2"; shift ;;
      --server-url=*) SERVER_URL="${1#*=}" ;;
      --forward-port) FORWARD_PORT="$2"; shift ;;
      --forward-port=*) FORWARD_PORT="${1#*=}" ;;
      --token-file) TOKEN_FILE="$2"; shift ;;
      --token-file=*) TOKEN_FILE="${1#*=}" ;;
      --api-token-file) API_TOKEN_FILE="$2"; shift ;;
      --api-token-file=*) API_TOKEN_FILE="${1#*=}" ;;
      --drain-wait) DRAIN_WAIT="$2"; shift ;;
      --drain-wait=*) DRAIN_WAIT="${1#*=}" ;;
      --verify-timeout) VERIFY_TIMEOUT="$2"; shift ;;
      --verify-timeout=*) VERIFY_TIMEOUT="${1#*=}" ;;
      --recopy) RECOPY=true ;;
      --dry-run) DRY_RUN=true ;;
      --rollback) ROLLBACK=true ;;
      --no-sync-back) SYNC_BACK=false ;;
      --print-manifest) PRINT_MANIFEST=true ;;
      -h | --help) usage; exit 0 ;;
      *) usage >&2; die "unknown option: $1" ;;
    esac
    shift
  done
  [[ "${DRAIN_WAIT}" =~ ^[0-9]+$ ]] || die "--drain-wait takes whole seconds, not ${DRAIN_WAIT}"
  [[ "${VERIFY_TIMEOUT}" =~ ^[0-9]+$ ]] || die "--verify-timeout takes whole seconds, not ${VERIFY_TIMEOUT}"
  [[ "${FORWARD_PORT}" =~ ^[0-9]+$ ]] || die "--forward-port takes a port number, not ${FORWARD_PORT}"
  TOKEN_FILE="${TOKEN_FILE:-${GRAIN_ETC_DIR}/hostd.token}"
}

# --- What the local install looks like -----------------------------------

# unit_file UNIT prints UNIT's own file: the one in SYSTEMD_DIR, or --
# once Stage 2 has moved it aside to mask the unit -- UNIT.pre-gke there.
unit_file() {
  local file="${SYSTEMD_DIR}/$1"
  if [[ -f "${file}" && ! -L "${file}" ]]; then
    printf '%s\n' "${file}"
  elif [[ -f "${file}${ASIDE_SUFFIX}" ]]; then
    printf '%s\n' "${file}${ASIDE_SUFFIX}"
  fi
}

# unit_value UNIT KEY prints the first KEY= value in UNIT's own file.
unit_value() {
  local file
  file="$(unit_file "$1")"
  [[ -n "${file}" ]] || return 0
  sed -n "s/^$2=//p" "${file}" | head -n1
}

# unit_execstart UNIT prints UNIT's ExecStart, joining '\'-continued
# lines, with systemd's own prefixes (-@:+!) stripped.
unit_execstart() {
  local file
  file="$(unit_file "$1")"
  [[ -n "${file}" ]] || return 0
  awk '
    /^[[:space:]]*ExecStart=/ { on = 1; sub(/^[[:space:]]*ExecStart=[-@:+!]*/, "") }
    on {
      line = $0
      cont = sub(/\\[[:space:]]*$/, "", line)
      out = out (out == "" ? "" : " ") line
      if (!cont) { print out; exit }
    }' "${file}" | tr -s ' '
}

# flag_value EXECSTART NAME prints what EXECSTART passes for -NAME
# (-NAME=v or -NAME v, one or two dashes).
flag_value() {
  local word next=false
  for word in $1; do
    if [[ "${next}" == "true" ]]; then
      printf '%s\n' "${word}"
      return 0
    fi
    case "${word}" in
      -"$2"=* | --"$2"=*) printf '%s\n' "${word#*=}"; return 0 ;;
      -"$2" | --"$2") next=true ;;
    esac
  done
}

resolve_data_dir() {
  if [[ -z "${DATA_DIR}" && -f "${GRAIN_ETC_DIR}/cli.env" ]]; then
    DATA_DIR="$(sed -n 's/^GRAIN_DATA_DIR=//p' "${GRAIN_ETC_DIR}/cli.env" | tail -n1)"
  fi
  if [[ -z "${DATA_DIR}" ]]; then
    if [[ -d "${GRAIN_ROOT_DIR}/data" ]]; then
      DATA_DIR="${GRAIN_ROOT_DIR}/data"
    else
      DATA_DIR="${GRAIN_ROOT_DIR}"
    fi
  fi
  API_TOKEN_FILE="${API_TOKEN_FILE:-${DATA_DIR}/local-api-token}"
}

# resolve_local_daemon reads the image, sandbox image and default target
# repository the local daemon runs with, for any the flags left unset.
resolve_local_daemon() {
  local exec
  exec="$(unit_execstart "${DAEMON_UNIT}")"
  if [[ -z "${IMAGE}" && "${exec}" == *"docker run"* ]]; then
    # The last word that looks like an image reference ahead of the
    # daemon subcommand is the container's image.
    IMAGE="$(awk '{ for (i = 1; i <= NF; i++) if ($i == "daemon") { print $(i-1); exit } }' <<<"${exec}")"
    [[ "${IMAGE}" == */* ]] || IMAGE=""
  fi
  IMAGE="${IMAGE:-${DEFAULT_IMAGE}}"
  TRANSFER_IMAGE="${TRANSFER_IMAGE:-${IMAGE}}"
  [[ -n "${SANDBOX_IMAGE}" ]] || SANDBOX_IMAGE="$(flag_value "${exec}" sandbox-image)"
  [[ -n "${SANDBOX_IMAGE}" ]] || SANDBOX_IMAGE="$(flag_value "${exec}" k8s-granule-image)"
  [[ -n "${TARGET_REPO}" ]] || TARGET_REPO="$(flag_value "${exec}" default-target-repo)"
}

hostd_user() {
  local u
  u="$(unit_value "${HOSTD_UNIT}" User)"
  printf '%s\n' "${u:-grain}"
}

daemon_user() {
  local u
  u="$(unit_value "${DAEMON_UNIT}" User)"
  printf '%s\n' "${u:-grain}"
}

# hostd_base_execstart is grain-hostd.service's own ExecStart -- the unit
# file setup.sh writes, never the drop-in this script adds beside it.
hostd_base_execstart() {
  unit_execstart "${HOSTD_UNIT}"
}

hostd_grain_bin() {
  local exec
  exec="$(hostd_base_execstart)"
  printf '%s\n' "${exec%% *}"
}

controller_url() {
  if [[ -n "${SERVER_URL}" ]]; then
    printf '%s\n' "${SERVER_URL%/}"
  else
    printf 'http://127.0.0.1:%s\n' "${FORWARD_PORT}"
  fi
}

# --- Stage 1: preflight ---------------------------------------------------

preflight() {
  log "Stage 1: preflight"
  if [[ "${DRY_RUN}" != "true" && "${GRAIN_MIGRATE_SKIP_ROOT_CHECK:-0}" != "1" && "$(id -u)" != "0" ]]; then
    die "run as root: this stops and rewrites systemd units and reads ${DATA_DIR}"
  fi
  local tool
  for tool in kubectl tar; do
    command -v "${tool}" >/dev/null 2>&1 || die "${tool} is not on PATH"
  done
  [[ -d "${DATA_DIR}" ]] || die "no local data directory at ${DATA_DIR} (pass --data-dir)"

  if [[ -f "${DATA_DIR}/store/grain.db" ]]; then
    log "  store: SQLite at ${DATA_DIR}/store/grain.db (copied to the grain-data PVC)"
  elif [[ -f "${DATA_DIR}/db-config.json" ]]; then
    log "  store: Postgres recorded in ${DATA_DIR}/db-config.json"
    warn "the cluster controller opens the same Postgres: make sure the cluster can reach it before cutting over"
  else
    die "${DATA_DIR} holds neither store/grain.db nor db-config.json -- is this a grain data directory?"
  fi
  local key=""
  for key in "${DATA_DIR}/secrets/secrets.key" "${DATA_DIR}/secrets.key"; do
    [[ -f "${key}" ]] && break
    key=""
  done
  [[ -n "${key}" ]] || die "no secrets.key under ${DATA_DIR}: without it the secrets in the state repository cannot be read anywhere"
  SECRETS_KEY="${key}"
  log "  secrets key: ${SECRETS_KEY}"
  if [[ -f "${DATA_DIR}/installation-id" ]]; then
    log "  installation id: $(head -c 64 "${DATA_DIR}/installation-id")"
  else
    warn "no ${DATA_DIR}/installation-id: the controller will mint a new one"
  fi
  if [[ -d "${DATA_DIR}/state-repo" ]]; then
    log "  state repository: ${DATA_DIR}/state-repo"
  else
    warn "no ${DATA_DIR}/state-repo: nothing to carry over from a state repository"
  fi

  if [[ -f "${SYSTEMD_DIR}/${HOSTD_UNIT}" ]]; then
    [[ -f "${GRAIN_ETC_DIR}/hostd.yaml" ]] || die "${HOSTD_UNIT} is installed but ${GRAIN_ETC_DIR}/hostd.yaml is not"
    [[ -n "$(hostd_base_execstart)" ]] || die "${SYSTEMD_DIR}/${HOSTD_UNIT} has no ExecStart= line"
    log "  hostd: ${HOSTD_UNIT} as $(hostd_user), allowlist ${GRAIN_ETC_DIR}/hostd.yaml ($(hostd_capabilities | tr '\n' ' '))"
  else
    die "no ${SYSTEMD_DIR}/${HOSTD_UNIT}: install hostd first (GRAIN_HOSTD=1 scripts/setup.sh)"
  fi
  if command -v jq >/dev/null 2>&1; then
    HAVE_JQ=true
  else
    HAVE_JQ=false
    warn "jq is not on PATH: the readiness check confirms the controller answers but cannot read which capability servers attached"
  fi

  resolve_local_daemon
  [[ -n "${SANDBOX_IMAGE}" ]] || die "no packed guest image for -k8s-granule-image: pass --sandbox-image"
  [[ -n "${TARGET_REPO}" ]] || die "no default target repository: pass --target-repo owner/name"
  [[ -f "${MANIFEST}" ]] || die "no manifest at ${MANIFEST}"
  log "  controller image: ${IMAGE}"
  log "  sandbox image:    ${SANDBOX_IMAGE}"
  log "  target repo:      ${TARGET_REPO}"
  log "  controller URL:   $(controller_url)$([[ -z "${SERVER_URL}" ]] && printf ' (via %s)' "${FORWARD_UNIT}")"
}

# hostd_capabilities prints the capability entries hostd.yaml offers.
hostd_capabilities() {
  awk '
    /^capabilities:/ { on = 1; next }
    /^[^[:space:]#]/ { on = 0 }
    on && /^  [A-Za-z0-9_.-]+:/ { sub(/^  /, ""); sub(/:.*/, ""); print }
  ' "${GRAIN_ETC_DIR}/hostd.yaml"
}

# --- Stage 1b: cluster ----------------------------------------------------

cluster() {
  log "Stage 1b: cluster readiness"
  if [[ -z "${KUBECONFIG_PATH}" ]]; then
    if [[ -n "${CLUSTER}" || -n "${PROJECT}" || -n "${LOCATION}" ]]; then
      [[ -n "${CLUSTER}" && -n "${PROJECT}" && -n "${LOCATION}" ]] ||
        die "--cluster, --project and --location go together"
      command -v gcloud >/dev/null 2>&1 || die "gcloud is not on PATH"
      KUBECONFIG_PATH="${GRAIN_ETC_DIR}/gke-admin.kubeconfig"
      local where="--region=${LOCATION}"
      [[ "$(tr -cd - <<<"${LOCATION}" | wc -c)" -ge 2 ]] && where="--zone=${LOCATION}"
      if [[ "${PROVISION}" == "true" ]]; then
        run grain init -platform=gke-sandboxes -gke-project="${PROJECT}" -gke-location="${LOCATION}" \
          -gke-cluster="${CLUSTER}" -start-daemon=false
      fi
      run env KUBECONFIG="${KUBECONFIG_PATH}" gcloud container clusters get-credentials "${CLUSTER}" \
        --project="${PROJECT}" "${where}"
      [[ "${DRY_RUN}" == "true" ]] || chmod 0600 "${KUBECONFIG_PATH}"
    elif [[ -f "${GRAIN_ROOT_DIR}/kubeconfig" ]]; then
      # gke-sandboxes' kubeconfig is a least-privilege ServiceAccount
      # that can create sandbox pods and nothing else: enough to find the
      # cluster, not to create a PVC, a Secret or a Deployment.
      die "${GRAIN_ROOT_DIR}/kubeconfig is gke-sandboxes' least-privilege kubeconfig, which cannot create the PVC, Secret or Deployment this needs: pass --cluster/--project/--location (the same cluster) or --kubeconfig with an admin kubeconfig"
    else
      die "no cluster: pass --kubeconfig, or --cluster/--project/--location (with --provision to create it)"
    fi
  fi
  [[ "${DRY_RUN}" == "true" || -f "${KUBECONFIG_PATH}" ]] || die "no kubeconfig at ${KUBECONFIG_PATH}"
  log "  kubeconfig: ${KUBECONFIG_PATH}"

  if [[ "${DRY_RUN}" == "true" ]]; then
    printf '+ kubectl --kubeconfig=%q get namespace %q\n' "${KUBECONFIG_PATH}" "${NAMESPACE}"
    printf '+ kubectl --kubeconfig=%q -n kube-system get daemonset granule-device-plugin\n' "${KUBECONFIG_PATH}"
    return 0
  fi
  kubectl --kubeconfig="${KUBECONFIG_PATH}" version >/dev/null 2>&1 ||
    die "the cluster does not answer with ${KUBECONFIG_PATH}"
  if ! kubectl --kubeconfig="${KUBECONFIG_PATH}" get namespace "${NAMESPACE}" >/dev/null 2>&1; then
    run kubectl --kubeconfig="${KUBECONFIG_PATH}" create namespace "${NAMESPACE}"
  fi
  # kubernetes-granule sandbox pods are privileged; the daemon's own pod
  # is not (deploy/kubernetes/grain-granule.yaml's header).
  local enforce
  enforce="$(kubectl --kubeconfig="${KUBECONFIG_PATH}" get namespace "${NAMESPACE}" \
    -o jsonpath='{.metadata.labels.pod-security\.kubernetes\.io/enforce}' 2>/dev/null || true)"
  if [[ "${enforce}" != "privileged" ]]; then
    run kubectl --kubeconfig="${KUBECONFIG_PATH}" label namespace "${NAMESPACE}" \
      pod-security.kubernetes.io/enforce=privileged --overwrite
  fi
  kubectl --kubeconfig="${KUBECONFIG_PATH}" -n kube-system get daemonset granule-device-plugin >/dev/null 2>&1 ||
    die "no granule-device-plugin DaemonSet in kube-system: the cluster cannot give sandbox pods /dev/kvm -- run grain init -platform=gke-sandboxes against it, or pass --provision"
  log "  namespace ${NAMESPACE} (enforce=privileged) and granule-device-plugin are in place"
}

# --- Rendering the manifest ------------------------------------------------

# render_manifest REPLICAS prints grain-granule.yaml with its CHANGEMEs
# filled in, the controller's replicas set to REPLICAS, and the
# migration's Secret mounted for the hostd token.
render_manifest() {
  local replicas="$1"
  awk -v image="${IMAGE}" -v sandbox="${SANDBOX_IMAGE}" -v repo="${TARGET_REPO}" \
    -v selector="${NODE_SELECTOR}" -v replicas="${replicas}" -v secret="${SECRET_NAME}" \
    -v mount="${SECRET_MOUNT}" '
    /^  replicas: [0-9]+$/ { print "  replicas: " replicas; next }
    /^          image: ghcr\.io\/.*\/grain:/ { sub(/image: .*/, "image: " image) }
    /^            - -k8s-granule-image=CHANGEME$/ { sub(/CHANGEME/, sandbox) }
    /^            - -default-target-repo=CHANGEME\/CHANGEME$/ { sub(/CHANGEME\/CHANGEME/, repo) }
    /^            # - -k8s-node-selector=CHANGEME=CHANGEME$/ {
      if (selector != "") { print "            - -k8s-node-selector=" selector; next }
    }
    /^      containers:$/ { in_containers = 1 }
    { print }
    /^            - -k8s-container=grain$/ {
      print "            # Added by scripts/migrate-local-to-gke.sh: the shared token this"
      print "            # host'"'"'s grain hostd dials in with."
      print "            - -hostd-token-file=" mount "/hostd-token"
    }
    in_containers && /^              mountPath: \/data$/ {
      print "            - name: migration"
      print "              mountPath: " mount
      print "              readOnly: true"
    }
    /^            claimName: grain-data$/ {
      print "        - name: migration"
      print "          secret:"
      print "            secretName: " secret
      print "            defaultMode: 0440"
    }
  ' "${MANIFEST}"
}

# --- Stage 2: drain and stop the local daemon -----------------------------

drain_and_stop() {
  log "Stage 2: drain and stop the local ${DAEMON_UNIT}"
  if [[ -z "$(unit_file "${DAEMON_UNIT}")" ]]; then
    log "  no ${DAEMON_UNIT} on this host: nothing to stop"
  elif systemctl is-active --quiet "${DAEMON_UNIT}" 2>/dev/null; then
    if command -v grain >/dev/null 2>&1; then
      run grain pause -set -reason "migrating this deployment to GKE (scripts/migrate-local-to-gke.sh)" ||
        warn "could not pause dispatch on the local daemon; stopping it anyway"
    fi
    if [[ "${DRAIN_WAIT}" -gt 0 ]]; then
      log "  waiting ${DRAIN_WAIT}s for in-flight runs before stopping"
      [[ "${DRY_RUN}" == "true" ]] || sleep "${DRAIN_WAIT}"
    fi
    run systemctl stop "${DAEMON_UNIT}"
  else
    log "  ${DAEMON_UNIT} is already stopped"
  fi
  if [[ -f "${SYSTEMD_DIR}/${DAEMON_UNIT}" && ! -L "${SYSTEMD_DIR}/${DAEMON_UNIT}" ]]; then
    # Disabled and masked: two controllers must never run against one
    # state repository, and a masked unit is one nothing -- not a reboot,
    # not a re-run of setup.sh's enable -- starts again until --rollback
    # unmasks it. Masking puts a link to /dev/null where the unit's own
    # file is, so that file moves aside first; --rollback moves it back.
    run systemctl disable "${DAEMON_UNIT}"
    run mv "${SYSTEMD_DIR}/${DAEMON_UNIT}" "${SYSTEMD_DIR}/${DAEMON_UNIT}${ASIDE_SUFFIX}"
    run systemctl mask "${DAEMON_UNIT}"
  fi
  if command -v docker >/dev/null 2>&1 && docker container inspect grain-daemon >/dev/null 2>&1; then
    run docker rm -f grain-daemon
  fi
  if [[ "${DRY_RUN}" != "true" ]] && systemctl is-active --quiet "${DAEMON_UNIT}" 2>/dev/null; then
    die "${DAEMON_UNIT} is still active after stopping it"
  fi
}

backup() {
  local stamp archive
  stamp="$(date -u +%Y%m%dT%H%M%SZ)"
  archive="${BACKUP_DIR}/grain-data-${stamp}.tar.gz"
  log "  backing ${DATA_DIR} up to ${archive}"
  run mkdir -p "${BACKUP_DIR}"
  run chmod 0700 "${BACKUP_DIR}"
  run tar -C "${DATA_DIR}" -czf "${archive}" .
  BACKUP_ARCHIVE="${archive}"
}

# --- Stage 3: secrets, PVC, data ------------------------------------------

ensure_token() {
  if [[ -s "${TOKEN_FILE}" ]]; then
    log "  shared hostd token: ${TOKEN_FILE} (kept)"
    return 0
  fi
  log "  minting the shared hostd token at ${TOKEN_FILE}"
  if [[ "${DRY_RUN}" == "true" ]]; then
    printf '+ write %s (mode 0600, 32 random bytes, hex)\n' "${TOKEN_FILE}"
    return 0
  fi
  mkdir -p "$(dirname "${TOKEN_FILE}")"
  (umask 077 && od -An -tx1 -N32 /dev/urandom | tr -d ' \n' >"${TOKEN_FILE}")
}

apply_secret() {
  log "  Secret/${SECRET_NAME}: secrets.key and the shared token"
  if [[ "${DRY_RUN}" == "true" ]]; then
    printf '+ kubectl --kubeconfig=%q -n %q create secret generic %q --from-file=secrets.key=%q --from-file=hostd-token=%q --dry-run=client -o yaml | kubectl --kubeconfig=%q -n %q apply -f -\n' \
      "${KUBECONFIG_PATH}" "${NAMESPACE}" "${SECRET_NAME}" "${SECRETS_KEY}" "${TOKEN_FILE}" "${KUBECONFIG_PATH}" "${NAMESPACE}"
    return 0
  fi
  kc create secret generic "${SECRET_NAME}" \
    --from-file=secrets.key="${SECRETS_KEY}" \
    --from-file=hostd-token="${TOKEN_FILE}" \
    --dry-run=client -o yaml | kc apply -f - >/dev/null
}

deployment_replicas() {
  [[ "${DRY_RUN}" == "true" ]] && { printf '0\n'; return 0; }
  local r
  r="$(kc get deployment grain -o jsonpath='{.spec.replicas}' 2>/dev/null || true)"
  printf '%s\n' "${r:-0}"
}

# apply_manifest REPLICAS applies the rendered manifest -- PVC, RBAC,
# Deployment, Service -- with the controller at REPLICAS.
apply_manifest() {
  local replicas="$1" rendered
  rendered="$(mktemp "${TMPDIR:-/tmp}/grain-granule.XXXXXX.yaml")"
  render_manifest "${replicas}" >"${rendered}"
  run kubectl --kubeconfig="${KUBECONFIG_PATH}" apply -f "${rendered}"
  rm -f "${rendered}"
}

transfer_pod_manifest() {
  cat <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: ${TRANSFER_POD}
  namespace: ${NAMESPACE}
  labels:
    app.kubernetes.io/name: grain-data-transfer
spec:
  restartPolicy: Never
  automountServiceAccountToken: false
  securityContext:
    runAsNonRoot: true
    runAsUser: 1000
    runAsGroup: 1000
    fsGroup: 1000
  containers:
    - name: transfer
      image: ${TRANSFER_IMAGE}
      command: ["sleep", "3600"]
      securityContext:
        allowPrivilegeEscalation: false
        capabilities:
          drop: ["ALL"]
      volumeMounts:
        - name: data
          mountPath: /data
  volumes:
    - name: data
      persistentVolumeClaim:
        claimName: grain-data
EOF
}

start_transfer_pod() {
  log "  starting pod/${TRANSFER_POD} (${TRANSFER_IMAGE}, uid 1000) on the grain-data PVC"
  run kc delete pod "${TRANSFER_POD}" --ignore-not-found --wait=true
  if [[ "${DRY_RUN}" == "true" ]]; then
    printf '+ kubectl --kubeconfig=%q -n %q apply -f <pod/%s>\n' "${KUBECONFIG_PATH}" "${NAMESPACE}" "${TRANSFER_POD}"
  else
    transfer_pod_manifest | kc apply -f - >/dev/null
  fi
  run kc wait --for=condition=Ready "pod/${TRANSFER_POD}" --timeout=300s
}

stop_transfer_pod() {
  run kc delete pod "${TRANSFER_POD}" --ignore-not-found --wait=false
}

# copy_data streams the data directory into the PVC. tar extracts as uid
# 1000 inside the pod, so every file lands owned by the uid the
# controller runs as whatever owned it here. The marker it leaves at the
# PVC's root is what a re-run reads to know the copy is done.
copy_data() {
  local marker=".migrated-from-local"
  if [[ "${DRY_RUN}" != "true" && "${RECOPY}" != "true" ]] &&
    kc exec "${TRANSFER_POD}" -- test -f "/data/${marker}" >/dev/null 2>&1; then
    log "  the grain-data PVC already holds a copy (/data/${marker}); pass --recopy to copy again"
    return 0
  fi
  log "  copying ${DATA_DIR} into the grain-data PVC"
  if [[ "${DRY_RUN}" == "true" ]]; then
    printf '+ tar -C %q -cf - --exclude=./kubeconfig --exclude=./scratch . | kubectl --kubeconfig=%q -n %q exec -i %s -- tar -C /data -xf - --no-same-owner\n' \
      "${DATA_DIR}" "${KUBECONFIG_PATH}" "${NAMESPACE}" "${TRANSFER_POD}"
    printf '+ kubectl --kubeconfig=%q -n %q exec %s -- sh -c %q\n' "${KUBECONFIG_PATH}" "${NAMESPACE}" "${TRANSFER_POD}" "date -u > /data/${marker}"
    return 0
  fi
  tar -C "${DATA_DIR}" -cf - --exclude=./kubeconfig --exclude=./scratch . |
    kc exec -i "${TRANSFER_POD}" -- tar -C /data -xf - --no-same-owner
  if [[ -f "${DATA_DIR}/store/grain.db" ]]; then
    kc exec "${TRANSFER_POD}" -- test -f /data/store/grain.db ||
      die "the copy did not land: /data/store/grain.db is missing from the PVC"
  fi
  kc exec "${TRANSFER_POD}" -- sh -c "date -u > /data/${marker}"
}

migrate_state() {
  log "Stage 3: migrate state and secrets to ${NAMESPACE}"
  ensure_token
  apply_secret
  local replicas
  replicas="$(deployment_replicas)"
  if [[ "${replicas}" -gt 0 && "${RECOPY}" == "true" ]]; then
    die "--recopy while Deployment/grain runs (replicas=${replicas}) would overwrite a live controller's data: scale it to 0 first"
  fi
  if [[ "${replicas}" -gt 0 ]]; then
    log "  Deployment/grain already runs (replicas=${replicas}): its data directory was copied by an earlier run"
    apply_manifest "${replicas}"
    return 0
  fi
  # replicas 0 until the copy is done, so the controller never starts on
  # an empty PVC and writes a fresh store there first.
  apply_manifest 0
  start_transfer_pod
  copy_data
  stop_transfer_pod
}

# --- Stage 4: the controller ----------------------------------------------

api() { # api METHOD PATH [curl args...]
  local method="$1" path="$2"
  shift 2
  local auth=()
  [[ -r "${API_TOKEN_FILE}" ]] && auth=(-H "Authorization: Bearer $(tr -d '[:space:]' <"${API_TOKEN_FILE}")")
  curl -fsS -X "${method}" "${auth[@]}" "$@" "$(controller_url)${path}"
}

deploy_controller() {
  log "Stage 4: start the cluster controller"
  run kc scale deployment/grain --replicas=1
  run kc rollout status deployment/grain --timeout=600s
}

# --- Stage 5: hostd -------------------------------------------------------

configure_forward() {
  [[ -z "${SERVER_URL}" ]] || return 0
  local user kubeconfig
  user="$(hostd_user)"
  kubeconfig="${GRAIN_ETC_DIR}/gke-forward.kubeconfig"
  log "  ${FORWARD_UNIT}: kubectl port-forward svc/grain to 127.0.0.1:${FORWARD_PORT}, as ${user}"
  run install -m 0600 -o "${user}" "${KUBECONFIG_PATH}" "${kubeconfig}"
  write_file "${SYSTEMD_DIR}/${FORWARD_UNIT}" 0644 <<EOF
# Managed by scripts/migrate-local-to-gke.sh: the local end of the grain
# controller in ${NAMESPACE}, for grain-hostd.service.
[Unit]
Description=grain: forward 127.0.0.1:${FORWARD_PORT} to the GKE grain controller (svc/grain)
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=0

[Service]
Type=simple
User=${user}
Environment=KUBECONFIG=${kubeconfig}
ExecStart=$(command -v kubectl) -n ${NAMESPACE} port-forward --address 127.0.0.1 svc/grain ${FORWARD_PORT}:80
Restart=always
RestartSec=2

[Install]
WantedBy=multi-user.target
EOF
  run systemctl daemon-reload
  run systemctl enable "${FORWARD_UNIT}"
  run systemctl restart "${FORWARD_UNIT}"
}

configure_hostd() {
  local user exec dropin url after=""
  user="$(hostd_user)"
  exec="$(hostd_base_execstart)"
  url="$(controller_url)"
  dropin="${SYSTEMD_DIR}/${HOSTD_UNIT}.d/gke.conf"
  log "  ${HOSTD_UNIT}: dial out to ${url}"
  run chown "${user}" "${TOKEN_FILE}"
  run chmod 0600 "${TOKEN_FILE}"
  if [[ -z "${SERVER_URL}" ]]; then
    after="After=${FORWARD_UNIT}
Wants=${FORWARD_UNIT}"
  fi
  write_file "${dropin}" 0644 <<EOF
# Managed by scripts/migrate-local-to-gke.sh -- --rollback removes it.
# grain-hostd.service's own ExecStart, dialing out to the GKE controller
# as well as listening on its socket.
[Unit]
${after}

[Service]
ExecStart=
ExecStart=${exec} -server ${url} -token-file ${TOKEN_FILE}
EOF
  run systemctl daemon-reload
  run systemctl restart "${HOSTD_UNIT}"
}

# --- Stage 5b: readiness --------------------------------------------------

verify() {
  log "Stage 5b: readiness"
  if [[ "${DRY_RUN}" == "true" ]]; then
    printf '+ curl -fsS %s/api/auth/access-code\n' "$(controller_url)"
    printf '+ curl -fsS -X PUT %s/api/settings -d {"sandboxBackend":"kubernetes-granule"}\n' "$(controller_url)"
    printf '+ curl -fsS %s/api/capability-servers (every run: host server attached)\n' "$(controller_url)"
    printf '+ curl -fsS -X DELETE %s/api/pipeline-pause\n' "$(controller_url)"
    return 0
  fi
  local deadline=$((SECONDS + VERIFY_TIMEOUT))
  until api GET /api/auth/access-code >/dev/null 2>&1; do
    [[ "${SECONDS}" -lt "${deadline}" ]] || die "the controller does not answer at $(controller_url) after ${VERIFY_TIMEOUT}s"
    sleep 2
  done
  log "  the controller answers at $(controller_url)"

  # The store carried the local backend over; the cluster builds
  # kubernetes-granule (the daemon falls back to its flags on a stored
  # backend it cannot build, and says so -- this makes it the choice).
  api PUT /api/settings -H 'Content-Type: application/json' -d '{"sandboxBackend":"kubernetes-granule"}' >/dev/null ||
    die "could not set sandboxBackend=kubernetes-granule on the controller"
  log "  sandbox backend: kubernetes-granule"

  if [[ "${HAVE_JQ}" == "true" ]]; then
    local servers pending
    while :; do
      servers="$(api GET /api/capability-servers 2>/dev/null || true)"
      pending="$(jq -r '[.[] | select(.status.run == "host") | select(.status.state != "attached")
        | "\(.id)=\(.status.state)"] | join(" ")' <<<"${servers:-[]}" 2>/dev/null || printf 'unreadable')"
      [[ -z "${pending}" ]] && break
      [[ "${SECONDS}" -lt "${deadline}" ]] ||
        die "hostd's capability servers did not all attach within ${VERIFY_TIMEOUT}s: ${pending}"
      sleep 3
    done
    log "  hostd attached: $(jq -r '[.[] | select(.status.run == "host") | .id] | join(", ")' <<<"${servers}")"
  fi
  api DELETE /api/pipeline-pause >/dev/null || warn "could not lift the migration's dispatch pause: grain resume against $(controller_url)"
  log "  dispatch resumed on the cluster controller"
}

migrate() {
  preflight
  cluster
  drain_and_stop
  backup
  migrate_state
  deploy_controller
  log "Stage 5: point this host at the cluster controller"
  configure_forward
  configure_hostd
  verify
  log "Done. The controller runs in ${NAMESPACE}; ${DAEMON_UNIT} is masked here."
  log "Backup: ${BACKUP_ARCHIVE}. Undo with: $0 --rollback --kubeconfig ${KUBECONFIG_PATH}"
}

# --- Rollback -------------------------------------------------------------

rollback() {
  log "Rollback: cluster controller -> local ${DAEMON_UNIT}"
  if [[ "${DRY_RUN}" != "true" && "${GRAIN_MIGRATE_SKIP_ROOT_CHECK:-0}" != "1" && "$(id -u)" != "0" ]]; then
    die "run as root"
  fi
  [[ -d "${DATA_DIR}" ]] || die "no local data directory at ${DATA_DIR} (pass --data-dir)"
  if [[ -z "${KUBECONFIG_PATH}" && -f "${GRAIN_ETC_DIR}/gke-admin.kubeconfig" ]]; then
    KUBECONFIG_PATH="${GRAIN_ETC_DIR}/gke-admin.kubeconfig"
  fi
  [[ -n "${KUBECONFIG_PATH}" ]] || die "pass --kubeconfig: rollback has to stop the cluster controller first"
  # The transfer pod's image, as the migration chose it.
  resolve_local_daemon

  log "  Step 1: stop Deployment/grain"
  run kc scale deployment/grain --replicas=0
  # "no matching resources" when nothing was running is not a failure.
  run kc wait --for=delete pod -l app.kubernetes.io/name=grain --timeout=300s || true
  if [[ "$(deployment_replicas)" -gt 0 ]]; then
    die "Deployment/grain still has replicas: two controllers must never run against one state repository"
  fi

  if [[ "${SYNC_BACK}" == "true" ]]; then
    local stamp aside
    stamp="$(date -u +%Y%m%dT%H%M%SZ)"
    aside="${DATA_DIR}.pre-rollback-${stamp}"
    log "  Step 2: copy the cluster's data directory back (the local one moves to ${aside})"
    start_transfer_pod
    run mv "${DATA_DIR}" "${aside}"
    run mkdir -p "${DATA_DIR}"
    if [[ "${DRY_RUN}" == "true" ]]; then
      printf '+ kubectl --kubeconfig=%q -n %q exec %s -- tar -C /data -cf - . | tar -C %q -xf -\n' "${KUBECONFIG_PATH}" "${NAMESPACE}" "${TRANSFER_POD}" "${DATA_DIR}"
    else
      kc exec "${TRANSFER_POD}" -- tar -C /data -cf - . | tar -C "${DATA_DIR}" -xf -
      rm -f "${DATA_DIR}/.migrated-from-local"
    fi
    run chown -R "$(daemon_user):" "${DATA_DIR}"
    stop_transfer_pod
  else
    log "  Step 2: keeping ${DATA_DIR} as it is (--no-sync-back): what the cluster controller did is not carried back"
  fi

  log "  Step 3: point hostd back at its local socket"
  run rm -f "${SYSTEMD_DIR}/${HOSTD_UNIT}.d/gke.conf"
  local unit
  for unit in "${MODEL_UNIT}" "${FORWARD_UNIT}"; do
    if [[ -f "${SYSTEMD_DIR}/${unit}" ]]; then
      run systemctl disable --now "${unit}"
      run rm -f "${SYSTEMD_DIR}/${unit}"
    fi
  done
  run systemctl daemon-reload
  run systemctl restart "${HOSTD_UNIT}"

  log "  Step 4: restart ${DAEMON_UNIT}"
  run systemctl unmask "${DAEMON_UNIT}"
  # Stage 2 moved the unit's own file aside to mask it; unmask has taken
  # the mask away (a dry run only prints it), so the file goes back.
  local unit_path="${SYSTEMD_DIR}/${DAEMON_UNIT}"
  if [[ -f "${unit_path}${ASIDE_SUFFIX}" ]]; then
    if [[ "${DRY_RUN}" == "true" || ! -e "${unit_path}" ]]; then
      run mv "${unit_path}${ASIDE_SUFFIX}" "${unit_path}"
      run systemctl daemon-reload
    else
      warn "${unit_path} was written again since the migration; leaving it, and the migration's copy at ${unit_path}${ASIDE_SUFFIX}"
    fi
  fi
  run systemctl enable --now "${DAEMON_UNIT}"
  if command -v grain >/dev/null 2>&1; then
    run grain pause -resume || warn "could not lift the dispatch pause: run grain pause -resume once the daemon answers"
  fi
  log "Done. ${DAEMON_UNIT} runs here again; Deployment/grain is at replicas=0."
}

main() {
  parse_args "$@"
  resolve_data_dir
  if [[ "${PRINT_MANIFEST}" == "true" ]]; then
    resolve_local_daemon
    render_manifest 1
    return 0
  fi
  [[ "${DRY_RUN}" == "true" ]] && log "dry run: printing every check and command, changing nothing"
  if [[ "${ROLLBACK}" == "true" ]]; then
    rollback
  else
    migrate
  fi
}

main "$@"
