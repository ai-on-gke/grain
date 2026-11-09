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

# §4 item 1 (task 3731): run scripts/grain-instance-init.sh and the
# hosted-vm-startup.sh reconcile body for real against a TLS PostgreSQL set up
# like deploy/test/registry-sandbox/files/sql-startup.sh (self-signed EC cert,
# `hostssl` only) and gcpstub.go's metadata + Secret Manager endpoints.
#
# Both scripts hard-code port 5432 in the DSN they build, so the TLS server
# listens on 127.0.0.1:5432 and the non-TLS control server on 4272.
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$HERE/../../.." && pwd)"
W="${W:-/tmp/hic}"
PGBIN="$(ls -d /usr/lib/postgresql/*/bin | tail -n 1)"
STUB=http://127.0.0.1:4271
PW="walk-pw-$(openssl rand -hex 12)"
SECRET=projects/made-up-proj/secrets/grain-inst-walk1-db-password
mkdir -p "$W/logs"

say() { printf '\n### %s\n' "$*"; }

start_pg() { # dir port ssl
  local dir=$1 port=$2 ssl=$3
  if [ ! -d "$dir" ]; then
    "$PGBIN/initdb" -D "$dir" -U postgres --auth-local=trust --auth-host=scram-sha-256 --encoding=UTF8 --locale=C --no-instructions >/dev/null
    if [ "$ssl" = on ]; then
      # As sql-startup.sh: self-signed P-256 certificate, key 0600.
      openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 3650 \
        -subj "/CN=grain-instance-postgres" -keyout "$dir/server.key" -out "$dir/server.crt" >/dev/null 2>&1
      chmod 0600 "$dir/server.key"
      printf 'local all all trust\nhostssl all all 127.0.0.1/32 scram-sha-256\n' >"$dir/pg_hba.conf"
    else
      printf 'local all all trust\nhost all all 127.0.0.1/32 scram-sha-256\n' >"$dir/pg_hba.conf"
    fi
  fi
  "$PGBIN/pg_ctl" -D "$dir" status >/dev/null 2>&1 ||
    "$PGBIN/pg_ctl" -D "$dir" -l "$W/logs/pg-$port.log" -w start \
      -o "-p $port -k $W -c listen_addresses=127.0.0.1 -c ssl=$ssl -c password_encryption=scram-sha-256" >/dev/null
  psql -h "$W" -p "$port" -U postgres -qc "DO \$\$BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname='gi_walk1') THEN CREATE ROLE gi_walk1 LOGIN; END IF; END\$\$" >/dev/null
  psql -h "$W" -p "$port" -U postgres -qc "ALTER ROLE gi_walk1 PASSWORD '$PW'" >/dev/null
  psql -h "$W" -p "$port" -U postgres -tAc "SELECT 1 FROM pg_database WHERE datname='grain_walk1'" | grep -q 1 ||
    psql -h "$W" -p "$port" -U postgres -qc "CREATE DATABASE grain_walk1 OWNER gi_walk1" >/dev/null
}

start_pg "$W/pg-tls" 5432 on
start_pg "$W/pg-plain" 4272 off
[ -x "$W/gcpstub" ] || (cd "$REPO" && go build -o "$W/gcpstub" "$HERE/gcpstub.go")
if ! curl -fsS "$STUB/_stub/log" >/dev/null 2>&1; then
  nohup "$W/gcpstub" -addr 127.0.0.1:4271 -state "$W/stub" >>"$W/logs/gcpstub.log" 2>&1 &
  sleep 0.5
fi
curl -fsS -X POST "$STUB/_stub/reset" >/dev/null
curl -fsS -X POST --data-binary "$PW" "$STUB/_stub/secret?name=$SECRET" >/dev/null

init() { # run grain-instance-init.sh as the systemd unit would (minus root-only steps)
  local tag=$1
  rm -rf "$W/run-$tag"
  mkdir -p "$W/run-$tag"
  GRAIN_METADATA_URL="$STUB/computeMetadata/v1" GRAIN_SM_API_URL="$STUB/sm" \
    GRAIN_DATA_DIR="$W/run-$tag/data" GRAIN_ETC_DIR="$W/run-$tag/etc" \
    GRAIN_SECRETS_RUN_DIR="$W/run-$tag/secrets" GRAIN_CLOUDSQL_DIR="$W/run-$tag/cloudsql" \
    GRAIN_USER="$(id -un)" GRAIN_SKIP_TMPFS_MOUNT=1 GRAIN_SKIP_SYSTEMCTL=1 \
    bash "$REPO/scripts/grain-instance-init.sh" >"$W/run-$tag/stdout" 2>"$W/run-$tag/stderr"
  local rc=$?
  echo "grain-instance-init.sh exit=$rc"
  sed 's/^/  stderr: /' "$W/run-$tag/stderr"
  return $rc
}

dsn_of() { sed -n 's/^GRAIN_DB_DSN=//p' "$W/run-$1/etc/daemon.env"; }
redact() { sed "s/$PW/<PASSWORD>/g"; }

