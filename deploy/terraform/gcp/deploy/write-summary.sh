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

# Write the deploy's job summary. Runs with if: always(), so
# it must never fail the job itself -- every value is read null-safe and
# a missing one prints as empty rather than aborting.
#
# Required env:
#   GITHUB_STEP_SUMMARY  set by the Actions runner
# Optional env:
#   DEPLOYMENT           which deployment this is, for the heading
#                        (default "grain")
#   URL, TUNNEL_COMMAND, INSTANCE, ZONE, PROJECT, DEPLOY_GENERATION
#
# Exactly one of URL and TUNNEL_COMMAND describes how to reach this
# deployment: a public load balancer, or IAP's TCP tunnel when
# expose_ui_publicly is off. Both empty means the apply did not get far
# enough to have outputs at all.
set -euo pipefail

summary="${GITHUB_STEP_SUMMARY:?GITHUB_STEP_SUMMARY is not set (is this running outside Actions?)}"

{
  echo "## ${DEPLOYMENT:-grain}"
  echo
  echo "| | |"
  echo "|---|---|"
  if [ -n "${URL:-}" ]; then
    echo "| URL | ${URL} |"
  elif [ -n "${TUNNEL_COMMAND:-}" ]; then
    echo "| URL | none -- tunnel-only (see below) |"
  else
    echo "| URL | <not applied> |"
  fi
  echo "| Instance | ${INSTANCE:-<not applied>} |"
  echo "| Zone | ${ZONE:-<not applied>} |"
  echo "| Project | ${PROJECT:-<not applied>} |"
  echo "| Generation | ${DEPLOY_GENERATION:-<none>} |"
  echo
  if [ -n "${URL:-}" ]; then
    echo "The URL is reachable only after signing in as one of \`iap_members\`."
  elif [ -n "${TUNNEL_COMMAND:-}" ]; then
    echo "This deployment has no public entry point. Forward the UI to your own"
    echo "machine over IAP's TCP tunnel, then open http://localhost:8080 --"
    echo "authenticated by \`roles/iap.tunnelResourceAccessor\`:"
    echo
    echo '```sh'
    echo "${TUNNEL_COMMAND}"
    echo '```'
  fi
  echo
  echo "Host log:"
  echo
  echo '```sh'
  echo "gcloud compute ssh ${INSTANCE:-INSTANCE} --zone ${ZONE:-ZONE} --project ${PROJECT:-PROJECT} \\"
  echo "  --tunnel-through-iap --command 'sudo journalctl -u grain-config-sync -f'"
  echo '```'
} >> "$summary"
