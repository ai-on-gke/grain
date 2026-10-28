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

# Push this deployment's secrets into Secret Manager, and mint a fresh
# minter service account key for this run.
#
# The only place a secret value is ever handled outside the host: each
# one becomes a new version of the secret secrets.tf created for it,
# "<name_prefix>-<key>", and every older version is destroyed, so a
# push is still a plain overwrite. Never through Terraform -- Terraform
# creates the secrets and their IAM, never a version -- so none of it is
# in the state file. The host reads each one back with its own service
# account's token (files/deploy.sh's secret), which is the only
# identity granted roles/secretmanager.secretAccessor on them.
#
# These used to be pushed into the instance's own metadata instead,
# where anything reaching the metadata server could read them with no
# credential at all (#2376). deploy/migrate-secrets.sh moves a
# deployment off that; this script never writes metadata.
#
# Run this once by hand after the first `terraform apply` (its outputs
# name PROJECT/INSTANCE/ZONE/MINTER_SERVICE_ACCOUNT for you), and again
# whenever a secret changes -- each run is a plain overwrite, safe to
# repeat.
#
# Secret values arrive as environment variables, deliberately never a
# command-line argument, where they would sit in a process list or shell
# history: the same rule pkg/secrets's own `grain secrets set`
# follows for -value-file/stdin.
#
# Required env:
#   PROJECT                            terraform output project_id
#   NAME_PREFIX                        the deployment's name_prefix -- or INSTANCE
#                                      (terraform output instance_name), which is
#                                      "<name_prefix>-host" and is what an existing
#                                      deploy workflow already passes; one of the two
# Optional env:
#   ZONE                               accepted and ignored: nothing here is per-instance
#                                      any more
# Optional env (empty leaves the host's existing copy untouched):
#   GRAIN_GITHUB_TOKEN                 the scoped PAT for this deployment's test_repos
#   GRAIN_GITHUB_APP_ID                 a GitHub App's own ID, together with
#   GRAIN_GITHUB_APP_INSTALLATION_ID    its installation ID on test_repos, and
#   GRAIN_GITHUB_APP_PRIVATE_KEY        its downloaded PEM private key -- an
#                                      alternative to GRAIN_GITHUB_TOKEN, stored under the
#                                      same credential_name, that pkg/gitproxy.CredentialSet
#                                      mints a refreshing installation token from instead of
#                                      reading as a bare PAT -- unlike a fine-grained PAT, it
#                                      can read the Checks API, which is what auto-merge
#                                      needs from a non-Actions CI provider. See this
#                                      directory's README, "There is no Checks permission to
#                                      grant". Registering the App itself is still a manual,
#                                      browser-based step on GitHub's own side; only pushing
#                                      the resulting three values here is automated. Set all
#                                      three together, or none -- scripts/setup.sh ignores
#                                      a partial set
#   GRAIN_GEMINI_API_KEY               the daemon's own operating key (pkg/agent/gemini) --
#                                      distinct from the gemini-key *capability*, which
#                                      mints its own short-lived keys per task once this
#                                      one gets the daemon running at all. Optional: with
#                                      enable_gemini_key on, the host mints this key for
#                                      itself from the minter credential pushed below --
#                                      see the module's README (../README.md), "The daemon's own
#                                      Gemini key". Set it only to use a key of your own.
#   GRAIN_CLAUDE_CODE_OAUTH_TOKEN      the Claude Code OAuth token agent/claude
#                                      authenticates as, for a deployment whose agent
#                                      framework is (or may be set to) "claude" -- the
#                                      counterpart of GRAIN_GEMINI_API_KEY above. Optional,
#                                      and so is that one: both credentials can be pasted
#                                      into the UI instead (Settings -> Models),
#                                      which stores them in the host's own secrets database
#                                      and takes precedence over whatever is pushed here.
#                                      Push one to have a deployment come up already able to
#                                      dispatch, without anyone opening the UI first.
#   GRAIN_OPENAI_API_KEY               the OpenAI API key agent/codex authenticates as,
#                                      for a deployment whose agent framework is (or may
#                                      be set to) "codex" -- the same counterpart again,
#                                      optional on the same terms
#   GRAIN_SECRETS_KEY                  this deployment's secrets private key
#                                      (pkg/secrets) -- the one value here that is not a
#                                      credential to some other service but grain's own,
#                                      and the one file a redeploy must carry: nothing
#                                      else can read the encrypted secrets file beside it,
#                                      so a host restored onto a fresh data directory with
#                                      a freshly minted key of its own cannot read a line
#                                      of it; pkg/secrets reports that as the unrecoverable
#                                      state it is rather than starting over silently.
#                                      Leave it unset on a first deploy -- the host mints
#                                      its own, and `grain state status` on that host
#                                      prints the file to go and keep a copy of. Set it
#                                      when standing a deployment back up from its
#                                      repository: scripts/setup.sh seeds it once, and a
#                                      key already on the host always wins
#   GRAIN_IMAGE_PULL_TOKEN             the password half of a `docker login` against the
#                                      registry grain_image lives in, for a deployment
#                                      running a private image (a GitHub PAT with
#                                      read:packages, for a private GHCR package). Optional
#                                      and normally unset: ghcr.io/bwsalmon/grain's own
#                                      package is public, and the host pulls anonymously.
#                                      The username half is not a secret and is a Terraform
#                                      input instead (grain_image_pull_user)
#   MINTER_SERVICE_ACCOUNT             terraform output minter_service_account -- if set,
#                                      mints a fresh key for it and pushes that too (see below)
set -euo pipefail

