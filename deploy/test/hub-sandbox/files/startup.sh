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
hosted_startup_raw="$(curl -fsSL -H "${META_HDR}" "${META_URL}/instance/attributes/grain-hosted-vm-startup")"

registry_image="$(jq -r '.registry_image // empty' <<<"${cfg_json}")"
hosted_daemon_image="$(jq -r '.hosted_daemon_image // "docker.io/grainai/grain:main"' <<<"${cfg_json}")"
project_id="$(jq -r '.project_id // empty' <<<"${cfg_json}")"
region="$(jq -r '.region // "us-central1"' <<<"${cfg_json}")"
network_name="$(jq -r '.network_name // empty' <<<"${cfg_json}")"
subnet_name="$(jq -r '.subnet_name // empty' <<<"${cfg_json}")"
sql_instance_name="$(jq -r '.sql_instance_name // empty' <<<"${cfg_json}")"
sql_connection_name="$(jq -r '.sql_connection_name // empty' <<<"${cfg_json}")"
sql_private_ip="$(jq -r '.sql_private_ip // empty' <<<"${cfg_json}")"
sql_admin_password="$(jq -r '.sql_admin_password // empty' <<<"${cfg_json}")"
registry_host="$(jq -r '.registry_host // empty' <<<"${cfg_json}")"
instance_domain="$(jq -r '.instance_domain // empty' <<<"${cfg_json}")"
iap_audience="$(jq -r '.iap_audience // empty' <<<"${cfg_json}")"
kms_key_name="$(jq -r '.kms_key_name // empty' <<<"${cfg_json}")"

if [[ -z "${registry_image}" || -z "${project_id}" || -z "${network_name}" || -z "${subnet_name}" || -z "${sql_instance_name}" || -z "${sql_connection_name}" || -z "${sql_private_ip}" || -z "${sql_admin_password}" || -z "${registry_host}" || -z "${instance_domain}" || -z "${iap_audience}" || -z "${kms_key_name}" ]]; then
  echo "grain-registry-reconcile: incomplete metadata in grain-registry-config" >&2
  exit 1
fi

install -d -m 0700 /var/lib/grain-registry
install -d -m 0700 /var/lib/grain-registry/pgdata
install -d -m 0750 -o root -g 1000 /etc/grain-registry

# Render hosted VM startup script with the configured hosted_daemon_image.
tmp_hosted="$(mktemp /etc/grain-registry/.hosted-vm-startup.sh.XXXXXX)"
awk -v img="${hosted_daemon_image}" '
  /^DEFAULT_DAEMON_IMAGE=/ {
    gsub(/\\/, "\\\\", img)
    gsub(/"/, "\\\"", img)
    print "DEFAULT_DAEMON_IMAGE=\"" img "\""
    next
  }
  { print }
' <<<"${hosted_startup_raw}" > "${tmp_hosted}"
chown root:1000 "${tmp_hosted}"
chmod 0640 "${tmp_hosted}"
mv -f "${tmp_hosted}" /etc/grain-registry/hosted-vm-startup.sh

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

# Wait for the shared instance_sql PostgreSQL pool VM on the VPC to accept TCP:5432.
sql_ready=0
for _ in $(seq 1 90); do
  if timeout 2 bash -c "</dev/tcp/${sql_private_ip}/5432" >/dev/null 2>&1; then
    sql_ready=1
    break
  fi
  sleep 1
done
if [[ "${sql_ready}" -ne 1 ]]; then
  echo "grain-registry-reconcile: shared SQL pool ${sql_private_ip}:5432 is not reachable" >&2
  exit 1
fi

# Authenticate Docker to Artifact Registry when pulling from *-docker.pkg.dev.
ar_host="${registry_image%%/*}"
if [[ "${ar_host}" =~ ^[a-z0-9-]+-docker\.pkg\.dev$ ]]; then
  access_token="$(curl -fsSL -H "${META_HDR}" "${META_URL}/instance/service-accounts/default/token" | jq -r '.access_token')"
  printf '%s' "${access_token}" | docker login -u oauth2accesstoken --password-stdin "https://${ar_host}"
fi

docker pull "${registry_image}"

