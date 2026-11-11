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

# End-to-end live verification for docs/test-plans/2026-11-07.md §9 Item 2:
#   - Legacy GHCR -> Docker Hub alias transition in installtrack, upgrade,
#     scripts/release-image-repos.sh, and a live grain daemon (#739)
#   - Docker Hub v2 API pruning across grain, sandbox*, and registry (#739, #780)
#   - Isolated DOCKER_CONFIG login handling (#747)
#   - Live OCI v2 promotion of grain + grain-registry (#780) and pre-Docker-Hub
#     GHCR copy-by-digest + signature/digest fail-closed checks (#753, Bug F4)
#
# Never runs git/release mutations inside /home/debian/work; uses /tmp/item2-scratch.

set -euo pipefail

REPO_ROOT="/home/debian/work"
RUN_DIR="$REPO_ROOT/docs/test-runs/2026-11-07-releases-upgrades-ci"
GRAIN_BIN="${GRAIN_BIN:-/home/debian/bin/grain}"
SCRATCH="/tmp/item2-scratch"
HUB_ADDR="127.0.0.1:4281"
GHCR_ADDR="127.0.0.1:4282"
DAEMON_ADDR="127.0.0.1:4283"

HUB_PID=""
GHCR_PID=""
DAEMON_PID=""

cleanup() {
  for p in "$DAEMON_PID" "$HUB_PID" "$GHCR_PID"; do
    if [ -n "$p" ] && kill -0 "$p" 2>/dev/null; then
      kill "$p" 2>/dev/null || true
      wait "$p" 2>/dev/null || true
    fi
  done
  docker image rm -f item2-stub-base:latest >/dev/null 2>&1 || true
  rm -rf "$SCRATCH"
}
trap cleanup EXIT

rm -rf "$SCRATCH"
mkdir -p "$SCRATCH/bin" "$SCRATCH/scripts"

echo "=== [2A] Legacy GHCR -> Docker Hub alias table, live daemon transition & Hub v2 prune ==="
(cd "$REPO_ROOT" && go run "$RUN_DIR/item2_alias_probe.go")

# Exercise scripts/release-image-repos.sh shell functions directly.
bash -c '
  set -euo pipefail
  . "'"$REPO_ROOT"'/scripts/release-image-repos.sh"
  test "$GRAIN_PUBLISHED_IMAGE_PACKAGES" = "grain,sandbox,sandbox-base,sandbox-k8s,sandbox-k8s-base,registry"
  test "$(canonical_oss_image_repo ghcr.io/ai-on-gke/grain/grain)" = "docker.io/grainai/grain"
  test "$(canonical_oss_image_repo ghcr.io/bwsalmon/grain/guest)" = "docker.io/grainai/sandbox"
  test "$(canonical_oss_image_repo ghcr.io/bwsalmon/grain/granule-sandbox)" = "docker.io/grainai/sandbox"
  test "$(canonical_oss_image_repo ghcr.io/bwsalmon/grain/k8s-sandbox)" = "docker.io/grainai/sandbox-k8s"
  test "$(canonical_oss_image_repo us-docker.pkg.dev/proj/repo/grain)" = "us-docker.pkg.dev/proj/repo/grain"
  test "$(legacy_ghcr_image_repo docker.io/grainai/sandbox)" = "ghcr.io/bwsalmon/grain/guest"
  test "$(legacy_ghcr_image_repo docker.io/grainai/registry)" = ""
  test "$(legacy_ghcr_image_for docker.io/grainai/grain:v3.1.0)" = "ghcr.io/bwsalmon/grain/grain:v3.1.0"
  test "$(legacy_ghcr_image_for docker.io/grainai/grain:v3.1.0@sha256:1234)" = ""
  echo "PASS scripts/release-image-repos.sh canonical & legacy mapping checks"
'

# Verify live grain daemon /api/upgrade reports transition from ghcr.io/ai-on-gke/grain/grain -> docker.io/grainai/grain
# while preserving explicit custom private registries.
mkdir -p "$SCRATCH/daemon-data" "$SCRATCH/daemon-sandbox"
printf 'GRAIN_IMAGE=ghcr.io/ai-on-gke/grain/grain:latest\n' > "$SCRATCH/daemon-image.env"
"$GRAIN_BIN" daemon \
  -data-dir "$SCRATCH/daemon-data" \
  -sandbox-dir "$SCRATCH/daemon-sandbox" \
  -ui-addr "$DAEMON_ADDR" \
  -sandbox-port-base=0 \
  -disable-native-sandboxes \
  -upgrade-image "ghcr.io/ai-on-gke/grain/grain" \
  -upgrade-image-ref-file "$SCRATCH/daemon-image.env" \
  >"$SCRATCH/daemon.log" 2>&1 &
DAEMON_PID=$!
for _ in $(seq 1 60); do
  if curl -fsS "http://$DAEMON_ADDR/healthz" >/dev/null 2>&1; then
    break
  fi
  sleep 0.25
