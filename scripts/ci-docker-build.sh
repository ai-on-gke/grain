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

# `docker build "$@"` from the repository root, on a host prepared the way
# scripts/release-images.sh prepares its own: Grain's CA staged into the
# build context as .grain-ca.crt (and removed again however the build
# ends), and in Grain CI the sandbox's MTU clamped and the package cache's
# CA installed. See scripts/grain-ci-host.sh for why each is there.
#
# For a job that builds an image of its own outside the release -- the
# nightly upgrade test's `grain-e2e:test` is the one:
#
#     bash scripts/ci-docker-build.sh -t grain-e2e:test .
set -euo pipefail
cd "$(dirname "$0")/.."
repo_root="$(pwd)"

# shellcheck source=scripts/grain-ci-host.sh
. "$repo_root/scripts/grain-ci-host.sh"
trap cleanup_ca EXIT INT TERM

ensure_transparent_cache
ensure_network_mtu
stage_grain_ca

docker build "$@"
