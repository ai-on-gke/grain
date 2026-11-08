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

# step-propose.sh (host side, run by fake-agy while the run is live):
# item 4. The run, holding the person-granted gcp#gcp-key={permission_set:
# ext-deploy}, calls propose_task through its own grain MCP server twice:
# once passing that grant on, once (the control) with no grant at all.
# token-walk.sh then reads what each proposal was filed as.
# Once per walk (token-walk.sh arms it): the control proposal is queued
# and runs this same step, and the run's own second turn runs it again.
[ -f /home/debian/walk/propose-armed ] || { echo "== step-propose: not armed, nothing proposed"; exit 0; }
rm -f /home/debian/walk/propose-armed
cp "$HOME/.gemini/config/mcp_config.json" /home/debian/walk/mcp-live.json
python3 /home/debian/walk/mcp-propose.py
