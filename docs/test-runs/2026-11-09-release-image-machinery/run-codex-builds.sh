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

# run-codex-builds.sh: 2026-11-09 §6 item 1. For each of Dockerfile and
# Dockerfile.k8s-sandbox, cuts the Codex install step (from
# `ARG INSTALL_CODEX_CLI` through the RUN's closing `fi`) out verbatim,
# appends it to a probe image that trusts codex-stub.go's CA, and runs a
# real `docker build --network host --add-host <github/npm>:127.0.0.1`
# against the stub in every -mode. Successful images are run once to
# confirm the binaries work and /tmp/codex-release.json is not in the
# final layer.
#
# Usage: run-codex-builds.sh <repo checkout> <out dir>
set -euo pipefail

repo=$(cd "${1:?repo checkout}" && pwd)
out=$(mkdir -p "${2:?out dir}" && cd "$2" && pwd)
here=$(cd "$(dirname "$0")" && pwd)
modes=(healthy 504-download 504-all tampered tampered-api tampered-npm html200 html200-api html200-all)

go build -o "$out/codex-stub" "$here/codex-stub.go"

docker build -q -t rim-codex-base - >/dev/null <<'EOF'
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates && rm -rf /var/lib/apt/lists/*
EOF

hosts=(--add-host github.com:127.0.0.1 --add-host api.github.com:127.0.0.1
	--add-host release-assets.githubusercontent.com:127.0.0.1 --add-host registry.npmjs.org:127.0.0.1)

summary="$out/summary.txt"
: >"$summary"
for df in Dockerfile Dockerfile.k8s-sandbox; do
	for mode in "${modes[@]}"; do
		ctx="$out/ctx-$df-$mode"
		rm -rf "$ctx" && mkdir -p "$ctx"
		: >"$ctx/requests.log"
		sudo "$out/codex-stub" -mode "$mode" -out "$ctx" >"$ctx/stub.log" 2>&1 &
		stub=$!
		for _ in $(seq 50); do [ -s "$ctx/digests.env" ] && [ -s "$ctx/ca.pem" ] && break; sleep 0.1; done
		sleep 0.3
		{
			echo "FROM rim-codex-base"
			echo "COPY ca.pem /usr/local/share/ca-certificates/rim-stub.crt"
			echo "RUN update-ca-certificates >/dev/null"
			sed -n '/^ARG INSTALL_CODEX_CLI/,/^[[:space:]]*fi$/p' "$repo/$df"
		} >"$ctx/Dockerfile"
		args=()
		while IFS= read -r kv; do args+=(--build-arg "$kv"); done <"$ctx/digests.env"
		tag="rim-codex:$(echo "$df-$mode" | tr 'A-Z.' 'a-z-')"
		set +e
		DOCKER_BUILDKIT=1 docker build --no-cache --progress=plain --network host "${hosts[@]}" "${args[@]}" \
			-t "$tag" "$ctx" >"$ctx/build.log" 2>&1
		rc=$?
		set -e
		final=""
		if [ $rc -eq 0 ]; then
			final=$(docker run --rm --network none "$tag" sh -c \
				'codex --version; codex-code-mode-host --help; ls -A /tmp | tr "\n" " "; test ! -e /tmp/codex-release.json && echo release-json-absent')
		fi
		sudo kill "$stub" 2>/dev/null || true
		wait "$stub" 2>/dev/null || true
		api_calls=$(grep -c ' api.github.com/repos/openai/codex/releases/tags/' "$ctx/requests.log" || true)
		npm_calls=$(grep -c ' registry.npmjs.org/' "$ctx/requests.log" || true)
		auth=$(grep -c 'auth=true' "$ctx/requests.log" || true)
		printf '%-24s %-13s build_rc=%d api_tag_calls=%s npm_calls=%s auth_headers=%s final=[%s]\n' \
			"$df" "$mode" "$rc" "$api_calls" "$npm_calls" "$auth" "$(echo "$final" | tr '\n' ';')" | tee -a "$summary"
	done
done