done
TOKEN="$("$GRAIN_BIN" access-code -data-dir "$SCRATCH/daemon-data")"
UPG_JSON="$(curl -fsS -H "Authorization: Bearer $TOKEN" "http://$DAEMON_ADDR/api/upgrade")"
printf '%s\n' "$UPG_JSON" | jq -e '
  .running.image == "ghcr.io/ai-on-gke/grain/grain:latest" and
  .imageRepo == "docker.io/grainai/grain" and
  .defaultImageRepo == "docker.io/grainai/grain" and
  .defaultImageRef == "docker.io/grainai/grain:latest"
' >/dev/null
echo "PASS live grain daemon /api/upgrade transitions ghcr.io/ai-on-gke/grain/grain -> docker.io/grainai/grain"
kill "$DAEMON_PID" 2>/dev/null || true
wait "$DAEMON_PID" 2>/dev/null || true
DAEMON_PID=""

mkdir -p "$SCRATCH/daemon-custom-data" "$SCRATCH/daemon-custom-sandbox"
printf 'GRAIN_IMAGE=us-docker.pkg.dev/my-proj/containers/grain:latest\n' > "$SCRATCH/daemon-custom-image.env"
"$GRAIN_BIN" daemon \
  -data-dir "$SCRATCH/daemon-custom-data" \
  -sandbox-dir "$SCRATCH/daemon-custom-sandbox" \
  -ui-addr "$DAEMON_ADDR" \
  -sandbox-port-base=0 \
  -disable-native-sandboxes \
  -upgrade-image "us-docker.pkg.dev/my-proj/containers/grain" \
  -upgrade-image-ref-file "$SCRATCH/daemon-custom-image.env" \
  >"$SCRATCH/daemon-custom.log" 2>&1 &
DAEMON_PID=$!
for _ in $(seq 1 60); do
  if curl -fsS "http://$DAEMON_ADDR/healthz" >/dev/null 2>&1; then
    break
  fi
  sleep 0.25
done
TOKEN_CUSTOM="$("$GRAIN_BIN" access-code -data-dir "$SCRATCH/daemon-custom-data")"
UPG_CUSTOM_JSON="$(curl -fsS -H "Authorization: Bearer $TOKEN_CUSTOM" "http://$DAEMON_ADDR/api/upgrade")"
printf '%s\n' "$UPG_CUSTOM_JSON" | jq -e '
  .running.image == "us-docker.pkg.dev/my-proj/containers/grain:latest" and
  .imageRepo == "us-docker.pkg.dev/my-proj/containers/grain" and
  .defaultImageRef == "us-docker.pkg.dev/my-proj/containers/grain:latest"
' >/dev/null
echo "PASS live grain daemon /api/upgrade preserves explicit custom registry us-docker.pkg.dev/my-proj/containers/grain"
kill "$DAEMON_PID" 2>/dev/null || true
wait "$DAEMON_PID" 2>/dev/null || true
DAEMON_PID=""

# Verify Hub v2 API pruning across Docker Hub repositories (including registry).
(cd "$REPO_ROOT" && go test -count=1 ./cmd/ghcr-prune -run 'TestDockerHub|TestDefaultPackages')
echo "PASS cmd/ghcr-prune Docker Hub v2 API & package coverage"

echo "=== [2B] Isolated DOCKER_CONFIG (#747) & Live OCI v2 Promotion of grain + grain-registry (#780) ==="
# Verify #747: logging into docker.io with PACKAGE_PUBLISH_TOKEN uses an isolated
# temp DOCKER_CONFIG and never writes credentials to $HOME/.docker/config.json.
mkdir -p "$SCRATCH/home-747/.docker" "$SCRATCH/stub-747"
cat > "$SCRATCH/stub-747/docker" <<'EOF'
#!/usr/bin/env bash
if [ "${1:-}" = "login" ]; then
  cat >/dev/null
  printf 'LOGIN_DOCKER_CONFIG=%s\n' "${DOCKER_CONFIG:-unset}" > "$STUB_747_OUT"
  exit 0
fi
exit 0
EOF
chmod +x "$SCRATCH/stub-747/docker"
HOME="$SCRATCH/home-747" \
  PATH="$SCRATCH/stub-747:$PATH" \
  STUB_747_OUT="$SCRATCH/login-747.txt" \
  RELEASE_IMAGES_STATE="$SCRATCH/missing-state.env" \
  GRAIN_CI_REPO="ai-on-gke/grain" \
  GRAIN_CI_REF="main" \
  GRAIN_CI_COMMIT="0123456789abcdef0123456789abcdef01234567" \
  PACKAGE_PUBLISH_REGISTRY="docker.io" \
  PACKAGE_PUBLISH_TOKEN="dckr_oat_isolated_token" \
  bash "$REPO_ROOT/scripts/release-images.sh" push >/dev/null 2>&1 || true
grep -q '^LOGIN_DOCKER_CONFIG=/tmp/' "$SCRATCH/login-747.txt"
test ! -f "$SCRATCH/home-747/.docker/config.json"
echo "PASS #747: docker login to docker.io isolated in ephemeral DOCKER_CONFIG; \$HOME/.docker/config.json untouched"

# Build ociregistry, static stub-cli (for FROM scratch daemon/registry images), and OCI cosign tool.
(cd "$REPO_ROOT" && go build -o "$SCRATCH/bin/ociregistry" "$RUN_DIR/ociregistry.go")

