#!/usr/bin/env bash
# Publish the terraform/gcp Terraform outputs the rest of the deploy
# workflow reads as GitHub Actions step outputs.
#
# Every value a later step reads as steps.<id>.outputs.<name> is written
# here. Adding a consumer without adding it here is the bug that made a
# v1 host come up with no GCP access at all (bwsalmon/agents#69): the
# expression resolved to empty rather than failing, so the step that
# minted and pushed the key was silently skipped. minter_service_account
# is exactly that shape again here -- push-secrets.sh mints no minter key
# at all when it is empty, and the gcp-key and gemini-key capabilities
# then have no credential to mint with.
#
# Required env:
#   GITHUB_OUTPUT  set by the Actions runner
set -euo pipefail

github_output="${GITHUB_OUTPUT:?GITHUB_OUTPUT is not set (is this running outside Actions?)}"

module_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$module_dir"

# One `terraform output -json`, parsed, rather than a `-raw` call per
# name. -raw exits non-zero on a null value -- which url, dns_name and
# load_balancer_ip all are when expose_ui_publicly is off -- and under
# hashicorp/setup-terraform the `terraform` on PATH is a wrapper that
# prints its own "::error::Terraform exited with code 1." annotation on
# *stdout*. A `|| true` around it therefore captured that annotation as
# the value, and the summary reported a URL of "::error::Terraform
# exited with code 1.". -json emits every output at once, nulls
# included, and needs no per-name failure handling.
outputs="$(terraform output -json)"

# A name terraform did not emit, and one whose value is null, both read as
# empty -- which is what the summary below treats as "not configured".
get() {
  jq -r --arg k "$1" 'if (.[$k].value // null) == null then "" else .[$k].value end' <<<"$outputs"
}

{
  echo "project_id=$(get project_id)"
  echo "instance=$(get instance_name)"
  echo "zone=$(get zone)"
  echo "url=$(get url)"
  # Empty exactly when expose_ui_publicly is off, which is how the
  # summary decides whether to print a URL or the tunnel command.
  echo "tunnel_command=$(get tunnel_command)"
  echo "agent_service_account=$(get agent_service_account)"
  echo "minter_service_account=$(get minter_service_account)"
} >> "$github_output"
