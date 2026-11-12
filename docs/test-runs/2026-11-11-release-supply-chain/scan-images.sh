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

# Walkthrough harness for docs/test-plans/2026-11-11.md section 7 item 5:
# `make scan-images` (scripts/scan-images.sh with its pinned Trivy, then
# cmd/image-vulns) over two locally built images:
#
#   walk-grain:vuln   FROM scratch, one Go binary built against
#                     golang.org/x/crypto v0.16.0 (a fixable CRITICAL,
#                     CVE-2024-45337, fixed in v0.31.0)
#   walk-sandbox:pack a granule pack: labelled
#                     dev.granule.granulefile.kind=pack and carrying an ext4
#                     /var/lib/granule/guest/disk.img with the same binary,
#                     so the guest disk is scanned as sandbox-rootfs
#
# and four allowlists: none (fails), a waiver for both (passes), the same
# waiver expired (fails again), and a waiver plus entries no finding
# matches (passes, the unmatched entries listed stale). Nothing is pushed.
#
#   bash docs/test-runs/2026-11-11-release-supply-chain/scan-images.sh [repo-root]
set -uo pipefail

root="$(cd "${1:-$(dirname "${BASH_SOURCE[0]}")/../../..}" && pwd)"
PATH="$PATH:/usr/sbin:/sbin" command -v mkfs.ext4 >/dev/null || { echo "mkfs.ext4 is not installed (e2fsprogs)" >&2; exit 2; }
work="$(mktemp -d /tmp/scanwalk.XXXXXX)"
trap '[ -n "${KEEP:-}" ] || rm -rf "$work"' EXIT

mkdir -p "$work/app" "$work/rootfs/usr/local/bin" "$work/rootfs/etc"
cat > "$work/app/main.go" <<'EOF'
package main

import (
	"fmt"

	"golang.org/x/crypto/ssh"
)

func main() { fmt.Println(ssh.CertAlgoRSAv01) }
EOF
(cd "$work/app" && go mod init example.test/walkapp >/dev/null 2>&1 &&
  go get golang.org/x/crypto@v0.16.0 >/dev/null 2>&1 &&
  CGO_ENABLED=0 go build -o app . ) || { echo "could not build the injected binary" >&2; exit 2; }
go version -m "$work/app/app" | grep -E 'x/crypto|^\s+go' | sed 's/^/injected: /'

cp "$work/app/app" "$work/rootfs/usr/local/bin/app"
printf 'ID=debian\nVERSION_ID="13"\n' > "$work/rootfs/etc/os-release"
truncate -s 64M "$work/disk.img"
PATH="$PATH:/usr/sbin:/sbin" mkfs.ext4 -q -F -d "$work/rootfs" "$work/disk.img"

cat > "$work/Dockerfile.grain" <<'EOF'
FROM scratch
COPY app/app /usr/local/bin/app
EOF
cat > "$work/Dockerfile.pack" <<'EOF'
FROM scratch
LABEL dev.granule.granulefile.kind=pack
ENTRYPOINT ["/usr/local/bin/granule-shim"]
COPY disk.img /var/lib/granule/guest/disk.img
EOF
docker build -q -t walk-grain:vuln -f "$work/Dockerfile.grain" "$work" >/dev/null
docker build -q -t walk-sandbox:pack -f "$work/Dockerfile.pack" "$work" >/dev/null

today="$(date -u +%F)"
future="$(date -u -d '+30 days' +%F)"
past="$(date -u -d '-1 day' +%F)"
waiver() { # waiver <expires>
  cat <<EOF
  - image: "*"
    package: golang.org/x/crypto
    target: usr/local/bin/app
    reason: walkthrough waiver for the injected x/crypto v0.16.0
    owner: walkthrough
    expires: "$1"
    ids: [$ids]
EOF
}
write_waivers() {
{ echo "entries:"; waiver "$future"; } > "$work/waived.yaml"
{ echo "entries:"; waiver "$past"; } > "$work/expired.yaml"
{ echo "entries:"; waiver "$future"; cat <<EOF
  - image: grain
    package: golang.org/x/net
    target: usr/local/bin/kubectl
    reason: nothing in these images matches this
    owner: walkthrough
    expires: "$future"
    ids: [CVE-2099-0001]
  - image: hub
    package: stdlib
    reason: an image this scan does not cover
    owner: walkthrough
    expires: "$future"
    ids: [CVE-2099-0002]
EOF
} > "$work/stale.yaml"
}
{ echo "entries: []"; } > "$work/none.yaml"

echo "today is $today; the waiver expires $future (or $past when expired)"
for case in none waived expired stale; do
  echo "=== allowlist: $case"
  status=0
  out="$(cd "$root" && IMAGE_VULNS_ALLOWLIST="$work/$case.yaml" make --no-print-directory scan-images \
    SCAN_OUT="$work/out-$case" SCAN_IMAGES="grain=walk-grain:vuln sandbox=walk-sandbox:pack" 2>&1)" || status=$?
  echo "    exit $status"
  printf '%s\n' "$out" | grep -E 'scanning|FAIL|PASS|fail|stale|Stale|expired|waived|CVE-2024-45337|CVE-2099|refus|blocking|Blocking|gate' |
    grep -v '^go: ' | head -40 | sed 's/^/    | /'
  ls "$work/out-$case" 2>/dev/null | paste -sd' ' - | sed 's/^/    reports: /'
  if [ "$case" = none ]; then
    # The waiver covers every id the injected x/crypto brought in (the
    # first scan's blocking findings), in both images, and nothing else.
    ids="$(jq -r '[.findings[] | select(.status=="blocking") | .id] | unique | join(", ")' "$work/out-none/image-vulns.json")"
    echo "    waiver ids: $ids"
    write_waivers
  fi
done
