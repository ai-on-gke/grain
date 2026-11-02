#!/bin/sh
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

# The walkthrough daemon: GRAIN=path/to/grain DIR=workdir JWKS=host:port ./run-daemon.sh [extra flags, e.g. -ui-allowed-principals ...]
exec ${GRAIN:-grain} daemon -data-dir ${DIR:-.}/data -sandbox-port-base=0 -host-sandboxes -sandbox-dir ${DIR:-.}/sb \
  -ui-addr 127.0.0.1:4300 -as owner@example.com \
  -ui-iap-audience /projects/123/global/backendServices/456 \
  -ui-iap-issuer https://iap.test.local \
  -ui-iap-keys-url http://${JWKS:-127.0.0.1:18080}/jwks "$@"
