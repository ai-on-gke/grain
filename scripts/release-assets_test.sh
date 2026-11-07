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

# Drives scripts/release-assets.sh against a fake docker and a local git
# remote, in dry-run mode, with no GitHub token: packaging every asset and
# a manifest that meets the release-asset contract (v2), refusing manifests that
# do not, and planning the publish, promote and retag of every release a
# build, a promotion and the release train make -- with no registry push
# anywhere. tests/deploy runs it (TestReleaseTrainShellSuites).
# The cases are eval'd strings, expanded when they run.
# shellcheck disable=SC2016,SC2034
set -euo pipefail

assets="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/release-assets.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

export GIT_AUTHOR_NAME=test GIT_AUTHOR_EMAIL=test@example.com
export GIT_COMMITTER_NAME=test GIT_COMMITTER_EMAIL=test@example.com
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
unset GITHUB_TOKEN GH_TOKEN DRY_RUN
export GITHUB_REPOSITORY=ai-on-gke/grain

failures=0
cases=0
out=""
status=0

run() {
  set +e
  out="$("$@" 2>&1)"
  status=$?
  set -e
}

check() {
  cases=$((cases + 1))
  if eval "$2"; then
    echo "ok   $1"
  else
    echo "FAIL $1"
    printf '%s\n' "$out" | sed 's/^/     /'
    failures=$((failures + 1))
  fi
}

# A docker that knows three images and logs every call; `push` and
# `login` fail the test outright.
mkdir -p "$work/bin"
cat > "$work/bin/docker" <<'EOF'
#!/usr/bin/env bash
echo "docker $*" >> "$DOCKER_LOG"
case "$1 ${2:-}" in
  "image inspect")
    ref="${!#}"
    printf 'sha256:%s\n' "$(printf '%s' "$ref" | sha256sum | cut -c1-64)"
    ;;
  "save "*) printf 'tarball of %s\n' "$2"; head -c 4096 /dev/zero ;;
  "load -i") cat "$3" > /dev/null ;;
  tag*) ;;
  push* | login*) echo "docker $1 must never run" >&2; exit 99 ;;
  *) echo "fake docker: unexpected $*" >&2; exit 98 ;;
esac
EOF
chmod +x "$work/bin/docker"
export PATH="$work/bin:$PATH" DOCKER_LOG="$work/docker.log"
: > "$DOCKER_LOG"

# A remote with a commit, a release tag and `latest` on it.
git init -q --bare "$work/remote.git"
git init -q "$work/repo"
cd "$work/repo"
git commit -q --allow-empty -m one
first="$(git rev-parse HEAD)"
git commit -q --allow-empty -m two
commit="$(git rev-parse HEAD)"
short="${commit:0:12}"
git tag -a v3.4.0 -m v3.4.0
git tag -a latest -m latest
git remote add origin "$work/remote.git"
git push -q origin HEAD:refs/heads/main --tags

export RELEASE_IMAGES_STATE="$work/state.env" RELEASE_ASSETS_DIR="$work/assets"
cat > "$RELEASE_IMAGES_STATE" <<EOF
RELEASE_IMAGE_COMMIT='$commit'
RELEASE_IMAGE_TARGET='assets'
RELEASE_IMAGE_VERSION='v3.5.0-dev.2+${commit:0:7}'
RELEASE_IMAGE_GRAIN='grain/grain:sha-$short'
RELEASE_IMAGE_GUEST='grain/guest:sha-$short'
RELEASE_IMAGE_K8S_SANDBOX='grain/k8s-sandbox:sha-$short'
EOF

run bash "$assets" package
check "package writes every asset, sidecar and images.json" \
  '[ "$status" = 0 ] && for f in images.json grain-image.tar.gz guest-image.tar.gz k8s-sandbox-image.tar.gz; do [ -s "$RELEASE_ASSETS_DIR/$f" ] || exit 1; done; for f in grain guest k8s-sandbox; do [ -s "$RELEASE_ASSETS_DIR/$f-image.tar.gz.sha256" ] || false; done'
check "sidecars are in sha256sum format and verify" \
  '(cd "$RELEASE_ASSETS_DIR" && sha256sum -c --quiet ./*.sha256)'
m="$RELEASE_ASSETS_DIR/images.json"
check "images.json is schema 2 for this repository, commit and version" \
  '[ "$(jq -r "[.schema, .repository, .commit, .version] | @tsv" "$m")" = "$(printf "2\tgithub.com/ai-on-gke/grain\t%s\tv3.5.0-dev.2+%s" "$commit" "${commit:0:7}")" ]'
check "images.json names local refs, the gzip sha256 and the image ID" \
  '[ "$(jq -r .images.guest.ref "$m")" = "grain/guest:sha-$short" ] &&
   [ "$(jq -r .images["\"k8s-sandbox\""].ref "$m")" = "grain/k8s-sandbox:sha-$short" ] &&
   [ "$(jq -r .images.grain.sha256 "$m")" = "$(sha256sum "$RELEASE_ASSETS_DIR/grain-image.tar.gz" | cut -c1-64)" ] &&
   [[ "$(jq -r .images.grain.imageId "$m")" =~ ^sha256:[0-9a-f]{64}$ ]] &&
   [ "$(jq -r "[.images[].release] | unique | .[]" "$m")" = "sha-$short" ]'
