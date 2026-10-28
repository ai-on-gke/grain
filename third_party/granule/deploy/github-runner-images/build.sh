#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
usage: build.sh ubuntu-22.04|ubuntu-24.04|ubuntu-26.04|ubuntu-slim

Environment for full images:
  AZURE_RESOURCE_GROUP   existing resource group for Packer output
  AZURE_LOCATION         Azure region (default: eastus)
  RUNNER_IMAGES_REF      actions/runner-images ref (default: main)

Common environment:
  OUTPUT_IMAGE           output OCI name (default: granule:github-<label>)
  PUSH                   true to push OUTPUT_IMAGE and immutable tags
  KEEP_WORK              true to retain local and Azure intermediates

The three full images require an authenticated Azure CLI session. The
builder needs packer, az, azcopy, guestfish, Docker,
rsync, mke2fs, and enough space for a 75 GB source VHD plus its unpacked
rootfs and output layers.
EOF
  exit 2
}

[[ $# -eq 1 ]] || usage
label=$1
case "$label" in
  ubuntu-22.04) image_os=ubuntu22; packer_build=ubuntu-22_04; install_kernel=false ;;
  ubuntu-24.04) image_os=ubuntu24; packer_build=ubuntu-24_04; install_kernel=false ;;
  ubuntu-26.04) image_os=ubuntu26; packer_build=ubuntu-26_04; install_kernel=false ;;
  ubuntu-slim) image_os=ubuntu24; packer_build=; install_kernel=true ;;
  *) usage ;;
esac

for command in git docker rsync mke2fs; do
  command -v "$command" >/dev/null || { echo "missing required command: $command" >&2; exit 1; }
done
if [[ $label != ubuntu-slim ]]; then
  for command in az azcopy packer guestfish; do
    command -v "$command" >/dev/null || { echo "missing required command: $command" >&2; exit 1; }
  done
  : "${AZURE_RESOURCE_GROUP:?AZURE_RESOURCE_GROUP is required for full images}"
fi

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd -- "$script_dir/../.." && pwd)
runner_ref=${RUNNER_IMAGES_REF:-main}
# The ref arrives from a workflow_dispatch text box and goes to
# `git checkout` as an argument, so it is held to what a commit, tag or
# branch name looks like: nothing that git would read as an option.
if [[ ! $runner_ref =~ ^[A-Za-z0-9][A-Za-z0-9._/-]*$ ]]; then
  echo "RUNNER_IMAGES_REF must be a commit, tag or branch name, got: $runner_ref" >&2
  exit 2
