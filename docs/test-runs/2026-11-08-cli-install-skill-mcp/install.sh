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

# §2 item 1: scripts/build-cli.sh archives -> a local release mirror ->
# a real `grain daemon` started with GRAIN_CLI_RELEASE_URL pointing at it ->
# `curl -fsSL <daemon>/cli/install.sh | sh`.
#
#   D=<daemon URL> RAW=<build-cli.sh output dir> WWW=<mirror docroot> \
#   MIRROR=<mirror URL> CODE=<access code> ./install.sh
#
# The mirror serves $WWW/releases/<version>/{grain-*.tar.gz,checksums.txt}.
# A `tar` shim on PATH records every invocation, which proves a refused
# archive is never handed to tar.
set -u
D=${D:?daemon URL}
RAW=${RAW:?build-cli.sh output dir}
WWW=${WWW:?mirror docroot}
CODE=${CODE:?access code}
W=$(mktemp -d /tmp/grain-install.XXXXXX)
export HOME=$W/home
mkdir -p "$HOME" "$W/shim"
REAL_TAR=$(command -v tar)
cat >"$W/shim/tar" <<EOF
#!/bin/sh
echo "tar \$*" >>"$W/tar.calls"
exec "$REAL_TAR" "\$@"
EOF
chmod +x "$W/shim/tar"
export PATH="$W/shim:$PATH"

run() { local label=$1; shift; echo "### $label"; echo "\$ $*"; "$@" 2>&1 | sed 's/^/    /'; echo "    -> exit=${PIPESTATUS[0]}"; }
tarcalls() { echo "    tar invocations so far: $( [ -f "$W/tar.calls" ] && wc -l <"$W/tar.calls" || echo 0)"; }

VER=$(curl -fsS "$D/cli/install.sh" | sed -n "s/^GRAIN_DAEMON_RELEASE='\(.*\)'$/\1/p")
URL=$(curl -fsS "$D/cli/install.sh" | sed -n "s/^GRAIN_DAEMON_RELEASE_URL='\(.*\)'$/\1/p")
echo "== the daemon pins release '$VER' at '$URL'"
REL=$WWW/releases/$VER
rm -rf "$WWW/releases"; mkdir -p "$REL"
cp "$RAW"/grain-*.tar.gz "$RAW/checksums.txt" "$REL/"

echo "== GET /cli/install.sh: public (no credential), headers, HEAD"
run "GET without a credential" sh -c "curl -s -D - -o /dev/null $D/cli/install.sh | tr -d '\r' | grep -iE '^(HTTP|content-type|cache-control|x-content-type-options)'"
run "GET with a wrong bearer" curl -s -o /dev/null -w '%{http_code}\n' -H 'Authorization: Bearer nope' "$D/cli/install.sh"
run "GET with the access code" curl -s -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer $CODE" "$D/cli/install.sh"
run "HEAD" curl -s -I -o /dev/null -w '%{http_code}\n' "$D/cli/install.sh"
run "served script holds no access code" sh -c "curl -fsS $D/cli/install.sh | grep -c -- '$CODE' || true"
run "shellcheck-free syntax check (sh -n)" sh -c "curl -fsS $D/cli/install.sh | sh -n && echo ok"

echo "== clean install into GRAIN_INSTALL_DIR"
rm -rf /tmp/grain-install-test
run "curl | GRAIN_INSTALL_DIR=/tmp/grain-install-test sh" sh -c "curl -fsSL $D/cli/install.sh | GRAIN_INSTALL_DIR=/tmp/grain-install-test sh"
run "mode" stat -c '%a %n' /tmp/grain-install-test/grain
run "grain version" /tmp/grain-install-test/grain version
run "no temp file left beside it" ls -A /tmp/grain-install-test
tarcalls

echo "== the plan's INSTALL_DIR= spelling (the script reads GRAIN_INSTALL_DIR)"
rm -rf /tmp/grain-install-test2 "$HOME/.local"
run "curl | INSTALL_DIR=/tmp/grain-install-test2 sh" sh -c "curl -fsSL $D/cli/install.sh | INSTALL_DIR=/tmp/grain-install-test2 sh"
run "INSTALL_DIR was ignored" sh -c "ls /tmp/grain-install-test2 2>&1; ls -l $HOME/.local/bin/grain"

echo "== reinstall over an existing binary (atomic rename)"
run "reinstall" sh -c "curl -fsSL $D/cli/install.sh | GRAIN_INSTALL_DIR=/tmp/grain-install-test sh"
run "mode after reinstall" stat -c '%a' /tmp/grain-install-test/grain

echo "== tampered archive: one byte flipped"
ASSET=grain-linux-$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/').tar.gz
cp "$REL/$ASSET" "$W/$ASSET.orig"
python3 - "$REL/$ASSET" <<'PY'
import sys
p = sys.argv[1]
b = bytearray(open(p, 'rb').read())
b[len(b) // 2] ^= 0xff
open(p, 'wb').write(bytes(b))
PY
rm -rf /tmp/grain-install-tamper; : >"$W/tar.calls"
run "curl | sh (tampered)" sh -c "curl -fsSL $D/cli/install.sh | GRAIN_INSTALL_DIR=/tmp/grain-install-tamper sh"
run "nothing installed" sh -c "ls /tmp/grain-install-tamper 2>&1 || true"
tarcalls

echo "== tampered archive with a matching forged checksum line removed for it"
cp "$REL/checksums.txt" "$W/checksums.orig"
grep -v " $ASSET\$" "$W/checksums.orig" >"$REL/checksums.txt"
cp "$W/$ASSET.orig" "$REL/$ASSET"
run "curl | sh (checksums.txt names no $ASSET)" sh -c "curl -fsSL $D/cli/install.sh | GRAIN_INSTALL_DIR=/tmp/grain-install-tamper sh"
tarcalls

echo "== checksums.txt missing"
rm -f "$REL/checksums.txt"
run "curl | sh (no checksums.txt)" sh -c "curl -fsSL $D/cli/install.sh | GRAIN_INSTALL_DIR=/tmp/grain-install-tamper sh"
run "nothing installed" sh -c "ls /tmp/grain-install-tamper 2>&1 || true"
tarcalls
cp "$W/checksums.orig" "$REL/checksums.txt"

echo "== archive missing (release not mirrored for this arch)"
mv "$REL/$ASSET" "$W/$ASSET.moved"
run "curl | sh (no archive)" sh -c "curl -fsSL $D/cli/install.sh | GRAIN_INSTALL_DIR=/tmp/grain-install-tamper sh"
mv "$W/$ASSET.moved" "$REL/$ASSET"

echo "== an archive that verifies but holds no grain binary"
mkdir -p "$W/evil" && echo 'echo pwned' >"$W/evil/notgrain" && "$REAL_TAR" -czf "$W/evil.tgz" -C "$W/evil" notgrain
mkdir -p "$WWW/releases/v0.0.0-evil" && cp "$W/evil.tgz" "$WWW/releases/v0.0.0-evil/$ASSET"
(cd "$WWW/releases/v0.0.0-evil" && sha256sum "$ASSET" >checksums.txt)
run "GRAIN_CLI_VERSION=v0.0.0-evil" sh -c "curl -fsSL $D/cli/install.sh | GRAIN_CLI_VERSION=v0.0.0-evil GRAIN_INSTALL_DIR=/tmp/grain-install-tamper sh"

echo "== restored mirror installs again"
run "curl | sh (restored)" sh -c "curl -fsSL $D/cli/install.sh | GRAIN_INSTALL_DIR=/tmp/grain-install-test sh && /tmp/grain-install-test/grain version"
