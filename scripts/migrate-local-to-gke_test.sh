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

# Unit tests for scripts/migrate-local-to-gke.sh against a local install
# laid out in a scratch directory and stubbed kubectl, systemctl, curl,
# docker, grain, install and chown: kubectl's PVC is a directory, and
# systemctl's units are files saying which are active, enabled and
# masked. tests/deploy/migrate_local_to_gke_test.go runs this in CI; by
# hand:
#
#   bash scripts/migrate-local-to-gke_test.sh
#
# Exits non-zero, naming each failing case, if any case fails.
set -euo pipefail

script="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/migrate-local-to-gke.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

failures=0
cases=0
out=""
status=0

check() { # check <case> <want-status> <want-substring>...
  local name="$1" want="$2" s
  shift 2
  cases=$((cases + 1))
  if [ "$status" != "$want" ]; then
    echo "FAIL $name: exit $status, want $want"
    printf '%s\n' "$out" | sed 's/^/    /'
    failures=$((failures + 1))
    return
  fi
  for s in "$@"; do
    if [[ "$out" != *"$s"* ]]; then
      echo "FAIL $name: output lacks \"$s\""
      printf '%s\n' "$out" | sed 's/^/    /'
      failures=$((failures + 1))
      return
    fi
  done
  echo "ok   $name"
}

lacks() { # lacks <case> <haystack> <substring>
  cases=$((cases + 1))
  if [[ "$2" == *"$3"* ]]; then
    echo "FAIL $1: has \"$3\""
    printf '%s\n' "$2" | sed 's/^/    /'
    failures=$((failures + 1))
    return
  fi
  echo "ok   $1"
}

holds() { # holds <case> <haystack> <substring>
  cases=$((cases + 1))
  if [[ "$2" != *"$3"* ]]; then
    echo "FAIL $1: lacks \"$3\""
    printf '%s\n' "$2" | sed 's/^/    /'
    failures=$((failures + 1))
    return
  fi
  echo "ok   $1"
}

truth() { # truth <case> <command...>
  local name="$1"
  shift
  cases=$((cases + 1))
  if "$@"; then
    echo "ok   $name"
  else
    echo "FAIL $name: $* was false"
    failures=$((failures + 1))
  fi
}

# --- Stubs -----------------------------------------------------------------

stubs="$work/bin"
mkdir -p "$stubs"

cat >"$stubs/kubectl" <<'EOF'
#!/usr/bin/env bash
# kubectl over $STUB/cluster: ns, labeled, replicas, pvc/, applied-N.yaml.
set -euo pipefail
echo "kubectl $*" >>"$STUB/calls.log"
c="$STUB/cluster"
mkdir -p "$c/pvc"
args=()
for a in "$@"; do
  case "$a" in --kubeconfig=*) ;; *) args+=("$a") ;; esac
done
set -- "${args[@]}"
if [ "${1:-}" = "-n" ]; then ns="$2"; shift 2; fi
case "$1" in
  version) exit 0 ;;
  create)
    case "$2" in
      namespace) touch "$c/ns" ;;
      secret) printf 'apiVersion: v1\nkind: Secret\nmetadata:\n  name: %s\n' "$4" ;;
    esac ;;
  label) touch "$c/labeled" ;;
  get)
    case "$2" in
      namespace)
        [ -f "$c/ns" ] || exit 1
        if [[ "$*" == *jsonpath* ]]; then [ -f "$c/labeled" ] && printf privileged; fi ;;
      daemonset) [ -z "${STUB_NO_DEVICE_PLUGIN:-}" ] || exit 1 ;;
      deployment) [ -f "$c/replicas" ] || exit 1; cat "$c/replicas" ;;
    esac ;;
  apply)
    n=$(ls "$c" | grep -c '^applied-' || true)
    if [ "$3" = "-" ]; then cat >"$c/applied-$n.yaml"; else cp "$3" "$c/applied-$n.yaml"; fi
    if grep -q '^kind: Deployment' "$c/applied-$n.yaml"; then
      sed -n 's/^  replicas: //p' "$c/applied-$n.yaml" | head -n1 >"$c/replicas"
    fi ;;
  scale) for a in "$@"; do case "$a" in --replicas=*) echo "${a#*=}" >"$c/replicas" ;; esac; done ;;
  exec)
    while [ "$1" != "--" ]; do shift; done
    shift
    cmd=()
    for a in "$@"; do cmd+=("${a//\/data/$c/pvc}"); done
    "${cmd[@]}" ;;
  wait | rollout | delete) exit 0 ;;
