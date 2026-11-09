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

# Startup and reconciliation script for the disposable test grain daemon VM in
# a GCP sandbox project. Reads `grain-daemon-config` from GCE instance metadata,
# pulls the `grain-daemon` image from Artifact Registry, and runs `grain daemon`
# in control-plane test/demo mode (`-disable-native-sandboxes`) with outbound
# registry tunnel connectivity enabled.

set -euo pipefail

export DEBIAN_FRONTEND=noninteractive

install_packages() {
  if command -v docker >/dev/null 2>&1 && command -v jq >/dev/null 2>&1 && command -v curl >/dev/null 2>&1 && command -v openssl >/dev/null 2>&1; then
    return 0
  fi
  apt-get -o Acquire::Retries=5 -o Acquire::http::Timeout=30 update
  apt-get -o Acquire::Retries=5 -o Acquire::http::Timeout=30 install -y --no-install-recommends \
    ca-certificates \
    curl \
    docker.io \
    jq \
    openssl
  systemctl enable --now docker
}

write_reconcile_script() {
  cat > /usr/local/sbin/grain-daemon-reconcile <<'RECONCILE_EOF'
#!/usr/bin/env bash
set -euo pipefail

META_URL="http://metadata.google.internal/computeMetadata/v1"
META_HDR="Metadata-Flavor: Google"

cfg_json="$(curl -fsSL -H "${META_HDR}" "${META_URL}/instance/attributes/grain-daemon-config")"

daemon_image="$(jq -r '.daemon_image // empty' <<<"${cfg_json}")"
entry_name="$(jq -r '.entry_name // empty' <<<"${cfg_json}")"
registry_host="$(jq -r '.registry_host // empty' <<<"${cfg_json}")"
instance_domain="$(jq -r '.instance_domain // empty' <<<"${cfg_json}")"
display_name="$(jq -r '.display_name // empty' <<<"${cfg_json}")"
purpose="$(jq -r '.purpose // empty' <<<"${cfg_json}")"
allowed_principals_csv="$(jq -r '(.allowed_principals // []) | join(",")' <<<"${cfg_json}")"

if [[ -z "${daemon_image}" || -z "${entry_name}" || -z "${registry_host}" || -z "${instance_domain}" || -z "${allowed_principals_csv}" ]]; then
  echo "grain-daemon-reconcile: incomplete metadata in grain-daemon-config" >&2
  exit 1
fi

entry_host="${entry_name}.${instance_domain}"
entry_url="https://${entry_host}"
jwks_url="https://${registry_host}/.well-known/jwks.json"

install -d -m 0755 /var/lib/grain-daemon
install -d -m 0700 -o 1000 -g 1000 /var/lib/grain-daemon/data
install -d -m 0700 -o 1000 -g 1000 /var/lib/grain-daemon/sandboxes

# Authenticate Docker to Artifact Registry when pulling from *-docker.pkg.dev.
ar_host="${daemon_image%%/*}"
if [[ "${ar_host}" =~ ^[a-z0-9-]+-docker\.pkg\.dev$ ]]; then
  access_token="$(curl -fsSL -H "${META_HDR}" "${META_URL}/instance/service-accounts/default/token" | jq -r '.access_token')"
  printf '%s' "${access_token}" | docker login -u oauth2accesstoken --password-stdin "https://${ar_host}"
fi

docker pull "${daemon_image}"

mapfile -t target_repos < <(jq -r '(.target_repos // [])[]' <<<"${cfg_json}")
repo_args=()
for repo in "${target_repos[@]}"; do
  if [[ -n "${repo}" ]]; then
    repo_args+=(-repo "${repo}")
  fi
done

daemon_args=(
  daemon
  -data-dir /var/lib/grain-daemon/data
  -sandbox-dir /var/lib/grain-daemon/sandboxes
  -disable-native-sandboxes
  -disable-self-upgrade
  -ui-addr 0.0.0.0:8080
  -ui-allowed-hosts "${entry_host}"
  -registry-host-kind workstation
)
if [[ -n "${display_name}" ]]; then
  daemon_args+=(-registry-display-name "${display_name}")
fi
if [[ -n "${purpose}" ]]; then
  daemon_args+=(-registry-purpose "${purpose}")
fi
daemon_args+=("${repo_args[@]}")

image_id="$(docker image inspect --format '{{.Id}}' "${daemon_image}")"
desired_spec="$(printf '%s|%s|%s' "${daemon_image}" "${image_id}" "$(printf '%s' "${cfg_json}" | sha256sum | awk '{print $1}')")"
current_spec=""
if [[ -r /var/lib/grain-daemon/running-spec ]]; then
  current_spec="$(cat /var/lib/grain-daemon/running-spec)"
fi

if ! docker inspect grain-daemon >/dev/null 2>&1 || [[ "${desired_spec}" != "${current_spec}" ]]; then
  docker rm -f grain-daemon >/dev/null 2>&1 || true
  docker run -d \
    --name grain-daemon \
    --restart unless-stopped \
    -p 127.0.0.1:8080:8080 \
    -v /var/lib/grain-daemon:/var/lib/grain-daemon \
    -e GRAIN_DATA_DIR=/var/lib/grain-daemon/data \
    -e GRAIN_REGISTRY_CONNECT=true \
    -e GRAIN_REGISTRY_ISSUER=grain-registry \
    -e GRAIN_REGISTRY_AUDIENCE="${entry_url}" \
    -e GRAIN_REGISTRY_KEYS_URL="${jwks_url}" \
    -e GRAIN_ALLOWED_PRINCIPALS="${allowed_principals_csv}" \
    "${daemon_image}" \
    "${daemon_args[@]}"
  umask 077
  printf '%s\n' "${desired_spec}" > /var/lib/grain-daemon/running-spec
  chmod 0600 /var/lib/grain-daemon/running-spec
else
  docker start grain-daemon >/dev/null || true
fi

# Install a convenience host wrapper so `sudo grain ...` runs against the local
# containerized daemon without putting GRAIN_REGISTRY_TOKEN in argv.
cat > /usr/local/bin/grain <<'WRAPPER_EOF'
#!/usr/bin/env bash
set -euo pipefail
docker_env=(-e GRAIN_DATA_DIR=/var/lib/grain-daemon/data -e GRAIN_SERVER=http://127.0.0.1:8080)
if [[ -n "${GRAIN_REGISTRY_TOKEN+x}" ]]; then
  docker_env+=(-e GRAIN_REGISTRY_TOKEN)
fi
exec docker exec -u grain "${docker_env[@]}" grain-daemon /usr/local/bin/grain "$@"
WRAPPER_EOF
chmod 0755 /usr/local/bin/grain

healthy=0
for _ in $(seq 1 30); do
  if curl -fsS http://127.0.0.1:8080/api/auth/whoami >/dev/null 2>&1; then
    healthy=1
    break
  fi
  sleep 1
done

if [[ "${healthy}" -ne 1 ]]; then
  echo "grain-daemon failed health check; recent container logs:" >&2
  docker logs --tail 80 grain-daemon >&2 || true
  exit 1
fi

# Keep the persisted access allowlist in sync on re-runs after initial DB creation.
mapfile -t principals < <(jq -r '(.allowed_principals // [])[]' <<<"${cfg_json}")
for principal in "${principals[@]}"; do
  if [[ -n "${principal}" ]]; then
    /usr/local/bin/grain access allow --reason "Admitted by daemon-up.sh" "${principal}" >/dev/null
  fi
done

echo "grain-daemon is healthy on 127.0.0.1:8080 (audience=${entry_url})"
RECONCILE_EOF
  chmod 0755 /usr/local/sbin/grain-daemon-reconcile
}

install_packages
write_reconcile_script
/usr/local/sbin/grain-daemon-reconcile
