#!/bin/bash
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

# §2 item 5: scripts/grain-instance-init.sh (-deployment-owner) against
# metadatastub.py (127.0.0.1:4305) and smstub.go (127.0.0.1:4303) in a
# scratch root, a real daemon booted from the daemon.env it writes, and
# scripts/migrate-local-to-gke.sh's grain-postgres.service guard.
HERE=$(cd "$(dirname "$0")" && pwd)
. "$HERE/lib.sh"
REPO=$(cd "$HERE/../../.." && pwd)
R=$W/i5-root; CFG=$W/i5-grain-config.json; DB=walk_i5
rm -rf "$R" "$W/i5-"*; mkdir -p "$R"; recreate_db $DB
python3 "$HERE/metadatastub.py" --addr 127.0.0.1:4305 --config "$CFG" >"$W/i5-metadata.log" 2>&1 &
MD=$!; sleep 1
SMP=projects/walk-proj/secrets/grain-inst-walk-
# the registry creates the instance's secrets before the VM boots
curl -s -o /dev/null -H 'Authorization: Bearer fake-sm-token' -X POST "http://127.0.0.1:4303/v1/projects/walk-proj/secrets?secretId=grain-inst-walk-local-api-token"
init() {
  env GRAIN_METADATA_URL=http://127.0.0.1:4305/computeMetadata/v1 GRAIN_SM_API_URL=http://127.0.0.1:4303 \
    GRAIN_DATA_DIR="$R/var/lib/grain/data" GRAIN_ETC_DIR="$R/etc/grain" GRAIN_SECRETS_RUN_DIR="$R/run/grain-secrets" \
    GRAIN_CLOUDSQL_DIR="$R/cloudsql" GRAIN_USER="$(id -un)" GRAIN_SKIP_TMPFS_MOUNT=1 GRAIN_SKIP_SYSTEMCTL=1 \
    bash "$REPO/scripts/grain-instance-init.sh"
}
modes() { for f in "$@"; do [ -e "$f" ] && printf '  %s %s\n' "$(stat -c '%a %U' "$f")" "${f#"$R"}"; done; }

say "5a. no grain-config attribute: a non-registry host is left alone"
init; echo "exit=$? files written: $(find "$R" -type f | wc -l)"

say "5b. grain-config without deployment_owner, and with a non-email owner"
echo '{"instance":"calm-otter","sm_prefix":"'$SMP'"}' >"$CFG"; init; echo "exit=$?"
echo '{"instance":"calm-otter","deployment_owner":"alice","sm_prefix":"'$SMP'"}' >"$CFG"; init; echo "exit=$?"

say "5c. full grain-config"
cat >"$CFG" <<EOF
{"instance":"calm-otter","instance_domain":"grain.example.test","deployment_owner":"alice@example.com",
 "owner_group":"platform@example.com","issuer":"https://registry.example.test","sm_prefix":"$SMP",
 "sql_instance":"walk-proj:us-central1:grain","object_store":"gs://grain-instances-walk/instances/calm-otter/"}
EOF
init; echo "exit=$?"
modes "$R/etc/grain/daemon.env" "$R/etc/grain/cloud-sql-proxy.env" "$R/run/grain-secrets" "$R/run/grain-secrets/local-api-token" "$R/var/lib/grain/data" "$R/var/lib/grain/data/local-api-token"
echo "daemon.env:"; sed 's/^/  /' "$R/etc/grain/daemon.env"
T1=$(cat "$R/var/lib/grain/data/local-api-token")
echo "local-api-token in Secret Manager matches the file: $(curl -s http://127.0.0.1:4303/_dump | grep -A1 'grain-inst-walk-local-api-token' | tail -1 | awk '{print $3}' | base64 -d | grep -c "$T1")"

say "5d. second boot: the token is read back, not regenerated"
init 2>&1 | grep -v "^$"; echo "token unchanged: $([ "$(cat "$R/var/lib/grain/data/local-api-token")" = "$T1" ] && echo yes || echo no)"

say "5e. a real daemon booted from daemon.env (DSN pointed at the local cluster, registry verifier left out: no JWKS here)"
# daemon.env is a systemd EnvironmentFile (values with spaces, unquoted), not shell
while IFS= read -r line; do case $line in GRAIN_*=*) export "${line%%=*}=${line#*=}";; esac; done <"$R/etc/grain/daemon.env"
unset GRAIN_REQUIRE_VERIFIER GRAIN_REGISTRY_ISSUER GRAIN_REGISTRY_AUDIENCE GRAIN_REGISTRY_KEYS_URL GRAIN_OBJECT_STORE GRAIN_EXTRA_DOCKER_RUN_ARGS
export GRAIN_DB_DSN=$(pgurl $DB) GRAIN_SECRET_MANAGER_ENDPOINT=http://127.0.0.1:4303 GRAIN_SECRET_MANAGER_TOKEN=fake-sm-token
D=$R/var/lib/grain/data
P=$(daemon i5 4306 "$D" $GRAIN_EXTRA_DAEMON_ARGS)
if wait_up 4306; then
  grep -h "self-upgrade\|deployment owner\|Secret Manager" "$W/i5.log" | head -3 | cut -c1-220
  api "$D" 4306 /api/access/principals | jq -c '.entries | map({entry, role, addedBy})' | cut -c1-500
  grep -h "secrets backend\|Secret Manager\|secretmanager" "$W/i5.log" | head -2 | cut -c1-200
  echo "Secret Manager secrets after the daemon's first start: $(curl -s http://127.0.0.1:4303/_dump | grep -c '^projects/walk-proj/secrets/grain-inst-walk-')"
  api "$D" 4306 /api/config | jq -c '{selfUpgradeDisabled, managementUrl, deploymentOwner: (.deploymentOwner // null)}'
  echo "try to remove the owner's floor:"; api "$D" 4306 "/api/access/principals/user:alice@example.com" -X DELETE -w ' [%{http_code}]\n' | cut -c1-240
