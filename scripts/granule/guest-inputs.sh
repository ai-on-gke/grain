#!/bin/bash
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

# Prints the paths, relative to the repository root, that the sandbox
# guest build reads -- which is to say everything in this repository that
# decides what comes out of it.
#
# It exists because the publishers cache both halves of that build on
# the content of their inputs -- scripts/release-images.sh, which is what
# .grain/workflows/release.yml publishes the guest with on every merge
# that releases, what .grain/workflows/image.yml's `guest-cache` job
# runs on demand for a branch that changes one of these paths and after
# a merge when grain dispatches that job on the base branch
# (model.EffectivePostMergeDispatchJobs), and
# .github/workflows/build-artifacts.yml, which did it alone until this
# account's Actions stopped running: a git tree hash over these paths
# names the image they produce, so a commit that changes none of them can
# pull the image an earlier commit already built instead of spending ~9
# minutes producing a byte-for-byte equivalent one.
#
# A path left off here is a change that does not invalidate the cache,
# and the way that fails is a deployment running a guest which is missing
# something its own commit added. So `third_party/granule` is on both
# lists: every image the guest build produces is packed onto the granule
# image scripts/granule/build-granule-image.sh builds out of that tree
# (`FROM <granule image>` plus its guest-parts, see that script), so a
# vendored resync changes what comes out of this build as surely as a
# Granulefile does -- and the granule doing the packing and the guest-side
# pieces it lays down have to come from one commit.
#
# The guest's /usr/local/bin/grain (cmd/grain-sandbox) is the one
# deliberate exception, and only in part: its own code is named below
# (cmd/grain-sandbox, pkg/sandboxcli, pkg/hostscriptcli), the rest of the
# packages it imports -- gitproxy, mcp, ci, model, about thirty that
# nearly every merge touches -- are not. Keyed on all of them, the guest
# would rebuild on almost half of all merges for a binary no run uses as
# built: sandbox setup places the daemon's own build of it over this one
# in every sandbox (pkg/orchestrator/sandbox_grain.go), so what a cached
# guest carries there is only ever a fallback.
#
# Usage:
#   ./guest-inputs.sh          # what the guest build reads
#   ./guest-inputs.sh base     # what base build alone reads
set -euo pipefail

case "${1:-guest}" in
  base)
    printf '%s\n' \
      Granulefile.guest-base \
      scripts/granule/build-granule-image.sh \
      third_party/granule \
      cmd/grain-connect/main.go \
      cmd/grain-sandbox \
      pkg/hostscriptcli \
      pkg/sandboxcli
    ;;
  guest)
    printf '%s\n' \
      Granulefile.guest-base \
      Granulefile \
      scripts/granule/build-granule-image.sh \
      third_party/granule \
      cmd/grain-connect/main.go \
      cmd/grain-sandbox \
      pkg/hostscriptcli \
      pkg/sandboxcli \
      go.mod \
      go.sum \
      ui/package.json \
      ui/package-lock.json
    ;;
  *)
    echo "guest-inputs.sh: usage: guest-inputs.sh [base|guest]" >&2
    exit 1
    ;;
esac
