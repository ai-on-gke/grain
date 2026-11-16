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

# Races RunModelDiscovery's first 10-minute tick against a Refresh models
# click: the provider answers the tick late and stale, and the refresh
# made while the tick is still waiting must win. Run against a daemon
# env.sh's start_daemon just started; prints a timeline.

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=env.sh
source "$HERE/env.sh"
set +e
CODE=$(tr -d '[:space:]' <"$WORK_DIR/data/local-api-token")
start=$(date -d "$(ps -o lstart= -p "$(cat "$WORK_DIR/daemon.pid")")" +%s)
ts() { date -u +%H:%M:%S.%N | cut -c1-12; }
show() { api GET /api/settings | jq -c '.agentBackends[] | select(.id=="anthro") | .discovered'; }

echo "$(ts) daemon started $(date -u -d @"$start" +%H:%M:%S); tick due $(date -u -d @"$((start + 600))" +%H:%M:%S)"
sleep $((start + 590 - $(date +%s)))
fake_models claude-walk-a claude-walk-stale
fake_mode slow
echo "$(ts) provider now serves [a, stale], answering 15s late"
before=$(curl -s "$FAKE_CTL/hits" | wc -l)
until (($(curl -s "$FAKE_CTL/hits" | wc -l) > before)); do sleep 0.2; done
echo "$(ts) the tick asked the provider: $(curl -s "$FAKE_CTL/hits" | tail -n1)"
sleep 2
fake_models claude-walk-a claude-walk-fresh
fake_mode ok
echo "$(ts) provider now serves [a, fresh]; clicking Refresh models (POST discover)"
echo "$(ts) refresh answered: $(discover anthro)"
sleep 1
echo "$(ts) stored after both: $(show)"
echo "$(ts) provider hits:"
curl -s "$FAKE_CTL/hits" | tail -n3
grep -n "advertising discovered models\|refreshed the models" "$WORK_DIR/daemon.log" | tail -n3