cat > "$SCRATCH/stub_cli.go" <<'EOF'
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		os.Exit(1)
	}
	switch os.Args[1] {
	case "sandbox-image":
		fmt.Println(os.Getenv("GRAIN_STUB_SANDBOX_IMAGE"))
	case "k8s-sandbox-image":
		fmt.Println(os.Getenv("GRAIN_STUB_K8S_SANDBOX_IMAGE"))
	case "image":
		fmt.Println(os.Getenv("GRAIN_STUB_IMAGE_REF"))
	case "version":
		ed := os.Getenv("GRAIN_STUB_EDITION")
		if ed != "" {
			fmt.Printf("{\"release\":%q,\"revision\":%q,\"edition\":%q,\"modified\":false}\n",
				os.Getenv("GRAIN_STUB_RELEASE"), os.Getenv("GRAIN_STUB_REVISION"), ed)
		} else {
			fmt.Printf("{\"release\":%q,\"revision\":%q,\"modified\":false}\n",
				os.Getenv("GRAIN_STUB_RELEASE"), os.Getenv("GRAIN_STUB_REVISION"))
		}
	default:
		os.Exit(1)
	}
}
EOF
CGO_ENABLED=0 go build -o "$SCRATCH/stub-cli" "$SCRATCH/stub_cli.go"
mkdir -p "$SCRATCH/base-img"
cp "$SCRATCH/stub-cli" "$SCRATCH/base-img/stub-cli"
cat > "$SCRATCH/base-img/Dockerfile" <<'EOF'
FROM scratch
COPY stub-cli /bin/stub-cli
ENTRYPOINT ["/bin/stub-cli"]
EOF
docker build -q -t item2-stub-base:latest "$SCRATCH/base-img" >/dev/null

# Build real Ed25519 OCI .sig cosign tool against HTTP OCI v2 registries.
cat > "$SCRATCH/cosign_tool.go" <<'EOF'
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

type sigDoc struct {
	Digest    string `json:"docker-manifest-digest"`
	Signature string `json:"signature"`
}

func splitDigestRef(ref string) (host, repo, digest string, err error) {
	at := strings.IndexByte(ref, '@')
	if at <= 0 {
		return "", "", "", fmt.Errorf("missing @digest in %q", ref)
	}
	repoPart := ref[:at]
	digest = ref[at+1:]
	slash := strings.IndexByte(repoPart, '/')
	if slash <= 0 {
		return "", "", "", fmt.Errorf("missing registry host in %q", ref)
	}
	return repoPart[:slash], repoPart[slash+1:], digest, nil
}

func sigTag(digest string) string {
	return strings.Replace(digest, ":", "-", 1) + ".sig"
}

