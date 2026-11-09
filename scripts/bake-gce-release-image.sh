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

# Bake a per-release Compute Engine VM image into family `grain-release`
# (design/design/hub.design.md, Step 8).
#
# What is baked into every `grain-release` image:
#   - Docker Engine, the `grain` system account, and pre-pulled release
#     `docker.io/grainai/grain:<tag>` and sandbox guest images via
#     scripts/setup.sh
#   - `cloud-sql-proxy` v2 (`--auto-iam-authn --unix-socket /cloudsql`) and
#     `cloud-sql-proxy.service`
#   - `/usr/local/lib/grain/grain-instance-init.sh` and
#     `grain-instance-init.service` (reads `grain-config` GCE metadata,
#     fetches `local-api-token` and `config-repo-key` from Secret Manager
#     into `/run/grain-secrets`, clones the per-instance config repo, and
#     writes `/etc/grain/daemon.env` including `-deployment-owner`)
#   - `grain-block-metadata.service` (blocks non-root/container processes
#     from reaching 169.254.169.254 directly)
#   - `grain-daemon.service` with bounded systemd shutdown drain
#     (`docker stop --time 300 grain-daemon`, `TimeoutStopSec=330`) so a
#     `gcloud compute instances stop` or replacement upgrade drains active
#     runs, flushes the final state-repo mirror commit, and exits cleanly.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

GRAIN_GCP_PROJECT="${GRAIN_GCP_PROJECT:-}"
GRAIN_GCP_ZONE="${GRAIN_GCP_ZONE:-us-central1-a}"
GRAIN_RELEASE_VERSION="${GRAIN_RELEASE_VERSION:-${GRAIN_IMAGE_TAG:-latest}}"
GRAIN_IMAGE="${GRAIN_IMAGE:-docker.io/grainai/grain}"
GRAIN_IMAGE_FAMILY="${GRAIN_IMAGE_FAMILY:-grain-release}"
GRAIN_GCE_IMAGE_NAME="${GRAIN_GCE_IMAGE_NAME:-}"
GRAIN_SOURCE_IMAGE_FAMILY="${GRAIN_SOURCE_IMAGE_FAMILY:-debian-12}"
GRAIN_SOURCE_IMAGE_PROJECT="${GRAIN_SOURCE_IMAGE_PROJECT:-debian-cloud}"
GRAIN_BUILDER_MACHINE_TYPE="${GRAIN_BUILDER_MACHINE_TYPE:-e2-standard-4}"
GRAIN_BUILDER_DISK_SIZE="${GRAIN_BUILDER_DISK_SIZE:-50GB}"
GRAIN_BUILDER_NETWORK="${GRAIN_BUILDER_NETWORK:-default}"
GRAIN_BUILDER_SUBNET="${GRAIN_BUILDER_SUBNET:-}"
GRAIN_CLOUD_SQL_PROXY_VERSION="${GRAIN_CLOUD_SQL_PROXY_VERSION:-v2.14.1}"
GRAIN_BAKE_ROOT="${GRAIN_BAKE_ROOT:-/}"
GRAIN_BAKE_SKIP_SETUP="${GRAIN_BAKE_SKIP_SETUP:-0}"
GRAIN_BAKE_SKIP_DOWNLOAD="${GRAIN_BAKE_SKIP_DOWNLOAD:-0}"

PREPARE_GUEST=0

usage() {
  cat <<'EOF'
Usage: scripts/bake-gce-release-image.sh [options]

Bake a per-release Compute Engine VM image into family `grain-release`.

Options:
  --project <id>               GCP project to build and publish the image in ($GRAIN_GCP_PROJECT)
  --zone <zone>                Compute Engine zone for the temporary builder VM (default: us-central1-a)
  --version <ver>              Release version / tag to bake (default: $GRAIN_RELEASE_VERSION or latest)
  --image-family <family>      Compute Engine image family (default: grain-release)
  --image-name <name>          Explicit Compute Engine image name
  --source-image-family <fam>  Base OS image family (default: debian-12)
  --source-image-project <p>   Base OS image project (default: debian-cloud)
  --machine-type <type>        Builder VM machine type (default: e2-standard-4)
  --disk-size <size>           Builder VM boot disk size (default: 50GB)
  --prepare-guest              Run in-guest image preparation steps on the current machine
  -h, --help                   Show this help message
EOF
}

log() {
  echo "bake-gce-release-image: $*" >&2
}

die() {
  echo "bake-gce-release-image: ERROR: $*" >&2
  exit 1
}

