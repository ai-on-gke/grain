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

# run-item3.sh executes docs/test-plans/2026-11-07.md §9 Item 3:
#   - item3_ci_probe.go (3A..3D: qualification stanza #831, scratch push grants
#     #621, superseded release.yml #775, review prompt #699, branch-scoped
#     completed-run polling #819, triage ancestry #745, empty Actions logs #828)
#   - scripts/candidate-gate.sh check-qualification numeric vs slug
#     CHECK_QUALIFICATION_APP (#642) in an isolated /tmp scratch tree
#   - scripts/release-images.sh on-demand (#775) in a /tmp scratch clone
#     against a live local OCI v2 registry (127.0.0.1:4286)
set -euo pipefail

REPO_ROOT="/home/debian/work"
HARNESS_DIR="$REPO_ROOT/docs/test-runs/2026-11-07-releases-upgrades-ci"
TMP_ROOT="$(mktemp -d /tmp/item3-run-XXXXXX)"
OCI_PID=""

cleanup() {
  if [[ -n "$OCI_PID" ]] && kill -0 "$OCI_PID" 2>/dev/null; then
    kill "$OCI_PID" 2>/dev/null || true
    wait "$OCI_PID" 2>/dev/null || true
  fi
  rm -rf "$TMP_ROOT"
}
trap cleanup EXIT

cd "$REPO_ROOT"

echo "=== Step 1: Run item3_ci_probe.go (3A..3D) ==="
go run "$HARNESS_DIR/item3_ci_probe.go" "$REPO_ROOT"