esac
EOF

cat >"$stubs/systemctl" <<'EOF'
#!/usr/bin/env bash
# systemctl over $STUB/units/<unit>.{active,enabled,masked}.
set -euo pipefail
echo "systemctl $*" >>"$STUB/calls.log"
u="$STUB/units"
mkdir -p "$u"
now=false
args=()
for a in "$@"; do
  case "$a" in --now) now=true ;; --quiet) ;; *) args+=("$a") ;; esac
done
set -- "${args[@]}"
case "$1" in
  is-active) [ -f "$u/$2.active" ] ;;
  stop) rm -f "$u/$2.active" ;;
  start | restart)
    [ ! -f "$u/$2.masked" ] || { echo "Unit $2 is masked." >&2; exit 1; }
    touch "$u/$2.active" ;;
  enable)
    [ ! -f "$u/$2.masked" ] || { echo "Unit $2 is masked." >&2; exit 1; }
    touch "$u/$2.enabled"
    if $now; then touch "$u/$2.active"; fi ;;
  disable) rm -f "$u/$2.enabled"; if $now; then rm -f "$u/$2.active"; fi ;;
  mask)
    # As systemd does: masking links the unit to /dev/null in
    # /etc/systemd/system, and is refused where the unit's own file is.
    f="${GRAIN_SYSTEMD_DIR:?}/$2"
    if [ -e "$f" ] && [ ! -L "$f" ]; then
      echo "Failed to mask unit: File $f already exists." >&2
      exit 1
    fi
    ln -sfn /dev/null "$f"
    touch "$u/$2.masked" ;;
  unmask)
    f="${GRAIN_SYSTEMD_DIR:?}/$2"
    if [ -L "$f" ]; then rm -f "$f"; fi
    rm -f "$u/$2.masked" ;;
  daemon-reload) ;;
esac
EOF

cat >"$stubs/curl" <<'EOF'
#!/usr/bin/env bash
# curl answering grain's API the way a migrated controller would.
echo "curl $*" >>"$STUB/calls.log"
url="${!#}"
case "$url" in
  */api/auth/access-code) echo '{}' ;;
  */api/settings) echo '{}' ;;
  */api/capability-servers)
    if [ -n "${STUB_CAPS:-}" ]; then
      echo "$STUB_CAPS"
    else
      echo '[{"id":"gcp","status":{"id":"gcp","state":"attached","run":"host"}},{"id":"github","status":{"state":"pending"}}]'
    fi ;;
  */api/pipeline-pause) echo '{}' ;;
  *) exit 22 ;;
esac
EOF

cat >"$stubs/docker" <<'EOF'
#!/usr/bin/env bash
echo "docker $*" >>"$STUB/calls.log"
case "$1 $2" in
  "container inspect") [ -n "${STUB_DOCKER_CONTAINER:-}" ] ;;
esac
EOF

for tool in grain gcloud chown; do
  cat >"$stubs/$tool" <<STUB
#!/usr/bin/env bash
echo "$tool \$*" >>"\$STUB/calls.log"
STUB
done

cat >"$stubs/install" <<'EOF'
#!/usr/bin/env bash
# install -m MODE -o USER SRC DST, without the chown.
echo "install $*" >>"$STUB/calls.log"
args=()
while [ $# -gt 0 ]; do
  case "$1" in -m | -o) shift ;; *) args+=("$1") ;; esac
  shift
