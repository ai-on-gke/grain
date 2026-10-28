#!/usr/bin/env bash
# Publish granule's images to GHCR by hand.
#
# This is now the only thing that publishes the guest images: CI builds
# no images at all, so nothing lands on GHCR until a person runs this.
# The GitHub runner images are still published on a weekly schedule
# (.grain/workflows/github-runner-images.yml), and the tags below are
# deliberately the ones that workflow produces, so a hand-run publish and
# a scheduled one are indistinguishable to whoever pulls afterwards.
set -euo pipefail

registry=${REGISTRY:-ghcr.io}
# Lowercased below: GHCR rejects an uppercase path, and the repository is
# written "GKE-labs/granule" in plenty of places a person copies from.
repo=${REPO:-gke-labs/granule}
dry_run=${DRY_RUN:-false}
grain_sandbox=${GRAIN_SANDBOX:-}
# grain's registry mirror, which the grain-sandbox setup below points
# docker and containerd at for ghcr.io. Read nowhere else.
grain_registry_mirror=${GRAIN_REGISTRY_MIRROR:-http://172.17.0.1:43325/v2/ghcr}
# The Dockerfile's GITHUB_PROXY build arg: empty, so github.com release
# assets are fetched directly, unless this is a grain sandbox (below).
github_proxy=""
do_login=true
allow_dirty=false
push=true
force_rootfs=false
print_rootfs_tags=false

usage() {
  cat >&2 <<'EOF'
usage: deploy/publish-images.sh [options] [image ...]

Images:
  debian      guest image, granule's own kernel      -> granule/debian:latest
  alpine      guest image, Alpine rootfs            -> granule/alpine:latest
  debian12    guest image, Debian kernel + headroom -> granule/debian12:latest
  github-ubuntu-22.04                               -> granule/github-ubuntu-22.04:latest
  github-ubuntu-24.04                               -> granule/github-ubuntu-24.04:latest
  github-ubuntu-26.04                               -> granule/github-ubuntu-26.04:latest
  github-ubuntu-slim                                -> granule/github-ubuntu-slim:latest

With no image named, the three guest images are published -- the set CI
publishes from main. "guests", "runners" and "all" name those groups.

Each guest is published twice: the packed, bootable image to
OWNER/NAME/<variant>, and the guest rootfs it was packed from -- the image a
consumer writes FROM to customize a guest -- to OWNER/NAME/<variant>.rootfs
under the :latest moving tag. So "debian" yields
ghcr.io/gke-labs/granule/debian:latest and
ghcr.io/gke-labs/granule/debian.rootfs:latest.

Every image also gets an immutable tag nobody moves, and the two halves
of a guest are keyed differently because they change for different
reasons:

  packed guest   <image>:<commit>, because a packed guest carries
                 granule's own guest-side half and so moves with every
                 commit that touches granule.
  guest rootfs   <image>:os-<key>, where <key> is deploy/guest-rootfs-key.sh's
                 hash of the rootfs recipe: the Dockerfile's guest rootfs
                 stages and the build args they read. The rootfs stages
                 build an operating system and nothing of granule's, so
                 this changes when the OS does and not once per commit.
                 A keyed tag already on the registry is left alone rather
                 than rebuilt over (--force-rootfs, and the caveats under
                 ROOTFS_REVISION below).
  runner image   the first twelve characters of the actions/runner-images
                 commit it was built from.

A package GHCR created on its first push is private, and a private
guest rootfs is one nobody can build FROM: set OWNER/NAME/<variant>.rootfs
to public in its GHCR settings after the first publish, and flip
REQUIRE_PUBLISHED_ROOTFS in the "published guest rootfs" job of
.grain/workflows/ci.yml, which pulls what this script pushed and
builds a guest on it.

Options:
  -n, --dry-run     print the builds and pushes, run neither
      --no-push     build and tag locally, push nothing
      --no-login    the docker daemon is already authenticated to the registry
      --allow-dirty publish from a dirty tree (immutable tags get "-dirty")
      --force-rootfs  push a rootfs key tag that is already published,
                      overwriting it. For a tag pushed by mistake; every
                      other reason wants ROOTFS_REVISION instead.
      --print-rootfs-tags  print "<variant> <moving image> <keyed image>"
                      for the rootfs of each selected guest and exit,
                      building nothing. This is how CI asks what this
                      tree would publish, and what the two names of a
                      published rootfs are.
      --grain-sandbox / --no-grain-sandbox
                    apply, or skip, the grain-sandbox host setup (see
                    GRAIN_SANDBOX below). Default: detect it.
  -r, --repo OWNER/NAME   default gke-labs/granule, or $REPO
      --registry HOST     default ghcr.io, or $REGISTRY
  -h, --help

Environment:
  GHCR_TOKEN / GITHUB_TOKEN   token with write:packages, for docker login
  GHCR_USER / GITHUB_USER     GitHub username to log in as
  ROOTFS_REVISION             mixed into every rootfs key. The rootfs
                              recipe names its upstream archive (a
                              debootstrap of a suite) rather than pinning
                              it, so when the reason to publish a rootfs is
                              that upstream moved -- a point release, a
                              security update -- and not that the recipe
                              did, set this to something new (a date reads
                              well) to mint a new key rather than
                              overwriting somebody's pin.
  GRAIN_SANDBOX               1 or 0. Inside a grain sandbox the script
                              rewrites /etc/resolv.conf, /etc/hosts, the
                              trusted CAs and docker/containerd's registry
                              config to use grain's nameserver, CA and
                              mirror. Unset, it does that only where grain's
                              CA ("Grain Internal Root CA") is installed;
                              anywhere else it touches none of them. It also
                              passes the Dockerfile GITHUB_PROXY=<that
                              nameserver>, so the build fetches its github
                              release assets through grain's proxy.
  GRAIN_REGISTRY_MIRROR       grain's registry mirror for ghcr.io, used only
                              by that setup (default
                              http://172.17.0.1:43325/v2/ghcr)
  AZURE_RESOURCE_GROUP        required by the three full runner images
  AZURE_LOCATION              Azure region for those (default eastus)
  RUNNER_IMAGES_REF           actions/runner-images ref (default main)

The guest images want an ordinary docker and around half an hour;
"runners" wants a builder with ~250 GB of scratch space, and all but
ubuntu-slim want an authenticated Azure CLI session. See
deploy/github-runner-images/README.md before reaching for those.
EOF
  exit 2
}

guest_images=(debian alpine debian12)
runner_images=(github-ubuntu-22.04 github-ubuntu-24.04 github-ubuntu-26.04 github-ubuntu-slim)

# The architecture the guest images are built for: the docker daemon's
# own, since the builds below pass no --platform. Debian names its kernel
# packages after the same amd64/arm64 that docker reports, so debian12's
# kernel package follows from it -- an arm64 publish that installed
# linux-image-amd64 would pack a kernel its guests cannot run.
# GUEST_ARCH overrides it; without a reachable daemon (a --dry-run on a
# laptop) this machine's own architecture stands in.
guest_arch() {
  if [[ -n ${GUEST_ARCH:-} ]]; then
    echo "$GUEST_ARCH"
    return
  fi
  local arch
  arch=$(docker version --format '{{.Server.Arch}}' 2>/dev/null) || arch=$(uname -m)
  case $arch in
    x86_64) echo amd64 ;;
    aarch64) echo arm64 ;;
    *) echo "$arch" ;;
  esac
}
guest_arch=$(guest_arch)