echo "=== Step 2: Exercise candidate-gate.sh check-qualification CHECK_QUALIFICATION_APP numeric vs slug (#642) ==="
SCRATCH_SCRIPTS="$TMP_ROOT/scripts"
mkdir -p "$SCRATCH_SCRIPTS"
cp "$REPO_ROOT"/scripts/*.sh "$REPO_ROOT"/scripts/release.conf "$SCRATCH_SCRIPTS/"

RC_SHA="a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"
RUNS_JSON="$TMP_ROOT/stage1-green.json"
cat >"$RUNS_JSON" <<EOF
{
  "jobs": [
    {"id": 101, "name": "go, shuffled and repeated", "head_sha": "$RC_SHA", "status": "completed", "conclusion": "success", "started_at": "2026-11-07T18:00:00Z"}
  ]
}
EOF

CHECKS_SPOOF_SLUG_42="$TMP_ROOT/checks-spoof-slug-42.json"
cat >"$CHECKS_SPOOF_SLUG_42" <<EOF
{
  "check_runs": [
    {
      "id": 501,
      "name": "grain/release-qualification",
      "head_sha": "$RC_SHA",
      "status": "completed",
      "conclusion": "success",
      "started_at": "2026-11-07T18:05:00Z",
      "app": {"id": 999, "slug": "42"}
    }
  ]
}
EOF

CHECKS_REAL_ID_42="$TMP_ROOT/checks-real-id-42.json"
cat >"$CHECKS_REAL_ID_42" <<EOF
{
  "check_runs": [
    {
      "id": 502,
      "name": "grain/release-qualification",
      "head_sha": "$RC_SHA",
      "status": "completed",
      "conclusion": "failure",
      "started_at": "2026-11-07T18:04:00Z",
      "app": {"id": 999, "slug": "evil-bot"}
    },
    {
      "id": 503,
      "name": "grain/release-qualification",
      "head_sha": "$RC_SHA",
      "status": "completed",
      "conclusion": "success",
      "started_at": "2026-11-07T18:05:00Z",
      "app": {"id": 42, "slug": "grain-bot"}
    }
  ]
}
EOF

# 2a. Unset CHECK_QUALIFICATION_APP must exit 64.
set +e
UNSET_OUT="$(CHECK_QUALIFICATION_JOBS='go-long=go, shuffled and repeated' CHECK_QUALIFICATION_APP='' \
  bash "$SCRATCH_SCRIPTS/candidate-gate.sh" check-qualification \
  --rc-tag v3.2.0-rc.1 --rc-sha "$RC_SHA" --runs-json "$RUNS_JSON" --checks-json "$CHECKS_REAL_ID_42" 2>&1)"
UNSET_RC=$?
set -e
if [[ "$UNSET_RC" -ne 64 ]]; then
  echo "FAIL: expected exit 64 when CHECK_QUALIFICATION_APP is unset, got $UNSET_RC: $UNSET_OUT" >&2
  exit 1
fi
echo "PASS 3E.1: unset CHECK_QUALIFICATION_APP refused with exit 64"

# 2b. Numeric CHECK_QUALIFICATION_APP=42 MUST NOT match app.slug="42" with app.id=999 (#642)!
set +e
SPOOF_OUT="$(CHECK_QUALIFICATION_JOBS='go-long=go, shuffled and repeated' CHECK_QUALIFICATION_APP='42' \
  bash "$SCRATCH_SCRIPTS/candidate-gate.sh" check-qualification \
  --rc-tag v3.2.0-rc.1 --rc-sha "$RC_SHA" --runs-json "$RUNS_JSON" --checks-json "$CHECKS_SPOOF_SLUG_42" 2>&1)"
SPOOF_RC=$?
set -e
if [[ "$SPOOF_RC" -ne 2 ]] || [[ "$SPOOF_OUT" != *"IGNORED"* ]] || [[ "$SPOOF_OUT" != *"Qualification: PENDING"* ]]; then
  echo "FAIL: numeric CHECK_QUALIFICATION_APP=42 trusted spoofed slug='42' (rc=$SPOOF_RC): $SPOOF_OUT" >&2
  exit 1
fi
echo "PASS 3E.2: numeric CHECK_QUALIFICATION_APP=42 ignored spoofed app{id:999, slug:'42'} -> PENDING (exit 2)"

# 2c. Numeric CHECK_QUALIFICATION_APP=42 matches app.id=42 while ignoring evil-bot failure.
NUM_OUT="$(CHECK_QUALIFICATION_JOBS='go-long=go, shuffled and repeated' CHECK_QUALIFICATION_APP='42' \
  bash "$SCRATCH_SCRIPTS/candidate-gate.sh" check-qualification \
  --rc-tag v3.2.0-rc.1 --rc-sha "$RC_SHA" --runs-json "$RUNS_JSON" --checks-json "$CHECKS_REAL_ID_42" 2>&1)"
if [[ "$NUM_OUT" != *"IGNORED"* ]] || [[ "$NUM_OUT" != *"Qualification: PASS"* ]]; then
  echo "FAIL: numeric CHECK_QUALIFICATION_APP=42 did not pass genuine app.id=42: $NUM_OUT" >&2
  exit 1
fi
echo "PASS 3E.3: numeric CHECK_QUALIFICATION_APP=42 trusted app{id:42, slug:'grain-bot'} and ignored evil-bot -> PASS"

# 2d. Slug CHECK_QUALIFICATION_APP=grain-bot matches app.slug="grain-bot" while ignoring evil-bot.
SLUG_OUT="$(CHECK_QUALIFICATION_JOBS='go-long=go, shuffled and repeated' CHECK_QUALIFICATION_APP='grain-bot' \
  bash "$SCRATCH_SCRIPTS/candidate-gate.sh" check-qualification \
  --rc-tag v3.2.0-rc.1 --rc-sha "$RC_SHA" --runs-json "$RUNS_JSON" --checks-json "$CHECKS_REAL_ID_42" 2>&1)"
if [[ "$SLUG_OUT" != *"IGNORED"* ]] || [[ "$SLUG_OUT" != *"Qualification: PASS"* ]]; then
  echo "FAIL: slug CHECK_QUALIFICATION_APP=grain-bot did not pass genuine slug: $SLUG_OUT" >&2
  exit 1
fi
echo "PASS 3E.4: slug CHECK_QUALIFICATION_APP=grain-bot trusted app{slug:'grain-bot'} and ignored evil-bot -> PASS"

echo "=== Step 3: Exercise release-images.sh on-demand (#775) in /tmp scratch clone against live OCI registry ==="
OCI_BIN="$TMP_ROOT/ociregistry"
go build -o "$OCI_BIN" "$HARNESS_DIR/ociregistry.go"
"$OCI_BIN" -addr 127.0.0.1:4286 -name hub >"$TMP_ROOT/oci.log" 2>&1 &
OCI_PID=$!
for _ in $(seq 1 30); do
  if curl -fsS http://127.0.0.1:4286/v2/ >/dev/null 2>&1; then
    break
  fi
  sleep 0.1
done

SCRATCH_BARE="$TMP_ROOT/remote.git"
SCRATCH_CLONE="$TMP_ROOT/clone"
git init --bare -q --initial-branch=main "$SCRATCH_BARE"
git clone -q "$REPO_ROOT" "$SCRATCH_CLONE"
git -C "$SCRATCH_CLONE" remote set-url origin "$SCRATCH_BARE"
git -C "$SCRATCH_CLONE" config user.name "Scratch Bot"
git -C "$SCRATCH_CLONE" config user.email "scratch@example.com"
echo "# superseded commit" >>"$SCRATCH_CLONE/README.md"
git -C "$SCRATCH_CLONE" add README.md
git -C "$SCRATCH_CLONE" commit -q -m "superseded release commit"
SUPERSEDED_SHA="$(git -C "$SCRATCH_CLONE" rev-parse HEAD)"
SUPERSEDED_SHORT="${SUPERSEDED_SHA:0:7}"
git -C "$SCRATCH_CLONE" tag v3.2.0 "$SUPERSEDED_SHA"
git -C "$SCRATCH_CLONE" tag -f latest "$SUPERSEDED_SHA"
git -C "$SCRATCH_CLONE" push -q origin HEAD:refs/heads/main refs/tags/v3.2.0 refs/tags/latest

# Build tiny base OCI image once so our stub `gh workflow run` can push :sha-<short> into 127.0.0.1:4286 on demand.
IMG_CTX="$TMP_ROOT/imgctx"
mkdir -p "$IMG_CTX"
cat >"$IMG_CTX/Dockerfile" <<EOF
FROM scratch
LABEL org.opencontainers.image.revision=$SUPERSEDED_SHA
COPY marker /marker
EOF
echo "ok" >"$IMG_CTX/marker"
docker build -q -t "127.0.0.1:4286/grainai/seed:base" "$IMG_CTX" >/dev/null

GH_BIN_DIR="$TMP_ROOT/bin"
GH_LOG="$TMP_ROOT/gh.log"
RUNS_STATE="$TMP_ROOT/gh-runs.txt"
mkdir -p "$GH_BIN_DIR"
cat >"$GH_BIN_DIR/gh" <<EOF
#!/usr/bin/env bash
set -euo pipefail
echo "\$*" >>"$GH_LOG"
case "\${1:-} \${2:-}" in
  "run list")
    [[ -f "$RUNS_STATE" ]] && cat "$RUNS_STATE"
    exit 0
    ;;
  "workflow run")
    for img in sandbox sandbox-k8s grain registry; do
      docker tag "127.0.0.1:4286/grainai/seed:base" "127.0.0.1:4286/grainai/\$img:sha-$SUPERSEDED_SHORT"
      docker push -q "127.0.0.1:4286/grainai/\$img:sha-$SUPERSEDED_SHORT" >/dev/null
    done
    echo "completed success http://127.0.0.1:4286/actions/runs/775" >"$RUNS_STATE"
    exit 0
    ;;
esac
exit 0
EOF
chmod +x "$GH_BIN_DIR/gh"

(
  cd "$SCRATCH_CLONE"
  PATH="$GH_BIN_DIR:$PATH" \
  PACKAGE_PUBLISH_REGISTRY="127.0.0.1:4286" \
  GRAIN_CI_REPO="ai-on-gke/grain" \
  RELEASE_ON_DEMAND_BUILD="dispatch" \
  bash scripts/release-images.sh on-demand v3.2.0
)

if ! grep -q "workflow run release.yml.*commit=$SUPERSEDED_SHA" "$GH_LOG"; then
  echo "FAIL: expected gh workflow run release.yml with commit=$SUPERSEDED_SHA, got:" >&2
  cat "$GH_LOG" >&2
  exit 1
fi
# Confirm :sha-<short> now exists in 127.0.0.1:4286 while :v3.2.0 was NOT moved by `on-demand` itself.
for img in sandbox sandbox-k8s grain registry; do
  curl -fsS -H "Accept: application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json" \
    "http://127.0.0.1:4286/v2/grainai/$img/manifests/sha-$SUPERSEDED_SHORT" >/dev/null
done
if curl -fsS "http://127.0.0.1:4286/v2/grainai/grain/manifests/v3.2.0" >/dev/null 2>&1; then
  echo "FAIL: on-demand phase unexpectedly pushed :v3.2.0 tag itself" >&2
  exit 1
fi
echo "PASS 3F.1: release-images.sh on-demand v3.2.0 dispatched release.yml for superseded commit $SUPERSEDED_SHORT and populated sha- images without moving release tags"

if git -C "$REPO_ROOT" config --local --get user.email >/dev/null 2>&1; then
  echo "FAIL: /home/debian/work/.git/config has local user.email override!" >&2
  exit 1
fi
echo "PASS 3F.2: /home/debian/work/.git/config hygiene preserved"
echo "ALL ITEM 3 CHECKS PASSED"