root_path() {
  local rel="${1#/}"
  if [ "${GRAIN_BAKE_ROOT%/}" = "" ]; then
    printf '/%s' "$rel"
  else
    printf '%s/%s' "${GRAIN_BAKE_ROOT%/}" "$rel"
  fi
}

install_cloud_sql_proxy() {
  local bin_path
  bin_path="$(root_path "/usr/local/bin/cloud-sql-proxy")"
  local systemd_dir
  systemd_dir="$(root_path "/etc/systemd/system")"
  local cloudsql_dir
  cloudsql_dir="$(root_path "/cloudsql")"

  install -d -m 0755 "$(dirname "$bin_path")" "$systemd_dir" "$cloudsql_dir"

  if [ "$GRAIN_BAKE_ROOT" = "/" ] && [ "$(id -u)" -eq 0 ]; then
    if ! id -u cloudsqlproxy >/dev/null 2>&1; then
      useradd --system --no-create-home --shell /usr/sbin/nologin cloudsqlproxy
    fi
    chown cloudsqlproxy:cloudsqlproxy "$cloudsql_dir"
  fi

  if [ "$GRAIN_BAKE_SKIP_DOWNLOAD" != "1" ] && [ ! -x "$bin_path" ]; then
    local arch
    case "$(uname -m)" in
      x86_64|amd64) arch="amd64" ;;
      aarch64|arm64) arch="arm64" ;;
      *) die "unsupported architecture $(uname -m) for cloud-sql-proxy" ;;
    esac
    local url="https://storage.googleapis.com/cloud-sql-connectors/cloud-sql-proxy/${GRAIN_CLOUD_SQL_PROXY_VERSION}/cloud-sql-proxy.linux.${arch}"
    log "downloading cloud-sql-proxy ${GRAIN_CLOUD_SQL_PROXY_VERSION} (${arch})"
    curl -fsSL "$url" -o "$bin_path"
    chmod 0755 "$bin_path"
  elif [ ! -e "$bin_path" ]; then
    cat > "$bin_path" <<' proxy_stub'
#!/usr/bin/env bash
exec /usr/local/bin/cloud-sql-proxy "$@"
 proxy_stub
    chmod 0755 "$bin_path"
  fi

  cat > "${systemd_dir}/cloud-sql-proxy.service" <<'UNIT'
[Unit]
Description=Google Cloud SQL Auth Proxy (IAM database authentication)
After=network-online.target
Wants=network-online.target
ConditionPathExists=/etc/grain/cloud-sql-proxy.env

[Service]
Type=simple
User=cloudsqlproxy
Group=cloudsqlproxy
EnvironmentFile=/etc/grain/cloud-sql-proxy.env
ExecStart=/usr/local/bin/cloud-sql-proxy --auto-iam-authn --unix-socket /cloudsql ${CLOUD_SQL_INSTANCE}
Restart=on-failure
RestartSec=5
TimeoutStopSec=30

[Install]
WantedBy=multi-user.target
UNIT
  chmod 0644 "${systemd_dir}/cloud-sql-proxy.service"
}

install_grain_instance_init() {
  local lib_dir
  lib_dir="$(root_path "/usr/local/lib/grain")"
  local systemd_dir
  systemd_dir="$(root_path "/etc/systemd/system")"
  local src_script="${SCRIPT_DIR}/grain-instance-init.sh"

  [ -f "$src_script" ] || die "missing ${src_script}"
  install -d -m 0755 "$lib_dir" "$systemd_dir"
  install -m 0755 "$src_script" "${lib_dir}/grain-instance-init.sh"

  cat > "${systemd_dir}/grain-instance-init.service" <<'UNIT'
[Unit]
Description=Grain registry instance initializer (grain-config metadata, Secret Manager, config repo)
After=network-online.target grain-block-metadata.service docker.service
Wants=network-online.target grain-block-metadata.service
Before=grain-daemon.service grain-hostd.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/usr/local/lib/grain/grain-instance-init.sh

[Install]
WantedBy=multi-user.target
UNIT
  chmod 0644 "${systemd_dir}/grain-instance-init.service"
}

verify_shutdown_drain_unit() {
  local unit_file
  unit_file="$(root_path "/etc/systemd/system/grain-daemon.service")"
  [ -f "$unit_file" ] || die "missing ${unit_file}"
  grep -q 'stop --time 300 grain-daemon' "$unit_file" \
    || die "${unit_file} is missing bounded docker stop --time 300 grain-daemon"
  grep -q '^TimeoutStopSec=330$' "$unit_file" \
    || die "${unit_file} is missing TimeoutStopSec=330 covering the 4m30s shutdown drain"
  grep -q '^EnvironmentFile=-/etc/grain/daemon.env$' "$unit_file" \
    || die "${unit_file} is missing EnvironmentFile=-/etc/grain/daemon.env"
}

