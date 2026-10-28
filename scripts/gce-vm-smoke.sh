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

# gce-vm-smoke.sh -- prove that a GCP credential can actually run the GCE
# instance lifecycle: create a throwaway VM, SSH into it, delete it again.
#
# This is the "can we deploy at all" check for a credential, not a test of
# anything in grain. terraform/gcp stands a deployment up out of exactly
# these three verbs, and a `gcp-key` capability grant (pkg/capability/gcpkey)
# is supposed to buy a sandboxed agent the same. Both fail in ways that read
# as something else entirely when the credential is short a role -- see the
# three gotchas below, each of which cost a run of task 195 to diagnose -- so
# it is worth being able to answer "is the credential good?" in ninety
# seconds, on its own, before blaming terraform or the agent.
#
# Usage:
#   scripts/gce-vm-smoke.sh [--zone Z] [--network N] [--subnet S] [--tags T]
#                           [--service-account SA|default] [--iap] [--no-address] [--keep]
#
# It authenticates as whatever gcloud already has active. From inside a
# grain sandbox holding the gcp-key capability, that means first:
#
#   gcloud auth activate-service-account --key-file=~/.gcp-service-account.json
#   gcloud config set project <project>
#
# Activating a key does not select a project, and gcloud's complaint when
# you skip the second line ("The required property [project] is not
# currently set") says nothing about the key -- so it gets misread as the
# key being bad.

set -euo pipefail

ZONE="${ZONE:-us-central1-a}"
NETWORK="${NETWORK:-default}"
SUBNET="${SUBNET:-}"
TAGS="${TAGS:-}"
MACHINE_TYPE="${MACHINE_TYPE:-e2-micro}"
SERVICE_ACCOUNT="${SERVICE_ACCOUNT:-}"
USE_IAP=0
NO_ADDRESS=0
KEEP=0

while [[ $# -gt 0 ]]; do
  # Accept --flag=value as well as --flag value. gcloud's own flags take
  # both, so a caller who has just been typing gcloud commands reasonably
  # expects these to, and the failure otherwise is a bare usage line that
  # does not say which argument it disliked.
  case "$1" in
    --*=*) set -- "${1%%=*}" "${1#*=}" "${@:2}" ;;
  esac
  case "$1" in
    --zone)    ZONE="$2"; shift 2 ;;
    --network) NETWORK="$2"; shift 2 ;;
    # Required for any custom-subnet-mode network -- which is every network
    # terraform/gcp creates (network.tf) and every grain-* network in a real
    # deployment. Only the auto-mode `default` network can infer one, so
    # leaving this off elsewhere fails the create with
    # "Network interface must specify a subnet if the network resource is in
    # custom subnet mode", naming a field the caller never set.
    --subnet)  SUBNET="$2"; shift 2 ;;
    # Network tags for the VM, comma-separated, passed straight to
    # `instances create --tags`. What they are for here is --iap: a
    # firewall rule that admits IAP's range only to a target tag reaches
    # an untagged VM not at all, and this is the flag that makes the VM
    # match one. See --iap below for which tag.
    --tags)    TAGS="$2"; shift 2 ;;
    # Which identity the VM itself runs as. Unset -- the default -- means
    # none at all (`--no-service-account`, see the create step for why that
    # is what makes this work as a non-owner). `default` means "whatever
    # gcloud would attach on its own", the project's default compute
    # account and the shape a production workload has; anything else is
    # taken as an account to attach by name. Both of those need
    # roles/iam.serviceAccountUser **on the named account**, so they answer
    # a different and larger question than the default does -- see
    # .github/workflows/gcp-smoke.yml, which exposes exactly this choice.
    --service-account) SERVICE_ACCOUNT="$2"; shift 2 ;;
    # Reach the VM over IAP TCP forwarding rather than an external IP. This
    # is the shape terraform/gcp actually deploys (network.tf gives the host
    # no external IP at all), so it is the leg worth checking before
    # trusting a credential with a real deployment. It needs more of the
    # project set up in advance than the default path does: the caller wants
    # roles/iap.tunnelResourceAccessor, and the network needs an ingress
    # rule allowing tcp:22 from 35.235.240.0/20, IAP's own range.
    #
    # That rule is usually scoped to a target tag rather than to the whole
    # network -- it is in every network terraform/gcp builds, where
    # network.tf's agent_iap_ssh covers the `<name_prefix>-agent-vm` tag
    # (`grain-agent-vm` for a default deployment, `grain-main-agent-vm`
    # for the one named grain-main) and nothing else. A VM created without
    # `--tags` then matches no rule at all and is unreachable however
    # correct the credential, and IAP says so only as
    # [4003: 'failed to connect to backend'], which is also what it says
    # about a VM whose sshd is merely not up yet. So pass --tags with
    # --iap on any network but `default`; the failure path below prints
    # the tags this network's own rules ask for.
    --iap)     USE_IAP=1; shift ;;
    # Do not assign an external IP to the VM. Required under organization
    # policies that restrict external IPs (such as
    # constraints/compute.vmExternalIpAccess) and implied by --iap.
    --no-address) NO_ADDRESS=1; shift ;;
    # Leave the VM running -- for poking at a failure by hand. It is then
    # yours to delete; the command is printed at the end.
    --keep)    KEEP=1; shift ;;
    *) echo "usage: $0 [--zone Z] [--network N] [--subnet S] [--tags T] [--service-account SA|default] [--iap] [--no-address] [--keep]" >&2; exit 2 ;;
  esac