config_new='{"instance":"walk1","deployment_owner":"alice@example.com","db_user":"gi_walk1","db_name":"grain_walk1","sql_private_ip":"127.0.0.1","db_password_secret":"'$SECRET'"}'

say "1a. new-style grain-config (db_password_secret) boots"
curl -fsS -X POST --data-binary "$config_new" "$STUB/_stub/metadata?attr=grain-config" >/dev/null
init new
echo "grain-config carries the password? $(printf '%s' "$config_new" | grep -c "$PW")"
echo "daemon.env mode: $(stat -c %a "$W/run-new/etc/daemon.env")"
echo "GRAIN_DB_DSN=$(dsn_of new | redact)"
echo "password in stdout/stderr: $(cat "$W/run-new/stdout" "$W/run-new/stderr" | grep -c "$PW")"
echo "password in stub request log: $(grep -c "$PW" "$W/stub/requests.log")"
echo "requests this run:"; curl -fsS "$STUB/_stub/log" | sed 's/^[^ ]* /  /'

say "1b. the DSN is TLS-only: psql and pgx against the TLS pool, then a non-TLS server"
DSN="$(dsn_of new)"
psql "$DSN" -tAc "SELECT current_user, ssl, version FROM pg_stat_ssl WHERE pid = pg_backend_pid()" 2>&1 | redact | sed 's/^/  psql tls: /'
(cd "$REPO" && go run "$HERE/pgcheck.go" "$DSN") 2>&1 | redact | sed 's/^/  pgx tls: /'
PLAIN="${DSN/:5432\//:4272/}"
psql "$PLAIN" -tAc 'SELECT 1' 2>&1 | redact | sed 's/^/  psql plain: /'
(cd "$REPO" && go run "$HERE/pgcheck.go" "$PLAIN") 2>&1 | redact | sed 's/^/  pgx plain: /'
NOSSL="${DSN/sslmode=require/sslmode=disable}"
psql "$NOSSL" -tAc 'SELECT 1' 2>&1 | redact | sed 's/^/  psql sslmode=disable vs hostssl-only pool: /'

for code in 403 404 503; do
  # 403/404 once; 503 on every attempt (the retries must give up and fail closed).
  count=1; [ "$code" = 503 ] && count=-1
  say "1c. Secret Manager answers $code on the password read (count=$count)"
  curl -fsS -X POST "$STUB/_stub/sm-fail?code=$code&count=$count" >/dev/null
  init "sm$code"
  echo "daemon.env written: $([ -f "$W/run-sm$code/etc/daemon.env" ] && echo yes || echo no)"
  echo "password in output: $(cat "$W/run-sm$code/stdout" "$W/run-sm$code/stderr" | grep -c "$PW")"
  curl -fsS -X POST "$STUB/_stub/sm-fail?code=0&count=0" >/dev/null
done

say "1c''. Secret Manager answers 503 twice, then recovers"
curl -fsS -X POST "$STUB/_stub/sm-fail?code=503&count=2" >/dev/null
init sm503x2
echo "daemon.env written: $([ -f "$W/run-sm503x2/etc/daemon.env" ] && echo yes || echo no)"
curl -fsS -X POST "$STUB/_stub/sm-fail?code=0&count=0" >/dev/null

say "1c'. the secret exists but has no version (404 from the stub's own store)"
curl -fsS -X POST "$STUB/_stub/reset" >/dev/null
init nover
curl -fsS -X POST --data-binary "$PW" "$STUB/_stub/secret?name=$SECRET" >/dev/null

say "1d. pre-existing instance: metadata still carries GRAIN_DB_PASSWORD and an sslmode=disable db_dsn"
config_old='{"instance":"walk1","deployment_owner":"alice@example.com","GRAIN_DB_PASSWORD":"'$PW'","db_dsn":"postgres://gi_walk1:'$PW'@127.0.0.1:4272/grain_walk1?sslmode=disable"}'
curl -fsS -X POST --data-binary "$config_old" "$STUB/_stub/metadata?attr=grain-config" >/dev/null
init old
echo "GRAIN_DB_DSN=$(dsn_of old | redact)"
psql "$(dsn_of old)" -tAc 'SELECT current_user' 2>&1 | sed 's/^/  old-style DSN against a plain (host) pool: /'
OLD_TLS="$(dsn_of old | sed 's/:4272\//:5432\//')"
psql "$OLD_TLS" -tAc 'SELECT current_user' 2>&1 | redact | sed 's/^/  old-style DSN against a hostssl-only pool: /'
echo "password in stdout/stderr: $(cat "$W/run-old/stdout" "$W/run-old/stderr" | grep -c "$PW")"
curl -fsS -X POST --data-binary "$config_new" "$STUB/_stub/metadata?attr=grain-config" >/dev/null
echo "after replacement grain-config is the new one: password in metadata = $(curl -fsS -H 'Metadata-Flavor: Google' "$STUB/computeMetadata/v1/instance/attributes/grain-config" | grep -c "$PW")"
