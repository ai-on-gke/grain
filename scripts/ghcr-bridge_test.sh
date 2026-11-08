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

# Tests for scripts/ghcr-bridge.sh against a stub docker: what it refuses
# before writing anything, and which copies it makes.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
script="$here/ghcr-bridge.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
fails=0
passes=0

expect() { # expect <name> <want-status> <want-output-substring> -- <args...>
  local name="$1" want="$2" sub="$3" got out
  shift 4
  set +e
  out="$(cd "$work/repo" && PATH="$work/bin:$PATH" "$@" 2>&1)"
  got=$?
  set -e
  if [ "$got" != "$want" ] || [[ "$out" != *"$sub"* ]]; then
    echo "FAIL $name: exit $got (want $want), output:"
    printf '%s\n' "$out" | sed 's/^/  /'
    fails=$((fails + 1))
  else
    echo "ok: $name"
    passes=$((passes + 1))
  fi
}

# A repository: old (v3.1.0), the loader, a main commit and v3.2.0 on it.
git init -q "$work/repo"
cd "$work/repo"
git config user.name test && git config user.email test@example.com
git commit -q --allow-empty -m old && git tag v3.1.0
old="$(git rev-parse HEAD)"
git commit -q --allow-empty -m loader
loader="$(git rev-parse HEAD)"
git commit -q --allow-empty -m release && git tag v3.2.0
new="$(git rev-parse HEAD)"
cd - >/dev/null

# The stub docker: a registry of tag -> "digest revision", from $work/reg,
# and a log of every write.
mkdir -p "$work/bin"
cat >"$work/bin/docker" <<'EOF'
#!/usr/bin/env bash
reg="$STUB_REG"
lookup() { local r="${1%%@*}"; case "$1" in *@*) r="$1" ;; esac; awk -v r="$r" '$1 == r { print $2, $3 }' "$reg" | tail -n 1; }
case "$1 $2" in
  "buildx version") exit 0 ;;
  "buildx imagetools")
    case "$3" in
      inspect)
        read -r d rev <<<"$(lookup "$4")"
        [ -n "$d" ] || { echo "not found: $4" >&2; exit 1; }
        case "$6" in
          *Manifest*) printf '{"digest":"%s"}\n' "$d" ;;
          *) printf '{"config":{"Labels":{"org.opencontainers.image.revision":"%s"}}}\n' "$rev" ;;
        esac ;;
      create)
        shift 3; tags=()
        while [ "$1" = --tag ]; do tags+=("$2"); shift 2; done
        src="$1"; read -r d rev <<<"$(lookup "$src")"
        for t in "${tags[@]}"; do echo "$t $d $rev" >>"$reg"; echo "copied $src $t" >>"$STUB_LOG"; done ;;
    esac ;;
  pull*) exit 0 ;;
  run*)
    case "$*" in
      *flag-names*) printf 'upgrade-image value\n%s' "${STUB_FLAGS-upgrade-image-source value
}" ;;
      *sandbox-image*) echo "$STUB_STAMP" ;;
    esac ;;
esac
EOF
chmod +x "$work/bin/docker"

reset() { # reset <docker.io latest tag>
  local short="${new:0:7}"
  cat >"$work/reg" <<EOF
docker.io/grainai/grain:main sha256:main $new
docker.io/grainai/grain@sha256:main sha256:main $new
docker.io/grainai/grain:v3.2.0 sha256:main $new
docker.io/grainai/grain:v3.1.0 sha256:old $old
docker.io/grainai/grain@sha256:old sha256:old $old
docker.io/grainai/grain:latest sha256:$1
docker.io/grainai/sandbox:sha-$short sha256:guest
docker.io/grainai/sandbox-k8s:sha-$short sha256:k8s
docker.io/grainai/sandbox@sha256:guest sha256:guest
docker.io/grainai/sandbox-k8s@sha256:k8s sha256:k8s
EOF
  : >"$work/log"
}
export STUB_REG="$work/reg" STUB_LOG="$work/log"
export STUB_STAMP="docker.io/grainai/sandbox:sha-${new:0:7}@sha256:guest"
export GHCR_BRIDGE_LOADER_COMMIT="$loader"
short="${new:0:7}"

reset main
expect "main copies the sandboxes, then the daemon, as main and sha-<7>" 0 \
  "ghcr.io/bwsalmon/grain/grain:sha-$short = sha256:main" -- bash "$script" main
want="copied docker.io/grainai/sandbox@sha256:guest ghcr.io/bwsalmon/grain/guest:main
copied docker.io/grainai/sandbox@sha256:guest ghcr.io/bwsalmon/grain/guest:sha-$short
copied docker.io/grainai/sandbox-k8s@sha256:k8s ghcr.io/bwsalmon/grain/k8s-sandbox:main
copied docker.io/grainai/sandbox-k8s@sha256:k8s ghcr.io/bwsalmon/grain/k8s-sandbox:sha-$short
copied docker.io/grainai/grain@sha256:main ghcr.io/bwsalmon/grain/grain:main
copied docker.io/grainai/grain@sha256:main ghcr.io/bwsalmon/grain/grain:sha-$short"
if [ "$(cat "$work/log")" != "$want" ]; then
  echo "FAIL: main's copies:"; sed 's/^/  /' "$work/log"; fails=$((fails + 1))
fi

reset main
expect "DRY_RUN writes nothing" 0 "would copy docker.io/grainai/grain@sha256:main" -- env DRY_RUN=1 bash "$script" main
[ ! -s "$work/log" ] || { echo "FAIL: DRY_RUN copied:"; cat "$work/log"; fails=$((fails + 1)); }

reset old
expect "latest refuses a release Docker Hub has not promoted" 1 \
  "bridge latest only from the promoted release" -- bash "$script" latest v3.2.0
[ ! -s "$work/log" ] || { echo "FAIL: a refused latest copied:"; cat "$work/log"; fails=$((fails + 1)); }

reset main
expect "latest from the promoted release writes latest, vX.Y.Z, candidate and rc" 0 \
  "ghcr.io/bwsalmon/grain/grain:rc = sha256:main" -- bash "$script" latest v3.2.0

reset old
expect "latest refuses a release without the loader" 1 \
  "does not contain the release-asset loader" -- bash "$script" latest v3.1.0
expect "latest refuses a branch" 1 "latest needs a released vX.Y.Z" -- bash "$script" latest main

reset main
expect "a daemon without -upgrade-image-source is refused" 1 "has no -upgrade-image-source flag" -- \
  env STUB_FLAGS= bash "$script" main
expect "a daemon stamped with another guest is refused" 1 "is stamped with sandbox image" -- \
  env STUB_STAMP=docker.io/grainai/sandbox:sha-0000000@sha256:other bash "$script" main
[ ! -s "$work/log" ] || { echo "FAIL: a refused main copied:"; cat "$work/log"; fails=$((fails + 1)); }

[ "$fails" -eq 0 ] || { echo "$fails failure(s)"; exit 1; }
echo "$passes passed"
