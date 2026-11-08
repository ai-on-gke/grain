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

# Start a `grain host -join` for /tmp/host-<name> in the background (its
# log is /tmp/host-<name>/host.log), against the walkthrough daemon.
#
#   join-host.sh <name> <enrollment-code> [extra grain host flags...]
set -eu
name=$1
code=$2
shift 2
root=/tmp/host-$name
cd "$root"
HOME=$root/home PATH=$root/home/bin:$PATH nohup /home/debian/bin/grain host \
	-server http://127.0.0.1:4280 -join "$code" \
	-key-file "$root/home/hostd-key" -token-file "$root/home/hostd-token" \
	-allowlist "$root/hostd.yaml" -socket "$root/hostd.sock" -state-dir "$root/state" \
	"$@" >"$root/host.log" 2>&1 &
echo $! >"$root/host.pid"
