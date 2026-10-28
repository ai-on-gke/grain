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

# The rollout mechanism. Runs forever as a systemd service.
#
# Mirrors v1's own config-sync mechanism exactly:
# Terraform writes a `grain-deploy-generation` attribute into instance
# metadata, and this service hangs on the metadata server's
# wait_for_change endpoint, re-fetching and running deploy.sh from
# metadata every time that value changes. No inbound SSH, no runner with
# credentials, no agent polling GitHub -- just `terraform apply` (or a
# metadata edit) changing one value the host is already watching.
#
# A failed deploy is retried on the next wake-up, so a transient failure
# (an apt mirror, a Docker pull, GitHub, a secret not pushed yet)
# self-heals without a second apply.
set -euo pipefail

readonly MD="http://metadata.google.internal/computeMetadata/v1"
readonly SELF="/opt/grain-deploy/config-sync.sh"
readonly DEPLOY="/opt/grain-deploy/deploy.sh"
readonly STATE="/var/lib/grain/data/.deploy-state"
readonly WAIT_SECS=300
# Generous, though less of it is spent on grain itself than it used to
# be: the deploy no longer builds a binary here at all -- it pulls the
# image CI published (bwsalmon/agents#645) -- but a granule deployment's
# first run still builds its own guest image with debootstrap against a
# real Debian mirror (scripts/setup.sh's ensure_granule_images_build),
# which is minutes, and a first pull of a several-hundred-megabyte image
# on a slow link is not free either.
readonly DEPLOY_TIMEOUT_SECS=2700

log() { echo "grain-config-sync: $*"; }

md() { curl -fsS -H "Metadata-Flavor: Google" "$MD/$1"; }

# Guest attributes are readable from outside with
# `gcloud compute instances get-guest-attributes`, which is how CI (or
# an operator) watches a rollout land. Never put anything sensitive here.
guest_attr() {
  curl -fsS -X PUT --data "$2" -H "Metadata-Flavor: Google" \
    "$MD/instance/guest-attributes/grain/$1" >/dev/null 2>&1 || true
}

# Replace ourselves if the metadata copy has changed, so an edit to this
# file rolls out like anything else. systemd's Restart=always brings the
# new one straight back up.
self_update() {
  local new
  new="$(mktemp)"
  if md instance/attributes/grain-config-sync-script > "$new" && [ -s "$new" ] \
     && ! cmp -s "$new" "$SELF"; then
    log "config-sync.sh changed in metadata; installing and restarting"
    install -m 0700 "$new" "$SELF"
    rm -f "$new"
    exit 0
  fi
  rm -f "$new"
}

# Fetch the current generation. With an etag, block until it differs or
# WAIT_SECS elapse -- so the common case is a long-lived hanging GET, not
# a poll.
GEN=""
ETAG=""
fetch_generation() {
  local url="$MD/instance/attributes/grain-deploy-generation"
  local hdr body
  if [ -n "$ETAG" ]; then
    url="$url?wait_for_change=true&timeout_sec=$WAIT_SECS&last_etag=$ETAG"
  fi
  hdr="$(mktemp)"; body="$(mktemp)"
  if curl -fsS --max-time $((WAIT_SECS + 30)) -D "$hdr" -o "$body" \
       -H "Metadata-Flavor: Google" "$url"; then
    GEN="$(cat "$body")"
    ETAG="$(grep -i '^etag:' "$hdr" | tail -1 | awk '{print $2}' | tr -d '\r')"
  else
    log "metadata fetch failed; retrying shortly"
    sleep 10
  fi
  rm -f "$hdr" "$body"
}

run_deploy() {
  local generation="$1" new rc
  new="$(mktemp)"
  if ! md instance/attributes/grain-deploy-script > "$new" || [ ! -s "$new" ]; then
    log "could not fetch deploy script from metadata"
    rm -f "$new"
    return 1
  fi
  install -m 0700 "$new" "$DEPLOY"
  rm -f "$new"

  log "deploying generation $generation (timeout ${DEPLOY_TIMEOUT_SECS}s)"
  guest_attr deploy-status "running"
  guest_attr deploy-generation "$generation"

  # Tee'd rather than run bare so a failure can publish its own last
  # words. "exit=127 generation=..." alone says a command was not found
  # but never which one, and reading the journal that does say needs SSH
  # to the host -- which is exactly what an operator locked out by OS
  # Login, or debugging from CI, does not have. The tail goes into a
  # guest attribute, which is readable with
  # `gcloud compute instances get-guest-attributes` and no shell at all.
  # tee's own stdout is this service's stdout, which systemd already
  # wires to the journal -- so the output streams there live and lands in
  # $out at the same time, with no second destination to name.
  #
  # It named one before: `tee /dev/stderr > "$out"`. Under systemd stderr
  # is a socket, and /dev/stderr (a symlink to /proc/self/fd/2) cannot be
  # reopened for writing on one -- tee died with "No such device or
  # address" and took the deploy with it through the broken pipe, so the
  # line added to explain failures was causing them.
  #
  # The status is taken in the `||` branch, not from a PIPESTATUS read on
  # a following line. `pipeline || true` runs `true`, and `true` is itself
  # a pipeline, so it resets PIPESTATUS -- a later ${PIPESTATUS[0]} reads
  # 0 no matter how the deploy exited, which reported every failed
  # rollout as converged.
  local out rc=0
  out="$(mktemp)"
  timeout --signal=TERM --kill-after=60 "$DEPLOY_TIMEOUT_SECS" "$DEPLOY" 2>&1 \
    | tee "$out" || rc="${PIPESTATUS[0]}"

  if [ "$rc" -eq 0 ]; then
    log "generation $generation deployed"
    echo "ok $generation" > "$STATE"
    guest_attr deploy-status "ok"
  else
    log "generation $generation FAILED (exit $rc); will retry"
    echo "failed $generation" > "$STATE"
    guest_attr deploy-status "failed"
    # Bounded hard: a guest attribute is a small value, and this is a
    # pointer at the failure, not a log shipper. The journal remains the
    # full record.
    guest_attr deploy-tail "$(tail -c 1200 "$out" | tr -d '\000')"
  fi
  guest_attr deploy-detail "exit=$rc generation=$generation"
  rm -f "$out"
  return "$rc"
}

main() {
  mkdir -p "$(dirname "$STATE")" 2>/dev/null || true
  log "started; watching grain-deploy-generation"

  while true; do
    self_update
    fetch_generation
    [ -n "$GEN" ] || continue

    local last_status="" last_gen=""
    if [ -f "$STATE" ]; then
      read -r last_status last_gen < "$STATE" || true
    fi

    if [ "$GEN" != "$last_gen" ] || [ "$last_status" != "ok" ]; then
      run_deploy "$GEN" || true
    fi
  done
}

main "$@"
