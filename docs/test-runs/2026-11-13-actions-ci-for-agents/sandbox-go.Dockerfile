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

# The stock sandbox image with a Go toolchain, for §7 item 5: the stock
# image carries no Go, so no sandbox on it ever gets a GOCACHEPROG (F2).
# docker build -t walk/sandbox-go:local -f sandbox-go.Dockerfile .
#
# Both bases are pinned by digest, the tags' digests on 2026-11-16 (the
# walk itself pulled the bare tags); to repeat it against a newer stock
# image, replace the sandbox-k8s digest with that image's.
FROM docker.io/library/golang:1.25@sha256:699337d620559a59b4a2bb298ad59611e535d2ee755a34cf2d2a98f37578dc80 AS go
FROM docker.io/grainai/sandbox-k8s:latest@sha256:a503c2a7fab40c00b1dad3baf24a2650a41dd0780ec765c6fe888ff13462b879
USER root
COPY --from=go /usr/local/go /usr/local/go
RUN ln -sf /usr/local/go/bin/go /usr/local/bin/go && ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt
USER debian