# Every guest variant the Dockerfile can build; see its comments for why
# each one exists and why debian12 asks for 2048 MB and not more.
guest_build_args() {
  case $1 in
    debian) echo "GUEST_DISTRO=debian" ;;
    alpine) echo "GUEST_DISTRO=alpine" ;;
    debian12) echo "GUEST_DISTRO=debian GUEST_KERNEL_PACKAGE=linux-image-$guest_arch GUEST_DISK_EXTRA_MB=2048" ;;
  esac
}

is_guest() { case " ${guest_images[*]} " in *" $1 "*) return 0 ;; *) return 1 ;; esac; }
is_runner() { case " ${runner_images[*]} " in *" $1 "*) return 0 ;; *) return 1 ;; esac; }

# Everything that changes the world goes through this, so --dry-run is one
# decision rather than one per call site.
run() {
  if [[ $dry_run == true ]]; then
    printf '+ %s\n' "$*"
    return 0
  fi
  "$@"
}

# docker push, retried. The guests share most of their layers, so a push
# cross-repository-mounts blobs the previous image's push uploaded moments
# before -- and GHCR can accept that mount, report "Layer already exists",
# and then reject the manifest with "blob unknown to registry" because the
# mount has not landed yet. A push a little later usually finds the blob.
#
# Not always, though: GHCR can go on answering a HEAD for the blob in the
# target repository (so docker reports "Layer already exists" and never
# uploads it) while refusing every manifest that names it, and no number
# of plain retries gets past that -- the release for ceb5ae6 failed four
# pushes of debian12.rootfs that way. So a failed push that names the
# blobs the registry does not know has them linked into the repository
# by hand (relink_blob) before it is tried again.
push_image() {
  local attempt max=4 out digest
  out=$(mktemp)
  for ((attempt = 1; ; attempt++)); do
    if run docker push "$1" 2>&1 | tee "$out"; then
      rm -f "$out"
      return 0
    fi
    if ((attempt >= max)); then
      rm -f "$out"
      echo "docker push $1 failed $max times" >&2
      return 1
    fi
    # shellcheck disable=SC2013 # digests are single words
    for digest in $(grep -oE 'blob unknown to registry - sha256:[0-9a-f]{64}' "$out" | grep -oE 'sha256:[0-9a-f]{64}' | sort -u); do
      relink_blob "$1" "$digest" "$attempt" || true
    done
    echo "docker push $1 failed (attempt $attempt/$max); retrying in $((attempt * 15))s" >&2
    sleep $((attempt * 15))
  done
}