done

PROJECT="$(gcloud config get-value project 2>/dev/null)"
if [[ -z "$PROJECT" || "$PROJECT" == "(unset)" ]]; then
  echo "no project set: gcloud config set project <project>" >&2
  exit 2
fi

VM="gce-smoke-$(date +%s)-$$"

step() { printf '\n=== %s ===\n' "$*"; }

# A leaked e2-micro is a slow, silent bill, and every path out of this
# script that is not --keep goes through here -- including set -e's, and
# including a Ctrl-C halfway through the SSH retry loop.
cleanup() {
  local rc=$?
  if [[ $KEEP -eq 1 ]]; then
    echo
    echo "--keep: VM $VM left running in $ZONE. Delete it with:"
    echo "  gcloud compute instances delete $VM --zone=$ZONE --quiet"
    return $rc
  fi
  if gcloud compute instances describe "$VM" --zone="$ZONE" >/dev/null 2>&1; then
    step "delete $VM"
    gcloud compute instances delete "$VM" --zone="$ZONE" --quiet
  fi
  return $rc
}
trap cleanup EXIT

CREATE_ARGS=(--network="$NETWORK")
[[ -n "$SUBNET" ]] && CREATE_ARGS+=(--subnet="$SUBNET")
[[ -n "$TAGS" ]] && CREATE_ARGS+=(--tags="$TAGS")
[[ $USE_IAP -eq 1 || $NO_ADDRESS -eq 1 ]] && CREATE_ARGS+=(--no-address)

# --no-service-account --no-scopes is not an optimisation, it is what makes
# this work as a non-owner. gcloud otherwise attaches the project's default
# compute service account to every VM it creates, and attaching *any*
# service account requires roles/iam.serviceAccountUser **on that account**
# -- a permission on the SA, which a project-level compute role does not
# include. Without it the create fails with
#
#   The user does not have access to service account
#   '<num>-compute@developer.gserviceaccount.com'
#
# which names an account the caller never asked for and reads like the
# credential itself is wrong. A smoke-test VM calls no Google API from
# inside, so it needs no identity of its own; declining one sidesteps the
# grant entirely.
#
# --service-account is how a caller asks for the larger question anyway:
# `default` leaves gcloud to attach the account it would have, and a name
# attaches that one. Either needs the actAs grant above, which is the
# point of asking.
if [[ -z "$SERVICE_ACCOUNT" ]]; then
  CREATE_ARGS+=(--no-service-account --no-scopes)
elif [[ "$SERVICE_ACCOUNT" != "default" ]]; then
  CREATE_ARGS+=(--service-account="$SERVICE_ACCOUNT")
fi

step "create $VM ($MACHINE_TYPE, $NETWORK${SUBNET:+/$SUBNET}, $ZONE, tagged ${TAGS:-nothing}, running as ${SERVICE_ACCOUNT:-no service account})"
gcloud compute instances create "$VM" \
  --zone="$ZONE" \
  --machine-type="$MACHINE_TYPE" \
  --image-family=debian-12 --image-project=debian-cloud \
  --boot-disk-size=10GB \
  "${CREATE_ARGS[@]}" \
  --labels=purpose=gce-vm-smoke

SSH_ARGS=(--zone="$ZONE" --quiet)
[[ $USE_IAP -eq 1 ]] && SSH_ARGS+=(--tunnel-through-iap)

step "ssh into $VM$([[ $USE_IAP -eq 1 ]] && echo ' (via IAP)')"
# Two things make the first attempts here noisy without anything being wrong,
# and both are worth expecting rather than debugging:
#
#   "Updating project ssh metadata... failed." -- gcloud publishes the
#   generated key to the *project's* common instance metadata first, which
#   needs compute.projects.setCommonInstanceMetadata. A project-level
#   compute admin role does not carry it. gcloud then falls back to the
#   instance's own metadata on its own, which does work, and which is the
#   better outcome anyway: the key is scoped to this one VM and is destroyed
#   with it instead of accumulating on the project.
#
#   "Connection refused" -- the API reports RUNNING as soon as the VM is
#   scheduled, well before the guest has booted far enough to start sshd.
#   There is nothing to poll for that means "sshd is listening", so retry.
#   Under --iap the same "too early" shows up as a wall of Python traceback
#   ending in [4003: 'failed to connect to backend'], which reads like a
#   missing IAP grant or firewall rule but clears on its own once sshd is
#   up. Judge either only by whether the loop below ever succeeds.
ssh_ok=0
for attempt in 1 2 3 4 5 6; do
  echo "--- attempt $attempt ---"
  if gcloud compute ssh "$VM" "${SSH_ARGS[@]}" \
      --command='echo SSH_OK; hostname; uptime' \
      -- -o StrictHostKeyChecking=no -o ConnectTimeout=15; then
    ssh_ok=1
    break
  fi
  sleep 15