done
cp "${args[0]}" "${args[1]}"
EOF
chmod +x "$stubs"/*

# --- A local install --------------------------------------------------------

# fresh lays out a local install under $work/host and a cluster with the
# grain namespace but no Deployment yet.
fresh() {
  rm -rf "$work/host" "$work/stub"
  local h="$work/host"
  mkdir -p "$h/var/lib/grain/data/store" "$h/var/lib/grain/data/secrets" \
    "$h/var/lib/grain/data/state-repo/settings" "$h/etc/grain" "$h/etc/systemd/system" \
    "$h/backups" "$work/stub/units" "$work/stub/cluster"
  echo "sqlite" >"$h/var/lib/grain/data/store/grain.db"
  echo "AGE-SECRET-KEY-1TEST" >"$h/var/lib/grain/data/secrets/secrets.key"
  echo "inst-1234" >"$h/var/lib/grain/data/installation-id"
  echo "local-token" >"$h/var/lib/grain/data/local-api-token"
  echo "sandbox_backend: granule" >"$h/var/lib/grain/data/state-repo/settings/config.yml"
  echo "admin" >"$h/etc/grain/admin.kubeconfig"
  cat >"$h/etc/grain/hostd.yaml" <<'YAML'
clis:
  agy:
    path: /usr/bin/agy
capabilities:
  grain-gcp:
    command: /usr/local/bin/grain
  grain-host-script:
    command: /usr/local/bin/grain
YAML
  cat >"$h/etc/systemd/system/grain-daemon.service" <<'UNIT'
[Service]
User=grain
ExecStart=/usr/local/bin/grain daemon -data-dir /var/lib/grain/data \
  -granule-sandboxes -sandbox-image docker.io/grainai/sandbox:v2.1.0 \
  -default-target-repo ai-on-gke/grain
UNIT
  cat >"$h/etc/systemd/system/grain-hostd.service" <<'UNIT'
[Service]
User=alice
ExecStart=/var/lib/grain-hostd/versions/current/grain hostd -socket /run/grain-hostd/hostd.sock -allowlist /etc/grain/hostd.yaml -state-dir /var/lib/grain-hostd
UNIT
  touch "$work/stub/units/grain-daemon.service.active" "$work/stub/units/grain-daemon.service.enabled" \
    "$work/stub/units/grain-hostd.service.active"
  touch "$work/stub/cluster/ns" "$work/stub/cluster/labeled"
  : >"$work/stub/calls.log"
}

# migrate <args...>: the script's output in $out, its exit in $status.
migrate() {
  status=0
  out="$(
    env PATH="$stubs:$PATH" STUB="$work/stub" \
      GRAIN_ROOT_DIR="$work/host/var/lib/grain" GRAIN_ETC_DIR="$work/host/etc/grain" \
      GRAIN_SYSTEMD_DIR="$work/host/etc/systemd/system" GRAIN_MIGRATE_BACKUP_DIR="$work/host/backups" \
      GRAIN_MIGRATE_SKIP_ROOT_CHECK=1 \
      bash "$script" --kubeconfig "$work/host/etc/grain/admin.kubeconfig" --verify-timeout 5 "$@" 2>&1
  )" || status=$?
}

calls() { cat "$work/stub/calls.log"; }
units() { ls "$work/stub/units"; }
sysd="$work/host/etc/systemd/system"

# --- Cases ------------------------------------------------------------------

fresh
migrate --help
check "help names every mode and flag" 0 --dry-run --rollback --kubeconfig --cluster --project --location \
  --provision --server-url --no-sync-back --print-manifest

fresh
migrate --dry-run
check "a dry run walks every stage" 0 "dry run" "Stage 1: preflight" "Stage 2: drain" "Stage 3: migrate" \
  "Stage 4: start" "Stage 5: point this host" "Stage 5b: readiness" \
  "+ systemctl stop grain-daemon.service" "+ systemctl mask grain-daemon.service" \
  "sandbox image:    docker.io/grainai/sandbox:v2.1.0" "target repo:      ai-on-gke/grain" \
  "tar -C" "-server http://127.0.0.1:4280 -token-file" "sandboxBackend"
lacks "a dry run stops nothing" "$(calls)" "systemctl stop"
lacks "a dry run applies nothing" "$(calls)" "kubectl apply"
truth "a dry run leaves the daemon running" test -f "$work/stub/units/grain-daemon.service.active"
truth "a dry run writes no drop-in" test ! -e "$sysd/grain-hostd.service.d/gke.conf"
truth "a dry run takes no backup" test -z "$(ls "$work/host/backups")"
# What a dry run prints is what an operator may paste: every kubectl in it
# names the migration's kubeconfig, not whatever context is current.
lacks "every kubectl it prints names the kubeconfig" "$(grep -E '(^[+] |[|] )kubectl' <<<"$out" | grep -v -- 'kubectl --kubeconfig=' || true)" "kubectl"
lacks "its copy keeps file modes as the real copy does" "$out" "--no-same-permissions"
holds "the unit is moved aside before it is masked" "$out" "+ mv $sysd/grain-daemon.service $sysd/grain-daemon.service.pre-gke"

fresh
rm "$work/host/var/lib/grain/data/secrets/secrets.key"
migrate
check "no secrets.key is refused" 1 "no secrets.key"
truth "and the daemon was not touched" test -f "$work/stub/units/grain-daemon.service.active"

# local_postgres turns the fresh install into one whose store is the local
# PostgreSQL server setup.sh provisions (docs/database.md): its unit, the
# password beside its cluster, /etc/grain/postgres.env read by the
# daemon's unit, and db-config.json naming 127.0.0.1 -- with the SQLite
# store it was migrated from still in place, as setup.sh leaves it.
local_postgres() {
  local h="$work/host" d="$work/host/var/lib/grain/data"
  mkdir -p "$d/postgres"
  echo 16 >"$d/postgres/PG_VERSION"
  echo "0123abcd" >"$d/secrets/postgres-password"
  touch "$d/.postgres-migrated"
  printf 'GRAIN_DB_DRIVER=postgres\nGRAIN_DB_DSN=postgres://grain:0123abcd@127.0.0.1:5433/grain?sslmode=disable\n' \
    >"$h/etc/grain/postgres.env"
  echo '{"driver":"postgres","dsn":"postgres://grain@127.0.0.1:5433/grain?sslmode=disable\u0026connect_timeout=5","passwordSecret":"database-password"}' \
    >"$d/db-config.json"
  cat >"$sysd/grain-postgres.service" <<'UNIT'
[Service]
Environment=GRAIN_POSTGRES_IMAGE=mirror.example.com/library/postgres:16-bookworm
Environment=GRAIN_POSTGRES_PORT=5433
ExecStart=/usr/bin/docker run --name grain-postgres --rm --network host postgres -c listen_addresses=127.0.0.1 -c port=5433
UNIT
  sed -i "s|^User=grain\$|User=grain\nRequires=grain-postgres.service\nEnvironmentFile=$h/etc/grain/postgres.env|" \
    "$sysd/grain-daemon.service"
  touch "$work/stub/units/grain-postgres.service.active"
}

fresh
local_postgres
migrate
check "a store on the local Postgres server is refused" 1 "store is the local PostgreSQL server (grain-postgres.service" \
  "names 127.0.0.1:5433" "Nothing was changed" "pg_dump" "setup.sh --db-dsn" "docs/database.md"
truth "and the daemon was not stopped" test -f "$work/stub/units/grain-daemon.service.active"
truth "nor the Postgres server" test -f "$work/stub/units/grain-postgres.service.active"
lacks "nothing was stopped" "$(calls)" "systemctl"
lacks "nothing was applied to the cluster" "$(calls)" "kubectl"
truth "and no backup was taken" test -z "$(ls "$work/host/backups")"

fresh
local_postgres
migrate --dry-run
check "a dry run on the local Postgres server is refused too" 1 "store is the local PostgreSQL server"

# Only the password left behind, and only db-config.json saying where the
# store is: still the local server.
fresh
local_postgres
rm "$sysd/grain-postgres.service" "$work/host/etc/grain/postgres.env"
sed -i '/^EnvironmentFile=/d' "$sysd/grain-daemon.service"
echo '{"driver":"postgres","dsn":"postgres://grain@localhost/grain"}' >"$work/host/var/lib/grain/data/db-config.json"
migrate --dry-run
check "the local server is found by its password and db-config.json" 1 \
  "store is the local PostgreSQL server" "db-config.json names localhost:5432"

# Moved onto a server the cluster can reach (setup.sh --db-dsn), with the
# old unit left behind: migrated like any external Postgres.
fresh
local_postgres
sed -i '/^EnvironmentFile=/d; /^Requires=/d' "$sysd/grain-daemon.service"
sed -i 's|-default-target-repo ai-on-gke/grain|-default-target-repo ai-on-gke/grain -db-driver postgres -db-dsn postgres://grain:pw@db.example.internal:5432/grain|' \
  "$sysd/grain-daemon.service"
echo '{"driver":"postgres","dsn":"host=db.example.internal port=5432 dbname=grain user=grain"}' \
  >"$work/host/var/lib/grain/data/db-config.json"
migrate --dry-run
check "a store moved off the local server migrates" 0 "grain-postgres.service is installed but is not the store" \
  "store: Postgres at db.example.internal:5432" "make sure the cluster can reach it" "Stage 5b: readiness"
lacks "and is not taken for SQLite" "$out" "store: SQLite"

fresh
touch "$work/host/var/lib/grain/kubeconfig"
status=0
out="$(env PATH="$stubs:$PATH" STUB="$work/stub" GRAIN_ROOT_DIR="$work/host/var/lib/grain" \
  GRAIN_ETC_DIR="$work/host/etc/grain" GRAIN_SYSTEMD_DIR="$sysd" GRAIN_MIGRATE_SKIP_ROOT_CHECK=1 \
  bash "$script" 2>&1)" || status=$?
check "gke-sandboxes' least-privilege kubeconfig alone is refused" 1 "least-privilege kubeconfig" "--cluster/--project/--location"

fresh
STUB_NO_DEVICE_PLUGIN=1 migrate
check "a cluster with no granule device plugin is refused" 1 "granule-device-plugin" "--provision"
truth "before the daemon is stopped" test -f "$work/stub/units/grain-daemon.service.active"

fresh
rm "$work/stub/cluster/ns" "$work/stub/cluster/labeled"
STUB_DOCKER_CONTAINER=1 migrate
check "a migration completes" 0 "the controller answers" "sandbox backend: kubernetes-granule" \
  "hostd attached: gcp" "dispatch resumed" "Done."
c="$(calls)"
holds "the namespace is created" "$c" "create namespace grain"
holds "and labelled privileged" "$c" "pod-security.kubernetes.io/enforce=privileged"
holds "dispatch is paused before the stop" "$c" "grain pause -set"
holds "the daemon container is removed" "$c" "docker rm -f grain-daemon"
truth "the local daemon is stopped" test ! -e "$work/stub/units/grain-daemon.service.active"
truth "and disabled" test ! -e "$work/stub/units/grain-daemon.service.enabled"
truth "and masked" test -e "$work/stub/units/grain-daemon.service.masked"
truth "by a link to /dev/null" test "$(readlink "$sysd/grain-daemon.service")" = /dev/null
truth "with its own file kept aside" test -f "$sysd/grain-daemon.service.pre-gke"
truth "a backup was taken" test -n "$(ls "$work/host/backups"/grain-data-*.tar.gz)"
truth "the store is on the PVC" test -f "$work/stub/cluster/pvc/store/grain.db"
truth "the installation id is on the PVC" test -f "$work/stub/cluster/pvc/installation-id"
truth "the state repo is on the PVC" test -f "$work/stub/cluster/pvc/state-repo/settings/config.yml"
truth "the copy is marked done" test -f "$work/stub/cluster/pvc/.migrated-from-local"
truth "the controller runs" test "$(cat "$work/stub/cluster/replicas")" = 1
applied="$(cat "$work"/stub/cluster/applied-*.yaml)"
holds "the Secret carries secrets.key and the token" "$c" "--from-file=secrets.key=$work/host/var/lib/grain/data/secrets/secrets.key --from-file=hostd-token=$work/host/etc/grain/hostd.token"
holds "the manifest is applied at replicas 0 first" "$applied" "  replicas: 0"
holds "with the hostd token flag" "$applied" "- -hostd-token-file=/etc/grain-secrets/hostd-token"
holds "with the sandbox image" "$applied" "- -k8s-granule-image=docker.io/grainai/sandbox:v2.1.0"
holds "with the target repo" "$applied" "- -default-target-repo=ai-on-gke/grain"
lacks "with no CHANGEME left in an arg" "$(grep -E '^ +- -' <<<"$applied")" "CHANGEME"
holds "the transfer pod runs as uid 1000" "$applied" "runAsUser: 1000"
truth "a token was minted" test -s "$work/host/etc/grain/hostd.token"
dropin="$(cat "$sysd/grain-hostd.service.d/gke.conf")"
holds "hostd dials the controller" "$dropin" "ExecStart=/var/lib/grain-hostd/versions/current/grain hostd -socket /run/grain-hostd/hostd.sock -allowlist /etc/grain/hostd.yaml -state-dir /var/lib/grain-hostd -server http://127.0.0.1:4280 -token-file $work/host/etc/grain/hostd.token"
holds "after the forward" "$dropin" "After=grain-gke-forward.service"
holds "the forward runs as hostd's user" "$(cat "$sysd/grain-gke-forward.service")" "User=alice"
holds "and port-forwards svc/grain" "$(cat "$sysd/grain-gke-forward.service")" "port-forward --address 127.0.0.1 svc/grain 4280:80"
truth "hostd was restarted" test -f "$work/stub/units/grain-hostd.service.active"
truth "no model server is set up" test ! -e "$sysd/grain-model-server.service"
holds "the API is called with the migrated local-api-token" "$c" "Authorization: Bearer local-token"
holds "the backend is set" "$c" '{"sandboxBackend":"kubernetes-granule"}'
holds "dispatch is resumed on the controller" "$c" "-X DELETE"

: >"$work/stub/calls.log"
migrate
check "a re-run is idempotent" 0 "already runs (replicas=1)" "token: $work/host/etc/grain/hostd.token (kept)" "Done."
lacks "and copies nothing again" "$out" "copying"
lacks "nor scales the controller to 0" "$(cat "$work/stub/cluster/replicas")" "0"

migrate --recopy
check "--recopy over a running controller is refused" 1 "--recopy while Deployment/grain runs"

fresh
migrate --server-url https://grain.example.com/
check "a --server-url migration needs the identity hostd connects as" 1 "--server-url needs --hostd-owner"

fresh
migrate --server-url https://grain.example.com/ --hostd-owner 'a\nb@example.com'
check "an owner with a backslash escape is refused" 1 "--hostd-owner takes a full email address"

fresh
migrate --server-url https://grain.example.com/ --hostd-owner a@example.com --iap-audience 'id -x'
check "an audience with whitespace is refused" 1 "--iap-audience takes an IAP OAuth client ID"

fresh
migrate --server-url https://grain.example.com/ --hostd-owner Hostd@Proj.iam.gserviceaccount.com --iap-audience iap-client.apps.example.com
check "a --server-url migration completes" 0 "Done." "hostd owner: hostd@proj.iam.gserviceaccount.com"
lacks "and sets up no model server" "$out" "model-server"
truth "a --server-url needs no forward" test ! -e "$sysd/grain-gke-forward.service"
holds "hostd dials the URL" "$(cat "$sysd/grain-hostd.service.d/gke.conf")" "-server https://grain.example.com -token-file"
holds "hostd gets an IAP token" "$(cat "$sysd/grain-hostd.service.d/gke.conf")" "-iap-audience iap-client.apps.example.com"
holds "the controller records the owner" "$(cat "$work"/stub/cluster/applied-*.yaml)" "- -hostd-token-owner=hostd@proj.iam.gserviceaccount.com"

fresh
STUB_CAPS='[{"id":"gcp","status":{"state":"failed","run":"host"}}]' migrate --verify-timeout 0
check "a capability server that never attaches fails the check" 1 "did not all attach" "gcp=failed"

# Rollback, after a migration: what the controller wrote comes back.
# The grain model-server unit an earlier version of this script set up
# is removed with the rest.
fresh
migrate
printf '[Service]\nExecStart=/bin/true\n' >"$sysd/grain-model-server.service"
echo "written in the cluster" >"$work/stub/cluster/pvc/from-gke"
status=0
out="$(env PATH="$stubs:$PATH" STUB="$work/stub" GRAIN_ROOT_DIR="$work/host/var/lib/grain" \
  GRAIN_ETC_DIR="$work/host/etc/grain" GRAIN_SYSTEMD_DIR="$sysd" GRAIN_MIGRATE_SKIP_ROOT_CHECK=1 \
  bash "$script" --kubeconfig "$work/host/etc/grain/admin.kubeconfig" --rollback --dry-run 2>&1)" || status=$?
check "a rollback dry run names the transfer pod's image" 0 "pod/grain-data-transfer (docker.io/grainai/grain:main, uid 1000)" \
  "+ mv $sysd/grain-daemon.service.pre-gke $sysd/grain-daemon.service"
lacks "and every kubectl it prints names the kubeconfig" "$(grep -E '(^[+] |[|] )kubectl' <<<"$out" | grep -v -- 'kubectl --kubeconfig=' || true)" "kubectl"
migrate --rollback
check "a rollback completes" 0 "Step 1: stop Deployment/grain" "Step 2: copy the cluster's data directory back" "runs here again"
truth "the controller is at 0" test "$(cat "$work/stub/cluster/replicas")" = 0
truth "the cluster's data is back" test -f "$work/host/var/lib/grain/data/from-gke"
truth "the store is back" test -f "$work/host/var/lib/grain/data/store/grain.db"
truth "with no migration marker" test ! -e "$work/host/var/lib/grain/data/.migrated-from-local"
truth "the local copy was moved aside" test -n "$(ls -d "$work/host/var/lib/grain/data.pre-rollback-"*)"
truth "the hostd drop-in is gone" test ! -e "$sysd/grain-hostd.service.d/gke.conf"
truth "the model server is gone" test ! -e "$sysd/grain-model-server.service"
truth "the forward is gone" test ! -e "$sysd/grain-gke-forward.service"
truth "the daemon is unmasked" test ! -e "$work/stub/units/grain-daemon.service.masked"
truth "with its own unit file back" test -f "$sysd/grain-daemon.service" -a ! -L "$sysd/grain-daemon.service"
truth "and none left aside" test ! -e "$sysd/grain-daemon.service.pre-gke"
truth "and running" test -f "$work/stub/units/grain-daemon.service.active"
holds "dispatch is resumed locally" "$(calls)" "grain pause -resume"

fresh
migrate
migrate --rollback --no-sync-back
check "a rollback can keep the local data" 0 "keeping" "runs here again"
truth "nothing is moved aside" test -z "$(ls -d "$work/host/var/lib/grain/data.pre-rollback-"* 2>/dev/null)"

fresh
migrate --print-manifest --node-selector cloud.google.com/gke-nodepool=granule
check "--print-manifest renders the manifest" 0 "kind: Deployment" "- -k8s-node-selector=cloud.google.com/gke-nodepool=granule" \
  "secretName: grain-migration" "- -hostd-token-file=/etc/grain-secrets/hostd-token"

# A setup.sh container install from release assets: the unit runs
# ${GRAIN_IMAGE} out of /etc/grain/image.env, which names a local grain/*
# ref no cluster can pull. Without --mirror that stops the migration before
# anything is touched; with it both images are pushed there and run.
fresh
mkdir -p "$work/host/etc/grain"
echo "GRAIN_IMAGE=grain/grain:sha-0123456789ab" >"$work/host/etc/grain/image.env"
cat >"$sysd/grain-daemon.service" <<'UNIT'
[Service]
EnvironmentFile=/etc/grain/image.env
ExecStart=/usr/bin/docker run --name grain-daemon ${GRAIN_IMAGE} daemon \
  -data-dir /var/lib/grain/data -granule-sandboxes \
  -sandbox-image grain/guest:sha-0123456789ab -default-target-repo ai-on-gke/grain
UNIT
migrate --dry-run
check "a release-asset host needs --mirror" 1 "release-asset image only this host has: pass --mirror"
truth "and the daemon was not touched" test -f "$work/stub/units/grain-daemon.service.active"
migrate --dry-run --mirror registry.example/proj/grain/
check "--mirror renames both release-asset images" 0 \
  "controller image: registry.example/proj/grain/grain:sha-0123456789ab" \
  "sandbox image:    registry.example/proj/grain/guest:sha-0123456789ab" \
  "+ docker push registry.example/proj/grain/grain:sha-0123456789ab" \
  "+ docker push registry.example/proj/grain/guest:sha-0123456789ab"
truth "the shared daemon/transfer image is pushed once" \
  test "$(grep -c '^+ docker push registry.example/proj/grain/grain:' <<<"$out")" = 1

echo
if [ "$failures" -gt 0 ]; then
  echo "$failures of $cases cases failed"
  exit 1
fi
echo "all $cases cases passed"