# The scheme the registry API is spoken in: plain http on loopback, as
# docker push itself does for the demo's local registry:2.
registry_scheme() {
  case $registry in
    localhost|localhost:*|127.0.0.1|127.0.0.1:*|'[::1]'|'[::1]:'*) echo http ;;
    *) echo https ;;
  esac
}

# A bearer token for the given repository scopes ("NAME:pull,push"), from
# the registry's token endpoint (GHCR's /token), or nothing when there are
# no credentials or the registry wants none.
registry_bearer() {
  local url scope
  [[ -n ${user:-} && -n ${token:-} ]] || return 0
  url="$(registry_scheme)://$registry/token?service=$registry"
  for scope in "$@"; do
    url+="&scope=repository:$scope"
  done
  curl -fsS --max-time 30 -u "$user:$token" "$url" 2>/dev/null |
    python3 -c 'import json, sys; d = json.load(sys.stdin); print(d.get("token") or d.get("access_token") or "")' 2>/dev/null || true
}

# Make blob $2 exist in the repository image $1 is pushed to, for a
# registry that rejected $1's manifest with "blob unknown" while telling
# docker the blob was already there. The blob is in a sibling guest
# repository -- that is where the push that reported it "already
# existing" got it from -- so the first attempt ($3 = 1) asks the registry
# to mount it from one of those, and a later one copies it outright: the
# blob is downloaded from a sibling and uploaded into the target, which a
# registry has no way to answer with "already exists".
relink_blob() {
  local image=$1 digest=$2 attempt=$3
  local name=${image%:*} target src bearer code location sep file
  local base auth=() variant
  target=${name#"$registry/"}
  base="$(registry_scheme)://$registry/v2"
  echo "   $digest is unknown to $target; linking it in from a sibling repository" >&2
  for variant in "${guest_images[@]}"; do
    for src in "$repo/$variant" "$repo/$variant.rootfs"; do
      [[ $src != "$target" ]] || continue
      bearer=$(registry_bearer "$target:pull,push" "$src:pull")
      auth=()
      [[ -z $bearer ]] || auth=(-H "Authorization: Bearer $bearer")
      if ((attempt == 1)); then
        code=$(curl -sS --max-time 60 -o /dev/null -w '%{http_code}' -X POST "${auth[@]}" \
          -H 'Content-Length: 0' "$base/$target/blobs/uploads/?mount=$digest&from=$src" 2>/dev/null) || code=000
        if [[ $code == 201 ]]; then
          echo "   mounted $digest into $target from $src" >&2
          return 0
        fi
        continue
      fi
      file=$(mktemp)
      # -L for the redirect to blob storage; curl drops the Authorization
      # header when that redirect leaves the registry's host, as it must.
      if ! curl -fsSL --max-time 900 "${auth[@]}" -o "$file" "$base/$src/blobs/$digest" 2>/dev/null ||
        [[ "sha256:$(sha256sum "$file" | cut -d' ' -f1)" != "$digest" ]]; then
        rm -f "$file"
        continue
      fi
      location=$(curl -sS --max-time 60 -o /dev/null -D - -X POST "${auth[@]}" \
        -H 'Content-Length: 0' "$base/$target/blobs/uploads/" 2>/dev/null |
        tr -d '\r' | awk 'tolower($1) == "location:" { print $2 }' | tail -n1)
      if [[ -z $location ]]; then
        rm -f "$file"
        continue
      fi
      [[ $location == http* ]] || location="$(registry_scheme)://$registry$location"
      sep='?'
      [[ $location != *\?* ]] || sep='&'
      code=$(curl -sS --max-time 900 -o /dev/null -w '%{http_code}' -X PUT "${auth[@]}" \
        -H 'Content-Type: application/octet-stream' --data-binary "@$file" \
        "$location${sep}digest=$digest" 2>/dev/null) || code=000
      rm -f "$file"
      if [[ $code == 201 ]]; then
        echo "   uploaded $digest into $target (copied from $src)" >&2
        return 0
      fi
    done
  done
  echo "   could not link $digest into $target from any sibling repository" >&2
  return 1
}

requested=()
while [[ $# -gt 0 ]]; do
  case $1 in
    -n|--dry-run) dry_run=true ;;
    --no-push) push=false ;;
    --no-login) do_login=false ;;
    --allow-dirty) allow_dirty=true ;;
    --force-rootfs) force_rootfs=true ;;
    --print-rootfs-tags) print_rootfs_tags=true ;;
    --grain-sandbox) grain_sandbox=1 ;;
    --no-grain-sandbox) grain_sandbox=0 ;;
    -r|--repo) [[ $# -ge 2 ]] || usage; repo=$2; shift ;;
    --registry) [[ $# -ge 2 ]] || usage; registry=$2; shift ;;
    -h|--help) usage ;;
    -*) echo "unknown option: $1" >&2; usage ;;
    guests) requested+=("${guest_images[@]}") ;;
    runners) requested+=("${runner_images[@]}") ;;
    all) requested+=("${guest_images[@]}" "${runner_images[@]}") ;;
    *)
      if is_guest "$1" || is_runner "$1"; then
        requested+=("$1")
      else
        echo "unknown image: $1" >&2
        usage
      fi
      ;;
  esac
  shift
