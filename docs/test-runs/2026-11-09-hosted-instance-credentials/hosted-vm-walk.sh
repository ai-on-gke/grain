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

# §4 item 1, hosted-vm-startup.sh path (task 3731). Runs the reconcile body
# deploy/test/registry-sandbox/files/hosted-vm-startup.sh writes, as root,
# with only its Google endpoints rewritten to gcpstub.go and `docker` replaced
# by a recorder; instance-init-walk.sh must have run first (TLS pool, stub).
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$HERE/../../.." && pwd)"
W="${W:-/tmp/hic}"
STUB=http://127.0.0.1:4271
SECRET=projects/made-up-proj/secrets/grain-inst-walk1-db-password
PW="$(psql -h "$W" -p 5432 -U postgres -tAc "SELECT 1" >/dev/null && curl -fsS -H 'Metadata-Flavor: Google' "$STUB/_stub/log" >/dev/null && echo ok)"
[ "$PW" = ok ] || { echo "run instance-init-walk.sh first" >&2; exit 1; }

mkdir -p "$W/hvm/bin"
cat >"$W/hvm/bin/docker" <<'EOF'
#!/usr/bin/env bash
echo "docker $*" >>"$HVM_LOG"
case "$1" in
  inspect) exit 1 ;;
  image) echo sha256:walk ;;
esac
exit 0
EOF
chmod +x "$W/hvm/bin/docker"

# Pull the reconcile body out of the startup script and point it at the stub.
awk "/<<'RECONCILE_EOF'/{f=1;next} /^RECONCILE_EOF/{f=0} f" \
  "$REPO/deploy/test/registry-sandbox/files/hosted-vm-startup.sh" |
  sed -e "s#http://metadata.google.internal/computeMetadata/v1#$STUB/computeMetadata/v1#" \
    -e "s#https://secretmanager.googleapis.com#$STUB/sm#" \
    -e 's#/usr/local/bin/grain#'"$W"'/hvm/grain-wrapper#' \
    -e "s#for _ in \$(seq 1 45)#for _ in 1#" >"$W/hvm/reconcile.sh"
{ echo '#!/usr/bin/env bash'; echo 'set -euo pipefail'; echo 'DEFAULT_DAEMON_IMAGE=walk/grain:test'; cat "$W/hvm/reconcile.sh"; } >"$W/hvm/reconcile"
chmod +x "$W/hvm/reconcile"

cfg='{"GRAIN_ENTRY_NAME":"walk1","GRAIN_REGISTRY_URL":"https://registry.walk.test","GRAIN_JWKS_URL":"https://registry.walk.test/jwks","GRAIN_INSTANCE_DOMAIN":"grain-user.test","GRAIN_OWNER_EMAIL":"alice@example.com","GRAIN_SQL_PRIVATE_IP":"127.0.0.1","GRAIN_DB_NAME":"grain_walk1","GRAIN_DB_USER":"gi_walk1","GRAIN_DB_PASSWORD_SECRET":"'$SECRET'"}'
run() { # tag
  sudo rm -f /etc/grain-daemon/daemon.env
  : >"$W/hvm/docker-$1.log"
  sudo env PATH="$W/hvm/bin:/usr/sbin:/sbin:$PATH" HVM_LOG="$W/hvm/docker-$1.log" bash "$W/hvm/reconcile" >"$W/hvm/out-$1" 2>&1
  echo "reconcile exit=$?"
  sed 's/^/  out: /' "$W/hvm/out-$1" | grep -v '^  out: *$' | tail -n 4
}
secret_pw() { curl -fsS -H 'Authorization: Bearer stub-vm-token' "$STUB/sm/v1/$SECRET/versions/latest:access" | jq -r .payload.data | base64 -d; }

echo "### hosted-vm: new-style grain-config"
curl -fsS -X POST --data-binary "$cfg" "$STUB/_stub/metadata?attr=grain-config" >/dev/null
run new
P="$(secret_pw)"
DSN="$(sudo sed -n 's/^GRAIN_DB_DSN=//p' /etc/grain-daemon/daemon.env)"
echo "daemon.env: $(sudo stat -c %a /etc/grain-daemon/daemon.env) $(echo "$DSN" | sed "s/$P/<PASSWORD>/")"
psql "$DSN" -tAc "SELECT current_user, ssl FROM pg_stat_ssl WHERE pid = pg_backend_pid()" 2>&1 | sed 's/^/  psql: /'
echo "password in script output: $(grep -c "$P" "$W/hvm/out-new")"
echo "password on any docker command line: $(grep -c "$P" "$W/hvm/docker-new.log")"

for code in 403 503; do
  count=1; [ "$code" = 503 ] && count=-1
  echo "### hosted-vm: Secret Manager $code"
  curl -fsS -X POST "$STUB/_stub/sm-fail?code=$code&count=$count" >/dev/null
  run "sm$code"
  echo "daemon.env written: $(sudo test -f /etc/grain-daemon/daemon.env && echo yes || echo no); docker run issued: $(grep -c 'docker run' "$W/hvm/docker-sm$code.log")"
  curl -fsS -X POST "$STUB/_stub/sm-fail?code=0&count=0" >/dev/null
done

echo "### hosted-vm: Secret Manager 503 twice, then recovers"
curl -fsS -X POST "$STUB/_stub/sm-fail?code=503&count=2" >/dev/null
run sm503x2
echo "daemon.env written: $(sudo test -f /etc/grain-daemon/daemon.env && echo yes || echo no); docker run issued: $(grep -c 'docker run' "$W/hvm/docker-sm503x2.log")"
curl -fsS -X POST "$STUB/_stub/sm-fail?code=0&count=0" >/dev/null

echo "### hosted-vm: pre-existing metadata (GRAIN_DB_PASSWORD, no secret name) under the new script"
old='{"GRAIN_ENTRY_NAME":"walk1","GRAIN_REGISTRY_URL":"https://registry.walk.test","GRAIN_JWKS_URL":"https://registry.walk.test/jwks","GRAIN_INSTANCE_DOMAIN":"grain-user.test","GRAIN_OWNER_EMAIL":"alice@example.com","GRAIN_SQL_PRIVATE_IP":"127.0.0.1","GRAIN_DB_NAME":"grain_walk1","GRAIN_DB_USER":"gi_walk1","GRAIN_DB_PASSWORD":"'$P'"}'
curl -fsS -X POST --data-binary "$old" "$STUB/_stub/metadata?attr=grain-config" >/dev/null
run old
curl -fsS -X POST --data-binary "$cfg" "$STUB/_stub/metadata?attr=grain-config" >/dev/null
