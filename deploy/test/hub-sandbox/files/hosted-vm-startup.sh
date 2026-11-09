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

# Startup and reconciliation script injected into hosted Grain GCE VM instances
# (`grain-vm-<entry>`, `kind = instance`) provisioned by `grain-registry`.
# Verifies `/dev/kvm` nested virtualization, configures per-instance PostgreSQL
# connectivity over the private VPC, and runs `grain daemon` on `:8080` with
# `granule` microVM sandboxes and `-ui-require-verifier=registry`.

set -euo pipefail

export DEBIAN_FRONTEND=noninteractive

DEFAULT_DAEMON_IMAGE="${DEFAULT_DAEMON_IMAGE:-docker.io/grainai/grain:main}"

install_packages() {
  if command -v docker >/dev/null 2>&1 && command -v jq >/dev/null 2>&1 && command -v curl >/dev/null 2>&1 && command -v nft >/dev/null 2>&1; then
    return 0
  fi
  apt-get -o Acquire::Retries=5 -o Acquire::http::Timeout=30 update
  apt-get -o Acquire::Retries=5 -o Acquire::http::Timeout=30 install -y --no-install-recommends \
    ca-certificates \
    curl \
    docker.io \
    jq \
    nftables \
    openssl
  systemctl enable --now docker
  systemctl enable --now nftables || true
}

