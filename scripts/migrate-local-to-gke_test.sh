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
  mask) touch "$u/$2.masked" ;;
  unmask) rm -f "$u/$2.masked" ;;
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
  */api/executors) printf '[{"host":"%s"}]\n' "$(hostname -s 2>/dev/null || hostname)" ;;
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
  -granule-sandboxes -sandbox-image ghcr.io/bwsalmon/grain/guest:v2.1.0 \
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
  --provision --server-url --model-server --no-sync-back --print-manifest

fresh
migrate --dry-run
check "a dry run walks every stage" 0 "dry run" "Stage 1: preflight" "Stage 2: drain" "Stage 3: migrate" \
  "Stage 4: start" "Stage 5: point this host" "Stage 5b: readiness" \
  "+ systemctl stop grain-daemon.service" "+ systemctl mask grain-daemon.service" \
  "sandbox image:    ghcr.io/bwsalmon/grain/guest:v2.1.0" "target repo:      ai-on-gke/grain" \
  "tar -C" "-server http://127.0.0.1:4280 -token-file" "sandboxBackend"
lacks "a dry run stops nothing" "$(calls)" "systemctl stop"
lacks "a dry run applies nothing" "$(calls)" "kubectl apply"
truth "a dry run leaves the daemon running" test -f "$work/stub/units/grain-daemon.service.active"
truth "a dry run writes no drop-in" test ! -e "$sysd/grain-hostd.service.d/gke.conf"
truth "a dry run takes no backup" test -z "$(ls "$work/host/backups")"

fresh
rm "$work/host/var/lib/grain/data/secrets/secrets.key"
migrate
check "no secrets.key is refused" 1 "no secrets.key"
truth "and the daemon was not touched" test -f "$work/stub/units/grain-daemon.service.active"

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
holds "with the sandbox image" "$applied" "- -k8s-granule-image=ghcr.io/bwsalmon/grain/guest:v2.1.0"
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
truth "no model server without agy" test ! -e "$sysd/grain-model-server.service"
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
echo '#!/bin/sh' >"$work/agy" && chmod +x "$work/agy"
migrate --agy "$work/agy" --server-url https://grain.example.com/
check "a model server is set up beside hostd" 0 "model server:     yes" "model-server" "connected" "Done."
model="$(cat "$sysd/grain-model-server.service")"
holds "it runs grain model-server against the controller" "$model" "/var/lib/grain-hostd/versions/current/grain model-server -server https://grain.example.com -token-file $work/host/etc/grain/hostd.token"
holds "with agy" "$model" "-binary agy=$work/agy"
holds "as hostd's user" "$model" "User=alice"
truth "and is started" test -f "$work/stub/units/grain-model-server.service.active"
truth "a --server-url needs no forward" test ! -e "$sysd/grain-gke-forward.service"
holds "hostd dials the URL" "$(cat "$sysd/grain-hostd.service.d/gke.conf")" "-server https://grain.example.com -token-file"

fresh
STUB_CAPS='[{"id":"gcp","status":{"state":"failed","run":"host"}}]' migrate --verify-timeout 0
check "a capability server that never attaches fails the check" 1 "did not all attach" "gcp=failed"

# Rollback, after a migration: what the controller wrote comes back.
fresh
migrate --agy "$work/agy"
echo "written in the cluster" >"$work/stub/cluster/pvc/from-gke"
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
  "secretName: grain-migration" "- -executor-token-file=/etc/grain-secrets/hostd-token"

echo
if [ "$failures" -gt 0 ]; then
  echo "$failures of $cases cases failed"
  exit 1
fi
echo "all $cases cases passed"
