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

# ci-apt-install.sh installs Debian packages on a GitHub Actions runner,
# riding out a flaky archive.
#
# Usage: scripts/ci-apt-install.sh <package>...
#
# A bare `apt-get update && apt-get install` fails the whole job on one
# DNS blip: on 2026-11-11 runners could not resolve security.ubuntu.com
# for over an hour, and every go, go unit, go orchestrator, go e2e and
# real vm job on every branch failed in its install step before running
# a test. So this installs only what the runner image lacks (a package
# already there needs no archive at all), asks apt to retry each fetch,
# and retries the update-and-install pair with a growing pause before
# giving up.
set -euo pipefail

if [ "$#" -eq 0 ]; then
  echo "usage: $0 <package>..." >&2
  exit 2
fi

# Third-party lists the runner image carries and these jobs never use;
# an outage of theirs must not fail apt-get update either.
sudo rm -f /etc/apt/sources.list.d/google-chrome.list /etc/apt/sources.list.d/microsoft-prod.list

missing=()
for pkg in "$@"; do
  if ! dpkg-query -W -f='${Status}' "$pkg" 2>/dev/null | grep -q "install ok installed"; then
    missing+=("$pkg")
  fi
done
if [ "${#missing[@]}" -eq 0 ]; then
  echo "ci-apt-install: already installed: $*"
  exit 0
fi

attempts=5
for attempt in $(seq 1 "$attempts"); do
  if sudo apt-get -o Acquire::Retries=3 update -qq &&
    sudo DEBIAN_FRONTEND=noninteractive apt-get -o Acquire::Retries=3 install -y --no-install-recommends "${missing[@]}"; then
    exit 0
  fi
  if [ "$attempt" -lt "$attempts" ]; then
    pause=$((attempt * 20))
    echo "ci-apt-install: attempt $attempt of $attempts failed; retrying in ${pause}s" >&2
    sleep "$pause"
  fi
done
echo "ci-apt-install: could not install ${missing[*]} after $attempts attempts" >&2
exit 1