done

# What this network's own firewall rules ask of a VM before IAP can reach
# port 22 on it, printed after the retries have run out -- which is the
# one moment it explains anything.
#
# [4003: 'failed to connect to backend'] is all IAP ever says, whether the
# guest is still booting, the tunnel grant is missing, or the packets are
# being dropped by a rule scoped to a tag this VM does not carry. The
# first clears on its own, the second names a role, and the third names
# nothing at all -- so print the rules and let the operator see which tag
# they are short. `firewall-rules list` needs compute.firewalls.list; if
# the credential under test lacks it, say nothing rather than turn a
# diagnosis into a second error.
iap_firewall_hint() {
  local rules
  rules="$(gcloud compute firewall-rules list --format="value[separator=' | '](
      network.basename(),name,direction,disabled,sourceRanges.list(),
      allowed[].map().firewall_rule().list(),targetTags.list())" 2>/dev/null)" || return 0

  # Only INGRESS rules on this network, from IAP's range (or from
  # anywhere), that actually cover port 22 -- a network's tunnel-to-UI
  # rule comes from the same range on a different port, and naming its
  # tag here would send the operator after the wrong one.
  local matching
  matching="$(awk -F ' \\| ' -v net="$NETWORK" '
    function admits_iap(ranges,   n, i, r) {
      n = split(ranges, r, ",")
      for (i = 1; i <= n; i++)
        if (r[i] == "35.235.240.0/20" || r[i] == "0.0.0.0/0") return 1
      return 0
    }
    function reaches_ssh(allowed,   n, i, a, lo_hi) {
      n = split(allowed, a, ",")
      for (i = 1; i <= n; i++) {
        if (a[i] == "tcp" || a[i] == "tcp:22") return 1
        if (a[i] ~ /^tcp:[0-9]+-[0-9]+$/) {
          split(substr(a[i], 5), lo_hi, "-")
          if (lo_hi[1] + 0 <= 22 && 22 <= lo_hi[2] + 0) return 1
        }
      }
      return 0
    }
    $1 == net && $3 == "INGRESS" && $4 == "False" && admits_iap($5) && reaches_ssh($6) {
      printf "  %s: %s from %s, target tags: %s\n", $2, $6, $5, ($7 == "" ? "(none -- every instance)" : $7)
    }' <<<"$rules")"

  echo >&2
  if [[ -z "$matching" ]]; then
    echo "No ingress rule on network $NETWORK admits tcp:22 from 35.235.240.0/20, IAP's own range, so no VM here is reachable over --iap until one exists." >&2
    return 0
  fi
  echo "Ingress rules on network $NETWORK that let IAP reach port 22:" >&2
  echo "$matching" >&2
  if [[ -z "$TAGS" ]]; then
    echo "This VM was created with no network tags, so only a rule with no target tags applies to it. If every rule above is tag-scoped, that is the failure: re-run with --tags=<tag> naming one of them." >&2
  else
    echo "This VM carries tags: $TAGS. A tag-scoped rule above reaches it only if its target tags include one of those." >&2
  fi
  echo "A VM already running can be tagged in place, which is quicker than another create:" >&2
  echo "  gcloud compute instances add-tags $VM --zone=$ZONE --tags=<tag>" >&2
}

if [[ $ssh_ok -ne 1 ]]; then
  echo "ssh never succeeded after 6 attempts" >&2
  if [[ $USE_IAP -eq 1 ]]; then iap_firewall_hint; fi
  # The EXIT trap still deletes the VM unless --keep was passed -- so the
  # add-tags above is something to run under --keep, or on the next run.
  exit 1
fi

# Deletion is the third verb under test, so run it here where a failure is
# reported as one, rather than leaving it to the trap's best-effort cleanup.
if [[ $KEEP -eq 0 ]]; then
  step "delete $VM"
  gcloud compute instances delete "$VM" --zone="$ZONE" --quiet
  # The boot disk goes with the instance (auto-delete is on by default for a
  # disk created inline like this one). Confirm it, because a leaked disk
  # bills like a leaked VM and is easier to miss.
  if gcloud compute disks describe "$VM" --zone="$ZONE" >/dev/null 2>&1; then
    echo "boot disk $VM outlived the instance -- delete it by hand" >&2
    exit 1
  fi
fi

# Say only what actually ran: under --keep the delete is skipped, and
# claiming it passed would make the one flag that leaves a VM behind the
# one that reports the cleanest.
printf '\nOK: create, ssh%s%s as %s in %s.\n' \
  "$([[ $USE_IAP -eq 1 ]] && echo ' (IAP)')" \
  "$([[ $KEEP -eq 1 ]] && echo ' succeeded; delete skipped (--keep)' || echo ' and delete all succeeded')" \
  "$(gcloud config get-value account 2>/dev/null)" "$PROJECT"
