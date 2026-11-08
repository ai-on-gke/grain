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

# Startup and reconciliation script for the shared PostgreSQL 16 pool VM
# (`instance_sql.sql_instances`) serving hosted Grain GCE VM instances on the
# private sandbox VPC.

set -euo pipefail

export DEBIAN_FRONTEND=noninteractive

install_packages() {
  if command -v docker >/dev/null 2>&1 && command -v jq >/dev/null 2>&1 && command -v curl >/dev/null 2>&1; then
    return 0
  fi
  apt-get -o Acquire::Retries=5 -o Acquire::http::Timeout=30 update
  apt-get -o Acquire::Retries=5 -o Acquire::http::Timeout=30 install -y --no-install-recommends \
    ca-certificates \
    curl \
    docker.io \
    jq
  systemctl enable --now docker
}

write_reconcile_script() {
  cat > /usr/local/sbin/grain-instance-sql-reconcile <<'RECONCILE_EOF'
#!/usr/bin/env bash
set -euo pipefail

META_URL="http://metadata.google.internal/computeMetadata/v1"
META_HDR="Metadata-Flavor: Google"

cfg_json="$(curl -fsSL -H "${META_HDR}" "${META_URL}/instance/attributes/grain-sql-config")"
admin_pw="$(jq -r '.admin_password // empty' <<<"${cfg_json}")"

if [[ -z "${admin_pw}" ]]; then
  echo "grain-instance-sql-reconcile: missing admin_password in grain-sql-config" >&2
  exit 1
fi

install -d -m 0700 /var/lib/grain-instance-sql
install -d -m 0700 /var/lib/grain-instance-sql/pgdata
install -d -m 0700 /etc/grain-instance-sql

umask 077
cat > /etc/grain-instance-sql/postgres.env <<ENV_EOF
POSTGRES_USER=postgres
POSTGRES_PASSWORD=${admin_pw}
POSTGRES_DB=postgres
POSTGRES_INITDB_ARGS=--auth-host=scram-sha-256 --auth-local=trust
ENV_EOF
chmod 0600 /etc/grain-instance-sql/postgres.env

# Pinned by digest so a retagged upstream image cannot change what runs here.
if ! docker inspect grain-instance-postgres >/dev/null 2>&1; then
  docker run -d \
    --name grain-instance-postgres \
    --restart unless-stopped \
    -p 0.0.0.0:5432:5432 \
    --env-file /etc/grain-instance-sql/postgres.env \
    -v /var/lib/grain-instance-sql/pgdata:/var/lib/postgresql/data \
    postgres:16-alpine@sha256:aa6cb33cf4c19c765cefcf48d96176258800870a9a1e930ad126200f9d825139 \
    postgres -c max_connections=300 -c password_encryption=scram-sha-256
else
  docker start grain-instance-postgres >/dev/null || true
fi

for _ in $(seq 1 30); do
  if docker exec grain-instance-postgres pg_isready -U postgres -d postgres >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
docker exec grain-instance-postgres pg_isready -U postgres -d postgres >/dev/null

# Converge the superuser password idempotently across metadata rotations.
docker exec -i grain-instance-postgres psql -v ON_ERROR_STOP=1 -U postgres -d postgres \
  -v pw="${admin_pw}" >/dev/null <<'SQL'
ALTER ROLE postgres WITH PASSWORD :'pw';
SQL

echo "grain-instance-postgres is healthy on 0.0.0.0:5432"
RECONCILE_EOF
  chmod 0755 /usr/local/sbin/grain-instance-sql-reconcile
}

install_packages
write_reconcile_script
/usr/local/sbin/grain-instance-sql-reconcile