host_json="$(jq -r '.registry_host | @json' <<<"${cfg_json}")"
instance_domain_json="$(jq -r '.instance_domain | @json' <<<"${cfg_json}")"
iap_audience_json="$(jq -r '.iap_audience | @json' <<<"${cfg_json}")"
kms_key_name_json="$(jq -r '.kms_key_name | @json' <<<"${cfg_json}")"
# Hosted GCE VMs (and their per-instance service accounts) may live in a
# separate project (hosted.tf); per-instance secrets stay in the registry project.
compute_project_id_json="$(jq -r '(.compute_project_id // .project_id) | @json' <<<"${cfg_json}")"
secrets_project_id_json="$(jq -r '(.secrets_project_id // .project_id) | @json' <<<"${cfg_json}")"
region_json="$(jq -r '(.region // "us-central1") | @json' <<<"${cfg_json}")"
network_name_json="$(jq -r '.network_name | @json' <<<"${cfg_json}")"
subnet_name_json="$(jq -r '.subnet_name | @json' <<<"${cfg_json}")"
sql_instance_name_json="$(jq -r '.sql_instance_name | @json' <<<"${cfg_json}")"
sql_connection_name_json="$(jq -r '.sql_connection_name | @json' <<<"${cfg_json}")"
sql_private_ip_json="$(jq -r '.sql_private_ip | @json' <<<"${cfg_json}")"
bind_project_iam_json="$(jq -r 'if .bind_project_iam == null then true else .bind_project_iam end | tostring' <<<"${cfg_json}")"
zones_yaml="$(jq -r '(.compute_zones // ["us-central1-a", "us-central1-b", "us-central1-c", "us-central1-f"])[] | "    - " + (@json)' <<<"${cfg_json}")"
allowed_domains_yaml="$(jq -r '.allowed_domains[] | "    - " + (@json)' <<<"${cfg_json}")"
admins_yaml="$(jq -r '.admins[] | "    - " + (@json)' <<<"${cfg_json}")"
creators_yaml="$(jq -r '.creators[] | "    - " + (@json)' <<<"${cfg_json}")"
sp_count="$(jq -r '.service_principals | length' <<<"${cfg_json}")"
if [[ "${sp_count}" -gt 0 ]]; then
  sp_yaml=$'\n'"$(jq -r '.service_principals[] | "    - " + (@json)' <<<"${cfg_json}")"
else
  sp_yaml=" []"
fi

enc_sql_admin_pw="$(printf '%s' "${sql_admin_password}" | jq -sRr @uri)"

tmp_cfg="$(mktemp /etc/grain-registry/.registry.yaml.XXXXXX)"
cat > "${tmp_cfg}" <<YAML_EOF
listen: ":8080"
jwks_listen: ":8081"
host: ${host_json}
instance_domain: ${instance_domain_json}
auth:
  provider: "iap"
  iap_audience: ${iap_audience_json}
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
  kms_key_name: ${kms_key_name_json}
tunnel:
  transport: "ws-mux"
  peer_listen: "127.0.0.1:8082"
  self_addr: "127.0.0.1:8082"
database:
  url: "postgres://grain:${pgpass}@127.0.0.1:5432/grain_registry?sslmode=disable"
compute:
  provider: "gce"
  project: ${compute_project_id_json}
  region: ${region_json}
  zones:
${zones_yaml}
  network: ${network_name_json}
  subnetwork: ${subnet_name_json}
  startup_script_file: "/etc/grain-registry/hosted-vm-startup.sh"
  bind_project_iam: ${bind_project_iam_json}
  health_timeout: 10m
  default_shape: "n2-standard-4"
  shapes:
    - name: "n2-standard-2"
      machine_type: "n2-standard-2"
      boot_disk_gb: 50
      boot_disk_type: "pd-balanced"
      quota_units: 1
      description: "2 vCPUs, 8 GB RAM, 50 GB pd-balanced — Lightweight development, single-agent tasks, low-footprint sandboxes"
    - name: "c4-standard-2"
      machine_type: "c4-standard-2"
      boot_disk_gb: 50
      boot_disk_type: "hyperdisk-balanced"
      quota_units: 1
      description: "2 vCPUs, 7.5 GB RAM, 50 GB hyperdisk-balanced — Lightweight development, single-agent tasks, low-footprint sandboxes"
    - name: "n2-standard-4"
      machine_type: "n2-standard-4"
      boot_disk_gb: 100
      boot_disk_type: "pd-balanced"
      quota_units: 2
      description: "4 vCPUs, 16 GB RAM, 100 GB pd-balanced — Standard developer instance, 1-2 concurrent sandboxes, typical repository builds"
    - name: "c4-standard-4"
      machine_type: "c4-standard-4"
      boot_disk_gb: 100
      boot_disk_type: "hyperdisk-balanced"
      quota_units: 2
      description: "4 vCPUs, 15 GB RAM, 100 GB hyperdisk-balanced — Standard developer instance, 1-2 concurrent sandboxes, typical repository builds"
    - name: "n2-standard-8"
      machine_type: "n2-standard-8"
      boot_disk_gb: 200
      boot_disk_type: "pd-balanced"
      quota_units: 4
      description: "8 vCPUs, 32 GB RAM, 200 GB pd-balanced — Heavier parallel compilation (Go/Rust/Bazel), 2-4 nested sandboxes, package caches"
    - name: "c4-standard-8"
      machine_type: "c4-standard-8"
      boot_disk_gb: 200
      boot_disk_type: "hyperdisk-balanced"
      quota_units: 4
      description: "8 vCPUs, 30 GB RAM, 200 GB hyperdisk-balanced — Heavier parallel compilation (Go/Rust/Bazel), 2-4 nested sandboxes, package caches"
    - name: "n2-standard-16"
      machine_type: "n2-standard-16"
      boot_disk_gb: 400
      boot_disk_type: "pd-balanced"
      quota_units: 8
      description: "16 vCPUs, 64 GB RAM, 400 GB pd-balanced — High-throughput multi-agent execution, multiple concurrent microVMs (11,200 IOPS)"
    - name: "c4-standard-16"
      machine_type: "c4-standard-16"
      boot_disk_gb: 400
      boot_disk_type: "hyperdisk-balanced"
      quota_units: 8
      description: "16 vCPUs, 60 GB RAM, 400 GB hyperdisk-balanced — High-throughput multi-agent execution, multiple concurrent microVMs"
    - name: "n2-standard-64"
      machine_type: "n2-standard-64"
      boot_disk_gb: 1000
      boot_disk_type: "pd-balanced"
      quota_units: 32
      description: "64 vCPUs, 256 GB RAM, 1000 GB pd-balanced — Massive parallel builds, large monorepos, and fleet sandbox execution (28,000 IOPS)"
