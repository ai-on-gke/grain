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

# §7 item 5: the shared Go build cache. A task sandbox presents no token,
# so its writes are refused; a trusted Grain CI job (base-branch CI on a
# push to main) is registered as a writer and its entry reaches a later
# task sandbox. acme/widgets runs on ci_mode: grain for this item only,
# since only Grain CI jobs are ever registered as writers.

set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/common.sh"
up
api PUT /api/repos/acme/widgets/ci-mode '{"ciMode":"grain"}' >/dev/null
api PUT /api/repos/acme/widgets/watch-default-branch '{"watchDefaultBranch":true}' >/dev/null
# The cache's own address, for a sandbox whose GOCACHEPROG names none (a
# host sandbox gets no GOCACHEPROG): a sandbox that wants to poison the
# cache need not be handed its address to find it.
CACHE_URL=$(grep -o 'the package cache is serving at http://[0-9.:]*' "$WORK_DIR/daemon.log" | tail -1 | awk '{print $NF}')
echo "package cache: $CACHE_URL"

# The same action ID for both writers: the trusted CI job's entry should
# be the one a later sandbox reads, and a sandbox must not be able to
# overwrite it. The output ID is the sha256 of the bytes PUT.
AID=$(printf 'walk-item5-action' | sha256sum | cut -d' ' -f1)
CI_BYTES='trusted output from the CI job on main'
CI_OID=$(printf '%s' "$CI_BYTES" | sha256sum | cut -d' ' -f1)
SB_BYTES='trojaned output from a task sandbox'
SB_OID=$(printf '%s' "$SB_BYTES" | sha256sum | cut -d' ' -f1)

