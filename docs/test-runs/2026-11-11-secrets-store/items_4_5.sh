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

# items_4_5.sh: §3 items 4 (retired keys) and 5 (export refusal and
# bundle import), on a copy of the walkthrough's data dir with no daemon on
# it. The key is unwrapped first so the copy needs no KMS.
set -uo pipefail
source "$(dirname "$0")/env.sh"
step() { printf '\n== %s\n' "$*"; }
D=$WORK/data45
G() { "$HEAD_BIN" "$@" 2>&1 | sed -E 's/grain-secret-pub-v1:[A-Za-z0-9+\/=]{20,}/<pub>/g; s/[0-9]{8}T[0-9]{6}(\.[0-9]+)?Z?/<time>/g'; echo "  [rc=${PIPESTATUS[0]}]"; }
stop_daemon
rm -rf "$D"; cp -a "$WORK/data" "$D"
"$HEAD_BIN" state -data-dir "$D" key unwrap >/dev/null 2>&1 || true
head -c 20 "$D/secrets/secrets.key"; echo

step "4a. two restores from other hosts, each followed by key import"
# sealed_to KEYFILE OUT: a secrets.enc encrypted to KEYFILE's key, as a
# restore from the host that key belongs to would bring.
sealed_to() {
	local tmp
	tmp=$(mktemp -d)
	mkdir -p "$tmp/secrets"
	cp "$1" "$tmp/secrets/secrets.key"
	printf 'restored' | "$HEAD_BIN" secrets -data-dir "$tmp" set walk-restored value >/dev/null 2>&1
	cp "$tmp/secrets/secrets.enc" "$2"
	rm -rf "$tmp"
}
mkkey() { (cd "$ROOT" && go run "$HERE/newkey.go") >"$1"; chmod 600 "$1"; }
mkkey "$WORK/values/k1"; mkkey "$WORK/values/k2"
sealed_to "$WORK/values/k1" "$WORK/values/enc-k1"
sealed_to "$WORK/values/k2" "$WORK/values/enc-k2"
G state -data-dir "$D" key import -key-file "$WORK/values/k1"
echo "(a key that does not open secrets.enc is refused, as designed)"
cp "$WORK/values/enc-k1" "$D/secrets/secrets.enc"
G state -data-dir "$D" key import -key-file "$WORK/values/k1"
sleep 1
cp "$WORK/values/enc-k2" "$D/secrets/secrets.enc"
G state -data-dir "$D" key import -key-file "$WORK/values/k2"
ls "$D/secrets" | grep secrets.key

step "4b. retired list (and state status)"
G state -data-dir "$D" key retired
G state -data-dir "$D" key retired list
G state -data-dir "$D" status | grep -i -A3 'retired\|replaced'

step "4c. delete refuses the live key and anything that is not an archive"
G state -data-dir "$D" key retired delete secrets.key
G state -data-dir "$D" key retired delete secrets.enc
G state -data-dir "$D" key retired delete ../secrets.key
G state -data-dir "$D" key retired delete

step "4d. restore a file sealed to k1 again: k1's archive is the one it is still sealed to"
cp "$WORK/values/enc-k1" "$D/secrets/secrets.enc"
mapfile -t archives < <(ls "$D/secrets" | grep 'secrets.key.replaced-' | sort)
k1_archive=""
for a in "${archives[@]}"; do cmp -s <(tr -d '[:space:]' <"$D/secrets/$a") <(tr -d '[:space:]' <"$WORK/values/k1") && k1_archive=$a; done
echo "k1 is archived as ${k1_archive:-<none>}"
G state -data-dir "$D" key retired delete "$k1_archive"
G state -data-dir "$D" key retired delete -all
ls "$D/secrets" | grep secrets.key

step "4e. back on a file the live key opens, -all deletes the rest"
cp "$WORK/values/enc-k2" "$D/secrets/secrets.enc"
G state -data-dir "$D" key retired delete -all
G state -data-dir "$D" key retired list
"$WORK/bin/storedigest" "$D" walk-restored/value | cut -c1-40

# Items 5 export the untouched copy.
rm -rf "$D"; cp -a "$WORK/data" "$D"
"$HEAD_BIN" state -data-dir "$D" key unwrap >/dev/null 2>&1 || true

step "5a. export without encryption is refused"
G export -data-dir "$D" -out "$WORK/b-plain.tar.gz"
ls "$WORK"/b-plain* 2>/dev/null || echo "no bundle written"
step "5b. -plaintext"
G export -data-dir "$D" -out "$WORK/b-plain.tar.gz" -plaintext
step "5c. -passphrase-file"
head -c 18 /dev/urandom | base64 >"$WORK/values/pass"; chmod 600 "$WORK/values/pass"
G export -data-dir "$D" -out "$WORK/b-pass.enc" -passphrase-file "$WORK/values/pass"
step "5d. -key-file"
openssl rand -hex 32 >"$WORK/values/bundlekey"; chmod 600 "$WORK/values/bundlekey"
G export -data-dir "$D" -out "$WORK/b-key.enc" -key-file "$WORK/values/bundlekey"
ls -l "$WORK"/b-* | awk '{print $1, $5, $NF}'
for b in "plain.tar.gz:" "pass.enc:-passphrase-file $WORK/values/pass" "key.enc:-key-file $WORK/values/bundlekey"; do
	f=${b%%:*}; flags=${b#*:}
	step "5e. import b-$f"
	rm -rf "$WORK/imp"
	# shellcheck disable=SC2086
	G import -data-dir "$WORK/imp" -in "$WORK/b-$f" $flags | tail -4
	"$WORK/bin/storedigest" "$WORK/imp" github-credential-bot/token package-cache-ca/bundle.pem | cut -c1-60
done
step "5f. wrong passphrase"
echo wrong >"$WORK/values/wrongpass"
rm -rf "$WORK/imp"; G import -data-dir "$WORK/imp" -in "$WORK/b-pass.enc" -passphrase-file "$WORK/values/wrongpass" | tail -2