else tail -15 "$W/i5.log"; fi
kill "$P" 2>/dev/null; wait_gone "$P"
unset $(grep -o '^GRAIN_[A-Z_]*' "$R/etc/grain/daemon.env") GRAIN_SECRET_MANAGER_ENDPOINT GRAIN_SECRET_MANAGER_TOKEN

say "5f. migrate-local-to-gke.sh: hosts setup.sh gave grain-postgres.service"
mkdir -p "$W/i5-bin"; printf '#!/bin/sh\necho "kubectl stub: $*" >&2\nexit 0\n' >"$W/i5-bin/kubectl"; chmod +x "$W/i5-bin/kubectl"
mig() { # case-name
  local M=$W/i5-mig-$1
  PATH=$W/i5-bin:$PATH GRAIN_ROOT_DIR=$M/var/lib/grain GRAIN_ETC_DIR=$M/etc/grain GRAIN_SYSTEMD_DIR=$M/systemd \
    GRAIN_MIGRATE_BACKUP_DIR=$M/backups GRAIN_DATA_DIR=$M/var/lib/grain/data GRAIN_MIGRATE_SKIP_ROOT_CHECK=1 \
    bash "$REPO/scripts/migrate-local-to-gke.sh" --dry-run --kubeconfig /dev/null --data-dir "$M/var/lib/grain/data" 2>&1 | grep -E "refus|store:|Stage 1|this host's store|ERROR|error|is installed but" | head -4 | cut -c1-230
  echo "  exit=${PIPESTATUS[0]}"
}
mkcase() { # name unit(yes/no) port dsn password(yes/no)
  local M=$W/i5-mig-$1; mkdir -p "$M/systemd" "$M/var/lib/grain/data/secrets" "$M/etc/grain"
  [ "$2" = yes ] && printf '[Service]\nEnvironment=GRAIN_POSTGRES_PORT=%s\nExecStart=/usr/bin/docker run grain-postgres\n' "$3" >"$M/systemd/grain-postgres.service"
  printf '[Service]\nExecStart=/usr/local/bin/grain daemon -data-dir %s\n' "$M/var/lib/grain/data" >"$M/systemd/grain-daemon.service"
  [ -n "$4" ] && printf '{"driver":"postgres","dsn":"%s"}\n' "$4" >"$M/var/lib/grain/data/db-config.json"
  [ "$5" = yes ] && echo pw >"$M/var/lib/grain/data/secrets/postgres-password"
  [ -z "$4" ] && [ "$5" != yes ] && mkdir -p "$M/var/lib/grain/data/store" && cp "$W/i1-data/store/grain.db" "$M/var/lib/grain/data/store/" 2>/dev/null
  echo key >"$M/var/lib/grain/data/secrets/secrets.key"
}
mkcase local yes 5432 "postgres://grain@127.0.0.1:5432/grain?sslmode=disable" yes;            echo "local server, db-config names 127.0.0.1:5432:"; mig local
mkcase pwonly no 5432 "" yes;                                                                  echo "no unit, password file only, nothing names a store:"; mig pwonly
mkcase moved yes 5432 "postgres://grain@db.internal.example:5432/grain?sslmode=require" yes;   echo "unit left behind, store moved to another server:"; mig moved
mkcase otherport yes 5433 "postgres://grain@localhost:5432/grain?sslmode=disable" no;          echo "unit on 5433, db-config names localhost:5432 (some other local server):"; mig otherport
mkcase sqlite no 5432 "" no;                                                                   echo "SQLite host:"; mig sqlite

say "5g. the grain-config the registry reconciler actually writes (pkg/registry/reconcile/reconciler.go startupEnv)"
cat >"$CFG" <<CFGEOF
{"GRAIN_CONFIG_REPO":"","GRAIN_DB_NAME":"grain_calm_otter","GRAIN_ENTRY_NAME":"calm-otter","GRAIN_GCS_BUCKET":"grain-instances-walk",
 "GRAIN_GCS_PREFIX":"instances/calm-otter/","GRAIN_JWKS_URL":"https://registry.example.test/.well-known/jwks.json",
 "GRAIN_REGISTRY_URL":"https://registry.example.test","GRAIN_SECRET_PREFIX":"grain-inst-calm-otter-","GRAIN_SQL_CONNECTION_NAME":"walk-proj:us-central1:grain",
 "GRAIN_SQL_INSTANCE":"grain","GRAIN_SQL_PRIVATE_IP":"10.0.0.5","GRAIN_VERSION":"v3.2.0"}
CFGEOF
rm -rf "$R"; mkdir -p "$R"; init; echo "exit=$? daemon.env written: $([ -f "$R/etc/grain/daemon.env" ] && echo yes || echo no)"
kill $MD