check "the assets are gzip" 'gzip -t "$RELEASE_ASSETS_DIR/guest-image.tar.gz"'

run bash "$assets" validate
check "validate accepts what package wrote" '[ "$status" = 0 ]'

bad() {
  rm -rf "$work/bad"
  cp -r "$RELEASE_ASSETS_DIR" "$work/bad"
  jq "$1" "$RELEASE_ASSETS_DIR/images.json" > "$work/bad/images.json"
  want="$3"
  run bash "$assets" validate "$work/bad"
  check "validate refuses $2" '[ "$status" != 0 ] && grep -q -- "$want" <<< "$out"'
}
bad '.schema = 3' "another schema" "schema is 3"
bad '.images.guest.release = "channel-main"' "an image held by a moving release" "guest.release"
bad '.schema = 1' "a schema 1 manifest naming releases" "which schema 1 does not"
bad '.images.guest.ref = "ghcr.io/x/grain/guest:sha-1@sha256:'"$(printf '0%.0s' {1..64})"'"' "a registry pin" "guest.ref"
bad '.images.guest.ref = "ghcr.io/guest:sha-'"$short"'"' "a ref naming a registry" "names a registry host"
bad 'del(.images["k8s-sandbox"])' "a missing image" "not grain, guest and k8s-sandbox"
bad '.images.grain.sha256 = "'"$(printf 'a%.0s' {1..64})"'"' "a sha256 the asset does not have" "grain-image.tar.gz.sha256"
bad '.commit = "abc"' "a short commit" "40-hex"
bad '.createdAt = "yesterday"' "a createdAt that is not RFC 3339" "createdAt"

run env RELEASE_ASSETS_MAX_BYTES=10 bash "$assets" package
check "an image over the per-asset limit fails, naming the limit" '[ "$status" != 0 ] && grep -q "per-asset limit" <<< "$out"'
bash "$assets" package > /dev/null

run bash "$assets" --dry-run publish "sha-$short" channel-main
check "a dry-run publish puts every asset on the sha- release and only images.json on the channel" \
  '[ "$status" = 0 ] && [ "$(grep -c "would POST .*/assets --upload" <<< "$out")" = 8 ] &&
   [ "$(sed -n "/channel-main: holds/,\$p" <<< "$out" | grep -c "would POST .*--upload")" = 1 ] &&
   sed -n "/channel-main: holds/,\$p" <<< "$out" | grep -q -- "--upload images.json"'
check "images.json is the last asset the sha- release gets" \
  '[ "$(sed -n "1,/published sha-/p" <<< "$out" | grep "would POST .*--upload" | tail -n 1 | grep -c images.json)" = 1 ]'
check "every release is created as a prerelease that can never be latest" \
  '[ "$(grep -c "\"prerelease\":true,\"make_latest\":\"false\"" <<< "$out")" = 2 ]'
check "a channel moves by uploading images.json.next and renaming it into place" \
  'sed -n "/channel-main: holds/,\$p" <<< "$out" | grep -q -- "--upload images.json.next" &&
   sed -n "/channel-main: holds/,\$p" <<< "$out" | grep -q "would rename images.json.next to images.json"'
check "nothing was pushed to a registry" '! grep -qE "docker (push|login|tag)" "$DOCKER_LOG"'

run bash "$assets" --dry-run publish main
check "a release named after a bare branch is refused" '[ "$status" != 0 ] && grep -q "never a bare branch name" <<< "$out"'
run bash "$assets" --dry-run publish latest
check "there is no release named latest" '[ "$status" != 0 ] && grep -q "no release named latest" <<< "$out"'
run bash "$assets" publish "sha-$short"
check "a real publish with no token fails before writing anything" '[ "$status" != 0 ] && grep -q "needs GITHUB_TOKEN" <<< "$out"'

run bash "$assets" --dry-run promote
check "promote copies the sha- release's assets onto vX.Y.Z and marks it GitHub's latest" \
  '[ "$status" = 0 ] && grep -q "promoting sha-$short as v3.4.0" <<< "$out" && grep -q "published v3.4.0" <<< "$out" &&
   grep -q "\"prerelease\":false,\"make_latest\":\"true\"" <<< "$out" && [ "$(grep -c "would POST .*--upload" <<< "$out")" = 7 ] &&
   [ "$(grep "would POST .*--upload" <<< "$out" | tail -n 1 | grep -c "upload images.json$")" = 1 ] &&
   ! grep -q "release latest" <<< "$out"'