# put_cmd <label> <bytes> <oid>: shell that PUTs an output and an action
# entry for $AID to the cache GOCACHEPROG names (with its credentials, if
# any), printing each status. The token is never printed.
put_cmd() {
	cat <<EOF
P=\$(go env GOCACHEPROG); U=\${P#* }; [ -n "\$P" ] || U=$CACHE_URL
echo "$1 GOCACHEPROG: \$(echo "\$P" | sed -E 's#//job:[0-9a-f]+@#//job:<token>@#')"
printf '%s' '$2' > /tmp/walk-out.\$\$
echo "$1 PUT output: \$(curl -s -o /dev/null -w '%{http_code}' -X PUT --data-binary @/tmp/walk-out.\$\$ "\$U/gocache/output/$3")"
echo "$1 PUT action: \$(curl -s -o /tmp/walk-body.\$\$ -w '%{http_code}' -X PUT -H 'Content-Type: application/json' -d '{"OutputID":"$3","Size":${#2}}' "\$U/gocache/action/$AID") \$(head -c 160 /tmp/walk-body.\$\$)"
echo "$1 PUT action, forged token: \$(curl -s -o /dev/null -w '%{http_code}' -X PUT -u job:$(printf '0%.0s' $(seq 64)) -d '{"OutputID":"$3","Size":${#2}}' "\$U/gocache/action/$AID")"
echo "$1 PUT trusted/action: \$(curl -s -o /dev/null -w '%{http_code}' -X PUT -H 'Grit-Cache-Trust: trusted' -d '{"OutputID":"$3","Size":${#2}}' "\$U/gocache/trusted/action/$AID")"
rm -f /tmp/walk-out.\$\$ /tmp/walk-body.\$\$
EOF
}

# get_cmd <label>: shell that GETs $AID's action entry and its output.
get_cmd() {
	cat <<EOF
P=\$(go env GOCACHEPROG); U=\${P#* }; [ -n "\$P" ] || U=$CACHE_URL
A=\$(curl -s -w ' %{http_code}' "\$U/gocache/action/$AID" | tr -d '\n'); echo "$1 GET action: \$A"
O=\$(echo "\$A" | sed -nE 's/.*"OutputID":"([0-9a-f]+)".*/\1/p')
[ -n "\$O" ] && echo "$1 GET output: \$(curl -s "\$U/gocache/output/\$O")"
true
EOF
}

echo "== a task sandbox before any trusted write: reads nothing, writes are refused"
calls=$(jq -sc . <<EOF
$(SH "$(get_cmd sandbox-1)")
$(SH "$(put_cmd sandbox-1 "$SB_BYTES" "$SB_OID")")
$(SH "$(get_cmd sandbox-1-after-own-put)")
$(SH 'mkdir -p /tmp/walk-mod && cd /tmp/walk-mod && printf "module walk\n\ngo 1.22\n" > go.mod && printf "package main\n\nfunc main() { println(\"walk\") }\n" > main.go && go build -o /dev/null . && echo "sandbox go build: ok (uploads are best effort)"')
$PARK
EOF
)
S1=$(file_task "Item 5: sandbox writer" "{\"kind\":\"mcp_sequence\",\"calls\":$calls}")
last_outputs "$S1" | grep -E '^sandbox|^--- ' | grep -v '^--- resume_later'

echo "== a push to main runs base-branch Grain CI, a trusted writer"
before=$("$G" ci list --repo acme/widgets 2>/dev/null | awk '{print $1}' | sort || true)
tmp=$(mktemp -d)
git clone -q "$WORK_DIR/widgets.git" "$tmp"
cat >"$tmp/.grain/workflows/ci.yml" <<EOF
name: ci
on:
  push:
  pull_request:
jobs:
  unit:
    capabilities: []
    sandbox:
      cpus: 2
      memory-mb: 4096
      disk-gb: 10
    steps:
      - name: write the shared Go build cache
        run: |
$(put_cmd ci "$CI_BYTES" "$CI_OID" | sed 's/^/          /')
$(get_cmd ci | sed 's/^/          /')
EOF
git -C "$tmp" add .
git -C "$tmp" -c user.name="Walk Seed" -c user.email="seed@example.com" commit -qm "ci: write the Go build cache"
git -C "$tmp" push -q origin main
MAIN=$(git -C "$tmp" rev-parse HEAD)
rm -rf "$tmp"
echo "main is now ${MAIN:0:12}"
run=""
for _ in $(seq 150); do
	run=$("$G" ci list --repo acme/widgets 2>/dev/null | awk '$5 == "push" {print $1}' | grep -vxF -f <(echo "$before") | head -1 || true)
	if [[ -n "$run" ]] && "$G" ci list --repo acme/widgets 2>/dev/null | grep -F "$run" | grep -Eq ' (success|failure|errored|cancelled) '; then
		break
	fi
	sleep 1
done
"$G" ci list --repo acme/widgets 2>&1 | head -5
echo "run: ${run:-<none>}"
if [[ -n "$run" ]]; then
	"$G" ci show "$run" | sed -n '1,4p'
	"$G" ci show -output all "$run" 2>&1 | grep -E '^ci ' | sed -E 's#//job:[0-9a-f]+@#//job:<token>@#g' | head -20 || true
fi
grep -E "writes the shared Go build cache|Go build-cache|refused PUT" "$WORK_DIR/daemon.log" | sed -E 's/^[0-9\/: ]+//' | cut -c1-300 | head -8 || true

echo "== a later task sandbox reads the CI job's entry, and still cannot overwrite it"
calls=$(jq -sc . <<EOF
$(SH "$(get_cmd sandbox-2)")
$(SH "$(put_cmd sandbox-2 "$SB_BYTES" "$SB_OID")")
$(SH "$(get_cmd sandbox-2-after-own-put)")
$PARK
EOF
)
S2=$(file_task "Item 5: sandbox reader" "{\"kind\":\"mcp_sequence\",\"calls\":$calls}")
last_outputs "$S2" | grep -E '^sandbox|^--- ' | grep -v '^--- resume_later'
echo "expected: CI OutputID=$CI_OID; a sandbox's OutputID=$SB_OID"
stop_env "$WORK_DIR"
