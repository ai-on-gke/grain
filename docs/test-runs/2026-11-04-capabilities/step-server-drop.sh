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

# step.sh (host side) for item 4: hold the run open while the capability
# server is stopped, then make one grain MCP tool call and record its result.
cfg="$HOME/.gemini/config/mcp_config.json"
echo "== mcp_config.json (secrets cut)"
sed -E 's/("[^"]*([Tt]oken|[Aa]uthorization|KEY|SECRET)[^"]*" *: *")[^"]{0,8}[^"]*"/\1<cut>"/g' "$cfg"
cp "$cfg" /home/debian/walk/mcp-live.json
touch /home/debian/walk/ready
echo "== $(date -u +%T) waiting for /home/debian/walk/go"
for i in $(seq 240); do [ -f /home/debian/walk/go ] && break; sleep 1; done
echo "== $(date -u +%T) go: calling a tool"
[ -f /home/debian/walk/call.sh ] && sh /home/debian/walk/call.sh 2>&1
rm -f /home/debian/walk/go /home/debian/walk/ready