fi
azure_location=${AZURE_LOCATION:-eastus}
output_image=${OUTPUT_IMAGE:-granule:github-$label}
push=${PUSH:-false}
keep_work=${KEEP_WORK:-false}
build_id=${GRANULE_BUILD_ID:-$(date -u +%Y%m%d%H%M%S)-$$}
build_id=${build_id//[^a-zA-Z0-9-]/-}
work=$(mktemp -d -t granule-runner-image.XXXXXXXX)
azure_image=
azure_disk=

cleanup() {
  if [[ $keep_work != true ]]; then
    if [[ -n $azure_disk ]]; then az disk delete -g "$AZURE_RESOURCE_GROUP" -n "$azure_disk" --yes --no-wait >/dev/null 2>&1 || true; fi
    if [[ -n $azure_image ]]; then az image delete -g "$AZURE_RESOURCE_GROUP" -n "$azure_image" >/dev/null 2>&1 || true; fi
    sudo rm -rf "$work"
  else
    echo "retained build directory: $work" >&2
    [[ -z $azure_image ]] || echo "retained Azure image: $azure_image" >&2
    [[ -z $azure_disk ]] || echo "retained Azure disk: $azure_disk" >&2
  fi
}
trap cleanup EXIT

git -C "$work" clone https://github.com/actions/runner-images.git runner-images
git -C "$work/runner-images" checkout --detach "$runner_ref"
runner_revision=$(git -C "$work/runner-images" rev-parse HEAD)

# GITHUB_PROXY is the Dockerfile build arg of the same name (see its
# fetch-chv stage); deploy/publish-images.sh sets it in a grain sandbox.
docker build --target runtime --build-arg GITHUB_PROXY="${GITHUB_PROXY:-}" \
  -t granule-runtime:"$runner_revision" "$repo_root"
docker build --target guest-agent-artifacts \
  --output type=local,dest="$work/agents" "$repo_root"
mkdir -p "$work/rootfs" "$work/output"

if [[ $label == ubuntu-slim ]]; then
  docker build -t granule-runner-source:"$runner_revision" \
    "$work/runner-images/images/ubuntu-slim"
  container=$(docker create granule-runner-source:"$runner_revision")
  trap 'docker rm -f "$container" >/dev/null 2>&1 || true; cleanup' EXIT
  docker export "$container" | sudo tar --numeric-owner -xpf - -C "$work/rootfs"
  docker rm "$container" >/dev/null
  trap cleanup EXIT
else
  azure_image="granule-${packer_build//_/-}-${runner_revision:0:8}-${build_id}"
  azure_image=${azure_image:0:72}
  azure_disk="$azure_image-export"
  templates="$work/runner-images/images/ubuntu/templates"
  packer init "$templates"
  packer build \
    -only "$packer_build.azure-arm.image" \
    -var use_azure_cli_auth=true \
    -var "subscription_id=$(az account show --query id -o tsv)" \
    -var "location=$azure_location" \
    -var "image_os=$image_os" \
    -var "image_version=granule-${runner_revision:0:12}" \
    -var "managed_image_name=$azure_image" \
    -var "managed_image_resource_group_name=$AZURE_RESOURCE_GROUP" \
    "$templates"

  image_id=$(az image show -g "$AZURE_RESOURCE_GROUP" -n "$azure_image" --query id -o tsv)
  az disk create -g "$AZURE_RESOURCE_GROUP" -n "$azure_disk" \
    --image-reference "$image_id" --hyper-v-generation V2 >/dev/null
  sas=$(az disk grant-access -g "$AZURE_RESOURCE_GROUP" -n "$azure_disk" \
    --access-level Read --duration-in-seconds 86400 --query accessSas -o tsv)
  azcopy copy "$sas" "$work/source.vhd"
  az disk revoke-access -g "$AZURE_RESOURCE_GROUP" -n "$azure_disk" >/dev/null
  guestfish --ro -a "$work/source.vhd" -i tar-out / - | \
    sudo tar --numeric-owner -xpf - -C "$work/rootfs"
fi

sudo "$script_dir/prepare-rootfs.sh" \
  "$work/rootfs" "$work/agents" "$work/output" "$install_kernel"

# The guest-side contract this rootfs was prepared against, read out of
# the overlay prepare-rootfs.sh just laid into it rather than written
# down again here: this image is assembled by hand (no `granule guest
# pack` to stamp it), and a second copy of the number is a second copy to
# forget. See pkg/guestimage and deploy/guest-image/README.md's
# "The guest-side contract"; without it a later `granule guest rebase`
# of this image cannot tell how old its guest side is.
guest_contract=$(cat "$repo_root/deploy/guest-image/overlay-common/etc/granule/guest-contract")

docker build -f "$script_dir/Dockerfile" \
  --build-arg GRANULE_RUNTIME_IMAGE=granule-runtime:"$runner_revision" \
  --build-arg GITHUB_RUNNER_LABEL="$label" \
  --build-arg RUNNER_IMAGES_REVISION="$runner_revision" \
  --build-arg GRANULE_GUEST_CONTRACT="$guest_contract" \
  -t "$output_image" "$work/output"

immutable_image="${output_image}-${runner_revision:0:12}"
docker tag "$output_image" "$immutable_image"
if [[ $label == ubuntu-24.04 ]]; then
  latest_image=${output_image%:*}:github-ubuntu-latest
  docker tag "$output_image" "$latest_image"
fi

# Publishing is the point of no return for a moving tag. Prove that the
# converted filesystem boots as a Granule guest and still contains the core
# GitHub toolchain before any tag leaves this builder.
"$script_dir/test-image.sh" "$output_image" "$label"

if [[ $push == true ]]; then
  docker push "$output_image"
  docker push "$immutable_image"
  [[ $label != ubuntu-24.04 ]] || docker push "$latest_image"
fi

echo "built $output_image from actions/runner-images@$runner_revision"