project="${PROJECT:?PROJECT is not set: the terraform project_id output}"
# instance.tf names the host "<name_prefix>-host", so INSTANCE -- which a
# deploy workflow written for the metadata version of this script
# already passes -- carries the prefix, and that workflow keeps working
# unchanged.
name_prefix="${NAME_PREFIX:-}"
if [ -z "$name_prefix" ] && [ -n "${INSTANCE:-}" ] && [ "${INSTANCE%-host}" != "$INSTANCE" ]; then
  name_prefix="${INSTANCE%-host}"
fi
[ -n "$name_prefix" ] || {
  echo "NAME_PREFIX is not set, and INSTANCE does not name a <name_prefix>-host instance to derive it from" >&2
  exit 1
}
minter_service_account="${MINTER_SERVICE_ACCOUNT:-}"

# $1 is the key, as secrets.tf's host_secret_keys names it. The value
# reaches gcloud on stdin, never as an argument or a file on disk.
push_secret() {
  local key="$1" value="$2" secret version old
  secret="$name_prefix-$key"
  if [ -z "$value" ]; then
    echo "$key: empty; leaving the host's copy untouched"
    return 0
  fi
  version="$(printf '%s' "$value" | gcloud secrets versions add "$secret" \
    --project="$project" --data-file=- --format='value(name.basename())')"
  # A push replaces, the way an overwritten metadata attribute did: the
  # value it supersedes -- a revoked PAT, a minter key deleted below --
  # should not stay readable to anyone who can enable an old version.
  gcloud secrets versions list "$secret" --project="$project" \
    --filter='state!=DESTROYED' --format='value(name.basename())' \
    | while read -r old; do
        [ "$old" = "$version" ] && continue
        gcloud secrets versions destroy "$old" --secret="$secret" \
          --project="$project" --quiet >/dev/null
      done
  echo "$key: pushed as $secret version $version"
}

push_secret "github-token" "${GRAIN_GITHUB_TOKEN:-}"
push_secret "github-app-id" "${GRAIN_GITHUB_APP_ID:-}"
push_secret "github-app-installation-id" "${GRAIN_GITHUB_APP_INSTALLATION_ID:-}"
push_secret "github-app-private-key" "${GRAIN_GITHUB_APP_PRIVATE_KEY:-}"
push_secret "gemini-api-key" "${GRAIN_GEMINI_API_KEY:-}"
push_secret "claude-oauth-token" "${GRAIN_CLAUDE_CODE_OAUTH_TOKEN:-}"
push_secret "openai-api-key" "${GRAIN_OPENAI_API_KEY:-}"
# grain's own key rather than a credential to anything else: what
# decrypts the encrypted secrets file under the data directory. Pushed only
# when a rebuilt host has to be given back the key its predecessor
# minted -- setup.sh seeds it once and never overwrites a key already on
# the host, so pushing it again is harmless and pushing it never is what
# costs a redeploy every secret it had.
push_secret "secrets-key" "${GRAIN_SECRETS_KEY:-}"
# Only for a deployment whose grain_image lives in a registry that needs
# credentials -- ghcr.io/bwsalmon/grain's own package is public and pulls
# anonymously, so this is empty (and skipped) in the ordinary case. The
# username half is not a secret and is a Terraform input
# (grain_image_pull_user); this is the token.
push_secret "image-pull-token" "${GRAIN_IMAGE_PULL_TOKEN:-}"

# The credential pkg/capability/gcpkey authenticates as to mint (and
# revoke) the agent account's per-task keys. Minted fresh on every run of
# this script rather than once -- iam.tf's deployer_manages_minter_keys
# is what lets the identity running this script do so -- and the two
# oldest keys beyond the newest are invalidated afterward, the same
# rotate-and-prune-to-two schedule v1's own push-host-secrets.sh used
# for its minter key, so a stale credential from a previous run
# does not linger indefinitely.
#
# The prune is only safe because the host converges on what is pushed
# here: scripts/setup.sh's seed_gcp_minter_key writes this attribute's
# value into the host's own secrets database on *every* run, so the
# newest key is the one the daemon authenticates with by the end of the
# next deploy. It used to seed only once and never overwrite, which
# made these two lines a scheduled outage -- three runs of this script
# and the key the daemon was still using had been deleted here, leaving
# every gcp-key mint failing with Google's `invalid_grant` on a
# deployment whose Settings pane read Ready. Keeping the previous key
# alive is what covers the gap between this script running and the host
# next deploying; a rotated key still only takes effect when it does, so
# bump deploy_generation (or wait for the next push) rather than
# expecting this script alone to move the running daemon.
if [ -n "$minter_service_account" ]; then
  umask 077
  minter_file="$(mktemp)"
  minter_key_id="$(gcloud iam service-accounts keys create "$minter_file" \
    --iam-account="$minter_service_account" --format='value(name.basename())')"
  push_secret "gcp-minter-key" "$(cat "$minter_file")"
  shred -u "$minter_file" 2>/dev/null || rm -f "$minter_file"
  echo "minted minter key: $minter_key_id"

  gcloud iam service-accounts keys list --iam-account="$minter_service_account" \
    --managed-by=user --sort-by='~validAfterTime' --format='value(name.basename())' \
    | tail -n +3 \
    | while read -r old_key_id; do
        gcloud iam service-accounts keys delete "$old_key_id" \
          --iam-account="$minter_service_account" --quiet
        echo "invalidated previous minter key: $old_key_id"
      done
else
  echo "MINTER_SERVICE_ACCOUNT is not set -- no minter key minted or pushed;" \
       "the gcp-key/gemini-key capabilities will have no credential to mint with."
fi
