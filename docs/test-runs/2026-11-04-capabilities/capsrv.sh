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

# capsrv.sh: the walk's droppable capability server -- grain's own
# config-map server under an id of its own -- which refuses to start while
# /home/debian/walk/capsrv.down exists, so a stopped server stays down.
[ -f /home/debian/walk/capsrv.down ] && { echo "capsrv: held down by the walk" >&2; exit 1; }
exec /home/debian/bin/grain capability config-map