done
[[ ${#requested[@]} -gt 0 ]] || requested=("${guest_images[@]}")

repo=$(printf '%s' "$repo" | tr '[:upper:]' '[:lower:]')
[[ $repo == */* ]] || { echo "--repo wants OWNER/NAME, got: $repo" >&2; exit 2; }
image_base="$registry/$repo"

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd -- "$script_dir/.." && pwd)
cd "$repo_root"

# Guest images: build every requested one before pushing any. A push is
# where a moving tag stops being this machine's business, and the builds
# are where the failures are (a distro mirror, a kernel package, 2 GB of
# disk headroom) -- so failing after ":latest" has moved but before
# ":alpine" has is worth the extra disk to avoid.
guest_selected=()
runner_selected=()
for image in "${requested[@]}"; do
  if is_guest "$image"; then
    case " ${guest_selected[*]-} " in *" $image "*) ;; *) guest_selected+=("$image") ;; esac
  else
    case " ${runner_selected[*]-} " in *" $image "*) ;; *) runner_selected+=("$image") ;; esac
  fi
done

# The immutable tag for a variant's rootfs is its content key rather than
# this commit; deploy/guest-rootfs-key.sh decides what that covers and
# why. Every key is computed here, once, before anything is built:
#
#   - the build loop and the push loop have to name the same tag, and a
#     hash run twice is a hash that can come out twice;
#   - a key that cannot be computed -- an unclassified build arg, a
#     Dockerfile whose recipe markers have gone -- has to stop the
#     publish rather than yield an empty string that ends up in a tag,
#     and an `exit` from inside a $(command substitution) exits only the
#     substitution.
declare -A rootfs_key=()
for variant in "${guest_selected[@]-}"; do
  [[ -n $variant ]] || continue
  # The same build args the docker build is given: keying on anything
  # else is how a tag comes to name a build nobody made. The script
  # ignores the ones that do not reach the rootfs and refuses any it has
  # never heard of, so a new build arg stops the publish here rather
  # than quietly falling outside the key.
  #
  # DOCKERFILE is named rather than inherited: the builds below are
  # "--file Dockerfile" whatever the environment says, so keying
  # anything else would tag one file's rootfs after another's.
  # shellcheck disable=SC2046  # the args are a word list on purpose
  key=$(DOCKERFILE="$repo_root/Dockerfile" "$script_dir/guest-rootfs-key.sh" $(guest_build_args "$variant")) || {
    echo "could not compute the $variant rootfs content key (see above); nothing published" >&2
    exit 1
  }
  [[ -n $key ]] || { echo "the $variant rootfs content key came out empty; nothing published" >&2; exit 1; }
  rootfs_key[$variant]=$key
done
guest_rootfs_key() { printf '%s' "${rootfs_key[$1]}"; }
guest_rootfs_immutable_tag() { printf 'os-%s' "$(guest_rootfs_key "$1")"; }

# Whether the registry already carries this exact rootfs, which is what
# the keyed tag is for: a publish whose operating system has not changed
# leaves the published rootfs alone instead of pushing a fresh build over
# a tag somebody has pinned. The fresh build would not be byte-identical
# to it -- the recipe names its upstream rather than pinning it, so a
# debootstrap a month later picks up a month of Debian -- so "already
# there" has to beat "just built", or an immutable tag would quietly
# change content every time anybody published anything at all.
rootfs_already_published() {
  local image=$1 out
  local inspect=(docker manifest inspect)
  # `docker push` talks plain http to a registry on loopback without
  # being asked; `docker manifest inspect` does not, and answers "no such
  # manifest" for a manifest that is plainly there -- which would have
  # this function report a published rootfs as absent and overwrite the
  # tag it exists to protect. The demo publishes to a local registry:2
  # (demo/README.md), so this is a path people run.
  case $registry in
    localhost|localhost:*|127.0.0.1|127.0.0.1:*|'[::1]'|'[::1]:'*) inspect+=(--insecure) ;;
  esac
  [[ $force_rootfs != true ]] || return 1
  if [[ $dry_run == true ]]; then
    printf '+ %s %s   # a dry run asks nothing of the registry, so: assume absent\n' "${inspect[*]}" "$image"
    return 1
  fi
  out=$("${inspect[@]}" "$image" 2>&1) && return 0
  case $out in
    *manifest*unknown*|*MANIFEST_UNKNOWN*|*"not found"*|*"no such manifest"*) ;;
    *)
      # A publish is a deliberate act with a person watching it, so a
      # registry that cannot answer is reported and pushed through
      # rather than stopping a publish that is very likely fine.
      echo "warning: could not tell whether $image is already published, so it will be pushed as new: $out" >&2
      ;;
  esac
  return 1
}

# Answering "what would this tree publish?" without building it: CI asks
# this, and so does anybody about to run the publish for real.
if [[ $print_rootfs_tags == true ]]; then
  for variant in "${guest_selected[@]-}"; do
    [[ -n $variant ]] || continue
    printf '%s %s/%s.rootfs:latest %s/%s.rootfs:%s\n' "$variant" \
      "$image_base" "$variant" \
      "$image_base" "$variant" "$(guest_rootfs_immutable_tag "$variant")"
  done
  exit 0
fi

command -v docker >/dev/null || { echo "missing required command: docker" >&2; exit 1; }
command -v git >/dev/null || { echo "missing required command: git" >&2; exit 1; }

# Inside a grain sandbox, point the host and the docker daemon at grain's
# nameserver, CA and registry mirror. None of this is right anywhere else:
# on an ordinary machine it would resolve ghcr.io and github.com to the
# default gateway (on GCE, the metadata server) and trust whatever
# certificate answered there. So it only runs when the machine says it is
# a grain sandbox -- the grain CA that grain installs into every sandbox --
# or when asked to with GRAIN_SANDBOX=1 / --grain-sandbox.
is_grain_sandbox() {
  case $grain_sandbox in
    1|true|yes) return 0 ;;
    0|false|no) return 1 ;;
    "") ;;
    *) echo "GRAIN_SANDBOX wants 1 or 0, got: $grain_sandbox" >&2; exit 2 ;;
  esac
  local cand
  for cand in /usr/local/share/ca-certificates/grain-ca.crt /etc/ssl/certs/grain-ca.pem; do
    if [[ -f $cand ]] && openssl x509 -in "$cand" -noout -subject 2>/dev/null | grep -q "Grain Internal Root CA"; then
      return 0
    fi
  done
  return 1
}

# grain's nameserver, which is also where its proxy cache answers.
grain_nameserver() {
  local ns=""
  if [[ -f /etc/resolv.conf ]]; then
    ns=$(grep -E '^nameserver' /etc/resolv.conf | awk '{print $2}' | grep -v -E '^(8\.8\.8\.8|8\.8\.4\.4|127\.|0\.)' | head -n 1 || true)
  fi
  if [[ -z $ns ]]; then
    ns=$(ip route 2>/dev/null | awk '/default/ {print $3}' | head -n 1 || true)
  fi
  printf '%s' "$ns"
}

grain_sandbox_setup() {
  ns=$(grain_nameserver)
  if [[ -n $ns ]]; then
    if ! grep -q -E "^nameserver[[:space:]]+$ns" /etc/resolv.conf 2>/dev/null; then
      echo "nameserver $ns" | sudo tee /etc/resolv.conf >/dev/null || true
    fi
    for h in "$registry" ghcr.io pkg-containers.githubusercontent.com github.com api.github.com objects.githubusercontent.com raw.githubusercontent.com; do
      [[ -n $h ]] || continue
      if ! grep -q -w "$h" /etc/hosts 2>/dev/null; then
        echo "$ns $h" | sudo tee -a /etc/hosts >/dev/null || true
      fi
    done
    if [[ ! -f /etc/docker/daemon.json ]] || ! grep -q '"dns"' /etc/docker/daemon.json; then
      sudo python3 -c "
import json, os
d = {}
if os.path.exists('/etc/docker/daemon.json'):
    try:
        with open('/etc/docker/daemon.json', 'r') as f:
            d = json.load(f)
    except Exception:
        pass
d['dns'] = ['$ns']
with open('/etc/docker/daemon.json', 'w') as f:
    json.dump(d, f, indent=2)
" 2>/dev/null || true
    fi
  fi

  found_ca=false
  for cand in /usr/local/share/ca-certificates/grain-ca.crt /etc/ssl/certs/grain-ca.pem; do
    if [[ -f "$cand" ]] && openssl x509 -in "$cand" -text -noout 2>/dev/null | grep -q "Grain Internal Root CA"; then
      sudo cp -L "$cand" /usr/local/share/ca-certificates/grain-ca.crt 2>/dev/null || true
      found_ca=true
      break
    fi
  done
  if [[ $found_ca != true ]]; then
    target_host=${ns:-172.17.0.1}
    openssl s_client -connect "$target_host:443" -showcerts </dev/null 2>/dev/null | \
      awk '/BEGIN CERTIFICATE/,/END CERTIFICATE/{ if (/BEGIN CERTIFICATE/) {cert++}; if (cert==2) print }' | \
      sudo tee /usr/local/share/ca-certificates/grain-ca.crt >/dev/null || true
  fi

  if [[ -f /usr/local/share/ca-certificates/grain-ca.crt ]]; then
    sudo update-ca-certificates 2>/dev/null || true
    for r in "$registry" ghcr.io ghcr.io:443 pkg-containers.githubusercontent.com pkg-containers.githubusercontent.com:443; do
      [[ -n $r ]] || continue
      sudo mkdir -p "/etc/docker/certs.d/$r" "/etc/containerd/certs.d/$r" 2>/dev/null || true
      sudo cp -L /usr/local/share/ca-certificates/grain-ca.crt "/etc/docker/certs.d/$r/ca.crt" 2>/dev/null || true
      sudo cp -L /usr/local/share/ca-certificates/grain-ca.crt "/etc/containerd/certs.d/$r/ca.crt" 2>/dev/null || true
    done
  fi
  sudo env GRAIN_REGISTRY_MIRROR="$grain_registry_mirror" python3 - << 'PYEOF'
import os, sys, urllib.request, json, ssl, base64

registry = sys.argv[1] if len(sys.argv) > 1 and sys.argv[1] else 'ghcr.io'
repo = sys.argv[2] if len(sys.argv) > 2 and sys.argv[2] else 'gke-labs/granule'
user = os.environ.get('GHCR_USER', os.environ.get('GITHUB_USER', ''))
token = os.environ.get('GHCR_TOKEN', os.environ.get('GITHUB_TOKEN', ''))
if not token and os.path.exists('/home/debian/.package-publish-token'):
    try:
        with open('/home/debian/.package-publish-token') as f:
            token = f.read().strip()
    except Exception:
        pass
if not user and os.path.exists('/home/debian/.package-publish-username'):
    try:
        with open('/home/debian/.package-publish-username') as f:
            user = f.read().strip()
    except Exception:
        pass

mirror = os.environ['GRAIN_REGISTRY_MIRROR']
bearer_token = None
# Verified, not CERT_NONE: the request below carries the publish token as
# Basic auth, and an unverified connection hands it to whatever answers
# for the registry's name. grain's CA is in the system store by now
# (update-ca-certificates above), which is what the sandbox's own
# registry endpoint presents; it is loaded explicitly too in case the
# store was not rebuilt.
ctx = ssl.create_default_context()
for ca in ('/usr/local/share/ca-certificates/grain-ca.crt', '/etc/ssl/certs/grain-ca.pem'):
    if os.path.exists(ca):
        try:
            ctx.load_verify_locations(cafile=ca)
        except Exception:
            pass

url = f'https://{registry}/token?service={registry}&scope=repository:{repo}/debian:pull,push'
req = urllib.request.Request(url)
if user and token:
    auth_bytes = f'{user}:{token}'.encode('utf-8')
    req.add_header('Authorization', 'Basic ' + base64.b64encode(auth_bytes).decode('ascii'))

try:
    with urllib.request.urlopen(req, context=ctx, timeout=10) as resp:
        data = json.loads(resp.read().decode())
        bearer_token = data.get('token') or data.get('access_token')
except Exception:
    pass

if not bearer_token and token:
    bearer_token = token

for base_dir in ['/etc/docker/certs.d', '/etc/containerd/certs.d']:
    for r in [registry, 'ghcr.io', 'pkg-containers.githubusercontent.com']:
        target_dir = os.path.join(base_dir, r)
        os.makedirs(target_dir, exist_ok=True)
        hosts_file = os.path.join(target_dir, 'hosts.toml')
        lines = [f'server = "https://{r}"', '']
        if r in ('ghcr.io', registry):
            lines.extend([
                f'[host."{mirror}"]',
                '  capabilities = ["pull", "resolve"]',
                '  override_path = true',
                ''
            ])
        lines.extend([
            f'[host."https://{r}"]',
            '  capabilities = ["pull", "resolve", "push"]'
        ])
        ca_paths = []
        if os.path.exists('/usr/local/share/ca-certificates/grain-ca.crt'):
            ca_paths.append('/usr/local/share/ca-certificates/grain-ca.crt')
        if os.path.exists('/etc/ssl/certs/grain-ca.pem'):
            ca_paths.append('/etc/ssl/certs/grain-ca.pem')
        if ca_paths:
            lines.append(f'  ca = {json.dumps(ca_paths)}')
        if bearer_token and r in (registry, 'ghcr.io'):
            lines.extend([
                f'  [host."https://{r}".header]',
                f'    Authorization = "Bearer {bearer_token}"'
            ])
        # 0600: this file can carry the registry bearer token (or the
        # publish token itself, when the exchange failed), and the
        # default umask would leave it readable by every user here.
        fd = os.open(hosts_file, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
        os.fchmod(fd, 0o600)
        with os.fdopen(fd, 'w') as f:
            f.write('\n'.join(lines) + '\n')
PYEOF
  sudo systemctl restart containerd docker 2>/dev/null || sudo systemctl restart docker 2>/dev/null || sudo systemctl reload docker 2>/dev/null || sudo kill -HUP "$(pidof dockerd 2>/dev/null)" 2>/dev/null || true
}

if is_grain_sandbox; then
  # github.com is reached through grain's proxy cache rather than
  # directly here, and the Dockerfile only goes there when told to.
  github_proxy=$(grain_nameserver)
  if ! command -v sudo >/dev/null 2>&1; then
    echo "warning: this looks like a grain sandbox but has no sudo; leaving its DNS, CA and registry mirror alone" >&2
  elif [[ $dry_run == true ]]; then
    echo "+ grain sandbox setup: nameserver, /etc/hosts, CA and registry mirror for $registry"
  else
    echo "grain sandbox: pointing this host and docker at grain's nameserver, CA and registry mirror"
    grain_sandbox_setup
  fi
fi

# The per-commit tag is the one a consumer pins a derived guest to, so it
# has to name a commit that really produced this image. A dirty tree
# cannot honour that: either stop, or say so in the tag itself.
#
# This is about the packed guests only. The rootfs's own immutable tag is
# a hash of the recipe in the working tree -- the tree that is about to be
# built -- so an uncommitted change to the rootfs stages produces a key
# that describes it correctly, with no commit to be wrong about and no
# "-dirty" to add. An uncommitted change to anything else does not reach
# the rootfs at all, which is the property this whole change rests on.
commit=$(git rev-parse HEAD)
dirty_suffix=
if [[ -n $(git status --porcelain) ]]; then
  # A dry run is allowed to describe a publish it would refuse to make:
  # printing the tags is how somebody checks them before committing.
  if [[ $allow_dirty != true && $dry_run != true ]]; then
    echo "the working tree has uncommitted changes, so the packed guests' <image>-<commit> tags would name a commit that did not build these images." >&2
    echo "commit them, or pass --allow-dirty to publish anyway." >&2
    exit 1
  fi
  dirty_suffix=-dirty
  echo "warning: publishing from a dirty tree; immutable tags will end in -dirty" >&2
fi
# Not fatal: a publish from a commit nobody else has is legitimate in a
# hurry, and unpleasant to discover afterwards from the tag alone.
if [[ -z $(git branch -r --contains HEAD 2>/dev/null) ]]; then
  echo "warning: $commit is on no remote branch, so the immutable tags name a commit only this machine has" >&2
fi

echo "publishing to $image_base from $commit"
echo "images: ${requested[*]}"
for variant in "${guest_selected[@]-}"; do
  [[ -n $variant ]] || continue
  echo "  $variant rootfs: $image_base/$variant.rootfs:$(guest_rootfs_immutable_tag "$variant")"
done
[[ $push == true ]] || echo "(--no-push: building and tagging only)"

if [[ $push == true && $do_login == true ]]; then
  token=${GHCR_TOKEN:-${GITHUB_TOKEN:-}}
  user=${GHCR_USER:-${GITHUB_USER:-}}
  if [[ -z $token && -f /home/debian/.package-publish-token ]]; then
    token=$(cat /home/debian/.package-publish-token)
  fi
  if [[ -z $user && -f /home/debian/.package-publish-username ]]; then
    user=$(cat /home/debian/.package-publish-username)
  fi
  if [[ -z $token || -z $user ]]; then
    # A dry run has nothing to authenticate to, so missing credentials
    # are worth saying and not worth stopping for.
    echo "set GHCR_USER and GHCR_TOKEN (a token with write:packages) to log in to $registry," >&2
    echo "or pass --no-login if this docker daemon is already authenticated." >&2
    [[ $dry_run == true ]] || exit 1
  elif [[ $dry_run == true ]]; then
    echo "+ docker login $registry -u $user --password-stdin"
  else
    printf '%s' "$token" | docker login "$registry" -u "$user" --password-stdin
  fi
fi

published=()
skipped=()

for variant in "${guest_selected[@]-}"; do
  [[ -n $variant ]] || continue
  variant_image="$image_base/$variant"
  moving="$variant_image:latest"
  immutable="$variant_image:$commit$dirty_suffix"
  build=(docker build --pull --file Dockerfile --tag "$moving" --tag "$immutable")
  for arg in $(guest_build_args "$variant"); do
    build+=(--build-arg "$arg")
  done
  [[ -z $github_proxy ]] || build+=(--build-arg "GITHUB_PROXY=$github_proxy")
  build+=(.)
  echo "== building $variant"
  run "${build[@]}"
  published+=("$moving" "$immutable")

  # The rootfs the disk above was packed from, as an image in its own
  # right: `guest-rootfs-image` is the `guest-customized` stage the build
  # above already ran through, plus labels, so this is a tag over layers
  # that exist and not a second build. The build args are the same ones
  # for that reason and one more: a GUEST_KERNEL_PACKAGE build installs
  # that kernel into the rootfs, so "debian12" is a rootfs of its own
  # rather than an alias for "debian".
  #
  # No --pull here, deliberately. The build above already pulled, and
  # re-pulling could catch an upstream tag that moved in between -- which
  # would publish a rootfs image built on a different base than the disk
  # that was just packed from it, under tags that claim to be two halves
  # of one build.
  #
  # The immutable tag here is the rootfs content key and not the commit:
  # what is inside this image is an operating system, and the commit is
  # the wrong name for one. The labels are what a commit is still good
  # for -- given a keyed tag, they say which tree built it -- and they
  # are applied here rather than written into the Dockerfile so that a
  # `docker build --target guest-rootfs-image` from a checkout is not
  # left claiming a provenance nobody gave it.
  rootfs_image="$image_base/$variant.rootfs"
  rootfs_moving="$rootfs_image:latest"
  rootfs_keyed="$rootfs_image:$(guest_rootfs_immutable_tag "$variant")"
  rootfs_build=(docker build --file Dockerfile --target guest-rootfs-image --tag "$rootfs_moving" --tag "$rootfs_keyed")
  rootfs_build+=(--label "org.opencontainers.image.revision=$commit$dirty_suffix")
  rootfs_build+=(--label "dev.granule.guest.rootfs.key=$(guest_rootfs_key "$variant")")
  for arg in $(guest_build_args "$variant"); do
    rootfs_build+=(--build-arg "$arg")
  done
  rootfs_build+=(.)
  echo "== tagging the $variant guest rootfs ($rootfs_keyed)"
  run "${rootfs_build[@]}"
  # With nothing to push, this is the whole outcome for the rootfs; with
  # a push ahead of it the decision is the push loop's, since a key
  # already on the registry is not republished.
  [[ $push == true ]] || published+=("$rootfs_moving" "$rootfs_keyed")
done

if [[ $push == true ]]; then
  for variant in "${guest_selected[@]-}"; do
    [[ -n $variant ]] || continue
    variant_image="$image_base/$variant"
    echo "== pushing $variant"
    push_image "$variant_image:latest"
    push_image "$variant_image:$commit$dirty_suffix"

    # The rootfs is pushed as a pair or not at all. Its moving tag means
    # "the current rootfs for this variant", and the keyed tag is that
    # same image under a name that says which recipe built it -- so
    # pushing one without the other is what would make them disagree.
    # When the key is already published, both are already right: the
    # moving tag points at that image because the publish that pushed it
    # pushed both.
    rootfs_image="$image_base/$variant.rootfs"
    rootfs_moving="$rootfs_image:latest"
    rootfs_keyed="$rootfs_image:$(guest_rootfs_immutable_tag "$variant")"
    if rootfs_already_published "$rootfs_keyed"; then
      echo "   $rootfs_keyed is already published: the $variant rootfs recipe has not changed, so neither it nor $rootfs_moving is pushed."
      echo "   (ROOTFS_REVISION mints a new key when upstream has moved under an unchanged recipe; --force-rootfs overwrites this one.)"
      skipped+=("$rootfs_keyed")
    else
      push_image "$rootfs_moving"
      push_image "$rootfs_keyed"
      published+=("$rootfs_moving" "$rootfs_keyed")
    fi
  done
fi

# Runner images build, self-test and push one at a time inside build.sh,
# which is where the boot gate lives: nothing it produces may be pushed
# until the converted filesystem has booted as a granule guest. So these
# are not split into a build phase and a push phase the way the guests
# above are -- the ordering is build.sh's to decide, not this script's.
for label in "${runner_selected[@]-}"; do
  [[ -n $label ]] || continue
  short=${label#github-}
  if [[ $label == github-ubuntu-slim ]]; then
    echo "== building $label"
  else
    echo "== building $label (a Packer run and a 75 GB export: hours, not minutes)"
  fi
  if [[ $dry_run == true ]]; then
    printf '+ OUTPUT_IMAGE=%s PUSH=%s GITHUB_PROXY=%s deploy/github-runner-images/build.sh %s\n' \
      "$image_base/$label:latest" "$push" "$github_proxy" "$short"
  else
    OUTPUT_IMAGE="$image_base/$label:latest" \
    PUSH="$push" \
    GITHUB_PROXY="$github_proxy" \
      deploy/github-runner-images/build.sh "$short"
  fi
  # build.sh also tags each runner image with the actions/runner-images
  # commit it resolved, which only it knows; it names that tag in its own
  # last line rather than here.
  published+=("$image_base/$label:latest")
done

echo
if [[ $push == true ]]; then
  echo "published:"
else
  echo "built (not pushed):"
fi
printf '  %s\n' "${published[@]}"
if [[ ${#skipped[@]} -gt 0 ]]; then
  echo
  echo "already published, left alone:"
  printf '  %s\n' "${skipped[@]}"
fi