write_reconcile_script() {
  cat > /usr/local/sbin/grain-hosted-vm-reconcile <<RECONCILE_HEAD
#!/usr/bin/env bash
set -euo pipefail
DEFAULT_DAEMON_IMAGE=$(printf '%q' "${DEFAULT_DAEMON_IMAGE}")
RECONCILE_HEAD
  cat >> /usr/local/sbin/grain-hosted-vm-reconcile <<'RECONCILE_EOF'

if [[ ! -e /dev/kvm ]]; then
  echo "grain-hosted-vm-reconcile: /dev/kvm is missing; nested virtualization is required" >&2
  exit 1
fi

META_URL="http://metadata.google.internal/computeMetadata/v1"
META_HDR="Metadata-Flavor: Google"

cfg_json="$(curl -fsSL -H "${META_HDR}" "${META_URL}/instance/attributes/grain-config")"

entry_name="$(jq -r '.GRAIN_ENTRY_NAME // empty' <<<"${cfg_json}")"
registry_url="$(jq -r '.GRAIN_HUB_URL // .GRAIN_REGISTRY_URL // empty' <<<"${cfg_json}")"
jwks_url="$(jq -r '.GRAIN_JWKS_URL // empty' <<<"${cfg_json}")"
instance_domain="$(jq -r '.GRAIN_INSTANCE_DOMAIN // empty' <<<"${cfg_json}")"
owner_email="$(jq -r '.GRAIN_OWNER_EMAIL // empty' <<<"${cfg_json}")"
owner_group="$(jq -r '.GRAIN_OWNER_GROUP // empty' <<<"${cfg_json}")"
sql_ip="$(jq -r '.GRAIN_SQL_PRIVATE_IP // empty' <<<"${cfg_json}")"
db_name="$(jq -r '.GRAIN_DB_NAME // empty' <<<"${cfg_json}")"
db_user="$(jq -r '.GRAIN_DB_USER // empty' <<<"${cfg_json}")"
db_pw_secret="$(jq -r '.GRAIN_DB_PASSWORD_SECRET // .db_password_secret // empty' <<<"${cfg_json}")"
daemon_image="$(jq -r '.GRAIN_DAEMON_IMAGE // empty' <<<"${cfg_json}")"

if [[ -z "${daemon_image}" ]]; then
  daemon_image="${DEFAULT_DAEMON_IMAGE}"
fi

if [[ -z "${entry_name}" || -z "${registry_url}" || -z "${jwks_url}" || -z "${instance_domain}" || -z "${owner_email}" || -z "${sql_ip}" || -z "${db_name}" || -z "${db_user}" || -z "${db_pw_secret}" ]]; then
  echo "grain-hosted-vm-reconcile: incomplete grain-config metadata on hosted VM" >&2
  exit 1
fi

entry_host="${entry_name}.${instance_domain}"
entry_url="https://${entry_host}"

allowed_principals="user:${owner_email}"
if [[ -n "${owner_group}" ]]; then
  allowed_principals="${allowed_principals},${owner_group}"
fi

if ! id -u grain >/dev/null 2>&1; then
  useradd -m -d /var/lib/grain/home -s /bin/bash grain
fi
uid="$(id -u grain)"
gid="$(id -g grain)"
docker_gid="$(getent group docker | cut -d: -f3 || true)"
kvm_gid="$(getent group kvm | cut -d: -f3 || true)"

install -d -m 0755 -o "${uid}" -g "${gid}" /var/lib/grain
install -d -m 0700 -o "${uid}" -g "${gid}" /var/lib/grain/home
install -d -m 0700 -o "${uid}" -g "${gid}" /var/lib/grain/data
install -d -m 0700 -o "${uid}" -g "${gid}" /var/lib/grain/sandboxes
install -d -m 0755 -o "${uid}" -g "${gid}" /var/lib/granule
install -d -m 0700 /etc/grain-daemon

# The registry keeps the database password out of grain-config (GCE metadata
# is readable by anyone with compute.instances.get) and names the Secret
# Manager secret holding it instead; read it with this VM's own token.
# Transient failures (429/5xx, refused connections) are retried; anything
# else fails closed before daemon.env is written.
sm_token="$(curl -fsSL -H "${META_HDR}" "${META_URL}/instance/service-accounts/default/token" | jq -r '.access_token')"
if ! db_pw_json="$(curl -fsSL --retry 5 --retry-delay 2 --retry-connrefused \
  -H @<(printf 'Authorization: Bearer %s\n' "${sm_token}") \
  "https://secretmanager.googleapis.com/v1/${db_pw_secret#/}/versions/latest:access")"; then
  echo "grain-hosted-vm-reconcile: reading database password secret ${db_pw_secret} from Secret Manager failed" >&2
  exit 1
fi
unset sm_token
db_pw="$(jq -r '.payload.data // empty' <<<"${db_pw_json}" | base64 -d | tr -d '\r\n')"
unset db_pw_json
if [[ -z "${db_pw}" ]]; then
  echo "grain-hosted-vm-reconcile: database password secret ${db_pw_secret} is empty or unreadable" >&2
  exit 1
fi

enc_user="$(printf '%s' "${db_user}" | jq -sRr @uri)"
enc_pw="$(printf '%s' "${db_pw}" | jq -sRr @uri)"
unset db_pw

umask 077
cat > /etc/grain-daemon/daemon.env <<ENV_EOF
GRAIN_DB_DRIVER=postgres
GRAIN_DB_DSN=postgres://${enc_user}:${enc_pw}@${sql_ip}:5432/${db_name}?sslmode=require
ENV_EOF
chmod 0600 /etc/grain-daemon/daemon.env

ar_host="${daemon_image%%/*}"
if [[ "${ar_host}" =~ ^[a-z0-9-]+-docker\.pkg\.dev$ ]]; then
  access_token="$(curl -fsSL -H "${META_HDR}" "${META_URL}/instance/service-accounts/default/token" | jq -r '.access_token')"
  printf '%s' "${access_token}" | docker login -u oauth2accesstoken --password-stdin "https://${ar_host}"
fi

docker pull "${daemon_image}"

docker_bridge_gw="$(docker network inspect bridge --format '{{(index .IPAM.Config 0).Gateway}}' 2>/dev/null || true)"
if [[ -z "${docker_bridge_gw}" ]]; then
  docker_bridge_gw="172.17.0.1"
fi

daemon_args=(
  daemon
  -data-dir /var/lib/grain/data
  -sandbox-dir /var/lib/grain/sandboxes
  -granule-sandboxes
  -granule-ssh-user debian
  -granule-workspace /home/debian
  -granule-git-proxy-host "${docker_bridge_gw}"
  -granule-create-arg=-disk-mode=overlay
  -granule-create-arg=-guest-user
  -granule-create-arg=debian
  -container-git-proxy-host "${docker_bridge_gw}"
  -disable-self-upgrade
  -management-url "${registry_url}/entries/${entry_name}"
  -deployment-owner "${owner_email}"
  -ui-allowed-principals "${allowed_principals}"
  -ui-addr 0.0.0.0:8080
  -ui-allowed-hosts "${entry_host},127.0.0.1,localhost"
  -ui-require-verifier registry
  -ui-hub-issuer grain-registry
  -ui-hub-audience "${entry_url}"
  -ui-hub-keys-url "${jwks_url}"
)

image_id="$(docker image inspect --format '{{.Id}}' "${daemon_image}")"
desired_spec="$(printf '%s|%s|%s' "${daemon_image}" "${image_id}" "$(printf '%s' "${cfg_json}" | sha256sum | awk '{print $1}')")"
current_spec=""
if [[ -r /var/lib/grain/running-spec ]]; then
  current_spec="$(cat /var/lib/grain/running-spec)"
fi

docker_extra_groups=()
if [[ -n "${docker_gid}" ]]; then
  docker_extra_groups+=(--group-add "${docker_gid}")
fi
if [[ -n "${kvm_gid}" ]]; then
  docker_extra_groups+=(--group-add "${kvm_gid}")
fi

if ! docker inspect grain-daemon >/dev/null 2>&1 || [[ "${desired_spec}" != "${current_spec}" ]]; then
  docker rm -f grain-daemon >/dev/null 2>&1 || true
  docker run -d \
    --name grain-daemon \
    --restart unless-stopped \
    --network host \
    --pid host \
    --user "${uid}:${gid}" \
    "${docker_extra_groups[@]}" \
    --cap-add NET_BIND_SERVICE \
    --cap-add NET_ADMIN \
    --device /dev/kvm:/dev/kvm \
    --env-file /etc/grain-daemon/daemon.env \
    -e HOME=/var/lib/grain/home \
    -e GRAIN_DATA_DIR=/var/lib/grain/data \
    -e GRAIN_HUB_ISSUER=grain-registry \
    -e GRAIN_HUB_AUDIENCE="${entry_url}" \
    -e GRAIN_HUB_KEYS_URL="${jwks_url}" \
    -e GRAIN_REGISTRY_ISSUER=grain-registry \
    -e GRAIN_REGISTRY_AUDIENCE="${entry_url}" \
    -e GRAIN_REGISTRY_KEYS_URL="${jwks_url}" \
    -v /var/run/docker.sock:/var/run/docker.sock \
    -v /var/lib/grain:/var/lib/grain \
    -v /var/lib/granule:/var/lib/granule \
    "${daemon_image}" \
    "${daemon_args[@]}"
  umask 077
  printf '%s\n' "${desired_spec}" > /var/lib/grain/running-spec
  chmod 0600 /var/lib/grain/running-spec
else
  docker start grain-daemon >/dev/null || true
fi

cat > /usr/local/bin/grain <<WRAPPER_EOF
#!/usr/bin/env bash
set -euo pipefail
exec docker exec -u "${uid}:${gid}" -e GRAIN_DATA_DIR=/var/lib/grain/data -e GRAIN_SERVER=http://127.0.0.1:8080 grain-daemon /usr/local/bin/grain "\$@"
WRAPPER_EOF
chmod 0755 /usr/local/bin/grain

healthy=0
for _ in $(seq 1 45); do
  if curl -fsS http://127.0.0.1:8080/api/auth/whoami >/dev/null 2>&1; then
    healthy=1
    break
  fi
  sleep 1
done

if [[ "${healthy}" -ne 1 ]]; then
  echo "grain-hosted-vm-reconcile: grain-daemon failed health check; recent container logs:" >&2
  docker logs --tail 80 grain-daemon >&2 || true
  exit 1
fi

echo "grain-daemon is healthy on 0.0.0.0:8080 with /dev/kvm enabled (entry=${entry_name})"
RECONCILE_EOF
  chmod 0755 /usr/local/sbin/grain-hosted-vm-reconcile
}

install_packages
write_reconcile_script
/usr/local/sbin/grain-hosted-vm-reconcile