prepare_guest() {
  log "preparing guest image for release ${GRAIN_RELEASE_VERSION}"
  if [ "$GRAIN_BAKE_ROOT" = "/" ] && [ "$GRAIN_BAKE_SKIP_SETUP" != "1" ]; then
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -y
    apt-get install -y ca-certificates curl git jq openssl
  fi

  install_cloud_sql_proxy
  install_grain_instance_init

  if [ "$GRAIN_BAKE_SKIP_SETUP" != "1" ]; then
    GRAIN_IMAGE="$GRAIN_IMAGE" \
    GRAIN_IMAGE_TAG="$GRAIN_RELEASE_VERSION" \
    GRAIN_DB_DRIVER="sqlite" \
      bash "${SCRIPT_DIR}/setup.sh"
  fi

  local daemon_unit
  daemon_unit="$(root_path "/etc/systemd/system/grain-daemon.service")"
  if [ -f "$daemon_unit" ]; then
    verify_shutdown_drain_unit
  fi

  local etc_grain
  etc_grain="$(root_path "/etc/grain")"
  install -d -m 0755 "$etc_grain"
  cat > "${etc_grain}/release-image" <<EOF
GRAIN_RELEASE_VERSION=${GRAIN_RELEASE_VERSION}
GRAIN_IMAGE=${GRAIN_IMAGE}:${GRAIN_RELEASE_VERSION}
GRAIN_IMAGE_FAMILY=${GRAIN_IMAGE_FAMILY}
EOF
  chmod 0644 "${etc_grain}/release-image"

  # Scrub any builder-time database, secrets, or instance-specific env files so
  # every VM booted from this image starts clean and is configured by
  # grain-instance-init.service.
  local data_dir
  data_dir="$(root_path "/var/lib/grain/data")"
  rm -rf \
    "${data_dir}/grain.db" \
    "${data_dir}/grain.db-shm" \
    "${data_dir}/grain.db-wal" \
    "${data_dir}/db-config.json" \
    "${data_dir}/local-api-token" \
    "${data_dir}/secrets" \
    "${data_dir}/state-repo" \
    "${data_dir}/state-repo.json" \
    "${etc_grain}/daemon.env" \
    "${etc_grain}/cloud-sql-proxy.env" \
    "${etc_grain}/postgres.env"

  if [ "$GRAIN_BAKE_ROOT" = "/" ] && [ "$GRAIN_BAKE_SKIP_SETUP" != "1" ] && command -v systemctl >/dev/null 2>&1; then
    systemctl stop grain-daemon.service 2>/dev/null || true
    systemctl stop grain-hostd.service 2>/dev/null || true
    systemctl stop grain-postgres.service 2>/dev/null || true
    systemctl disable grain-postgres.service 2>/dev/null || true
    systemctl daemon-reload
    systemctl enable grain-block-metadata.service grain-instance-init.service cloud-sql-proxy.service grain-daemon.service
  fi
  log "guest preparation complete for ${GRAIN_IMAGE}:${GRAIN_RELEASE_VERSION}"
}

sanitize_gce_label() {
  printf '%s' "$1" \
    | tr '[:upper:]' '[:lower:]' \
    | sed -E 's/[^a-z0-9-]+/-/g; s/^-+//; s/-+$//; s/-+/-/g' \
    | cut -c1-40
}

