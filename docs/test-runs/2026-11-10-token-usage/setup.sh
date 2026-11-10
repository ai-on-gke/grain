#!/bin/bash
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

# setup.sh <data-dir> <port> [cli-path]: one antigravity connection run by fake-agy
# (no model credentials in the sandbox), pinned to one model.
D=$(dirname "$0")
"$D/api.sh" "$1" "$2" PUT /api/config '{"maxWorkers":4,"defaultModel":"fake-model","agentBackends":[
  {"id":"agy-walk","name":"Walk agy","framework":"antigravity","cliPath":"'"${3:-/home/debian/walk/fake-agy}"'","models":["fake-model"]}]}' | tail -n 1
"$D/api.sh" "$1" "$2" PUT /api/agent-backends/agy-walk/credential '{"value":"walk-key-not-real"}' | tail -n 1