func main() {
	if len(os.Args) < 2 {
		os.Exit(2)
	}
	switch os.Args[1] {
	case "genkey":
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			os.Exit(1)
		}
		_ = os.WriteFile(os.Args[2], []byte("ed25519-priv:"+hex.EncodeToString(priv)+"\n"), 0o600)
		_ = os.WriteFile(os.Args[3], []byte("ed25519-pub:"+hex.EncodeToString(pub)+"\n"), 0o644)
	case "public-key":
		var keyPath string
		for i := 2; i < len(os.Args); i++ {
			if os.Args[i] == "--key" && i+1 < len(os.Args) {
				keyPath = os.Args[i+1]
			}
		}
		raw, err := os.ReadFile(keyPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		s := strings.TrimPrefix(strings.TrimSpace(string(raw)), "ed25519-priv:")
		privBytes, err := hex.DecodeString(s)
		if err != nil || len(privBytes) != ed25519.PrivateKeySize {
			fmt.Fprintln(os.Stderr, "invalid ed25519 private key")
			os.Exit(1)
		}
		pub := ed25519.PrivateKey(privBytes).Public().(ed25519.PublicKey)
		fmt.Printf("ed25519-pub:%s\n", hex.EncodeToString(pub))
	case "sign":
		var keyPath, ref string
		for i := 2; i < len(os.Args); i++ {
			switch {
			case os.Args[i] == "--key" && i+1 < len(os.Args):
				keyPath = os.Args[i+1]
				i++
			case strings.HasPrefix(os.Args[i], "--"):
			default:
				ref = os.Args[i]
			}
		}
		raw, err := os.ReadFile(keyPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		privBytes, err := hex.DecodeString(strings.TrimPrefix(strings.TrimSpace(string(raw)), "ed25519-priv:"))
		if err != nil || len(privBytes) != ed25519.PrivateKeySize {
			fmt.Fprintln(os.Stderr, "invalid signing key")
			os.Exit(1)
		}
		host, repo, digest, err := splitDigestRef(ref)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		sig := ed25519.Sign(ed25519.PrivateKey(privBytes), []byte("oci-sig:"+digest))
		body, _ := json.Marshal(sigDoc{Digest: digest, Signature: hex.EncodeToString(sig)})
		req, _ := http.NewRequest(http.MethodPut, fmt.Sprintf("http://%s/v2/%s/manifests/%s", host, repo, sigTag(digest)), bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode >= 300 {
			fmt.Fprintf(os.Stderr, "PUT signature failed: err=%v\n", err)
			os.Exit(1)
		}
	case "verify":
		var keyPath, ref string
		for i := 2; i < len(os.Args); i++ {
			switch {
			case os.Args[i] == "--key" && i+1 < len(os.Args):
				keyPath = os.Args[i+1]
				i++
			case strings.HasPrefix(os.Args[i], "--"):
			default:
				ref = os.Args[i]
			}
		}
		raw, err := os.ReadFile(keyPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		pubBytes, err := hex.DecodeString(strings.TrimPrefix(strings.TrimSpace(string(raw)), "ed25519-pub:"))
		if err != nil || len(pubBytes) != ed25519.PublicKeySize {
			fmt.Fprintln(os.Stderr, "invalid public key")
			os.Exit(1)
		}
		host, repo, digest, err := splitDigestRef(ref)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		resp, err := http.Get(fmt.Sprintf("http://%s/v2/%s/manifests/%s", host, repo, sigTag(digest)))
		if err != nil {
			fmt.Fprintf(os.Stderr, "dial tcp: %v\n", err)
			os.Exit(1)
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			fmt.Fprintf(os.Stderr, "no matching signatures: %s\n", ref)
			os.Exit(1)
		}
		data, _ := io.ReadAll(resp.Body)
		var doc sigDoc
		if err := json.Unmarshal(data, &doc); err != nil || doc.Digest != digest {
			fmt.Fprintln(os.Stderr, "corrupt signature manifest")
			os.Exit(1)
		}
		sigBytes, err := hex.DecodeString(doc.Signature)
		if err != nil || !ed25519.Verify(ed25519.PublicKey(pubBytes), []byte("oci-sig:"+digest), sigBytes) {
			fmt.Fprintln(os.Stderr, "signature verification failed")
			os.Exit(1)
		}
	default:
		os.Exit(2)
	}
}
EOF
go build -o "$SCRATCH/bin/cosign" "$SCRATCH/cosign_tool.go"
"$SCRATCH/bin/cosign" genkey "$SCRATCH/cosign.key" "$SCRATCH/cosign.pub"

# Start live OCI v2 registries on 127.0.0.1:4281 (Hub) and 127.0.0.1:4282 (legacy GHCR).
"$SCRATCH/bin/ociregistry" -addr "$HUB_ADDR" -name hub >"$SCRATCH/hub.log" 2>&1 &
HUB_PID=$!
"$SCRATCH/bin/ociregistry" -addr "$GHCR_ADDR" -name ghcr >"$SCRATCH/ghcr.log" 2>&1 &
GHCR_PID=$!
sleep 0.5

# Copy scripts/ to scratch so legacy GHCR prefix & default registry point at local OCI servers.
cp "$REPO_ROOT/scripts/release-images.sh" "$SCRATCH/scripts/"
cp "$REPO_ROOT/scripts/release-image-repos.sh" "$SCRATCH/scripts/"
cp "$REPO_ROOT/scripts/grain-ci-host.sh" "$SCRATCH/scripts/"
cp "$REPO_ROOT/scripts/build-version.sh" "$SCRATCH/scripts/"
sed -i "s|^RELEASE_IMAGE_REGISTRY_DEFAULT=.*|RELEASE_IMAGE_REGISTRY_DEFAULT=\"$HUB_ADDR\"|" "$SCRATCH/scripts/release-image-repos.sh"
sed -i "s|^GRAIN_LEGACY_GHCR_PREFIX=.*|GRAIN_LEGACY_GHCR_PREFIX=\"$GHCR_ADDR/bwsalmon/grain\"|" "$SCRATCH/scripts/release-image-repos.sh"

# Create post-Docker-Hub release repository (v1.5.0) with Dockerfile + Dockerfile.registry + build_registry_image().
git init -q --bare "$SCRATCH/post-remote.git"
git init -q -b main "$SCRATCH/post-src"
git -C "$SCRATCH/post-src" config user.name "Scratch Release"
git -C "$SCRATCH/post-src" config user.email "scratch@example.invalid"
mkdir -p "$SCRATCH/post-src/scripts"
printf 'module github.com/ai-on-gke/grain\n\ngo 1.24.0\n' > "$SCRATCH/post-src/go.mod"
cat > "$SCRATCH/post-src/Dockerfile" <<'EOF'
FROM item2-stub-base:latest
ARG GO_IMAGE=golang
ARG GO_VERSION=
ARG BUILDVCS=auto
ARG MAIN_GRAIN=./cmd/grain
ARG SANDBOX_IMAGE=
ARG K8S_SANDBOX_IMAGE=
ARG GRAIN_IMAGE_REF=
ARG GIT_REVISION=
ARG RELEASE=
ENV GRAIN_STUB_SANDBOX_IMAGE=${SANDBOX_IMAGE} \
    GRAIN_STUB_K8S_SANDBOX_IMAGE=${K8S_SANDBOX_IMAGE} \
    GRAIN_STUB_IMAGE_REF=${GRAIN_IMAGE_REF} \
    GRAIN_STUB_REVISION=${GIT_REVISION} \
    GRAIN_STUB_RELEASE=${RELEASE}
LABEL org.opencontainers.image.revision=${GIT_REVISION} \
      org.opencontainers.image.version=${RELEASE}
EOF
cat > "$SCRATCH/post-src/Dockerfile.registry" <<'EOF'
FROM item2-stub-base:latest
ARG GO_IMAGE=golang
ARG GO_VERSION=
ARG BUILDVCS=auto
ARG MAIN_GRAIN_REGISTRY=./cmd/grain-registry
ARG GIT_REVISION=
ARG RELEASE=
ENV GRAIN_STUB_REVISION=${GIT_REVISION} \
    GRAIN_STUB_RELEASE=${RELEASE}
LABEL org.opencontainers.image.revision=${GIT_REVISION} \
      org.opencontainers.image.version=${RELEASE}
EOF
cp "$REPO_ROOT/scripts/release-image-repos.sh" "$SCRATCH/post-src/scripts/release-image-repos.sh"
printf 'build_registry_image() {\n  :\n}\n' > "$SCRATCH/post-src/scripts/release-images.sh"
git -C "$SCRATCH/post-src" add .
git -C "$SCRATCH/post-src" commit -q -m "post-dockerhub release v1.5.0"
POST_SHA="$(git -C "$SCRATCH/post-src" rev-parse HEAD)"
POST_SHORT="${POST_SHA:0:7}"
git -C "$SCRATCH/post-src" tag -a v1.5.0 -m "Release v1.5.0"
git -C "$SCRATCH/post-src" tag -a latest -m "Release v1.5.0"
git -C "$SCRATCH/post-src" remote add origin "$SCRATCH/post-remote.git"
git -C "$SCRATCH/post-src" push -q origin main --tags

# Seed sandbox, sandbox-k8s, and grain sha-<short> WITHOUT registry:sha-<short> first -> verify fail-closed (#780)!
docker build -q --build-arg GIT_REVISION="$POST_SHA" -t "$HUB_ADDR/grainai/sandbox:sha-$POST_SHORT" "$SCRATCH/post-src" >/dev/null
docker push -q "$HUB_ADDR/grainai/sandbox:sha-$POST_SHORT" >/dev/null
SANDBOX_DGST="$(docker buildx imagetools inspect "$HUB_ADDR/grainai/sandbox:sha-$POST_SHORT" --format '{{.Manifest.Digest}}')"
"$SCRATCH/bin/cosign" sign --yes --key "$SCRATCH/cosign.key" --tlog-upload=false "$HUB_ADDR/grainai/sandbox@$SANDBOX_DGST"

docker build -q --build-arg GIT_REVISION="$POST_SHA" -t "$HUB_ADDR/grainai/sandbox-k8s:sha-$POST_SHORT" "$SCRATCH/post-src" >/dev/null
docker push -q "$HUB_ADDR/grainai/sandbox-k8s:sha-$POST_SHORT" >/dev/null
K8S_DGST="$(docker buildx imagetools inspect "$HUB_ADDR/grainai/sandbox-k8s:sha-$POST_SHORT" --format '{{.Manifest.Digest}}')"
"$SCRATCH/bin/cosign" sign --yes --key "$SCRATCH/cosign.key" --tlog-upload=false "$HUB_ADDR/grainai/sandbox-k8s@$K8S_DGST"

docker build -q \
  --build-arg SANDBOX_IMAGE="$HUB_ADDR/grainai/sandbox:sha-$POST_SHORT@$SANDBOX_DGST" \
  --build-arg K8S_SANDBOX_IMAGE="$HUB_ADDR/grainai/sandbox-k8s:sha-$POST_SHORT@$K8S_DGST" \
  --build-arg GRAIN_IMAGE_REF="$HUB_ADDR/grainai/grain:sha-$POST_SHORT" \
  --build-arg GIT_REVISION="$POST_SHA" \
  --build-arg RELEASE="v1.5.0-rc.1" \
  -t "$HUB_ADDR/grainai/grain:sha-$POST_SHORT" -f "$SCRATCH/post-src/Dockerfile" "$SCRATCH/post-src" >/dev/null
docker push -q "$HUB_ADDR/grainai/grain:sha-$POST_SHORT" >/dev/null
GRAIN_CAND_DGST="$(docker buildx imagetools inspect "$HUB_ADDR/grainai/grain:sha-$POST_SHORT" --format '{{.Manifest.Digest}}')"
"$SCRATCH/bin/cosign" sign --yes --key "$SCRATCH/cosign.key" --tlog-upload=false "$HUB_ADDR/grainai/grain@$GRAIN_CAND_DGST"

set +e
MISS_REG_OUT="$(cd "$SCRATCH/post-src" && \
  PATH="$SCRATCH/bin:$PATH" \
  GRAIN_CI_REPO="ai-on-gke/grain" \
  GRAIN_CI_REF="main" \
  GRAIN_CI_COMMIT="$POST_SHA" \
  RELEASE_IMAGES_REMOTE="$SCRATCH/post-remote.git" \
  PACKAGE_PUBLISH_REGISTRY="$HUB_ADDR" \
  RELEASE_IMAGE_REPO="grainai" \
  IMAGE_SIGNING_KEY="$SCRATCH/cosign.key" \
  REQUIRE_IMAGE_SIGNING=true \
  bash "$SCRATCH/scripts/release-images.sh" promote 2>&1)"
MISS_REG_RC=$?
set -e
test "$MISS_REG_RC" -ne 0
printf '%s\n' "$MISS_REG_OUT" | grep -q "never published $HUB_ADDR/grainai/registry:sha-$POST_SHORT"
echo "PASS #780 fail-closed: missing registry:sha-$POST_SHORT rejected before moving any release tags"

# Now push & sign registry:sha-<short> and run live `promote` -> verify both grain & registry rebuild stamped v1.5.0!
docker build -q \
  --build-arg GIT_REVISION="$POST_SHA" \
  --build-arg RELEASE="v1.5.0-rc.1" \
  -t "$HUB_ADDR/grainai/registry:sha-$POST_SHORT" -f "$SCRATCH/post-src/Dockerfile.registry" "$SCRATCH/post-src" >/dev/null
docker push -q "$HUB_ADDR/grainai/registry:sha-$POST_SHORT" >/dev/null
REG_CAND_DGST="$(docker buildx imagetools inspect "$HUB_ADDR/grainai/registry:sha-$POST_SHORT" --format '{{.Manifest.Digest}}')"
"$SCRATCH/bin/cosign" sign --yes --key "$SCRATCH/cosign.key" --tlog-upload=false "$HUB_ADDR/grainai/registry@$REG_CAND_DGST"

(cd "$SCRATCH/post-src" && \
  PATH="$SCRATCH/bin:$PATH" \
  GRAIN_CI_REPO="ai-on-gke/grain" \
  GRAIN_CI_REF="main" \
  GRAIN_CI_COMMIT="$POST_SHA" \
  RELEASE_IMAGES_REMOTE="$SCRATCH/post-remote.git" \
  PACKAGE_PUBLISH_REGISTRY="$HUB_ADDR" \
  RELEASE_IMAGE_REPO="grainai" \
  IMAGE_SIGNING_KEY="$SCRATCH/cosign.key" \
  REQUIRE_IMAGE_SIGNING=true \
  bash "$SCRATCH/scripts/release-images.sh" promote) >"$SCRATCH/promote-v150.log" 2>&1

# Verify grain:v1.5.0, grain:latest, grain:sha-<short> AND registry:v1.5.0, registry:latest, registry:sha-<short>
# all share exact carbon-copy digests and pass cosign signature verification!
for img in grain registry; do
  D_V="$(docker buildx imagetools inspect "$HUB_ADDR/grainai/$img:v1.5.0" --format '{{.Manifest.Digest}}')"
  D_L="$(docker buildx imagetools inspect "$HUB_ADDR/grainai/$img:latest" --format '{{.Manifest.Digest}}')"
  D_S="$(docker buildx imagetools inspect "$HUB_ADDR/grainai/$img:sha-$POST_SHORT" --format '{{.Manifest.Digest}}')"
  test "$D_V" = "$D_L"
  test "$D_V" = "$D_S"
  "$SCRATCH/bin/cosign" verify --key "$SCRATCH/cosign.pub" --insecure-ignore-tlog=true "$HUB_ADDR/grainai/$img@$D_L"
  docker pull -q "$HUB_ADDR/grainai/$img:latest" >/dev/null
  VER_JSON="$(docker run --rm --network none "$HUB_ADDR/grainai/$img:latest" version --json)"
  printf '%s\n' "$VER_JSON" | jq -e --arg rev "$POST_SHA" '.release == "v1.5.0" and .revision == $rev and .modified == false' >/dev/null
done
echo "PASS #780 + Bug F4: grain & registry rebuilt with RELEASE=v1.5.0, signed, and carbon-copied to :latest and :sha-$POST_SHORT"

echo "=== [2C] Pre-Docker-Hub GHCR Promotion (#753) & Fail-Closed Digest/Signature Checks ==="
git init -q --bare "$SCRATCH/pre-remote.git"
git init -q -b main "$SCRATCH/pre-src"
git -C "$SCRATCH/pre-src" config user.name "Scratch Pre"
git -C "$SCRATCH/pre-src" config user.email "scratch@example.invalid"
printf 'module github.com/ai-on-gke/grain\n\ngo 1.24.0\n' > "$SCRATCH/pre-src/go.mod"
cp "$SCRATCH/post-src/Dockerfile" "$SCRATCH/pre-src/Dockerfile"
# Deliberately omit scripts/release-image-repos.sh so release_takes_main_grain detects pre-Docker-Hub (#739)!
git -C "$SCRATCH/pre-src" add .
git -C "$SCRATCH/pre-src" commit -q -m "pre-dockerhub release v3.1.0"
PRE_SHA="$(git -C "$SCRATCH/pre-src" rev-parse HEAD)"
PRE_SHORT="${PRE_SHA:0:7}"
git -C "$SCRATCH/pre-src" tag -a v3.1.0 -m "Release v3.1.0"
git -C "$SCRATCH/pre-src" tag -a latest -m "Release v3.1.0"
git -C "$SCRATCH/pre-src" remote add origin "$SCRATCH/pre-remote.git"
git -C "$SCRATCH/pre-src" push -q origin main --tags

# 2C.1: Empty GHCR -> fails closed naming predates Docker Hub (#739).
set +e
EMPTY_GHCR_OUT="$(cd "$SCRATCH/pre-src" && \
  PATH="$SCRATCH/bin:$PATH" \
  GRAIN_CI_REPO="ai-on-gke/grain" \
  GRAIN_CI_REF="main" \
  GRAIN_CI_COMMIT="$PRE_SHA" \
  RELEASE_IMAGES_REMOTE="$SCRATCH/pre-remote.git" \
  PACKAGE_PUBLISH_REGISTRY="$HUB_ADDR" \
  RELEASE_IMAGE_REPO="grainai" \
  bash "$SCRATCH/scripts/release-images.sh" promote v3.1.0 2>&1)"
EMPTY_GHCR_RC=$?
set -e
test "$EMPTY_GHCR_RC" -ne 0
printf '%s\n' "$EMPTY_GHCR_OUT" | grep -q "predates the switch to Docker Hub (#739)"
echo "PASS #753 fail-closed (1/4): missing GHCR source rejected with pre-Docker-Hub diagnostic"

# Populate simulated GHCR (127.0.0.1:4282) WITHOUT signatures first.
for leaf in guest k8s-sandbox guest-base k8s-sandbox-base; do
  docker build -q --build-arg GIT_REVISION="$PRE_SHA" --build-arg RELEASE="v3.1.0" \
    -t "$GHCR_ADDR/bwsalmon/grain/$leaf:sha-$PRE_SHORT" "$SCRATCH/pre-src" >/dev/null
  docker push -q "$GHCR_ADDR/bwsalmon/grain/$leaf:sha-$PRE_SHORT" >/dev/null
done
docker build -q \
  --build-arg SANDBOX_IMAGE="$GHCR_ADDR/bwsalmon/grain/guest:sha-$PRE_SHORT" \
  --build-arg K8S_SANDBOX_IMAGE="$GHCR_ADDR/bwsalmon/grain/k8s-sandbox:sha-$PRE_SHORT" \
  --build-arg GRAIN_IMAGE_REF="$GHCR_ADDR/bwsalmon/grain/grain:v3.1.0" \
  --build-arg GIT_REVISION="$PRE_SHA" \
  --build-arg RELEASE="v3.1.0" \
  -t "$GHCR_ADDR/bwsalmon/grain/grain:v3.1.0" "$SCRATCH/pre-src" >/dev/null
docker push -q "$GHCR_ADDR/bwsalmon/grain/grain:v3.1.0" >/dev/null

# 2C.2: Unsigned GHCR source with REQUIRE_IMAGE_SIGNING=true -> fails closed, copies nothing.
set +e
UNSIG_OUT="$(cd "$SCRATCH/pre-src" && \
  PATH="$SCRATCH/bin:$PATH" \
  GRAIN_CI_REPO="ai-on-gke/grain" \
  GRAIN_CI_REF="main" \
  GRAIN_CI_COMMIT="$PRE_SHA" \
  RELEASE_IMAGES_REMOTE="$SCRATCH/pre-remote.git" \
  PACKAGE_PUBLISH_REGISTRY="$HUB_ADDR" \
  RELEASE_IMAGE_REPO="grainai" \
  IMAGE_SIGNING_KEY="$SCRATCH/cosign.key" \
  REQUIRE_IMAGE_SIGNING=true \
  bash "$SCRATCH/scripts/release-images.sh" promote v3.1.0 2>&1)"
UNSIG_RC=$?
set -e
test "$UNSIG_RC" -ne 0
printf '%s\n' "$UNSIG_OUT" | grep -q "carries no signature made with .* and REQUIRE_IMAGE_SIGNING is set, so it is not copied"
echo "PASS #753 fail-closed (2/4): unsigned GHCR source rejected under REQUIRE_IMAGE_SIGNING=true"

# 2C.3: Corrupt signing key -> fails closed before copying anything.
printf 'corrupt-key-material\n' > "$SCRATCH/bad.key"
set +e
BADKEY_OUT="$(cd "$SCRATCH/pre-src" && \
  PATH="$SCRATCH/bin:$PATH" \
  GRAIN_CI_REPO="ai-on-gke/grain" \
  GRAIN_CI_REF="main" \
  GRAIN_CI_COMMIT="$PRE_SHA" \
  RELEASE_IMAGES_REMOTE="$SCRATCH/pre-remote.git" \
  PACKAGE_PUBLISH_REGISTRY="$HUB_ADDR" \
  RELEASE_IMAGE_REPO="grainai" \
  IMAGE_SIGNING_KEY="$SCRATCH/bad.key" \
  REQUIRE_IMAGE_SIGNING=true \
  bash "$SCRATCH/scripts/release-images.sh" promote v3.1.0 2>&1)"
BADKEY_RC=$?
set -e
test "$BADKEY_RC" -ne 0
printf '%s\n' "$BADKEY_OUT" | grep -q "could not derive a public key"
echo "PASS #753 fail-closed (3/4): unusable signing key rejected cleanly"

# Sign all 5 GHCR images with our Ed25519 release key.
for pair in \
  "guest:sha-$PRE_SHORT" \
  "k8s-sandbox:sha-$PRE_SHORT" \
  "guest-base:sha-$PRE_SHORT" \
  "k8s-sandbox-base:sha-$PRE_SHORT" \
  "grain:v3.1.0"; do
  repo_name="${pair%:*}"
  DGST="$(docker buildx imagetools inspect "$GHCR_ADDR/bwsalmon/grain/$pair" --format '{{.Manifest.Digest}}')"
  "$SCRATCH/bin/cosign" sign --yes --key "$SCRATCH/cosign.key" --tlog-upload=false "$GHCR_ADDR/bwsalmon/grain/$repo_name@$DGST"
done

# 2C.4: Corrupt target manifest on Hub -> copy_by_digest fails closed ("the copy is not the image that was tested").
curl -fsS "http://$HUB_ADDR/_corrupt_next_manifest?enable=true" >/dev/null
set +e
CORRUPT_OUT="$(cd "$SCRATCH/pre-src" && \
  PATH="$SCRATCH/bin:$PATH" \
  GRAIN_CI_REPO="ai-on-gke/grain" \
  GRAIN_CI_REF="main" \
  GRAIN_CI_COMMIT="$PRE_SHA" \
  RELEASE_IMAGES_REMOTE="$SCRATCH/pre-remote.git" \
  PACKAGE_PUBLISH_REGISTRY="$HUB_ADDR" \
  RELEASE_IMAGE_REPO="grainai" \
  IMAGE_SIGNING_KEY="$SCRATCH/cosign.key" \
  REQUIRE_IMAGE_SIGNING=true \
  bash "$SCRATCH/scripts/release-images.sh" promote v3.1.0 2>&1)"
CORRUPT_RC=$?
set -e
test "$CORRUPT_RC" -ne 0
printf '%s\n' "$CORRUPT_OUT" | grep -q "the copy is not the image that was tested"
if curl -fsS "http://$HUB_ADDR/v2/grainai/grain/manifests/v3.1.0" >/dev/null 2>&1; then
  echo "FAIL: grain:v3.1.0 should not exist after corrupt copy failure" >&2
  exit 1
fi
echo "PASS #753 fail-closed (4/4): digest mismatch rejected and zero release tags moved"

# Delete the corrupted sandbox:sha- manifest ref and run full signed GHCR -> Hub promotion!
curl -fsS "http://$HUB_ADDR/_delete_manifest?repo=grainai/sandbox&ref=sha-$PRE_SHORT" >/dev/null

(cd "$SCRATCH/pre-src" && \
  PATH="$SCRATCH/bin:$PATH" \
  GRAIN_CI_REPO="ai-on-gke/grain" \
  GRAIN_CI_REF="main" \
  GRAIN_CI_COMMIT="$PRE_SHA" \
  RELEASE_IMAGES_REMOTE="$SCRATCH/pre-remote.git" \
  PACKAGE_PUBLISH_REGISTRY="$HUB_ADDR" \
  RELEASE_IMAGE_REPO="grainai" \
  IMAGE_SIGNING_KEY="$SCRATCH/cosign.key" \
  REQUIRE_IMAGE_SIGNING=true \
  bash "$SCRATCH/scripts/release-images.sh" promote v3.1.0) >"$SCRATCH/promote-v310.log" 2>&1

grep -q "already reports v3.1.0; nothing to rebuild" "$SCRATCH/promote-v310.log"
SRC_GRAIN_DGST="$(docker buildx imagetools inspect "$GHCR_ADDR/bwsalmon/grain/grain:v3.1.0" --format '{{.Manifest.Digest}}')"
DST_GRAIN_V310="$(docker buildx imagetools inspect "$HUB_ADDR/grainai/grain:v3.1.0" --format '{{.Manifest.Digest}}')"
DST_GRAIN_LATEST="$(docker buildx imagetools inspect "$HUB_ADDR/grainai/grain:latest" --format '{{.Manifest.Digest}}')"
test "$SRC_GRAIN_DGST" = "$DST_GRAIN_V310"
test "$SRC_GRAIN_DGST" = "$DST_GRAIN_LATEST"
"$SCRATCH/bin/cosign" verify --key "$SCRATCH/cosign.pub" --insecure-ignore-tlog=true "$HUB_ADDR/grainai/grain@$DST_GRAIN_LATEST"
echo "PASS #753 happy path: all 5 signed pre-Docker-Hub GHCR images verified, carbon-copied by digest, and re-signed on target Hub without rebuilding stamped grain:v3.1.0"

echo "=== [2D] Verify /home/debian/work git config hygiene ==="
if git -C "$REPO_ROOT" config --local --get user.email >/dev/null 2>&1; then
  echo "FAIL: local user.email set in $REPO_ROOT" >&2
  exit 1
fi
echo "PASS /home/debian/work/.git/config clean"
echo "ALL §9 ITEM 2 CHECKS PASSED"