check "a promoted manifest names vX.Y.Z as its version and as every image's release" \
  '[ "$(jq -r .version "$m")" = v3.4.0 ] && [ "$(jq -r .commit "$m")" = "$commit" ] &&
   [ "$(jq -r "[.images[].release] | unique | join(\",\")" "$m")" = v3.4.0 ] && bash "$assets" validate > /dev/null'

git tag -a v3.3.0 -m v3.3.0 "$first"
git push -q origin v3.3.0
rm -rf "$RELEASE_ASSETS_DIR"
run bash "$assets" --dry-run promote v3.3.0
check "promoting a release whose sha- assets cannot be fetched fails closed" '[ "$status" != 0 ]'

bash "$assets" package > /dev/null
run bash "$assets" --dry-run retag "sha-$short" v3.5.0-rc.1 v3.5.0-rc.1 channel-candidate
check "retag points the release train's rc and channel-candidate at the sha- release" \
  '[ "$status" = 0 ] && grep -q "published v3.5.0-rc.1" <<< "$out" && grep -q "published channel-candidate" <<< "$out" && [ "$(jq -r .version "$m")" = v3.5.0-rc.1 ] &&
   [ "$(grep -c "would POST .*--upload" <<< "$out")" = 2 ] && ! grep -q "upload .*tar.gz" <<< "$out"'
run bash "$assets" --dry-run retag "sha-$short" v3.5.0 v3.5.0-pending
check "retag plans a pending release" '[ "$status" = 0 ] && grep -q "published v3.5.0-pending" <<< "$out"'
run bash "$assets" --dry-run retag "sha-$short" v3.5.0-rc.1 v3.5.0-rc.1-discarded
check "retag plans a discarded rc marker" '[ "$status" = 0 ] && grep -q "published v3.5.0-rc.1-discarded" <<< "$out"'

git tag -a v3.5.0-rc.1 -m rc1 "$first"
git tag -a v3.5.0-rc.2 -m rc2 "$commit"
git tag -a v3.5.0-pending -m pending "$commit"
git push -q origin v3.5.0-rc.1 v3.5.0-rc.2 v3.5.0-pending
bash "$assets" package > /dev/null
run bash "$assets" --dry-run rc v3.5.0-rc.2
check "the train's newest rc is published as itself and moves channel-candidate" \
  '[ "$status" = 0 ] && grep -q "published v3.5.0-rc.2" <<< "$out" && grep -q "published channel-candidate" <<< "$out"'
run bash "$assets" --dry-run train-tag v3.5.0-pending
check "a pushed -pending tag publishes vX.Y.Z-pending with version vX.Y.Z" \
  '[ "$status" = 0 ] && grep -q "published v3.5.0-pending" <<< "$out" && [ "$(jq -r .version "$m")" = v3.5.0 ]'
run bash "$assets" --dry-run train-tag v3.5.0-rc.2-discarded
check "a pushed -discarded tag publishes the discarded marker" '[ "$status" = 0 ] && grep -q "published v3.5.0-rc.2-discarded" <<< "$out"'
for promoted in v3.4.0 latest; do
  run bash "$assets" --dry-run train-tag "$promoted"
  check "train-tag refuses to promote $promoted (that is promote.yml's and promote-release.yml's)" \
    '[ "$status" != 0 ] && grep -q "not a release-train tag" <<< "$out" && ! grep -q "published" <<< "$out"'
done
run bash "$assets" --dry-run discard-pending v3.5.0
check "a rejected pending release gets its -rejected marker" '[ "$status" = 0 ] && grep -q "published v3.5.0-rejected" <<< "$out"'
run bash "$assets" --dry-run retract v3.4.0
check "a retracted release gets its -retracted marker" '[ "$status" = 0 ] && grep -q "published v3.4.0-retracted" <<< "$out"'
run bash "$assets" --dry-run rc v3.5.0-rc.1
check "an older rc never moves channel-candidate (and its commit's assets must be fetched)" \
  '[ "$status" != 0 ] || ! grep -q "published channel-candidate" <<< "$out"'
check "the train phases pushed nothing to a registry" '! grep -qE "docker (push|login|tag)" "$DOCKER_LOG"'
: > "$DOCKER_LOG"

bash "$assets" package > /dev/null
run env RELEASE_ASSETS_REUSE=true bash "$assets" --dry-run mirror "sha-$short" registry.example/proj/grain k8s-sandbox
check "mirror retags a release's image into the deployment's registry" \
  '[ "$status" = 0 ] && grep -qx "k8s-sandbox=registry.example/proj/grain/k8s-sandbox:sha-$short" <<< "$out"'

bash "$assets" package > /dev/null
run bash "$assets" --dry-run prune
check "prune plans retention without writing anything" '[ "$status" = 0 ] && grep -q "keep the newest 50" <<< "$out"'
run env RELEASE_RETAIN_SHA=7 bash "$assets" --dry-run prune
check "RELEASE_RETAIN_SHA sets how many sha- releases prune keeps" '[ "$status" = 0 ] && grep -q "keep the newest 7" <<< "$out"'
run env RELEASE_RETAIN_SHA=many bash "$assets" --dry-run prune
check "a RELEASE_RETAIN_SHA that is not a number fails" '[ "$status" != 0 ] && grep -q "RELEASE_RETAIN_SHA=many is not a number" <<< "$out"'

run bash "$assets" load
check "load checks every image ID the manifest names" '[ "$status" = 0 ] && [ "$(grep -c "^release-assets.sh: loaded" <<< "$out")" = 3 ]'

echo "$((cases - failures)) of $cases passed"
[ "$failures" = 0 ]