bake_gce_image() {
  [ -n "$GRAIN_GCP_PROJECT" ] || die "--project (or GRAIN_GCP_PROJECT) is required"

  local clean_ver
  clean_ver="$(sanitize_gce_label "$GRAIN_RELEASE_VERSION")"
  [ -n "$clean_ver" ] || clean_ver="latest"
  local ts
  ts="$(date -u +%Y%m%d-%H%M%S)"
  local image_name="$GRAIN_GCE_IMAGE_NAME"
  if [ -z "$image_name" ]; then
    image_name="${GRAIN_IMAGE_FAMILY}-${clean_ver}-${ts}"
  fi
  BUILDER_VM="grain-img-bake-${clean_ver}-${ts}"
  BUILDER_VM="$(printf '%s' "$BUILDER_VM" | cut -c1-62 | sed -E 's/-+$//')"

  local create_args=(
    compute instances create "$BUILDER_VM"
    "--project=${GRAIN_GCP_PROJECT}"
    "--zone=${GRAIN_GCP_ZONE}"
    "--machine-type=${GRAIN_BUILDER_MACHINE_TYPE}"
    "--boot-disk-size=${GRAIN_BUILDER_DISK_SIZE}"
    "--boot-disk-type=pd-ssd"
    "--image-family=${GRAIN_SOURCE_IMAGE_FAMILY}"
    "--image-project=${GRAIN_SOURCE_IMAGE_PROJECT}"
    "--network=${GRAIN_BUILDER_NETWORK}"
    "--scopes=cloud-platform"
  )
  if [ -n "$GRAIN_BUILDER_SUBNET" ]; then
    create_args+=("--subnet=${GRAIN_BUILDER_SUBNET}")
  fi

  cleanup_builder() {
    [ -n "${BUILDER_VM:-}" ] || return 0
    log "cleaning up temporary builder VM ${BUILDER_VM}"
    gcloud compute instances delete "$BUILDER_VM" \
      "--project=${GRAIN_GCP_PROJECT}" \
      "--zone=${GRAIN_GCP_ZONE}" \
      --quiet >/dev/null 2>&1 || true
  }
  trap cleanup_builder EXIT

  log "creating temporary builder VM ${BUILDER_VM} in ${GRAIN_GCP_PROJECT}/${GRAIN_GCP_ZONE}"
  gcloud "${create_args[@]}"

  log "uploading installer and instance-init scripts to ${BUILDER_VM}"
  gcloud compute scp \
    "--project=${GRAIN_GCP_PROJECT}" \
    "--zone=${GRAIN_GCP_ZONE}" \
    "${SCRIPT_DIR}/setup.sh" \
    "${SCRIPT_DIR}/grain-instance-init.sh" \
    "${SCRIPT_DIR}/bake-gce-release-image.sh" \
    "${BUILDER_VM}:/tmp/"

  log "running --prepare-guest inside ${BUILDER_VM}"
  gcloud compute ssh "$BUILDER_VM" \
    "--project=${GRAIN_GCP_PROJECT}" \
    "--zone=${GRAIN_GCP_ZONE}" \
    --command="sudo env GRAIN_IMAGE='${GRAIN_IMAGE}' GRAIN_RELEASE_VERSION='${GRAIN_RELEASE_VERSION}' GRAIN_IMAGE_FAMILY='${GRAIN_IMAGE_FAMILY}' bash /tmp/bake-gce-release-image.sh --prepare-guest"

  log "stopping builder VM ${BUILDER_VM}"
  gcloud compute instances stop "$BUILDER_VM" \
    "--project=${GRAIN_GCP_PROJECT}" \
    "--zone=${GRAIN_GCP_ZONE}" \
    --quiet

  log "creating Compute Engine image ${image_name} in family ${GRAIN_IMAGE_FAMILY}"
  gcloud compute images create "$image_name" \
    "--project=${GRAIN_GCP_PROJECT}" \
    "--source-disk=${BUILDER_VM}" \
    "--source-disk-zone=${GRAIN_GCP_ZONE}" \
    "--family=${GRAIN_IMAGE_FAMILY}" \
    "--description=Grain release ${GRAIN_RELEASE_VERSION} (${GRAIN_IMAGE}:${GRAIN_RELEASE_VERSION})"

  log "baked Compute Engine image ${image_name} (family: ${GRAIN_IMAGE_FAMILY})"
}

while [ $# -gt 0 ]; do
  case "$1" in
    --project)
      GRAIN_GCP_PROJECT="$2"
      shift 2
      ;;
    --zone)
      GRAIN_GCP_ZONE="$2"
      shift 2
      ;;
    --version)
      GRAIN_RELEASE_VERSION="$2"
      shift 2
      ;;
    --image-family)
      GRAIN_IMAGE_FAMILY="$2"
      shift 2
      ;;
    --image-name)
      GRAIN_GCE_IMAGE_NAME="$2"
      shift 2
      ;;
    --source-image-family)
      GRAIN_SOURCE_IMAGE_FAMILY="$2"
      shift 2
      ;;
    --source-image-project)
      GRAIN_SOURCE_IMAGE_PROJECT="$2"
      shift 2
      ;;
    --machine-type)
      GRAIN_BUILDER_MACHINE_TYPE="$2"
      shift 2
      ;;
    --disk-size)
      GRAIN_BUILDER_DISK_SIZE="$2"
      shift 2
      ;;
    --prepare-guest)
      PREPARE_GUEST=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      die "unknown argument: $1"
      ;;
  esac
done

if [ "$PREPARE_GUEST" = "1" ]; then
  prepare_guest
else
  bake_gce_image
fi
