#!/usr/bin/env bash
# Move a deployment's secrets out of its host's instance metadata and into
# the Secret Manager secrets secrets.tf creates for them (#2376), then
# remove them from the instance. Run once per deployment that predates
# secrets.tf; safe to run again, and a no-op on an instance that carries
# none of them.
#
# Needs the secrets to exist already -- so it runs between an apply
# targeted at them and the full one:
#
#   terraform apply -var-file=... \
#     -target=google_secret_manager_secret_iam_member.host_reads
#   PROJECT=... INSTANCE=... ZONE=... ./deploy/migrate-secrets.sh
#   terraform apply -var-file=...
#
# deploy/terraform-apply.sh does exactly that on its own whenever the
# instance still carries one of these attributes, so a deployment applied
# from CI migrates on its first deploy with no workflow change. The full
# apply removes any attribute still left on the instance either way --
# instance.tf no longer ignores them -- so this is what keeps a value
# nobody has a copy of elsewhere (a GitHub App key pushed once by hand,
# a secrets key) from going with it.
#
# A secret that already has a version in Secret Manager keeps it: that is
# a push-secrets.sh run newer than anything in metadata. Its metadata
# copy is removed all the same.
#
# Values never touch an argument list or the terminal: they go from
# `gcloud compute instances describe` through jq to `gcloud secrets
# versions add` on stdin.
#
# Usage: migrate-secrets.sh [--pending]
#   --pending   change nothing; exit 0 if the instance still carries one of
#               these attributes and 1 if not (or if it cannot be read) --
#               how terraform-apply.sh decides whether to run the rest
#
# Required env:
#   PROJECT, INSTANCE, ZONE   terraform output project_id/instance_name/zone
# Optional env:
#   NAME_PREFIX               defaults to INSTANCE without its "-host" suffix
set -euo pipefail

project="${PROJECT:?PROJECT is not set: the terraform project_id output}"
instance="${INSTANCE:?INSTANCE is not set: the terraform instance_name output}"
zone="${ZONE:?ZONE is not set: the terraform zone output}"
name_prefix="${NAME_PREFIX:-${instance%-host}}"
pending_only=0
[ "${1:-}" = "--pending" ] && pending_only=1

# metadata attribute -> secrets.tf key. Every name here is also one
# deploy/terraform-apply.sh's redact covers, since the apply that drops
# an attribute this missed prints its value.
legacy_keys=(
  "grain-github-token=github-token"
  "grain-github-app-id=github-app-id"
  "grain-github-app-installation-id=github-app-installation-id"
  "grain-github-app-private-key=github-app-private-key"
  "grain-gemini-api-key=gemini-api-key"
  "grain-claude-oauth-token=claude-oauth-token"
  "grain-openai-api-key=openai-api-key"
  "grain-secrets-key=secrets-key"
  "grain-image-pull-token=image-pull-token"
  "grain-gcp-minter-key=gcp-minter-key"
)

umask 077
metadata="$(mktemp)"
trap 'shred -u "$metadata" 2>/dev/null || rm -f "$metadata"' EXIT
if ! gcloud compute instances describe "$instance" --project="$project" --zone="$zone" \
     --format=json > "$metadata"; then
  echo "could not read $instance's metadata" >&2
  exit 1
fi

value_of() {
  jq -j --arg k "$1" '[.metadata.items[]? | select(.key == $k) | .value][0] // empty' "$metadata"
}
has_key() {
  jq -e --arg k "$1" 'any(.metadata.items[]?; .key == $k)' "$metadata" >/dev/null
}

if [ "$pending_only" -eq 1 ]; then
  for pair in "${legacy_keys[@]}"; do
    has_key "${pair%%=*}" && exit 0
  done
  exit 1
fi

remove=()
for pair in "${legacy_keys[@]}"; do
  attr="${pair%%=*}"
  secret="$name_prefix-${pair#*=}"
  has_key "$attr" || continue

  # Never overwrite: a version already here was pushed after this
  # attribute was, by a push-secrets.sh that no longer writes metadata.
  if [ -n "$(gcloud secrets versions list "$secret" --project="$project" \
               --filter='state=ENABLED' --limit=1 --format='value(name)')" ]; then
    echo "$attr: $secret already has a version; keeping it"
  elif [ -z "$(value_of "$attr")" ]; then
    echo "$attr: empty; nothing to copy"
  else
    value_of "$attr" | gcloud secrets versions add "$secret" \
      --project="$project" --data-file=- >/dev/null
    echo "$attr: copied into $secret"
  fi
  remove+=("$attr")
done

if [ "${#remove[@]}" -eq 0 ]; then
  echo "no secret attributes on $instance; nothing to migrate"
  exit 0
fi

# Only once every copy above has succeeded -- set -e has already stopped
# this script otherwise, with every attribute still in place.
gcloud compute instances remove-metadata "$instance" --project="$project" --zone="$zone" \
  --keys="$(IFS=,; echo "${remove[*]}")" >/dev/null
echo "removed from $instance's metadata: ${remove[*]}"
