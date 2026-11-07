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
  cat > /usr/local/sbin/grain-registry-reconcile <<'RECONCILE_EOF'
#!/usr/bin/env bash
set -euo pipefail

META_URL="http://metadata.google.internal/computeMetadata/v1"
META_HDR="Metadata-Flavor: Google"

cfg_json="$(curl -fsSL -H "${META_HDR}" "${META_URL}/instance/attributes/grain-registry-config")"

registry_image="$(jq -r '.registry_image' <<<"${cfg_json}")"
registry_host="$(jq -r '.registry_host' <<<"${cfg_json}")"
instance_domain="$(jq -r '.instance_domain' <<<"${cfg_json}")"
iap_audience="$(jq -r '.iap_audience' <<<"${cfg_json}")"
kms_key_name="$(jq -r '.kms_key_name' <<<"${cfg_json}")"

if [[ -z "${registry_image}" || -z "${registry_host}" || -z "${instance_domain}" || -z "${iap_audience}" || -z "${kms_key_name}" ]]; then
  echo "grain-registry-reconcile: incomplete metadata in grain-registry-config" >&2
  exit 1
fi

install -d -m 0700 /var/lib/grain-registry
install -d -m 0700 /var/lib/grain-registry/pgdata
install -d -m 0750 -o root -g 1000 /etc/grain-registry

pgpass_file="/var/lib/grain-registry/pgpass"
if [[ ! -s "${pgpass_file}" ]]; then
  umask 077
  openssl rand -hex 24 > "${pgpass_file}"
fi
chmod 0600 "${pgpass_file}"
pgpass="$(tr -d '\r\n' < "${pgpass_file}")"

# Pinned by digest so a retagged upstream image cannot change what runs here.
if ! docker inspect grain-registry-postgres >/dev/null 2>&1; then
  docker run -d \
    --name grain-registry-postgres \
    --restart unless-stopped \
    -p 127.0.0.1:5432:5432 \
    -e POSTGRES_USER=grain \
    -e POSTGRES_PASSWORD="${pgpass}" \
    -e POSTGRES_DB=grain_registry \
    -v /var/lib/grain-registry/pgdata:/var/lib/postgresql/data \
    postgres:16-alpine@sha256:aa6cb33cf4c19c765cefcf48d96176258800870a9a1e930ad126200f9d825139
else
  docker start grain-registry-postgres >/dev/null || true
fi

for _ in $(seq 1 30); do
  if docker exec grain-registry-postgres pg_isready -U grain -d grain_registry >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
docker exec grain-registry-postgres pg_isready -U grain -d grain_registry >/dev/null

# Authenticate Docker to Artifact Registry when pulling from *.pkg.dev.
ar_host="${registry_image%%/*}"
if [[ "${ar_host}" == *.pkg.dev ]]; then
  access_token="$(curl -fsSL -H "${META_HDR}" "${META_URL}/instance/service-accounts/default/token" | jq -r '.access_token')"
  printf '%s' "${access_token}" | docker login -u oauth2accesstoken --password-stdin "https://${ar_host}"
fi

docker pull "${registry_image}"

allowed_domains_yaml="$(jq -r '.allowed_domains[] | "    - " + (@json)' <<<"${cfg_json}")"
admins_yaml="$(jq -r '.admins[] | "    - " + (@json)' <<<"${cfg_json}")"
creators_yaml="$(jq -r '.creators[] | "    - " + (@json)' <<<"${cfg_json}")"
sp_count="$(jq -r '.service_principals | length' <<<"${cfg_json}")"
if [[ "${sp_count}" -gt 0 ]]; then
  sp_yaml=$'\n'"$(jq -r '.service_principals[] | "    - " + (@json)' <<<"${cfg_json}")"
else
  sp_yaml=" []"
fi

tmp_cfg="$(mktemp /etc/grain-registry/.registry.yaml.XXXXXX)"
cat > "${tmp_cfg}" <<YAML_EOF
listen: ":8080"
jwks_listen: ":8081"
host: "${registry_host}"
instance_domain: "${instance_domain}"
auth:
  provider: "iap"
  iap_audience: "${iap_audience}"
  allowed_domains:
${allowed_domains_yaml}
  admins:
${admins_yaml}
  creators:
${creators_yaml}
  service_principals:${sp_yaml}
groups:
  provider: "static"
signing:
  provider: "kms"
  kms_key_name: "${kms_key_name}"
tunnel:
  transport: "ws-mux"
  peer_listen: "127.0.0.1:8082"
  self_addr: "127.0.0.1:8082"
database:
  url: "postgres://grain:${pgpass}@127.0.0.1:5432/grain_registry?sslmode=disable"
compute:
  provider: "none"
config_repos:
  provider: "none"
YAML_EOF

chown root:1000 "${tmp_cfg}"
chmod 0640 "${tmp_cfg}"

# Validate the rendered config with grain-registry itself before swapping.
docker run --rm --network host \
  -v "${tmp_cfg}:/etc/grain-registry/registry.yaml:ro" \
  "${registry_image}" \
  validate -config /etc/grain-registry/registry.yaml

mv -f "${tmp_cfg}" /etc/grain-registry/registry.yaml

desired_spec="${registry_image}|$(sha256sum /etc/grain-registry/registry.yaml | awk '{print $1}')"
current_spec=""
if [[ -r /var/lib/grain-registry/running-spec ]]; then
  current_spec="$(cat /var/lib/grain-registry/running-spec)"
fi

if ! docker inspect grain-registry >/dev/null 2>&1 || [[ "${desired_spec}" != "${current_spec}" ]]; then
  docker rm -f grain-registry >/dev/null 2>&1 || true
  docker run -d \
    --name grain-registry \
    --restart unless-stopped \
    --network host \
    -v /etc/grain-registry/registry.yaml:/etc/grain-registry/registry.yaml:ro \
    "${registry_image}" \
    serve -config /etc/grain-registry/registry.yaml
  printf '%s\n' "${desired_spec}" > /var/lib/grain-registry/running-spec
else
  docker start grain-registry >/dev/null || true
fi

for _ in $(seq 1 30); do
  if curl -fsS -H "Host: ${registry_host}" http://127.0.0.1:8080/healthz >/dev/null 2>&1 &&
     curl -fsS -H "Host: ${registry_host}" http://127.0.0.1:8081/healthz >/dev/null 2>&1; then
    echo "grain-registry is healthy on :8080 and :8081"
    exit 0
  fi
  sleep 1
done

echo "grain-registry failed health check; recent container logs:" >&2
docker logs --tail 80 grain-registry >&2 || true
exit 1
RECONCILE_EOF
  chmod 0755 /usr/local/sbin/grain-registry-reconcile
}

install_packages
write_reconcile_script
/usr/local/sbin/grain-registry-reconcile
