#!/bin/sh
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

# Installs the grain CLI on a workstation, served by a grain daemon as
# GET /cli/install.sh (design/spec/external-cli/external-cli.spec.md#XC-32):
#
#   curl -fsSL https://grain.example.com/cli/install.sh | sh
#
# It downloads the release archive of the daemon's own version for this
# machine (grain-<os>-<arch>.tar.gz, as scripts/build-cli.sh writes it),
# checks it against the release's checksums.txt before extracting
# anything, and installs grain to ${GRAIN_INSTALL_DIR:-$HOME/.local/bin}.
#
# Environment:
#   GRAIN_INSTALL_DIR      where grain goes (default $HOME/.local/bin)
#   GRAIN_CLI_VERSION      a release other than the daemon's own
#   GRAIN_CLI_RELEASE_URL  the releases base URL, archives at
#                          <url>/<version>/grain-<os>-<arch>.tar.gz
set -eu

# Filled in by the daemon serving this script.
GRAIN_DAEMON_RELEASE='@GRAIN_CLI_RELEASE@'
GRAIN_DAEMON_RELEASE_URL='@GRAIN_CLI_RELEASE_URL@'

die() {
  echo "grain install: $*" >&2
  exit 1
}

version="${GRAIN_CLI_VERSION:-$GRAIN_DAEMON_RELEASE}"
base="${GRAIN_CLI_RELEASE_URL:-$GRAIN_DAEMON_RELEASE_URL}"
dir="${GRAIN_INSTALL_DIR:-${HOME:?HOME is not set}/.local/bin}"

[ -n "$version" ] || die "the daemon is an unversioned build and names no release to install; set GRAIN_CLI_VERSION=vX.Y.Z"
[ -n "$base" ] || die "no release URL; set GRAIN_CLI_RELEASE_URL"

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) die "unsupported OS $(uname -s): grain ships for Linux and macOS" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) die "unsupported architecture $(uname -m): grain ships for amd64 and arm64" ;;
esac

asset="grain-${os}-${arch}.tar.gz"
url="${base%/}/${version}"

fetch() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL -o "$2" "$1"
  elif command -v wget >/dev/null 2>&1; then
    wget -q -O "$2" "$1"
  else
    die "needs curl or wget"
  fi
}

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | cut -d' ' -f1
  else
    die "needs sha256sum or shasum to verify the download"
  fi
}

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "grain install: downloading grain ${version} for ${os}/${arch}"
fetch "${url}/${asset}" "${tmp}/${asset}" || die "could not download ${url}/${asset}"
fetch "${url}/checksums.txt" "${tmp}/checksums.txt" || die "could not download ${url}/checksums.txt"

want="$(awk -v a="$asset" '$2 == a || $2 == "*" a { print $1; exit }' "${tmp}/checksums.txt")"
[ -n "$want" ] || die "checksums.txt for ${version} names no ${asset}"
got="$(sha256 "${tmp}/${asset}")"
[ "$got" = "$want" ] || die "${asset} has sha256 ${got}, not the ${want} checksums.txt names: not installing it"

mkdir -p "${tmp}/x"
tar -xzf "${tmp}/${asset}" -C "${tmp}/x" grain || die "${asset} holds no grain binary"
mkdir -p "$dir"
cp "${tmp}/x/grain" "${dir}/.grain.$$"
chmod 0755 "${dir}/.grain.$$"
mv -f "${dir}/.grain.$$" "${dir}/grain"

echo "grain install: installed ${dir}/grain (${version})"
case ":${PATH}:" in
  *":${dir}:"*) ;;
  *) echo "grain install: ${dir} is not on your PATH; add it, e.g. export PATH=\"${dir}:\$PATH\"" ;;
esac