instance_sql:
  cloud_sql_admin: false
  # The pool VM serves TLS (sql-startup.sh). This connection carries the
  # pool superuser password and every hosted instance's generated role
  # password (ALTER ROLE ... PASSWORD), so it must not cross the VPC in clear.
  admin_dsn_template: "postgres://postgres:${enc_sql_admin_pw}@${sql_private_ip}:5432/{database}?sslmode=require"
  sql_instances:
    - name: ${sql_instance_name_json}
      connection_name: ${sql_connection_name_json}
      private_ip: ${sql_private_ip_json}
      max_databases: 100
secrets:
  project: ${secrets_project_id_json}
quotas:
  default_instance_units: 64
config_repos:
  provider: "none"
YAML_EOF

chown root:1000 "${tmp_cfg}"
chmod 0640 "${tmp_cfg}"

# Validate the rendered config with grain-registry itself before swapping.
docker run --rm --network host \
  -v "${tmp_cfg}:/etc/grain-registry/registry.yaml:ro" \
  -v "/etc/grain-registry/hosted-vm-startup.sh:/etc/grain-registry/hosted-vm-startup.sh:ro" \
  "${registry_image}" \
  validate -config /etc/grain-registry/registry.yaml

mv -f "${tmp_cfg}" /etc/grain-registry/registry.yaml

# Remove any obsolete unreferenced placeholder shapes left over from provider=none deploys.
docker exec -i grain-registry-postgres psql -v ON_ERROR_STOP=1 -U grain -d grain_registry >/dev/null <<'SQL' || true
DO $$
BEGIN
  IF to_regclass('registry.shapes') IS NOT NULL THEN
    DELETE FROM registry.shapes
    WHERE name IN ('small', 'medium', 'large')
      AND NOT EXISTS (SELECT 1 FROM registry.entries WHERE shape = registry.shapes.name);
  END IF;
END
$$;
SQL

image_id="$(docker image inspect --format '{{.Id}}' "${registry_image}")"
cfg_sha="$(sha256sum /etc/grain-registry/registry.yaml | awk '{print $1}')"
hosted_sha="$(sha256sum /etc/grain-registry/hosted-vm-startup.sh | awk '{print $1}')"
desired_spec="${registry_image}|${image_id}|${cfg_sha}|${hosted_sha}"
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
    -v /etc/grain-registry/hosted-vm-startup.sh:/etc/grain-registry/hosted-vm-startup.sh:ro \
    "${registry_image}" \
    serve -config /etc/grain-registry/registry.yaml
  umask 077
  printf '%s\n' "${desired_spec}" > /var/lib/grain-registry/running-spec
  chmod 0600 /var/lib/grain-registry/running-spec
else
  docker start grain-registry >/dev/null || true
fi

for _ in $(seq 1 30); do
  if curl -fsS -H "Host: ${registry_host}" http://127.0.0.1:8080/healthz >/dev/null 2>&1 &&
     curl -fsS -H "Host: ${registry_host}" http://127.0.0.1:8081/healthz >/dev/null 2>&1; then
    docker exec -i grain-registry-postgres psql -v ON_ERROR_STOP=1 -U grain -d grain_registry >/dev/null <<'SQL' || true
DELETE FROM registry.shapes
WHERE name IN ('small', 'medium', 'large')
  AND NOT EXISTS (SELECT 1 FROM registry.entries WHERE shape = registry.shapes.name);
SQL
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
