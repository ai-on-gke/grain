#!/usr/bin/env bash
# Block until the deployed host reports it has converged on this
# deploy generation.
#
# The host publishes progress through guest attributes
# (terraform/gcp/files/config-sync.sh), so CI fails when the rollout
# fails rather than when `terraform apply` returns -- applying the
# Terraform is only the start of a deploy, and the interesting half
# happens on the VM afterwards.
#
# The guest attribute namespace is "grain". Guest attributes are
# per-instance and this reads the instance it is given, so two grain
# deployments in one project never read each other's status.
#
# Required env:
#   PROJECT, INSTANCE, ZONE   from the Terraform outputs
#   DEPLOY_GENERATION         the token the host is expected to report
# Optional env:
#   TIMEOUT_MINUTES  give up after this long (default 45)
#   POLL_SECONDS     seconds between polls (default 20)
set -euo pipefail

project="${PROJECT:?PROJECT is not set: the terraform project_id output}"
instance="${INSTANCE:?INSTANCE is not set: the terraform instance_name output}"
zone="${ZONE:?ZONE is not set: the terraform zone output}"
deploy_generation="${DEPLOY_GENERATION:?DEPLOY_GENERATION is not set}"
timeout_minutes="${TIMEOUT_MINUTES:-45}"
poll_seconds="${POLL_SECONDS:-20}"

attr() {
  gcloud compute instances get-guest-attributes "$instance" \
    --project="$project" --zone="$zone" --query-path="grain/$1" \
    --format='value(value)' 2>/dev/null || true
}

log_hint() {
  echo "Read the host's own log with:"
  echo "  gcloud compute ssh $instance --zone $zone --project $project \\"
  echo "    --tunnel-through-iap --command 'sudo journalctl -u grain-config-sync -n 200'"
}

# terraform/gcp/instance.tf folds a short hash of grain_config's own
# content onto the end of the value it writes to grain-deploy-generation
# (bwsalmon/agents#592: "changing max concurrent agents takes no effect
# even after reboot"), so the host reports "$deploy_generation-<hash>",
# never the bare token CI passed to `terraform apply` as
# var.deploy_generation. A plain string comparison against that bare
# token therefore never matched, even once the host had actually
# converged -- every rollout after #592 landed ran out the clock here and
# was reported as hung/failed despite the host finishing within seconds
# (bwsalmon/agents#633).
generation_matches() {
  case "$1" in
    "$deploy_generation" | "$deploy_generation"-*) return 0 ;;
    *) return 1 ;;
  esac
}

# config-sync.sh retries a failed generation on its own -- see that
# script's own top comment, "self-heals without a second apply" -- so the
# first "failed" this sees is not yet a verdict: config-sync has already
# gone back to watching for the next wake-up and will re-run deploy.sh
# against this same generation. Bailing out here on that first sighting
# reported deploys as broken that the host went on to land on its own a
# few minutes later (bwsalmon/agents#554) -- a real deploy, doing exactly
# what config-sync.sh promises, still got reported as a hard failure.
#
# So this only gives up once the host's own retry has *also* failed:
# seen_failure remembers the first "failed", retried notices deploy-status
# move to "running" again afterward (config-sync starting that retry),
# and a second "failed" seen after that is what actually exits 1. A
# "failed" that never resolves and never gets retried (config-sync itself
# wedged, say) still surfaces through the timeout branch below, whose own
# comment already accounts for this.
deadline=$(( SECONDS + timeout_minutes * 60 ))
seen_failure=0
retried=0
while [ "$SECONDS" -lt "$deadline" ]; do
  status="$(attr deploy-status)"
  generation="$(attr deploy-generation)"
  if generation_matches "$generation"; then
    case "$status" in
      ok)
        echo "host converged on $deploy_generation"
        exit 0
        ;;
      running)
        if [ "$seen_failure" -eq 1 ]; then
          retried=1
        fi
        ;;
      failed)
        if [ "$seen_failure" -eq 0 ] || [ "$retried" -eq 0 ]; then
          if [ "$seen_failure" -eq 0 ]; then
            echo "host reported generation $deploy_generation failed once: $(attr deploy-detail)"
            echo "config-sync retries a failed generation on its own -- waiting for that retry rather than giving up immediately"
            tail="$(attr deploy-tail)"
            if [ -n "$tail" ]; then
              echo "--- the last of the host's own deploy output (may be superseded by a retry) ---"
              echo "$tail"
              echo "--- end ---"
            fi
          fi
          seen_failure=1
        else
          echo "::error::the host failed to converge on $deploy_generation, twice in a row: $(attr deploy-detail)"
          tail="$(attr deploy-tail)"
          if [ -n "$tail" ]; then
            echo "--- the last of the host's own deploy output ---"
            echo "$tail"
            echo "--- end ---"
          fi
          log_hint
          exit 1
        fi
        ;;
    esac
  fi
  echo "waiting: status=${status:-<none>} generation=${generation:-<none>} (want $deploy_generation)"
  sleep "$poll_seconds"
done

# A timeout is not the same as a failure: config-sync retries on its own
# roughly every five minutes, so a rollout that is merely slow (a first
# boot downloads a base image and builds the binary) may still land after
# this gives up.
echo "::error::timed out after ${timeout_minutes}m waiting for the host to converge on $deploy_generation."
echo "The host keeps retrying on its own; this step gave up, the rollout may not have."
log_hint
exit 1
